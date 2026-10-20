//go:build linux

package executioncollect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resourceFixture(t *testing.T, group, mountRoot string) (string, string) {
	t.Helper()
	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	mount := filepath.Join(root, "cgroup")
	for _, directory := range []string{filepath.Join(proc, "self"), mount} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeResource(t, filepath.Join(proc, "meminfo"), "MemTotal:       16777216 kB\n")
	writeResource(t, filepath.Join(proc, "self", "cgroup"), "0::"+group+"\n")
	writeResource(t, filepath.Join(proc, "self", "mountinfo"), fmt.Sprintf("29 23 0:26 %s %s rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n", mountRoot, mount))
	return proc, mount
}

func writeResource(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxResourcesUseTightestVisibleAncestorLimits(t *testing.T) {
	proc, mount := resourceFixture(t, "/team/job", "/")
	for path, value := range map[string]string{
		"cpu.max": "max 100000", "memory.max": "max",
		"team/cpu.max": "250000 100000", "team/memory.max": "4294967296",
		"team/job/cpu.max": "400000 100000", "team/job/memory.max": "8589934592",
	} {
		writeResource(t, filepath.Join(mount, path), value)
	}
	cpu, memory := linuxResources(number(8), proc, func() (int64, error) { return 4, nil })
	if cpu.LimitKnowledge != "partial" || cpu.QuotaMilliCPU == nil || *cpu.QuotaMilliCPU != 2500 || cpu.AffinityLogical == nil || *cpu.AffinityLogical != 4 || cpu.EffectiveMilliCPU != nil {
		t.Fatalf("CPU=%+v", cpu)
	}
	if memory.LimitKnowledge != "partial" || memory.VisibleTotalBytes == nil || *memory.VisibleTotalBytes != 16<<30 || memory.CgroupLimitBytes == nil || *memory.CgroupLimitBytes != 4<<30 || memory.EffectiveLimitBytes != nil {
		t.Fatalf("memory=%+v", memory)
	}
}

func TestLinuxIncompleteHierarchyKeepsKnownBoundsButNoEffectiveCapacity(t *testing.T) {
	proc, mount := resourceFixture(t, "/team/job", "/")
	for path, value := range map[string]string{"cpu.max": "max 100000", "memory.max": "max", "team/job/cpu.max": "50000 100000", "team/job/memory.max": "1073741824"} {
		writeResource(t, filepath.Join(mount, path), value)
	}
	cpu, memory := linuxResources(number(8), proc, func() (int64, error) { return 4, nil })
	if cpu.LimitKnowledge != "partial" || cpu.QuotaMilliCPU == nil || *cpu.QuotaMilliCPU != 500 || cpu.EffectiveMilliCPU != nil {
		t.Fatalf("CPU=%+v", cpu)
	}
	if memory.LimitKnowledge != "partial" || memory.CgroupLimitBytes == nil || *memory.CgroupLimitBytes != 1<<30 || memory.EffectiveLimitBytes != nil {
		t.Fatalf("memory=%+v", memory)
	}
}

func TestLinuxVisibleUnlimitedHierarchyDoesNotEstablishOuterAncestry(t *testing.T) {
	proc, mount := resourceFixture(t, "/job", "/")
	for path, value := range map[string]string{"cpu.max": "max 100000", "memory.max": "max", "job/cpu.max": "max 100000", "job/memory.max": "max"} {
		writeResource(t, filepath.Join(mount, path), value)
	}
	cpu, memory := linuxResources(number(8), proc, func() (int64, error) { return 2, nil })
	if cpu.LimitKnowledge != "partial" || cpu.QuotaMilliCPU != nil || cpu.AffinityLogical == nil || *cpu.AffinityLogical != 2 || cpu.EffectiveMilliCPU != nil || memory.LimitKnowledge != "partial" || memory.CgroupLimitBytes != nil || memory.VisibleTotalBytes == nil || *memory.VisibleTotalBytes != 16<<30 || memory.EffectiveLimitBytes != nil {
		t.Fatalf("CPU=%+v, memory=%+v", cpu, memory)
	}
	cpu, _ = linuxResources(number(8), proc, func() (int64, error) { return 0, errors.New("unavailable") })
	if cpu.LimitKnowledge != "partial" || cpu.EffectiveMilliCPU != nil {
		t.Fatalf("unknown affinity presented as complete: %+v", cpu)
	}
}

func TestLinuxRootOnlyAndSubtreeViewsArePartial(t *testing.T) {
	for _, test := range []struct{ group, root string }{{"/", "/"}, {"/hidden/job", "/hidden"}} {
		t.Run(test.group, func(t *testing.T) {
			proc, mount := resourceFixture(t, test.group, test.root)
			for path, value := range map[string]string{"cpu.max": "max 100000", "memory.max": "max", "job/cpu.max": "100000 100000", "job/memory.max": "1073741824"} {
				writeResource(t, filepath.Join(mount, path), value)
			}
			cpu, memory := linuxResources(number(8), proc, func() (int64, error) { return 4, nil })
			if cpu.LimitKnowledge != "partial" || cpu.EffectiveMilliCPU != nil || memory.LimitKnowledge != "partial" || memory.EffectiveLimitBytes != nil {
				t.Fatalf("CPU=%+v, memory=%+v", cpu, memory)
			}
		})
	}
}

func TestLinuxMissingAndInvalidResourcesStayUnknown(t *testing.T) {
	proc, mount := resourceFixture(t, "/job", "/")
	writeResource(t, filepath.Join(proc, "meminfo"), "MemTotal: 9007199254740991 kB\n")
	for path, value := range map[string]string{"cpu.max": "nonsense", "memory.max": "-1", "job/cpu.max": "0 100000", "job/memory.max": "9223372036854775807"} {
		writeResource(t, filepath.Join(mount, path), value)
	}
	cpu, memory := linuxResources(number(8), proc, func() (int64, error) { return 0, errors.New("unavailable") })
	if cpu.LimitKnowledge != "unknown" || cpu.AffinityLogical != nil || cpu.QuotaMilliCPU != nil || cpu.EffectiveMilliCPU != nil || memory.LimitKnowledge != "unknown" || memory.VisibleTotalBytes != nil || memory.CgroupLimitBytes != nil || memory.EffectiveLimitBytes != nil {
		t.Fatalf("CPU=%+v, memory=%+v", cpu, memory)
	}
	if err := os.Remove(filepath.Join(proc, "self", "cgroup")); err != nil {
		t.Fatal(err)
	}
	cpu, memory = linuxResources(number(8), proc, func() (int64, error) { return 4, nil })
	if cpu.LimitKnowledge != "unknown" || memory.LimitKnowledge != "unknown" {
		t.Fatal("missing hierarchy treated as unlimited")
	}
}

func TestLinuxV1LimitsAreExplicitlyPartial(t *testing.T) {
	proc, mount := resourceFixture(t, "/job", "/")
	writeResource(t, filepath.Join(proc, "self", "cgroup"), "2:cpu,cpuacct:/job\n3:memory:/job\n")
	writeResource(t, filepath.Join(proc, "self", "mountinfo"), fmt.Sprintf("29 23 0:26 / %s rw - cgroup cgroup rw,cpu,cpuacct,memory\n", mount))
	writeResource(t, filepath.Join(mount, "job", "cpu.cfs_quota_us"), "150000")
	writeResource(t, filepath.Join(mount, "job", "cpu.cfs_period_us"), "100000")
	writeResource(t, filepath.Join(mount, "job", "memory.limit_in_bytes"), "1073741824")
	cpu, memory := linuxResources(number(8), proc, func() (int64, error) { return 4, nil })
	if cpu.LimitKnowledge != "partial" || cpu.QuotaMilliCPU == nil || *cpu.QuotaMilliCPU != 1500 || cpu.EffectiveMilliCPU != nil || memory.LimitKnowledge != "partial" || memory.CgroupLimitBytes == nil || *memory.CgroupLimitBytes != 1<<30 || memory.EffectiveLimitBytes != nil {
		t.Fatalf("CPU=%+v, memory=%+v", cpu, memory)
	}
}

func TestLinuxResourceParsersRejectInvalidAndOversizedData(t *testing.T) {
	for _, value := range []string{"", "max", "1 0", "0 100000", "-1 100000", "-2 100000", "1 1000000000", "9223372036854775807 1", "100000 100000 extra"} {
		if _, valid := cpuMax(value); valid {
			t.Fatalf("accepted CPU limit %q", value)
		}
	}
	for _, value := range []string{"", "0", "-1", "9223372036854775807", "1 extra"} {
		if _, valid := memoryMax(value); valid {
			t.Fatalf("accepted memory limit %q", value)
		}
	}
	path := filepath.Join(t.TempDir(), "oversized")
	writeResource(t, path, strings.Repeat("x", maximumKernelFileBytes+1))
	if _, err := readKernelFile(path); err == nil {
		t.Fatal("accepted oversized kernel file")
	}
	if groups := parseCgroups("0::/../../escape\n"); groups.unified != "" {
		t.Fatal("accepted escaping cgroup")
	}
	if mounts := parseCgroupMounts("29 23 0:26 / /sys/fs/cgroup/../escape rw - cgroup2 cgroup rw"); len(mounts) != 0 {
		t.Fatal("accepted unclean mount")
	}
}
