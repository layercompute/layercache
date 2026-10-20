package xcodecache

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc"
)

type startupFailure struct{ cause error }

func (e startupFailure) Error() string { return e.cause.Error() }
func (e startupFailure) Unwrap() error { return e.cause }

type cacheRuntime struct {
	lock      *os.File
	store     *artifact.Store
	service   *Service
	server    *grpc.Server
	done      chan struct{}
	serveErr  error
	closeOnce sync.Once
	closeErr  error
}

func validateOptions(options Options) error {
	if !filepath.IsAbs(options.Socket) || len(options.Socket) > 100 {
		return errors.New("an absolute Unix socket path of at most 100 bytes is required")
	}
	if options.DataDir == "" || options.MaxBytes <= 0 {
		return errors.New("a data directory and positive compilation-cache quota are required")
	}
	return validateIdentity(options.Project, options.Compatibility)
}

// Listen only in a private directory. An old socket may be removed after an
// exclusive owner has established that its listener no longer exists.
func listenPrivate(socket string) (*net.UnixListener, error) {
	directory, err := privateDirectory(filepath.Dir(socket), true)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	if info, err := os.Lstat(socket); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0600 {
			return nil, fmt.Errorf("%w: socket leaf is not a private socket", errUnsafePath)
		}
		connection, dialErr := net.DialTimeout("unix", socket, 200*time.Millisecond)
		if dialErr == nil {
			connection.Close()
			return nil, errors.New("Xcode cache socket already has a listener")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
			return nil, dialErr
		}
		current, err := os.Lstat(socket)
		if err == nil && !os.SameFile(info, current) {
			return nil, errors.New("Xcode cache socket changed during recovery")
		}
		if err == nil {
			err = os.Remove(socket)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

type readyListener struct {
	net.Listener
	ready chan struct{}
	once  sync.Once
}

func (l *readyListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.ready) })
	return l.Listener.Accept()
}

func startRuntime(ctx context.Context, options Options, serverOptions ...grpc.ServerOption) (_ *cacheRuntime, result error) {
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	// Keep the existing run fallback classes: unavailable storage may compile
	// uncached, while unsafe paths and competing owners must fail explicitly.
	if err := os.MkdirAll(filepath.Dir(options.Socket), 0700); err != nil {
		return nil, startupFailure{err}
	}
	parent, err := privateDirectory(filepath.Dir(options.Socket), false)
	if err != nil {
		if errors.Is(err, errUnsafePath) {
			return nil, fmt.Errorf("socket parent must be a private directory: %w", err)
		}
		return nil, startupFailure{err}
	}
	parent.Close()
	lock, err := lockDirectory(options.DataDir)
	if err != nil {
		if errors.Is(err, errDirectoryInUse) || errors.Is(err, errUnsafePath) {
			return nil, err
		}
		return nil, startupFailure{err}
	}
	r := &cacheRuntime{lock: lock, done: make(chan struct{})}
	defer func() {
		if result != nil {
			if r.store != nil {
				r.store.Close()
			}
			lock.Close()
		}
	}()
	r.store, err = artifact.Open(ctx, options.DataDir, options.MaxBytes, 0)
	if err != nil {
		return nil, startupFailure{err}
	}
	if err = r.store.ResetReadLeases(ctx, readLeaseNamespace); err != nil {
		return nil, startupFailure{err}
	}
	r.service, err = New(r.store, options.Project, options.Compatibility)
	if err != nil {
		return nil, err
	}
	listener, err := listenPrivate(options.Socket)
	if err != nil {
		return nil, fmt.Errorf("listen for Xcode cache: %w", err)
	}
	serverOptions = append([]grpc.ServerOption{grpc.MaxRecvMsgSize(MaxMessageBytes), grpc.MaxSendMsgSize(MaxMessageBytes), grpc.MaxConcurrentStreams(16), grpc.WaitForHandlers(true)}, serverOptions...)
	r.server = grpc.NewServer(serverOptions...)
	cas.RegisterCASDBServiceServer(r.server, r.service)
	kv.RegisterKeyValueDBServer(r.server, r.service)
	ready := make(chan struct{})
	go func() { r.serveErr = r.server.Serve(&readyListener{Listener: listener, ready: ready}); close(r.done) }()
	select {
	case <-ready:
		return r, nil
	case <-r.done:
		r.server.Stop()
		return nil, fmt.Errorf("Xcode cache RPC listener failed: %w", r.serveErr)
	case <-ctx.Done():
		r.server.Stop()
		<-r.done
		return nil, ctx.Err()
	}
}

func (r *cacheRuntime) Close() error {
	r.closeOnce.Do(func() {
		r.server.Stop()
		<-r.done
		r.closeErr = r.store.Close()
		if err := r.lock.Close(); r.closeErr == nil {
			r.closeErr = err
		}
	})
	return r.closeErr
}
