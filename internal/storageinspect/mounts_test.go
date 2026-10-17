//go:build linux || darwin

package storageinspect

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectionSkipsMountedFilesAndDirectoriesButKeepsLaterSiblings(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a-before", "b-mounted-file", "c-after-file", "d-mounted-directory/payload", "e-after-directory"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mountedFile := filepath.Join(root, "b-mounted-file")
	mounts := map[string]bool{
		mountedFile: true,
		filepath.Join(root, "d-mounted-directory"): true,
	}
	// Supplied mount metadata makes the same-device file-bind case testable
	// through real traversal without creating privileged mounts.
	report, err := inspectRoots(context.Background(), []string{root}, mounts)
	if err != nil || report.Incomplete || report.SkippedMounts != 2 || report.Totals.Files != 3 || report.Totals.Directories != 1 {
		t.Fatalf("nested mounts = %+v, %v", report, err)
	}
	explicit, err := inspectRoots(context.Background(), []string{root, mountedFile}, mounts)
	if err != nil || explicit.Incomplete || explicit.SkippedMounts != 2 || explicit.Totals.Files != 4 || explicit.Totals.Directories != 1 {
		t.Fatalf("explicit mounted file = %+v, %v", explicit, err)
	}
	onlyFile, err := inspectRoots(context.Background(), []string{mountedFile}, mounts)
	if err != nil || onlyFile.Incomplete || onlyFile.SkippedMounts != 0 || onlyFile.Totals.Files != 1 {
		t.Fatalf("mounted file selected as root = %+v, %v", onlyFile, err)
	}
	info, err := os.Lstat(mountedFile)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Totals.ApparentBytes != report.Totals.ApparentBytes+uint64(info.Size()) {
		t.Fatalf("explicit mount did not add its exact payload size: before=%+v after=%+v", report.Totals, explicit.Totals)
	}
}
