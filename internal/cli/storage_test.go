package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/storageinspect"
)

func storageFixture(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStorageInspectIsAccountFreeAndPreservesUnfinishedCacheData(t *testing.T) {
	root := storageFixture(t)
	state := storageFixture(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(state, "absent-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(state, "absent-cache"))
	staging := filepath.Join(root, "staging")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(staging, "unfinished-upload")
	if err := os.WriteFile(payload, []byte("must survive inspection"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(payload)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{"storage", "inspect", "--path", root, "--path", staging, "--json"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("inspection: %v; stderr=%s", err, stderr.String())
	}
	var report storageinspect.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Incomplete || report.Totals.Files != 1 || report.Totals.Directories != 2 || len(report.Roots) != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("inspection initialized configuration/cache: %v, %v", entries, err)
	}
	after, err := os.Stat(payload)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		t.Fatalf("inspection changed staging file: before=%v after=%v error=%v", before, after, err)
	}
	content, err := os.ReadFile(payload)
	if err != nil || string(content) != "must survive inspection" {
		t.Fatalf("staging content: %q, %v", content, err)
	}
}

func TestStorageInspectPartialJSONReturnsFailureAndKeepsSuccessfulRoot(t *testing.T) {
	root := storageFixture(t)
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, _, err := callCLI("storage", "inspect", "--path", root, "--path", filepath.Join(root, "missing"), "--json")
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("partial scan succeeded: %v, %s", err, output)
	}
	var report storageinspect.Report
	if decodeErr := json.Unmarshal([]byte(output), &report); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !report.Incomplete || report.IssueCount != 1 || len(report.Issues) != 1 || report.Totals.Files != 1 {
		t.Fatalf("partial report: %+v", report)
	}
}

func TestStorageInspectHumanOutputExplainsAccountingLimits(t *testing.T) {
	root := storageFixture(t)
	if err := os.Mkdir(filepath.Join(root, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	output, _, err := callCLI("storage", "inspect", "--path", root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Allocated inode bytes", "Apparent bytes", "build-output", "categories do not identify deletable data", "not exclusive physical usage or reclaimable space"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %s", want, output)
		}
	}
	after, err := os.ReadDir(root)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("inspection changed root: %v, %v, %v", before, after, err)
	}
}

func TestStorageInspectRequiresExplicitPathsAndRejectsUnknownOptions(t *testing.T) {
	for _, args := range [][]string{
		{"storage"},
		{"storage", "delete"},
		{"storage", "inspect"},
		{"storage", "inspect", "/tmp"},
		{"storage", "inspect", "--path", "relative"},
		{"storage", "inspect", "--path", "/tmp", "--delete"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := callCLI(args...)
			if err == nil {
				t.Fatalf("accepted invalid arguments: %v", args)
			}
		})
	}
}

func TestStorageInspectCancellationReturnsPartialJSONAndCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	err := Run(ctx, []string{"storage", "inspect", "--path", storageFixture(t), "--json"}, &stdout, &stderr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation cause: %v", err)
	}
	var report storageinspect.Report
	if decodeErr := json.Unmarshal(stdout.Bytes(), &report); decodeErr != nil || !report.Incomplete {
		t.Fatalf("canceled report: %s, %v", stdout.String(), decodeErr)
	}
}
