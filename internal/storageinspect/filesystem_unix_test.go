//go:build linux || darwin

package storageinspect_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/layercache/layercache/internal/storageinspect"
)

func TestInspectionReportsSparseAllocationInsteadOfApparentSize(t *testing.T) {
	root := directory(t)
	path := filepath.Join(root, "sparse.qcow2")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64 << 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("data"), 32<<20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	allocated := uint64(info.Sys().(*syscall.Stat_t).Blocks) * 512
	if allocated >= uint64(info.Size()) {
		t.Skip("fixture filesystem does not expose sparse allocation")
	}
	report, err := storageinspect.Inspect(context.Background(), []string{path})
	if err != nil || report.Incomplete || report.Totals.ApparentBytes != 64<<20 || report.Totals.AllocatedBytes == nil || *report.Totals.AllocatedBytes != allocated || report.Totals.SparseFiles != 1 {
		t.Fatalf("sparse allocation = %+v, %v; want allocated %d", report, err, allocated)
	}
	volume := report.Roots[0].Filesystem
	if volume.DeviceID == nil || volume.Type == "unknown" || volume.CapacityBytes == nil || volume.AvailableBytes == nil {
		t.Fatalf("Unix filesystem information unavailable: %+v", volume)
	}
}

func TestInspectionReportsUnreadableDirectoryWithoutDeletingIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode-000 directories")
	}
	root := directory(t)
	blocked := filepath.Join(root, "blocked")
	write(t, filepath.Join(blocked, "private"), "data")
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	report, err := storageinspect.Inspect(context.Background(), []string{root})
	if err != nil || !report.Incomplete || report.IssueCount != 1 || report.Totals.Files != 0 || report.Totals.Directories != 2 {
		t.Fatalf("unreadable directory = %+v, %v", report, err)
	}
}
