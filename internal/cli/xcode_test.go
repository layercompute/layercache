package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/xcodecache"
)

func TestXcodeCLIServeStatusAndStop(t *testing.T) {
	root := xcodeCLITempDir(t)
	readyFile := filepath.Join(root, "ready.json")
	_, done := startXcodeCLIService(t, root, readyFile)
	want := waitForXcodeCLIReady(t, readyFile, done)

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"xcode", "status", "--ready-file", readyFile}, &stdout, &stderr); err != nil {
		t.Fatalf("status: %v\n%s", err, &stderr)
	}
	var got xcodecache.LifecycleStatus
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, &stdout)
	}
	if got.InstanceID != want.InstanceID || got.Phase != "ready" || got.Project != "cli-project" ||
		got.Compatibility != "cli-toolchain" || got.Socket != filepath.Join(root, "cache.sock") ||
		got.StoreClosed || got.OwnershipReleased {
		t.Fatalf("status did not describe the live instance: %#v", got)
	}

	stdout.Reset()
	stderr.Reset()
	if err := Run(context.Background(), []string{"xcode", "stop", "--ready-file", readyFile}, &stdout, &stderr); err != nil {
		t.Fatalf("stop: %v\n%s", err, &stderr)
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stop JSON: %v\n%s", err, &stdout)
	}
	if got.InstanceID != want.InstanceID || got.Phase != "stopped" || !got.StoreClosed || !got.OwnershipReleased {
		t.Fatalf("stop did not acknowledge the same closed instance: %#v", got)
	}
	waitForXcodeCLIExit(t, done)
	if _, err := os.Lstat(filepath.Join(root, "cache.sock")); !os.IsNotExist(err) {
		t.Fatalf("stopped compiler socket still exists: %v", err)
	}

	stdout.Reset()
	if err := Run(context.Background(), []string{"xcode", "status", "--ready-file", readyFile}, &stdout, &stderr); err != nil {
		t.Fatalf("closed status: %v\n%s", err, &stderr)
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.InstanceID != want.InstanceID || got.Phase != "stopped" || !got.StoreClosed || !got.OwnershipReleased {
		t.Fatalf("closed receipt changed identity or closure guarantees: %#v", got)
	}
}

func TestXcodeCLIServeClosesOnContextCancellation(t *testing.T) {
	root := xcodeCLITempDir(t)
	readyFile := filepath.Join(root, "ready.json")
	cancel, done := startXcodeCLIService(t, root, readyFile)
	want := waitForXcodeCLIReady(t, readyFile, done)
	cancel()
	waitForXcodeCLIExit(t, done)

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"xcode", "status", "--ready-file", readyFile}, &stdout, &stderr); err != nil {
		t.Fatalf("status after cancellation: %v\n%s", err, &stderr)
	}
	var got xcodecache.LifecycleStatus
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.InstanceID != want.InstanceID || got.Phase != "stopped" || !got.StoreClosed || !got.OwnershipReleased {
		t.Fatalf("cancellation did not close the served instance: %#v", got)
	}
}

func TestXcodeCLIRunPreservesChildArguments(t *testing.T) {
	root := xcodeCLITempDir(t)
	child := filepath.Join(root, "child.sh")
	if err := os.WriteFile(child, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "cache.sock")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"xcode", "run", "--data-dir", filepath.Join(root, "store"), "--socket", socket,
		"--project", "cli-project", "--compatibility", "cli-toolchain", "--max-size", "1048576",
		"--", child, "a value", "--child-flag",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, &stderr)
	}
	want := "a value\n--child-flag\nCOMPILATION_CACHE_ENABLE_CACHING=YES\n" +
		"COMPILATION_CACHE_ENABLE_PLUGIN=YES\nCOMPILATION_CACHE_REMOTE_SERVICE_PATH=" + socket + "\n" +
		"COMPILATION_CACHE_ENABLE_DETACHED_KEY_QUERIES=YES\nCOMPILATION_CACHE_ENABLE_DIAGNOSTIC_REMARKS=YES\n"
	if stdout.String() != want {
		t.Fatalf("compiler arguments changed:\ngot %q\nwant %q", stdout.String(), want)
	}
}

func TestXcodeCLIRejectsMalformedLifecycleArguments(t *testing.T) {
	root := xcodeCLITempDir(t)
	readyFile := filepath.Join(root, "ready.json")
	serve := []string{"xcode", "serve", "--data-dir", filepath.Join(root, "store"), "--socket", filepath.Join(root, "cache.sock"), "--project", "cli-project", "--compatibility", "cli-toolchain"}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing subcommand", []string{"xcode"}, "use xcode"},
		{"unknown subcommand", []string{"xcode", "restart"}, "use xcode"},
		{"serve missing data", []string{"xcode", "serve", "--ready-file", readyFile}, "data-dir is required"},
		{"serve missing ready file", serve, "absolute ready-file"},
		{"serve relative ready file", append(append([]string{}, serve...), "--ready-file", "ready.json"), "absolute ready-file"},
		{"serve child", append(append([]string{}, serve...), "--ready-file", readyFile, "--", "xcodebuild"), "child command"},
		{"serve positional", append(append([]string{}, serve...), "--ready-file", readyFile, "extra"), "positional"},
		{"status missing ready file", []string{"xcode", "status"}, "absolute ready-file"},
		{"status relative ready file", []string{"xcode", "status", "--ready-file", "ready.json"}, "absolute ready-file"},
		{"status positional", []string{"xcode", "status", "--ready-file", readyFile, "extra"}, "positional"},
		{"status service option", []string{"xcode", "status", "--ready-file", readyFile, "--data-dir", root}, "flag provided but not defined"},
		{"stop missing ready file", []string{"xcode", "stop"}, "absolute ready-file"},
		{"stop relative ready file", []string{"xcode", "stop", "--ready-file", "ready.json"}, "absolute ready-file"},
		{"stop child", []string{"xcode", "stop", "--ready-file", readyFile, "--", "xcodebuild"}, "positional"},
		{"stop service option", []string{"xcode", "stop", "--ready-file", readyFile, "--socket", filepath.Join(root, "cache.sock")}, "flag provided but not defined"},
		{"run missing child", []string{"xcode", "run", "--data-dir", filepath.Join(root, "store")}, "command required after --"},
		{"run lifecycle option", []string{"xcode", "run", "--data-dir", filepath.Join(root, "store"), "--ready-file", readyFile}, "flag provided but not defined"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), test.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run(%v) = %v, want error containing %q", test.args, err, test.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("invalid arguments produced stdout: %s", &stdout)
			}
		})
	}
	if _, err := os.Lstat(filepath.Join(root, "store")); !os.IsNotExist(err) {
		t.Fatalf("invalid arguments initialized a cache store: %v", err)
	}
}

func xcodeCLITempDir(t *testing.T) string {
	t.Helper()
	// The Darwin Unix socket limit can be shorter than testing.T's temp path.
	root, err := os.MkdirTemp("/tmp", "lc-cli-xcode-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func startXcodeCLIService(t *testing.T, root, readyFile string) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{
			"xcode", "serve", "--data-dir", filepath.Join(root, "store"),
			"--socket", filepath.Join(root, "cache.sock"), "--ready-file", readyFile,
			"--project", "cli-project", "--compatibility", "cli-toolchain",
			"--stats-file", filepath.Join(root, "stats.json"), "--max-size", "1048576",
		}, io.Discard, io.Discard)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Xcode CLI service did not stop during cleanup")
		}
	})
	return cancel, done
}

func waitForXcodeCLIReady(t *testing.T, readyFile string, done <-chan error) xcodecache.LifecycleStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		status, err := xcodecache.Status(ctx, readyFile)
		if err == nil && status.Phase == "ready" && status.InstanceID != "" {
			return status
		}
		lastErr = err
		select {
		case err := <-done:
			t.Fatalf("Xcode CLI service exited before readiness: %v", err)
		case <-ctx.Done():
			t.Fatalf("Xcode CLI service did not become ready: %v", lastErr)
		case <-ticker.C:
		}
	}
}

func waitForXcodeCLIExit(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Xcode CLI service exit: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Xcode CLI service did not acknowledge stop")
	}
}
