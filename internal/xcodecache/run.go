package xcodecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc"
)

type Options struct {
	DataDir, Socket, Project, Compatibility, StatsFile string
	MaxBytes                                           int64
}

// Run starts the cache before xcodebuild and closes SQLite after the child exits.
// Snapshots taken afterwards contain a closed, consistent store. No background
// service or launch agent survives the build.
func Run(ctx context.Context, options Options, command []string, stdout, stderr io.Writer) error {
	if len(command) == 0 {
		return errors.New("xcodebuild command required")
	}
	if !filepath.IsAbs(options.Socket) || len(options.Socket) > 100 {
		return errors.New("an absolute Unix socket path of at most 100 bytes is required")
	}
	if options.MaxBytes <= 0 {
		return errors.New("positive compilation-cache quota required")
	}
	if err := os.MkdirAll(filepath.Dir(options.Socket), 0700); err != nil {
		return err
	}
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
		return err
	}
	defer store.Close()
	service, err := New(store, options.Project, options.Compatibility)
	if err != nil {
		return err
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(MaxMessageBytes), grpc.MaxSendMsgSize(MaxMessageBytes), grpc.MaxConcurrentStreams(16))
	cas.RegisterCASDBServiceServer(server, service)
	kv.RegisterKeyValueDBServer(server, service)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	// Stop is bounded even if the build left an unfinished cache RPC behind.
	defer server.Stop()
	arguments := append([]string{}, command[1:]...)
	arguments = append(arguments,
		"COMPILATION_CACHE_ENABLE_CACHING=YES",
		"COMPILATION_CACHE_ENABLE_PLUGIN=YES",
		"COMPILATION_CACHE_REMOTE_SERVICE_PATH="+options.Socket,
		"COMPILATION_CACHE_ENABLE_DETACHED_KEY_QUERIES=YES",
		"COMPILATION_CACHE_ENABLE_DIAGNOSTIC_REMARKS=YES")
	child := exec.CommandContext(ctx, command[0], arguments...)
	child.Stdout, child.Stderr = stdout, stderr
	child.Stdin = os.Stdin
	child.WaitDelay = 5 * time.Second
	err = child.Run()
	server.Stop()
	<-done
	// Close/checkpoint before reporting success: callers can now safely snapshot.
	closeErr := store.Close()
	if options.StatsFile != "" {
		encoded, encodeErr := json.MarshalIndent(service.Stats(), "", "  ")
		if encodeErr != nil {
			return encodeErr
		}
		if writeErr := os.WriteFile(options.StatsFile, append(encoded, '\n'), 0600); writeErr != nil {
			return writeErr
		}
	}
	if err != nil {
		return err
	}
	return closeErr
}
