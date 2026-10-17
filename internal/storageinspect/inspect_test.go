package storageinspect_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/storageinspect"
)

func directory(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionCountsOverlappingRootsAndInodeAliasesOnce(t *testing.T) {
	root := directory(t)
	first := filepath.Join(root, "node_modules", "package", "index.js")
	write(t, first, "dependency payload")
	alias := filepath.Join(root, "linked.js")
	if err := os.Link(first, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	write(t, filepath.Join(root, "build", "output"), "compiled payload")
	paths := []string{root, filepath.Join(root, "node_modules"), first, root + string(filepath.Separator)}
	report, err := storageinspect.Inspect(context.Background(), paths)
	if err != nil || report.Incomplete {
		t.Fatalf("Inspect = %+v, %v", report, err)
	}
	if report.Totals.Files != 2 || report.Totals.Directories != 4 || report.HardlinkAliases != 1 || len(report.Roots) != 3 {
		t.Fatalf("unique counts = %+v; roots=%+v", report, report.Roots)
	}
	one, err := storageinspect.Inspect(context.Background(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one.Totals, report.Totals) || !reflect.DeepEqual(one.Categories, report.Categories) {
		t.Fatalf("overlap changed counts: single=%+v overlap=%+v", one, report)
	}
	if report.Roots[0].Filesystem.CapacityBytes != nil && (*report.Roots[0].Filesystem.CapacityBytes == 0 || report.Roots[0].Filesystem.AvailableBytes == nil) {
		t.Fatalf("incomplete filesystem capacity: %+v", report.Roots[0].Filesystem)
	}
}

func TestInspectionNeverFollowsSymlinksOrWritesCacheState(t *testing.T) {
	root, outside := directory(t), directory(t)
	write(t, filepath.Join(root, "staging", "unfinished"), "owned data")
	write(t, filepath.Join(outside, "must-not-count"), "outside payload")
	link := filepath.Join(root, "outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(filepath.Join(root, "staging", "unfinished"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := storageinspect.Inspect(context.Background(), []string{root})
	if err != nil || report.Incomplete || report.Totals.Files != 1 || report.Totals.Symlinks != 1 {
		t.Fatalf("symlink scan = %+v, %v", report, err)
	}
	linkReport, err := storageinspect.Inspect(context.Background(), []string{link})
	if err != nil || linkReport.Totals.Symlinks != 1 || linkReport.Totals.Files != 0 || linkReport.Totals.Directories != 0 {
		t.Fatalf("symlink root = %+v, %v", linkReport, err)
	}
	escapeReport, err := storageinspect.Inspect(context.Background(), []string{filepath.Join(link, "must-not-count")})
	if err != nil || !escapeReport.Incomplete || escapeReport.Totals.Files != 0 || escapeReport.IssueCount != 1 {
		t.Fatalf("symlink ancestor = %+v, %v", escapeReport, err)
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("inspection created or deleted entries: before=%v after=%v", before, after)
	}
	content, err := os.ReadFile(filepath.Join(root, "staging", "unfinished"))
	if err != nil || string(content) != "owned data" {
		t.Fatalf("inspection changed staging: %q, %v", content, err)
	}
	afterInfo, err := os.Stat(filepath.Join(root, "staging", "unfinished"))
	if err != nil || beforeInfo.Mode() != afterInfo.Mode() || beforeInfo.ModTime() != afterInfo.ModTime() {
		t.Fatalf("inspection changed file metadata: before=%v after=%v err=%v", beforeInfo, afterInfo, err)
	}
}

func TestInspectionDeduplicatesHardlinksAcrossIndependentRoots(t *testing.T) {
	first, second := directory(t), directory(t)
	write(t, filepath.Join(first, "payload"), "shared bytes")
	if err := os.Link(filepath.Join(first, "payload"), filepath.Join(second, "alias")); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	report, err := storageinspect.Inspect(context.Background(), []string{first, second})
	if err != nil || report.Incomplete || report.Totals.Files != 1 || report.Totals.Directories != 2 || report.HardlinkAliases != 1 {
		t.Fatalf("independent roots = %+v, %v", report, err)
	}
}

func TestInspectionBoundsErrorsAndKeepsSuccessfulRoots(t *testing.T) {
	root := directory(t)
	write(t, filepath.Join(root, "keep"), "payload")
	paths := []string{root}
	for index := range 45 {
		paths = append(paths, filepath.Join(root, fmt.Sprintf("missing-%02d", index)))
	}
	report, err := storageinspect.Inspect(context.Background(), paths)
	if err != nil || !report.Incomplete || report.IssueCount != 45 || report.DisappearedEntries != 45 || len(report.Issues) != 32 || report.Totals.Files != 1 {
		t.Fatalf("bounded partial report = %+v, %v", report, err)
	}
}

func TestInspectionCategoriesAreDisjointPathHints(t *testing.T) {
	root := directory(t)
	for _, name := range []string{"node_modules/package.js", "avd/device/data", "device.qcow2", "system.img", "build/output", ".cxx/output", "DerivedData/output", "source.go"} {
		write(t, filepath.Join(root, name), name)
	}
	report, err := storageinspect.Inspect(context.Background(), []string{root})
	if err != nil || report.Incomplete {
		t.Fatalf("Inspect = %+v, %v", report, err)
	}
	want := map[string]uint64{"dependencies": 1, "emulator": 3, "build-output": 3, "unclassified": 1}
	var apparent uint64
	for _, category := range report.Categories {
		if category.Counts.Files != want[category.Name] {
			t.Fatalf("category %+v, want %d files", category, want[category.Name])
		}
		delete(want, category.Name)
		apparent += category.Counts.ApparentBytes
	}
	if len(want) != 0 || apparent != report.Totals.ApparentBytes {
		t.Fatalf("categories do not partition total: %+v", report)
	}
}

func TestInspectionCategoriesIgnoreAncestorsOutsideTheSelectedRoot(t *testing.T) {
	for _, ancestor := range []string{"build", "node_modules", "avd", "device.avd"} {
		t.Run(ancestor, func(t *testing.T) {
			root := filepath.Join(directory(t), ancestor, "widget")
			write(t, filepath.Join(root, "src", "main.go"), "source")
			write(t, filepath.Join(root, "build", "output"), "build output")
			report, err := storageinspect.Inspect(context.Background(), []string{root})
			if err != nil || report.Incomplete {
				t.Fatalf("Inspect = %+v, %v", report, err)
			}
			files := make(map[string]uint64)
			for _, category := range report.Categories {
				files[category.Name] = category.Counts.Files
			}
			if !reflect.DeepEqual(files, map[string]uint64{"build-output": 1, "unclassified": 1}) {
				t.Fatalf("ancestor %q contaminated categories: %v", ancestor, files)
			}
			withOverlap, err := storageinspect.Inspect(context.Background(), []string{filepath.Join(root, "build"), filepath.Join(root, "src"), root})
			if err != nil || withOverlap.Incomplete || !reflect.DeepEqual(report.Totals, withOverlap.Totals) || !reflect.DeepEqual(report.Categories, withOverlap.Categories) {
				t.Fatalf("overlap changed categories: single=%+v overlap=%+v err=%v", report, withOverlap, err)
			}
		})
	}
}

func TestInspectionCategoriesRetainTheSelectedRootBasename(t *testing.T) {
	for rootName, want := range map[string]string{
		"node_modules": "dependencies", "avd": "emulator", "device.avd": "emulator",
		"build": "build-output", ".cxx": "build-output", "DerivedData": "build-output",
	} {
		t.Run(rootName, func(t *testing.T) {
			root := filepath.Join(directory(t), rootName)
			child := filepath.Join(root, "package")
			write(t, filepath.Join(child, "payload"), "payload")
			report, err := storageinspect.Inspect(context.Background(), []string{root})
			if err != nil || report.Incomplete || len(report.Categories) != 1 || report.Categories[0].Name != want || report.Categories[0].Counts.Files != 1 {
				t.Fatalf("selected %q = %+v, %v; want %q", rootName, report, err, want)
			}
			withOverlap, err := storageinspect.Inspect(context.Background(), []string{child, root})
			if err != nil || withOverlap.Incomplete || !reflect.DeepEqual(report.Totals, withOverlap.Totals) || !reflect.DeepEqual(report.Categories, withOverlap.Categories) {
				t.Fatalf("selected category root changed with overlap: single=%+v overlap=%+v err=%v", report, withOverlap, err)
			}
		})
	}
}

func TestInspectionRejectsImplicitRootsAndHonorsCancellation(t *testing.T) {
	for _, paths := range [][]string{nil, {""}, {"relative"}, {directory(t), "relative"}} {
		if _, err := storageinspect.Inspect(context.Background(), paths); err == nil {
			t.Fatalf("accepted invalid roots %q", paths)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if report, err := storageinspect.Inspect(ctx, []string{directory(t)}); !errors.Is(err, context.Canceled) || !report.Incomplete {
		t.Fatalf("canceled inspection = %+v, %v", report, err)
	}
	root := directory(t)
	write(t, filepath.Join(root, "payload"), "present")
	stepped := &cancelAfterChecks{Context: context.Background(), remaining: 3}
	report, err := storageinspect.Inspect(stepped, []string{root})
	if !errors.Is(err, context.Canceled) || !report.Incomplete || report.Totals.Directories != 1 || len(report.Categories) != 1 {
		t.Fatalf("mid-scan cancellation lost its partial report: %+v, %v", report, err)
	}
}

// A deterministic cancellation during traversal, without a timing-dependent
// large-directory fixture or a production filesystem dependency hook.
type cancelAfterChecks struct {
	context.Context
	remaining int
}

func (ctx *cancelAfterChecks) Err() error {
	ctx.remaining--
	if ctx.remaining < 0 {
		return context.Canceled
	}
	return nil
}

func TestInspectionToleratesConcurrentEntryRemoval(t *testing.T) {
	root := directory(t)
	for index := range 80 {
		write(t, filepath.Join(root, fmt.Sprintf("file-%03d", index)), "payload")
	}
	var worker sync.WaitGroup
	worker.Add(1)
	go func() {
		defer worker.Done()
		for index := range 80 {
			_ = os.Remove(filepath.Join(root, fmt.Sprintf("file-%03d", index)))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := storageinspect.Inspect(ctx, []string{root})
	worker.Wait()
	if err != nil || report.Totals.Files > 80 || len(report.Issues) > 32 {
		t.Fatalf("concurrent removal = %+v, %v", report, err)
	}
}
