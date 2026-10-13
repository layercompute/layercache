package xcodecache

import (
	"bytes"
	"context"
	"errors"
	"github.com/layercache/layercache/internal/artifact"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reviewOptions(t *testing.T) Options {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "lc-review-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return Options{DataDir: filepath.Join(t.TempDir(), "store"), Socket: filepath.Join(root, "cache.sock"), Project: "test-project", Compatibility: "test-toolchain", StatsFile: filepath.Join(root, "stats.json"), MaxBytes: 32 << 20}
}

func compilerCommand() []string {
	return []string{os.Args[0], "-test.run=^TestXcodeCompilerChild$", "--"}
}

func TestStartupCacheFailureUsesUncachedCompilerAndPreservesItsResult(t *testing.T) {
	t.Setenv("LAYERCACHE_XCODE_CHILD", "1")
	t.Setenv("LAYERCACHE_XCODE_QUOTA_TEST", "1")
	for _, badStore := range []string{"directory", "metadata"} {
		for _, failed := range []bool{false, true} {
			t.Run(badStore+map[bool]string{false: "-success", true: "-failure"}[failed], func(t *testing.T) {
				options := reviewOptions(t)
				if badStore == "directory" {
					if err := os.WriteFile(options.DataDir, []byte("not a directory"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.MkdirAll(options.DataDir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(options.DataDir, "metadata.db"), []byte("invalid SQLite metadata"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("LAYERCACHE_XCODE_CHILD_FAIL", map[bool]string{false: "0", true: "1"}[failed])
				var output bytes.Buffer
				err := Run(context.Background(), options, compilerCommand(), &output, &output)
				if failed {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 7 {
						t.Fatalf("compiler result: %v %s", err, &output)
					}
				} else if err != nil {
					t.Fatalf("uncached compilation: %v %s", err, &output)
				}
				if !strings.Contains(output.String(), "startup unavailable") {
					t.Fatalf("missing warning: %s", &output)
				}
				stats, err := os.ReadFile(options.StatsFile)
				if err != nil || !bytes.Contains(stats, []byte(`"uncachedFallback": true`)) {
					t.Fatalf("fallback receipt: %s %v", stats, err)
				}
				if _, err = os.Lstat(options.Socket); !os.IsNotExist(err) {
					t.Fatalf("socket leaked: %v", err)
				}
			})
		}
	}
}

func TestStatsOutageCannotReplaceSuccessfulOrFailedCompilerResult(t *testing.T) {
	t.Setenv("LAYERCACHE_XCODE_CHILD", "1")
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			options := reviewOptions(t)
			options.StatsFile = filepath.Join(filepath.Dir(options.Socket), "missing", "stats.json")
			t.Setenv("LAYERCACHE_XCODE_CHILD_FAIL", map[bool]string{false: "0", true: "1"}[failed])
			var output bytes.Buffer
			err := Run(context.Background(), options, compilerCommand(), &output, &output)
			if failed {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Fatalf("compiler result: %v %s", err, &output)
				}
			} else if err != nil {
				t.Fatalf("successful compiler: %v %s", err, &output)
			}
			if !strings.Contains(output.String(), "statistics unavailable") {
				t.Fatalf("missing statistics warning: %s", &output)
			}
		})
	}
}

func TestSharedSocketParentIsRejectedBeforeStartingCompiler(t *testing.T) {
	options := reviewOptions(t)
	if err := os.Chmod(filepath.Dir(options.Socket), 0755); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := Run(context.Background(), options, []string{"must-not-start"}, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "private directory") {
		t.Fatalf("shared parent accepted: %v", err)
	}
	if _, err = os.Lstat(options.Socket); !os.IsNotExist(err) {
		t.Fatalf("socket created: %v", err)
	}
}

func TestSecondSocketOwnerCannotDisturbAnActiveUpload(t *testing.T) {
	options := reviewOptions(t)
	owner, err := lockDirectory(options.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	store, err := artifact.Open(context.Background(), options.DataDir, 32<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := artifact.Key{Integration: "xcode", Project: "test-project", Compatibility: "test-toolchain", Native: "active-upload", Version: "1"}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	finished := make(chan error, 1)
	go func() {
		_, _, err := store.Put(context.Background(), key, artifact.Metadata{}, reader)
		finished <- err
	}()
	if _, err = writer.Write([]byte("active upload")); err != nil {
		t.Fatal(err)
	}
	options.Socket = filepath.Join(filepath.Dir(options.Socket), "second.sock")
	var output bytes.Buffer
	err = Run(context.Background(), options, []string{"must-not-start"}, &output, &output)
	if !errors.Is(err, errDirectoryInUse) {
		t.Fatalf("second owner accepted: %v %s", err, &output)
	}
	entries, err := os.ReadDir(filepath.Join(options.DataDir, "staging"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("active staging removed: %v %v", entries, err)
	}
	writer.Close()
	select {
	case err = <-finished:
		if err != nil {
			t.Fatalf("active upload disturbed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active upload did not complete")
	}
	_, cached, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(cached)
	cached.Close()
	if err != nil || string(data) != "active upload" {
		t.Fatalf("active bytes lost: %s %v", data, err)
	}
	store.Close()
	owner.Close()
	// Ownership releases only after closure; a successor can start normally.
	t.Setenv("LAYERCACHE_XCODE_CHILD", "1")
	if err = Run(context.Background(), options, compilerCommand(), &output, &output); err != nil {
		t.Fatalf("successor: %v %s", err, &output)
	}
}
