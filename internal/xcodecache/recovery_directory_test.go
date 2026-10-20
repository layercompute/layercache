//go:build linux || darwin

package xcodecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const directoryOpenFailureChild = "LAYERCACHE_XCODE_DIRECTORY_OPEN_FAILURE_CHILD"

// Resource exhaustion makes a real directory open fail without changing its
// ownership or mode. Keep the resource limit isolated from the main test process.
func TestDirectoryOpenFailureKeepsUncachedCompilerFallback(t *testing.T) {
	for _, mode := range []string{"store", "compiler-success", "compiler-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDirectoryOpenFailureChild$")
			child.Env = append(os.Environ(), directoryOpenFailureChild+"="+mode)
			output, err := child.CombinedOutput()
			if err != nil {
				t.Fatalf("directory-open regression: %v\n%s", err, output)
			}
		})
	}
}

type releaseDescriptorsWriter struct {
	bytes.Buffer
	release func()
}

func (w *releaseDescriptorsWriter) Write(data []byte) (int, error) {
	// The warning precedes compiler startup. Release the artificial exhaustion
	// at that boundary so the compiler itself can open its executable and files.
	w.release()
	return w.Buffer.Write(data)
}

func TestDirectoryOpenFailureChild(t *testing.T) {
	mode := os.Getenv(directoryOpenFailureChild)
	if mode == "" {
		return
	}
	options := reviewOptions(t)
	if err := os.MkdirAll(options.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAYERCACHE_XCODE_CHILD", "1")
	t.Setenv("LAYERCACHE_XCODE_QUOTA_TEST", "1")
	t.Setenv("LAYERCACHE_XCODE_CHILD_FAIL", map[bool]string{false: "0", true: "1"}[mode == "compiler-failure"])

	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &original); err != nil {
		t.Fatal(err)
	}
	limited := original
	if limited.Cur > 64 {
		limited.Cur = 64
	}
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &limited); err != nil {
		t.Fatal(err)
	}
	defer unix.Setrlimit(unix.RLIMIT_NOFILE, &original)
	var descriptors []int
	release := func() {
		for _, descriptor := range descriptors {
			unix.Close(descriptor)
		}
		descriptors = nil
	}
	defer release()
	for {
		descriptor, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.EMFILE) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		descriptors = append(descriptors, descriptor)
	}

	if mode == "store" {
		store, err := lockDirectory(options.DataDir)
		release()
		if store != nil {
			store.Close()
			t.Fatal("store opened despite descriptor exhaustion")
		}
		if !errors.Is(err, unix.EMFILE) || errors.Is(err, errUnsafePath) {
			t.Fatalf("ordinary store unavailability was classified as an unsafe path: %v", err)
		}
		return
	}

	var stdout bytes.Buffer
	stderr := &releaseDescriptorsWriter{release: release}
	err := Run(context.Background(), options, compilerCommand(), &stdout, stderr)
	release()
	if mode == "compiler-failure" {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 7 {
			t.Fatalf("uncached compiler exit was not preserved: %v\n%s", err, stderr.String())
		}
	} else if err != nil {
		t.Fatalf("directory open failure blocked the uncached compiler: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "startup unavailable") {
		t.Fatalf("uncached startup warning absent: %s", stderr.String())
	}
	data, err := os.ReadFile(options.StatsFile)
	if err != nil {
		t.Fatal(err)
	}
	var stats runStats
	if err := json.Unmarshal(data, &stats); err != nil || !stats.UncachedFallback || stats.CacheErrors != 1 {
		t.Fatalf("uncached fallback receipt missing: %s %v", data, err)
	}
	if _, err := os.Lstat(options.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed startup left an RPC socket: %v", err)
	}
}

func TestUnsafeDirectoryCannotUseUncachedCompilerFallback(t *testing.T) {
	for _, kind := range []string{"store-symlink", "store-mode", "store-unreadable", "socket-symlink"} {
		t.Run(kind, func(t *testing.T) {
			options := reviewOptions(t)
			switch kind {
			case "store-symlink":
				if err := os.Symlink(t.TempDir(), options.DataDir); err != nil {
					t.Fatal(err)
				}
			case "store-mode", "store-unreadable":
				if err := os.MkdirAll(options.DataDir, 0700); err != nil {
					t.Fatal(err)
				}
				mode := os.FileMode(0755)
				if kind == "store-unreadable" {
					mode = 0000
				}
				if err := os.Chmod(options.DataDir, mode); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(options.DataDir, 0700)
			case "socket-symlink":
				alias := filepath.Join(filepath.Dir(options.Socket), "alias")
				if err := os.Symlink(filepath.Dir(options.Socket), alias); err != nil {
					t.Fatal(err)
				}
				options.Socket = filepath.Join(alias, "cache.sock")
			}
			var output bytes.Buffer
			err := Run(context.Background(), options, []string{"must-not-start"}, &output, &output)
			if !errors.Is(err, errUnsafePath) || strings.Contains(output.String(), "startup unavailable") {
				t.Fatalf("unsafe directory reached uncached fallback: %v\n%s", err, output.String())
			}
		})
	}
}
