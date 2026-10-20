package xcodecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

type Options struct {
	DataDir, Socket, Project, Compatibility, StatsFile, ReadyFile string
	MaxBytes                                                      int64
}

type runStats struct {
	Stats
	UncachedFallback bool `json:"uncachedFallback"`
	UncachedRetry    bool `json:"uncachedRetry"`
}

func runChild(ctx context.Context, command []string, arguments []string, stdout, stderr io.Writer) error {
	child := exec.CommandContext(ctx, command[0], arguments...)
	child.Stdout, child.Stderr, child.Stdin = stdout, stderr, os.Stdin
	child.WaitDelay = 5 * time.Second
	return child.Run()
}

func reportStats(path string, stats runStats, stderr io.Writer) {
	if path == "" {
		return
	}
	encoded, err := json.MarshalIndent(stats, "", "  ")
	if err == nil {
		err = os.WriteFile(path, append(encoded, '\n'), 0600)
	}
	if err != nil {
		fmt.Fprintf(stderr, "Layer Cache statistics unavailable: %v\n", err)
	}
}

func uncached(ctx context.Context, options Options, command []string, stdout, stderr io.Writer, reason error) error {
	fmt.Fprintf(stderr, "Layer Cache startup unavailable; compiling without the cache: %v\n", reason)
	args := append(append([]string{}, command[1:]...), "COMPILATION_CACHE_ENABLE_CACHING=NO", "COMPILATION_CACHE_ENABLE_PLUGIN=NO", "COMPILATION_CACHE_REMOTE_SERVICE_PATH=")
	err := runChild(ctx, command, args, stdout, stderr)
	reportStats(options.StatsFile, runStats{Stats: Stats{CacheErrors: 1}, UncachedFallback: true}, stderr)
	return err
}

// Run owns the store until all RPC handlers stop and SQLite closes. Only then
// may callers snapshot it; a second owner cannot run startup reconciliation.
func Run(ctx context.Context, options Options, command []string, stdout, stderr io.Writer) error {
	if len(command) == 0 {
		return errors.New("xcodebuild command required")
	}
	runtime, err := startRuntime(ctx, options)
	if err != nil {
		var fallback startupFailure
		if errors.As(err, &fallback) {
			return uncached(ctx, options, command, stdout, stderr, err)
		}
		return err
	}
	defer runtime.Close()
	arguments := append(append([]string{}, command[1:]...),
		"COMPILATION_CACHE_ENABLE_CACHING=YES", "COMPILATION_CACHE_ENABLE_PLUGIN=YES",
		"COMPILATION_CACHE_REMOTE_SERVICE_PATH="+options.Socket,
		"COMPILATION_CACHE_ENABLE_DETACHED_KEY_QUERIES=YES", "COMPILATION_CACHE_ENABLE_DIAGNOSTIC_REMARKS=YES")
	err = runChild(ctx, command, arguments, stdout, stderr)
	closeErr := runtime.Close()
	stats := runStats{Stats: runtime.service.Stats()}
	if closeErr != nil {
		stats.CacheErrors++
		fmt.Fprintf(stderr, "Layer Cache closure failed: %v\n", closeErr)
	}
	// A cache error does not establish why the compiler failed. Preserve its
	// result instead of rerunning a genuine compiler failure or masking it.
	reportStats(options.StatsFile, stats, stderr)
	return err
}
