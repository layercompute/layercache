//go:build linux

package executioncollect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/layercache/layercache/internal/execution"
	"golang.org/x/sys/unix"
)

const maximumKernelFileBytes = 128 << 10

func collectPlatform(cpu *execution.CPU, memory *execution.Memory) {
	*cpu, *memory = linuxResources(cpu.VisibleLogical, "/proc", func() (int64, error) {
		var set unix.CPUSet
		if err := unix.SchedGetaffinity(0, &set); err != nil {
			return 0, err
		}
		return int64(set.Count()), nil
	})
}

func linuxResources(visible *int64, procRoot string, affinity func() (int64, error)) (execution.CPU, execution.Memory) {
	cpu := execution.CPU{VisibleLogical: visible, LimitKnowledge: "unknown"}
	memory := execution.Memory{LimitKnowledge: "unknown"}
	if count, err := affinity(); err == nil && count > 0 && (visible == nil || count <= *visible) {
		cpu.AffinityLogical = number(count)
	}
	if data, err := readKernelFile(filepath.Join(procRoot, "meminfo")); err == nil {
		memory.VisibleTotalBytes = memTotal(data)
	}
	cgroups, groupErr := readKernelFile(filepath.Join(procRoot, "self", "cgroup"))
	mounts, mountErr := readKernelFile(filepath.Join(procRoot, "self", "mountinfo"))
	if groupErr != nil || mountErr != nil {
		return cpu, memory
	}
	groups := parseCgroups(cgroups)
	for _, mount := range parseCgroupMounts(mounts) {
		if mount.version == 2 && groups.unified != "" {
			if target, ok := mount.target(groups.unified); ok {
				cpu.QuotaMilliCPU, cpu.LimitKnowledge = hierarchyLimit(target, mount, "cpu.max", cpuMax)
				memory.CgroupLimitBytes, memory.LimitKnowledge = hierarchyLimit(target, mount, "memory.max", memoryMax)
				break
			}
		}
	}
	// v1 exposes separate controller mounts. Values are useful upper bounds, but
	// this first collector does not claim a complete v1 hierarchy observation.
	if cpu.LimitKnowledge == "unknown" {
		for _, mount := range parseCgroupMounts(mounts) {
			if mount.version != 1 || !mount.controllers["cpu"] || groups.cpu == "" {
				continue
			}
			if target, ok := mount.target(groups.cpu); ok {
				quota, qerr := readKernelFile(filepath.Join(target, "cpu.cfs_quota_us"))
				period, perr := readKernelFile(filepath.Join(target, "cpu.cfs_period_us"))
				if qerr == nil && perr == nil {
					if strings.TrimSpace(quota) == "-1" {
						quota = "max"
					}
					value, valid := cpuMax(strings.TrimSpace(quota) + " " + strings.TrimSpace(period))
					if valid {
						cpu.QuotaMilliCPU, cpu.LimitKnowledge = value, "partial"
					}
				}
			}
		}
	}
	if memory.LimitKnowledge == "unknown" {
		for _, mount := range parseCgroupMounts(mounts) {
			if mount.version != 1 || !mount.controllers["memory"] || groups.memory == "" {
				continue
			}
			if target, ok := mount.target(groups.memory); ok {
				if data, err := readKernelFile(filepath.Join(target, "memory.limit_in_bytes")); err == nil {
					value, valid := memoryMax(data)
					if valid {
						memory.CgroupLimitBytes, memory.LimitKnowledge = value, "partial"
					}
				}
			}
		}
	}
	// Even an ordinary-looking mount may belong to a cgroup namespace that
	// hides an outer limit. Keep visible facts, without claiming an effective
	// budget until that ancestry can be established independently.
	return cpu, memory
}

func readKernelFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumKernelFileBytes+1))
	if err != nil || len(data) > maximumKernelFileBytes {
		return "", errors.New("resource file unavailable or exceeds bound")
	}
	return string(data), nil
}

func memTotal(data string) *int64 {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemTotal:" && fields[2] == "kB" {
			value, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil && value > 0 && value <= execution.MaximumSafeInteger/1024 {
				return number(value * 1024)
			}
		}
	}
	return nil
}

type cgroupPaths struct{ unified, cpu, memory string }

func parseCgroups(data string) cgroupPaths {
	var result cgroupPaths
	for _, line := range strings.Split(data, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || !cleanAbsolute(parts[2]) {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			result.unified = parts[2]
		}
		for _, controller := range strings.Split(parts[1], ",") {
			switch controller {
			case "cpu":
				result.cpu = parts[2]
			case "memory":
				result.memory = parts[2]
			}
		}
	}
	return result
}

type cgroupMount struct {
	version     int
	root, path  string
	controllers map[string]bool
}

func parseCgroupMounts(data string) []cgroupMount {
	var result []cgroupMount
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 6 || len(fields) < separator+4 || len(fields) < 6 {
			continue
		}
		version := 0
		switch fields[separator+1] {
		case "cgroup2":
			version = 2
		case "cgroup":
			version = 1
		}
		root, path := unescapeMount(fields[3]), unescapeMount(fields[4])
		if version == 0 || !cleanAbsolute(root) || !cleanAbsolute(path) {
			continue
		}
		controllers := make(map[string]bool)
		for _, controller := range strings.Split(fields[separator+3], ",") {
			controllers[controller] = true
		}
		result = append(result, cgroupMount{version: version, root: root, path: path, controllers: controllers})
		if len(result) == 64 {
			break
		}
	}
	return result
}

func unescapeMount(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && len(path) <= 4096 && !strings.ContainsRune(path, '\x00')
}

func (mount cgroupMount) target(group string) (string, bool) {
	if group != mount.root && !strings.HasPrefix(group, strings.TrimRight(mount.root, "/")+"/") {
		return "", false
	}
	relative, err := filepath.Rel(mount.root, group)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", false
	}
	return filepath.Join(mount.path, relative), true
}

// A cgroup namespace can hide outer ancestors even when its mount looks like
// a full hierarchy. Readable limits are useful bounds, always marked partial.
func hierarchyLimit(target string, mount cgroupMount, filename string, parse func(string) (*int64, bool)) (*int64, string) {
	knowledge := "unknown"
	var tightest *int64
	for depth := 0; depth < 64; depth++ {
		data, err := readKernelFile(filepath.Join(target, filename))
		if value, valid := parse(data); err == nil && valid {
			knowledge = "partial"
			if value != nil && (tightest == nil || *value < *tightest) {
				tightest = value
			}
		}
		if target == mount.path {
			return tightest, knowledge
		}
		parent := filepath.Dir(target)
		if parent == target || parent != mount.path && !strings.HasPrefix(parent, mount.path+"/") {
			return tightest, knowledge
		}
		target = parent
	}
	return tightest, knowledge
}

func cpuMax(data string) (*int64, bool) {
	fields := strings.Fields(data)
	if len(fields) != 2 {
		return nil, false
	}
	period, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || period <= 0 || period > execution.MaximumSafeInteger {
		return nil, false
	}
	if fields[0] == "max" {
		return nil, true
	}
	quota, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || quota <= 0 || quota > execution.MaximumSafeInteger/1000 {
		return nil, false
	}
	value := quota * 1000 / period
	if value <= 0 {
		return nil, false
	}
	return number(value), true
}

func memoryMax(data string) (*int64, bool) {
	value := strings.TrimSpace(data)
	if value == "max" {
		return nil, true
	}
	bytes, err := strconv.ParseInt(value, 10, 64)
	if err != nil || bytes <= 0 || bytes > execution.MaximumSafeInteger {
		return nil, false
	}
	return number(bytes), true
}

func filesystem(path string) (string, execution.Filesystem, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", execution.Filesystem{}, false
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", execution.Filesystem{}, false
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return "", execution.Filesystem{}, false
	}
	result := execution.Filesystem{CapacityBytes: bytesNumber(fs.Blocks, fs.Bsize), FreeBytes: bytesNumber(fs.Bfree, fs.Bsize), AvailableBytes: bytesNumber(fs.Bavail, fs.Bsize)}
	if result.CapacityBytes == nil || *result.CapacityBytes == 0 || result.FreeBytes == nil || result.AvailableBytes == nil || *result.FreeBytes > *result.CapacityBytes || *result.AvailableBytes > *result.FreeBytes {
		return "", execution.Filesystem{}, false
	}
	return fmt.Sprintf("linux-device:%d", status.Dev), result, true
}
