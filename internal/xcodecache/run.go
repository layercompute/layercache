package xcodecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type Options struct {
	DataDir, Socket, Project, Compatibility, StatsFile string
	MaxBytes                                           int64
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
	if !filepath.IsAbs(options.Socket) || len(options.Socket) > 100 {
		return errors.New("an absolute Unix socket path of at most 100 bytes is required")
	}
	if options.DataDir == "" || options.MaxBytes <= 0 {
		return errors.New("a data directory and positive compilation-cache quota are required")
	}
	// Validate identity before recovery so invalid configuration cannot bypass it.
	if err := validateIdentity(options.Project, options.Compatibility); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(options.Socket), 0700); err != nil {
		return uncached(ctx, options, command, stdout, stderr, err)
	}
	parent, err := os.Lstat(filepath.Dir(options.Socket))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return errors.New("socket parent must be a private directory")
	}
	if info, ok := parent.Sys().(*syscall.Stat_t); !ok || info.Uid != uint32(os.Geteuid()) {
		return errors.New("socket parent must belong to the current user")
	}
	lock, err := lockDirectory(options.DataDir)
	if err != nil {
		if errors.Is(err, errDirectoryInUse) {
			return err
		}
		return uncached(ctx, options, command, stdout, stderr, err)
	}
	defer lock.Close()
	listener, err := net.Listen("unix", options.Socket)
	if err != nil {
		return fmt.Errorf("listen for Xcode cache: %w", err)
	}
	defer listener.Close()
	if err = os.Chmod(options.Socket, 0600); err != nil {
		return err
	}
	store, err := artifact.Open(ctx, options.DataDir, options.MaxBytes, 0)
	if err != nil {
		return uncached(ctx, options, command, stdout, stderr, err)
	}
	defer store.Close()
	// Only this runtime, holding the exclusive directory lock, can release
	// interrupted compiler restores from the previous adapter process.
	if err = store.ResetReadLeases(ctx, readLeaseNamespace); err != nil {
		return uncached(ctx, options, command, stdout, stderr, err)
	}
	service, err := New(store, options.Project, options.Compatibility)
	if err != nil {
		return err
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(MaxMessageBytes), grpc.MaxSendMsgSize(MaxMessageBytes), grpc.MaxConcurrentStreams(16), grpc.WaitForHandlers(true))
	cas.RegisterCASDBServiceServer(server, service)
	kv.RegisterKeyValueDBServer(server, service)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer server.Stop()
	arguments := append(append([]string{}, command[1:]...),
		"COMPILATION_CACHE_ENABLE_CACHING=YES", "COMPILATION_CACHE_ENABLE_PLUGIN=YES",
		"COMPILATION_CACHE_REMOTE_SERVICE_PATH="+options.Socket,
		"COMPILATION_CACHE_ENABLE_DETACHED_KEY_QUERIES=YES", "COMPILATION_CACHE_ENABLE_DIAGNOSTIC_REMARKS=YES")
	err = runChild(ctx, command, arguments, stdout, stderr)
	server.Stop()
	<-done
	stats := runStats{Stats: service.Stats()}
	if closeErr := store.Close(); closeErr != nil {
		stats.CacheErrors++
		fmt.Fprintf(stderr, "Layer Cache closure failed: %v\n", closeErr)
	}
	// A cache error does not establish why the compiler failed. Preserve its
	// result instead of rerunning a genuine compiler failure or masking it.
	reportStats(options.StatsFile, stats, stderr)
	return err
}
