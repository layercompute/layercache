//go:build linux || darwin

package xcodecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const lifecycleProcessOptions = "LAYERCACHE_XCODE_LIFECYCLE_PROCESS_OPTIONS"

// This subprocess owns a real foreground service, rather than sharing the
// parent's goroutines or file descriptors for its store and ownership locks.
func TestXcodeLifecycleProcessChild(t *testing.T) {
	raw := os.Getenv(lifecycleProcessOptions)
	if raw == "" {
		return
	}
	var options Options
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signals)
	defer cancel()
	go func() {
		var command [1]byte
		if count, _ := os.Stdin.Read(command[:]); count == 1 && command[0] == 'c' {
			cancel()
		}
	}()
	if err := Serve(ctx, options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type lifecycleProcess struct {
	command *exec.Cmd
	input   *os.File
	log     string
	done    chan struct{}
	err     error // Read only after done closes.
}

func lifecycleProcessFixture(t *testing.T) Options {
	t.Helper()
	// Keep Unix socket paths within Darwin's limit, including control IDs.
	root, err := os.MkdirTemp("/tmp", "lc-life-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return Options{
		DataDir: filepath.Join(root, "store"), Socket: filepath.Join(root, "cache.sock"),
		ReadyFile: filepath.Join(root, "state", "ready.json"), StatsFile: filepath.Join(root, "stats.json"),
		Project: "lifecycle-process-test", Compatibility: "test-toolchain", MaxBytes: 32 << 20,
	}
}

func startLifecycleProcess(t *testing.T, options Options) *lifecycleProcess {
	t.Helper()
	encoded, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.CreateTemp(filepath.Dir(options.Socket), ".child-log-")
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		log.Close()
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestXcodeLifecycleProcessChild$")
	command.Env = append(os.Environ(), lifecycleProcessOptions+"="+string(encoded))
	command.Stdin, command.Stdout, command.Stderr = reader, log, log
	if err := command.Start(); err != nil {
		reader.Close()
		writer.Close()
		log.Close()
		t.Fatal(err)
	}
	reader.Close()
	log.Close()
	child := &lifecycleProcess{command: command, input: writer, log: log.Name(), done: make(chan struct{})}
	go func() { child.err = command.Wait(); close(child.done) }()
	t.Cleanup(func() {
		child.input.Close()
		select {
		case <-child.done:
			return
		default:
		}
		// This is only the exact child created by this fixture, never a PID
		// discovered from the readiness file or another process's state.
		_ = child.command.Process.Kill()
		select {
		case <-child.done:
		case <-time.After(5 * time.Second):
			t.Error("owned lifecycle subprocess did not exit after cleanup")
		}
	})
	return child
}

func (child *lifecycleProcess) output() string {
	contents, _ := os.ReadFile(child.log)
	return string(contents)
}

func (child *lifecycleProcess) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-child.done:
		return child.err
	case <-time.After(10 * time.Second):
		t.Fatalf("lifecycle subprocess did not exit: %s", child.output())
		return nil
	}
}

func (child *lifecycleProcess) ready(t *testing.T, options Options) LifecycleStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-child.done:
			t.Fatalf("lifecycle subprocess exited before readiness: %v: %s", child.err, child.output())
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		state, err := Status(ctx, options.ReadyFile)
		cancel()
		if err == nil && state.Phase == "ready" {
			if state.Historical || state.StoreClosed || state.OwnershipReleased || state.Project != options.Project || state.Compatibility != options.Compatibility || state.Socket != options.Socket {
				t.Fatalf("invalid live readiness: %+v", state)
			}
			for _, path := range []string{options.ReadyFile, state.Socket, state.ControlSocket} {
				info, err := os.Lstat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("readiness path is not private: %s: %v", path, err)
				}
			}
			return state
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("lifecycle subprocess did not become ready: %v: %s", lastErr, child.output())
	return LifecycleStatus{}
}

func lifecycleCAS(t *testing.T, socket string) cas.CASDBServiceClient {
	t.Helper()
	connection, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	return cas.NewCASDBServiceClient(connection)
}

func saveLifecycleCAS(t *testing.T, client cas.CASDBServiceClient, payload []byte) *cas.CASDataID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	saved, err := client.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(payload)}})
	if err != nil || len(saved.GetCasId().GetId()) != 32 {
		t.Fatalf("ready service did not accept real CAS bytes: %v %v", saved, err)
	}
	return saved.GetCasId()
}

func loadLifecycleCAS(t *testing.T, client cas.CASDBServiceClient, id *cas.CASDataID, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	loaded, err := client.Load(ctx, &cas.CASLoadRequest{CasId: id})
	if err != nil || loaded.GetOutcome() != cas.CASLoadResponse_SUCCESS || !bytes.Equal(loaded.GetData().GetBlob().GetData(), payload) {
		t.Fatalf("CAS bytes did not survive lifecycle transition: %v %v", loaded, err)
	}
}

func stopLifecycleProcess(t *testing.T, child *lifecycleProcess, options Options, instance string) LifecycleStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	state, err := Stop(ctx, options.ReadyFile)
	if err != nil || state.Historical || state.InstanceID != instance || state.Phase != "stopped" || !state.StoreClosed || !state.OwnershipReleased {
		t.Fatalf("stop did not acknowledge this instance's completed closure: %+v %v", state, err)
	}
	if err := child.wait(t); err != nil {
		t.Fatalf("orderly service exit failed: %v: %s", err, child.output())
	}
	return state
}

func assertLifecycleClosedStore(t *testing.T, options Options, id *cas.CASDataID, payload []byte) {
	t.Helper()
	// Reacquire the real lock rather than interpreting an absent socket as
	// permission to read or snapshot a possibly still-owned SQLite store.
	lock, err := lockDirectory(options.DataDir)
	if err != nil {
		t.Fatalf("closure acknowledgement left the store owned: %v", err)
	}
	defer lock.Close()
	store, err := artifact.Open(context.Background(), options.DataDir, options.MaxBytes, 0)
	if err != nil {
		t.Fatalf("closed SQLite store could not be reopened: %v", err)
	}
	defer store.Close()
	service, err := New(store, options.Project, options.Compatibility)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	loaded, err := service.Load(ctx, &cas.CASLoadRequest{CasId: id})
	if err != nil || loaded.GetOutcome() != cas.CASLoadResponse_SUCCESS || !bytes.Equal(loaded.GetData().GetBlob().GetData(), payload) {
		t.Fatalf("closed producer did not leave verified CAS bytes: %v %v", loaded, err)
	}
}

func TestLifecycleProcessReadinessRPCStopAndRetainedStats(t *testing.T) {
	options := lifecycleProcessFixture(t)
	child := startLifecycleProcess(t, options)
	ready := child.ready(t, options)
	client := lifecycleCAS(t, ready.Socket)
	payload := []byte("compiler output from the foreground producer")
	id := saveLifecycleCAS(t, client, payload)
	loadLifecycleCAS(t, client, id, payload)
	stopped := stopLifecycleProcess(t, child, options, ready.InstanceID)
	if stopped.Stats.CASWrites != 1 || stopped.Stats.CASReads != 1 || stopped.Stats.CASHits != 1 {
		t.Fatalf("stop lost final protocol statistics: %+v", stopped.Stats)
	}
	status, err := Status(context.Background(), options.ReadyFile)
	historical := stopped
	historical.Historical = true
	if err != nil || status != historical {
		t.Fatalf("persisted closure receipt changed: %+v %v", status, err)
	}
	encoded, err := os.ReadFile(options.StatsFile)
	if err != nil {
		t.Fatal(err)
	}
	var statistics runStats
	if err := json.Unmarshal(encoded, &statistics); err != nil || statistics.Stats != stopped.Stats || statistics.UncachedFallback || statistics.UncachedRetry {
		t.Fatalf("final statistics do not match completed service: %+v %v", statistics, err)
	}
	for _, path := range []string{ready.Socket, ready.ControlSocket} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("orderly shutdown left its socket: %s: %v", path, err)
		}
	}
	assertLifecycleClosedStore(t, options, id, payload)
}

func TestLifecycleProcessCrashRecoveryAndStaleReceiptCannotStopSuccessor(t *testing.T) {
	options := lifecycleProcessFixture(t)
	producer := startLifecycleProcess(t, options)
	previous := producer.ready(t, options)
	payload := []byte("committed bytes before process interruption")
	id := saveLifecycleCAS(t, lifecycleCAS(t, previous.Socket), payload)
	if err := producer.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var killed *exec.ExitError
	if err := producer.wait(t); !errors.As(err, &killed) {
		t.Fatalf("crash fixture did not exit by signal: %v", err)
	}
	if status, err := Status(context.Background(), options.ReadyFile); err == nil || status.StoreClosed || status.OwnershipReleased {
		t.Fatalf("crash was incorrectly acknowledged as closed: %+v %v", status, err)
	}
	for _, path := range []string{options.ReadyFile, previous.Socket, previous.ControlSocket} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("SIGKILL did not leave the stale recovery fixture: %s: %v", path, err)
		}
	}
	successor := startLifecycleProcess(t, options)
	current := successor.ready(t, options)
	if current.InstanceID == previous.InstanceID || current.ControlSocket == previous.ControlSocket {
		t.Fatal("successor reused the crashed instance identity or control endpoint")
	}
	client := lifecycleCAS(t, current.Socket)
	loadLifecycleCAS(t, client, id, payload)
	for _, addressSuccessor := range []bool{false, true} {
		stale := previous
		if addressSuccessor {
			// Exercise instance matching even if a stale caller addresses the
			// replacement, not only isolation by distinct control socket names.
			stale.ControlSocket = current.ControlSocket
		}
		encoded, err := json.Marshal(stale)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(filepath.Dir(options.Socket), "stale-receipt.json")
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err = Stop(ctx, path)
		cancel()
		if err == nil {
			t.Fatal("stale readiness receipt stopped or acknowledged the replacement")
		}
		status, err := Status(context.Background(), options.ReadyFile)
		if err != nil || status.InstanceID != current.InstanceID || status.Phase != "ready" {
			t.Fatalf("stale stop disturbed the successor: %+v %v", status, err)
		}
		loadLifecycleCAS(t, client, id, payload)
	}
	stopLifecycleProcess(t, successor, options, current.InstanceID)
	assertLifecycleClosedStore(t, options, id, payload)
}

func TestLifecycleProcessSIGTERMAndContextCancellationCloseOrderly(t *testing.T) {
	for _, mode := range []string{"SIGTERM", "context"} {
		t.Run(mode, func(t *testing.T) {
			options := lifecycleProcessFixture(t)
			child := startLifecycleProcess(t, options)
			ready := child.ready(t, options)
			payload := []byte("bytes survive " + mode)
			id := saveLifecycleCAS(t, lifecycleCAS(t, ready.Socket), payload)
			var err error
			if mode == "SIGTERM" {
				err = child.command.Process.Signal(syscall.SIGTERM)
			} else {
				_, err = child.input.Write([]byte("c"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := child.wait(t); err != nil {
				t.Fatalf("orderly %s failed: %v: %s", mode, err, child.output())
			}
			status, err := Status(context.Background(), options.ReadyFile)
			if err != nil || !status.Historical || status.InstanceID != ready.InstanceID || status.Phase != "stopped" || !status.StoreClosed || !status.OwnershipReleased || status.Stats.CASWrites != 1 {
				t.Fatalf("%s did not retain a completed closure receipt: %+v %v", mode, status, err)
			}
			assertLifecycleClosedStore(t, options, id, payload)
		})
	}
}

func TestLifecycleProcessHistoricalStopCannotAcknowledgeAnotherStoreOwner(t *testing.T) {
	originalOptions := lifecycleProcessFixture(t)
	original := startLifecycleProcess(t, originalOptions)
	originalReady := original.ready(t, originalOptions)
	payload := []byte("bytes retained while a different lifecycle owns the same store")
	id := saveLifecycleCAS(t, lifecycleCAS(t, originalReady.Socket), payload)
	stopLifecycleProcess(t, original, originalOptions, originalReady.InstanceID)

	replacementOptions := originalOptions
	root := filepath.Dir(originalOptions.Socket)
	replacementOptions.Socket = filepath.Join(root, "replacement.sock")
	replacementOptions.ReadyFile = filepath.Join(root, "replacement-state", "ready.json")
	replacementOptions.StatsFile = filepath.Join(root, "replacement-stats.json")
	replacement := startLifecycleProcess(t, replacementOptions)
	replacementReady := replacement.ready(t, replacementOptions)
	if replacementReady.InstanceID == originalReady.InstanceID {
		t.Fatal("different lifecycle reused the original instance identity")
	}
	client := lifecycleCAS(t, replacementReady.Socket)
	loadLifecycleCAS(t, client, id, payload)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	oldStop, err := Stop(ctx, originalOptions.ReadyFile)
	cancel()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "historical") || !oldStop.Historical || oldStop.InstanceID != originalReady.InstanceID {
		t.Fatalf("historical receipt acknowledged current store closure: %+v %v", oldStop, err)
	}
	oldStatus, err := Status(context.Background(), originalOptions.ReadyFile)
	if err != nil || !oldStatus.Historical || oldStatus.InstanceID != originalReady.InstanceID || oldStatus.Phase != "stopped" {
		t.Fatalf("original instance history was not retained accurately: %+v %v", oldStatus, err)
	}
	current, err := Status(context.Background(), replacementOptions.ReadyFile)
	if err != nil || current.Historical || current.InstanceID != replacementReady.InstanceID || current.Phase != "ready" {
		t.Fatalf("historical stop disturbed the current store owner: %+v %v", current, err)
	}
	if lock, err := lockDirectory(originalOptions.DataDir); !errors.Is(err, errDirectoryInUse) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("historical stop released the replacement's store lock: %v", err)
	}
	loadLifecycleCAS(t, client, id, payload)
	stopLifecycleProcess(t, replacement, replacementOptions, replacementReady.InstanceID)
	assertLifecycleClosedStore(t, replacementOptions, id, payload)
}

func TestLifecycleProcessCompetingOwnersCannotDisturbLiveService(t *testing.T) {
	options := lifecycleProcessFixture(t)
	owner := startLifecycleProcess(t, options)
	ready := owner.ready(t, options)
	client := lifecycleCAS(t, ready.Socket)
	payload := []byte("the first owner retains its bytes and identity")
	id := saveLifecycleCAS(t, client, payload)
	for _, conflict := range []string{"same-store", "same-ready-file"} {
		t.Run(conflict, func(t *testing.T) {
			competing := options
			root := filepath.Dir(options.Socket)
			competing.Socket = filepath.Join(root, "other.sock")
			competing.StatsFile = filepath.Join(root, "other-stats.json")
			if conflict == "same-store" {
				competing.ReadyFile = filepath.Join(root, "other-state", "ready.json")
			} else {
				competing.DataDir = filepath.Join(root, "other-store")
			}
			child := startLifecycleProcess(t, competing)
			if err := child.wait(t); err == nil || !strings.Contains(child.output(), errDirectoryInUse.Error()) {
				t.Fatalf("competing %s owner was not rejected: %v: %s", conflict, err, child.output())
			}
			status, err := Status(context.Background(), options.ReadyFile)
			if err != nil || status.InstanceID != ready.InstanceID || status.Phase != "ready" {
				t.Fatalf("competing owner disturbed readiness: %+v %v", status, err)
			}
			if _, err := os.Lstat(competing.Socket); !os.IsNotExist(err) {
				t.Fatalf("rejected owner left an RPC socket: %v", err)
			}
			loadLifecycleCAS(t, client, id, payload)
		})
	}
	stopLifecycleProcess(t, owner, options, ready.InstanceID)
	assertLifecycleClosedStore(t, options, id, payload)
}
