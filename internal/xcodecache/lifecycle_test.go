package xcodecache

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func lifecycleOptions(t *testing.T) Options {
	t.Helper()
	options := reviewOptions(t)
	options.ReadyFile = filepath.Join(filepath.Dir(options.Socket), "ready.json")
	return options
}

func waitForLifecycle(t *testing.T, options Options) LifecycleStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		state, err := Status(ctx, options.ReadyFile)
		cancel()
		if err == nil && state.Phase == "ready" {
			return state
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("foreground Xcode instance did not become ready")
	return LifecycleStatus{}
}

func TestStopCannotAcknowledgeBeforeRPCHandlersAndStoreClose(t *testing.T) {
	options := lifecycleOptions(t)
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- serve(ctx, options, grpc.UnaryInterceptor(func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			close(entered)
			<-release
			return handler(ctx, request)
		}))
	}()
	var released bool
	defer func() {
		if !released {
			close(release)
		}
		cancel()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("test instance did not terminate")
		}
	}()
	waitForLifecycle(t, options)
	connection, err := grpc.NewClient("unix://"+options.Socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		requestCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		cas.NewCASDBServiceClient(connection).Save(requestCtx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("in-flight"))}})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("real RPC handler did not start")
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err = Stop(stopCtx, options.ReadyFile)
	stop()
	if err == nil {
		t.Fatal("stop acknowledged an in-flight RPC")
	}
	select {
	case <-finished:
		t.Fatal("foreground instance returned before handler completion")
	default:
	}
	if lock, err := lockDirectory(options.DataDir); !errors.Is(err, errDirectoryInUse) {
		if lock != nil {
			lock.Close()
		}
		t.Fatal("store ownership released before handlers stopped", err)
	}
	state, err := Status(context.Background(), options.ReadyFile)
	if err != nil || state.Phase != "stopping" || state.StoreClosed || state.OwnershipReleased {
		t.Fatal("incomplete closure reported as stopped", state, err)
	}
	close(release)
	released = true
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("RPC did not terminate")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err = Status(context.Background(), options.ReadyFile)
		if err == nil && state.Phase == "stopped" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil || !state.StoreClosed || !state.OwnershipReleased {
		t.Fatal("closed receipt unavailable", state, err)
	}
	lock, err := lockDirectory(options.DataDir)
	if err != nil {
		t.Fatal("closed receipt retained store ownership", err)
	}
	lock.Close()
}

func TestReadinessRejectsUnsafeLeavesAndNeverPublishesOnFailure(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "permissions", "malformed", "oversized", "parent-symlink", "parent-permissions", "inside-store"} {
		t.Run(kind, func(t *testing.T) {
			options := lifecycleOptions(t)
			outside := filepath.Join(filepath.Dir(options.Socket), "outside.json")
			if err := os.WriteFile(outside, []byte("preserve private bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, options.ReadyFile); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(outside, options.ReadyFile); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.WriteFile(options.ReadyFile, []byte("private"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(options.ReadyFile, 0644); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(options.ReadyFile, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(options.ReadyFile, []byte(strings.Repeat(" ", maximumStateBytes+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				alias := filepath.Join(filepath.Dir(options.Socket), "alias")
				if err := os.Symlink(filepath.Dir(options.Socket), alias); err != nil {
					t.Fatal(err)
				}
				options.ReadyFile = filepath.Join(alias, "ready.json")
			case "parent-permissions":
				parent := filepath.Join(filepath.Dir(options.Socket), "public")
				if err := os.Mkdir(parent, 0755); err != nil {
					t.Fatal(err)
				}
				options.ReadyFile = filepath.Join(parent, "ready.json")
			case "inside-store":
				options.ReadyFile = filepath.Join(options.DataDir, "ready.json")
			}
			if _, err := Status(context.Background(), options.ReadyFile); err == nil {
				t.Fatal("unsafe state accepted by status")
			}
			if _, err := Stop(context.Background(), options.ReadyFile); err == nil {
				t.Fatal("unsafe state accepted by stop")
			}
			if err := Serve(context.Background(), options); err == nil {
				t.Fatal("unsafe state accepted by serve")
			}
			if _, err := os.Lstat(options.Socket); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("startup failure created an RPC socket", err)
			}
			contents, err := os.ReadFile(outside)
			if err != nil || string(contents) != "preserve private bytes" {
				t.Fatal("unsafe readiness modified another file", err)
			}
		})
	}
}

func TestStoreLockRejectsAliasedFilesWithoutMutation(t *testing.T) {
	for _, kind := range []string{"root-symlink", "lock-symlink", "lock-hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "store")
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte("preserve"), 0640); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, 0640); err != nil {
				t.Fatal(err)
			}
			if kind == "root-symlink" {
				if err := os.Symlink(root, data); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(data, 0700); err != nil {
					t.Fatal(err)
				}
				lock := filepath.Join(data, ".layercache-runtime.lock")
				var err error
				if kind == "lock-symlink" {
					err = os.Symlink(target, lock)
				} else {
					err = os.Link(target, lock)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			lock, err := lockDirectory(data)
			if err == nil {
				lock.Close()
				t.Fatal("aliased store lock accepted")
			}
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatal("lock validation changed foreign permissions", err)
			}
			contents, err := os.ReadFile(target)
			if err != nil || string(contents) != "preserve" {
				t.Fatal("lock validation changed foreign bytes", err)
			}
		})
	}
}

func TestStandaloneStatsCannotOverwriteStoreOrLifecycleState(t *testing.T) {
	for _, kind := range []string{"metadata", "readiness", "ownership-lock", "rpc-socket", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			options := lifecycleOptions(t)
			if err := os.MkdirAll(options.DataDir, 0700); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(options.DataDir, "metadata.db")
			if err := os.WriteFile(metadata, []byte("preserve existing metadata"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "metadata":
				options.StatsFile = metadata
			case "readiness":
				options.StatsFile = options.ReadyFile
			case "ownership-lock":
				options.StatsFile = options.ReadyFile + ".lock"
			case "rpc-socket":
				options.StatsFile = options.Socket
			case "symlink":
				options.StatsFile = filepath.Join(filepath.Dir(options.Socket), "stats-alias.json")
				if err := os.Symlink(metadata, options.StatsFile); err != nil {
					t.Fatal(err)
				}
			}
			if err := Serve(context.Background(), options); err == nil {
				t.Fatal("unsafe statistics path started a cache instance")
			}
			data, err := os.ReadFile(metadata)
			if err != nil || string(data) != "preserve existing metadata" {
				t.Fatal("statistics path modified stored bytes", err)
			}
			if _, err := os.Lstat(options.ReadyFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid statistics path published readiness", err)
			}
		})
	}
}

func TestStandaloneStatisticsRejectHardlinksBeforeTruncation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	stats := filepath.Join(root, "stats.json")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, stats); err != nil {
		t.Fatal(err)
	}
	if err := writeLifecycleStats(stats, Stats{CASReads: 1}); err == nil {
		t.Fatal("hardlinked statistics output accepted")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "preserve" {
		t.Fatal("statistics validation truncated another file", err)
	}
}

func TestStaleInstanceCannotStopLiveControlEndpoint(t *testing.T) {
	options := lifecycleOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- Serve(ctx, options) }()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("instance did not terminate")
		}
	}()
	state := waitForLifecycle(t, options)
	state.InstanceID = strings.Repeat("f", 64)
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(filepath.Dir(options.ReadyFile), "stale.json")
	if err := os.WriteFile(stale, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Stop(context.Background(), stale); err == nil {
		t.Fatal("stale identity stopped a live replacement endpoint")
	}
	if state, err := Status(context.Background(), options.ReadyFile); err != nil || state.Phase != "ready" {
		t.Fatal("stale stop disrupted owner", state, err)
	}
}
