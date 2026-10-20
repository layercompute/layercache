package xcodecache

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
)

const lifecycleVersion = 1
const maximumStateBytes = 16 << 10
const controlTimeout = 5 * time.Second

// LifecycleStatus describes one process instance, not exclusive permission to
// snapshot the store. A snapshot must acquire the store lock for its own copy.
type LifecycleStatus struct {
	Version           int    `json:"version"`
	InstanceID        string `json:"instanceId"`
	Phase             string `json:"phase"`
	Project           string `json:"project"`
	Compatibility     string `json:"compatibility"`
	Socket            string `json:"socket"`
	ControlSocket     string `json:"controlSocket"`
	Stats             Stats  `json:"stats"`
	StoreClosed       bool   `json:"storeClosed"`
	OwnershipReleased bool   `json:"ownershipReleased"`
	Historical        bool   `json:"historical"`
}

type stateOwner struct {
	directory, lock *os.File
	name            string
}

func openStateOwner(path string) (*stateOwner, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("ready-file must be a clean absolute path")
	}
	directory, err := privateDirectory(filepath.Dir(path), true)
	if err != nil {
		return nil, err
	}
	lock, err := lockPrivateFile(directory, filepath.Base(path)+".lock")
	if err != nil {
		directory.Close()
		return nil, err
	}
	owner := &stateOwner{directory: directory, lock: lock, name: filepath.Base(path)}
	return owner, nil
}

func (o *stateOwner) Close() { o.lock.Close(); o.directory.Close() }

func readStateAt(directory *os.File, name string) (LifecycleStatus, error) {
	var state LifecycleStatus
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return state, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	if err := privateRegularFile(fd); err != nil {
		return state, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumStateBytes+1))
	if err != nil {
		return state, err
	}
	if len(data) > maximumStateBytes {
		return state, errors.New("Xcode readiness state exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, errors.New("invalid Xcode readiness state")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return state, errors.New("invalid Xcode readiness state")
	}
	if err := validateState(state); err != nil {
		return state, err
	}
	return state, nil
}

func validateState(state LifecycleStatus) error {
	id, err := hex.DecodeString(state.InstanceID)
	if err != nil || len(id) != 32 || hex.EncodeToString(id) != state.InstanceID || state.Version != lifecycleVersion {
		return errors.New("invalid Xcode readiness identity")
	}
	if err := validateIdentity(state.Project, state.Compatibility); err != nil {
		return err
	}
	for _, path := range []string{state.Socket, state.ControlSocket} {
		if !filepath.IsAbs(path) || len(path) > 100 {
			return errors.New("invalid Xcode readiness socket")
		}
	}
	switch state.Phase {
	case "ready", "stopping":
		if state.StoreClosed || state.OwnershipReleased || state.Historical {
			return errors.New("invalid active Xcode readiness state")
		}
	case "stopped":
		if !state.StoreClosed || !state.OwnershipReleased {
			return errors.New("incomplete Xcode closure receipt")
		}
	case "failed":
	default:
		return errors.New("invalid Xcode readiness phase")
	}
	return nil
}

func readState(path string) (LifecycleStatus, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return LifecycleStatus{}, errors.New("ready-file must be a clean absolute path")
	}
	directory, err := privateDirectory(filepath.Dir(path), false)
	if err != nil {
		return LifecycleStatus{}, err
	}
	defer directory.Close()
	return readStateAt(directory, filepath.Base(path))
}

func (o *stateOwner) write(state LifecycleStatus, previous string) error {
	old, err := readStateAt(o.directory, o.name)
	if err == nil && previous != "" && old.InstanceID != previous {
		return errors.New("Xcode readiness owner changed")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if previous != "" && errors.Is(err, os.ErrNotExist) {
		return errors.New("Xcode readiness state disappeared")
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary := ".xcode-state-" + state.InstanceID
	fd, err := unix.Openat(int(o.directory.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(o.directory.Fd()), temporary, 0)
	file := os.NewFile(uintptr(fd), temporary)
	_, err = file.Write(append(encoded, '\n'))
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := unix.Renameat(int(o.directory.Fd()), temporary, int(o.directory.Fd()), o.name); err != nil {
		return err
	}
	return unix.Fsync(int(o.directory.Fd()))
}

func writeLifecycleStats(path string, stats Stats) error {
	if path == "" {
		return nil
	}
	directory, err := privateDirectory(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer directory.Close()
	fd, err := unix.Openat(int(directory.Fd()), filepath.Base(path), unix.O_WRONLY|unix.O_CREAT|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "xcode-statistics")
	defer file.Close()
	if err := privateRegularFile(fd); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(runStats{Stats: stats}, "", "  ")
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func outsideStore(dataDir, path string) error {
	store, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return err
	}
	store, err = filepath.Abs(store)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(store, filepath.Join(parent, filepath.Base(path)))
	if err != nil {
		return err
	}
	if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("Xcode readiness and socket paths must be outside data-dir")
	}
	return nil
}

type controlRequest struct {
	Version    int    `json:"version"`
	InstanceID string `json:"instanceId"`
	Command    string `json:"command"`
}
type controlResponse struct {
	Status LifecycleStatus `json:"status"`
	Error  string          `json:"error,omitempty"`
}
type lifecycleControl struct {
	listener    *net.UnixListener
	stop        chan struct{}
	closed      chan struct{}
	once        sync.Once
	mu          sync.RWMutex
	state       LifecycleStatus
	closeErr    error
	service     *Service
	connections sync.WaitGroup
	accepted    chan struct{}
}

func (c *lifecycleControl) status() LifecycleStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	state := c.state
	if state.Phase == "ready" || state.Phase == "stopping" {
		state.Stats = c.service.Stats()
	}
	return state
}
func (c *lifecycleControl) accept() {
	defer close(c.accepted)
	limit := make(chan struct{}, 16)
	for {
		connection, err := c.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case limit <- struct{}{}:
		default:
			connection.Close()
			continue
		}
		c.connections.Add(1)
		go func() { defer c.connections.Done(); defer func() { <-limit }(); c.handle(connection) }()
	}
}
func (c *lifecycleControl) handle(connection *net.UnixConn) {
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(controlTimeout))
	decoder := json.NewDecoder(io.LimitReader(connection, 1025))
	decoder.DisallowUnknownFields()
	var request controlRequest
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Version != lifecycleVersion || request.InstanceID != c.status().InstanceID {
		return
	}
	if request.Command != "status" && request.Command != "stop" {
		return
	}
	if request.Command == "stop" {
		c.once.Do(func() { c.mu.Lock(); c.state.Phase = "stopping"; c.mu.Unlock(); close(c.stop) })
		timer := time.NewTimer(controlTimeout)
		defer timer.Stop()
		select {
		case <-c.closed:
		case <-timer.C:
			return
		}
	}
	response := controlResponse{Status: c.status()}
	c.mu.RLock()
	if c.closeErr != nil {
		response.Error = "Xcode cache closure did not complete successfully"
	}
	c.mu.RUnlock()
	json.NewEncoder(connection).Encode(response)
}

// Serve stays in the foreground. Readiness is a verified local control claim;
// a stale file after a crash never authorizes signals to another process.
func Serve(ctx context.Context, options Options) error { return serve(ctx, options) }

func serve(ctx context.Context, options Options, serverOptions ...grpc.ServerOption) (result error) {
	if err := validateOptions(options); err != nil {
		return err
	}
	if options.ReadyFile == "" {
		return errors.New("ready-file is required")
	}
	data, err := privateDirectory(options.DataDir, true)
	if err != nil {
		return err
	}
	data.Close()
	owner, err := openStateOwner(options.ReadyFile)
	if err != nil {
		return err
	}
	defer owner.Close()
	parent, err := privateDirectory(filepath.Dir(options.Socket), true)
	if err != nil {
		return err
	}
	parent.Close()
	for _, path := range []string{options.Socket, options.ReadyFile} {
		if err := outsideStore(options.DataDir, path); err != nil {
			return err
		}
	}
	if options.StatsFile != "" {
		options.StatsFile, err = filepath.Abs(options.StatsFile)
		if err != nil {
			return err
		}
		parent, err := privateDirectory(filepath.Dir(options.StatsFile), true)
		if err != nil {
			return err
		}
		parent.Close()
		if err := outsideStore(options.DataDir, options.StatsFile); err != nil {
			return err
		}
		for _, reserved := range []string{options.Socket, options.ReadyFile, options.ReadyFile + ".lock"} {
			a, err := filepath.EvalSymlinks(filepath.Dir(reserved))
			if err != nil {
				return err
			}
			b, err := filepath.EvalSymlinks(filepath.Dir(options.StatsFile))
			if err != nil {
				return err
			}
			if filepath.Join(a, filepath.Base(reserved)) == filepath.Join(b, filepath.Base(options.StatsFile)) {
				return errors.New("stats-file must not overlap Xcode lifecycle state or sockets")
			}
		}
		if info, err := os.Lstat(options.StatsFile); err == nil && !info.Mode().IsRegular() {
			return errors.New("stats-file must be a regular private file")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// A released state lock proves the previous publisher is gone. Remove its
	// validated stale receipt before startup; failures must not leave it ready.
	if _, err := readStateAt(owner.directory, owner.name); err == nil {
		if err := unix.Unlinkat(int(owner.directory.Fd()), owner.name, 0); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	runtime, err := startRuntime(ctx, options, serverOptions...)
	if err != nil {
		return err
	}
	defer runtime.Close()
	var identity [32]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return err
	}
	state := LifecycleStatus{Version: lifecycleVersion, InstanceID: hex.EncodeToString(identity[:]), Phase: "ready", Project: options.Project, Compatibility: options.Compatibility, Socket: options.Socket}
	state.ControlSocket = filepath.Join(filepath.Dir(options.ReadyFile), ".lc-xcode-"+state.InstanceID[:16]+".sock")
	if len(state.ControlSocket) > 100 {
		return errors.New("ready-file directory is too long for a private control socket")
	}
	listener, err := listenPrivate(state.ControlSocket)
	if err != nil {
		return err
	}
	control := &lifecycleControl{listener: listener, stop: make(chan struct{}), closed: make(chan struct{}), accepted: make(chan struct{}), state: state, service: runtime.service}
	go control.accept()
	defer func() { listener.Close(); <-control.accepted; control.connections.Wait() }()
	if err := owner.write(state, ""); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-control.stop:
	case <-runtime.done:
		result = runtime.serveErr
	}
	control.mu.Lock()
	control.state.Phase = "stopping"
	control.mu.Unlock()
	closeErr := runtime.Close()
	state.Stats = runtime.service.Stats()
	state.StoreClosed = closeErr == nil
	state.OwnershipReleased = closeErr == nil
	state.Phase = "stopped"
	if closeErr != nil {
		state.Phase = "failed"
		state.Stats.CacheErrors++
		result = closeErr
	}
	if result != nil {
		state.Phase = "failed"
	}
	if err := writeLifecycleStats(options.StatsFile, state.Stats); err != nil {
		fmt.Fprintf(os.Stderr, "Layer Cache statistics unavailable: %v\n", err)
	}
	retained := state
	retained.Historical = true
	if err := owner.write(retained, state.InstanceID); err != nil {
		result = err
		state.Phase = "failed"
	}
	// Release both ownership locks before publishing a successful stop reply.
	owner.Close()
	control.mu.Lock()
	control.state = state
	control.closeErr = result
	control.mu.Unlock()
	close(control.closed)
	return result
}

func controlExchange(ctx context.Context, path, command string) (LifecycleStatus, error) {
	if err := ctx.Err(); err != nil {
		return LifecycleStatus{}, err
	}
	state, err := readState(path)
	if err != nil {
		return state, err
	}
	if state.Phase == "stopped" {
		state.Historical = true
		if command == "stop" {
			return state, errors.New("Xcode stopped receipt is historical; no live instance acknowledged this stop")
		}
		return state, nil
	}
	if state.Phase == "failed" {
		return state, errors.New("Xcode cache closure failed")
	}
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	parent, err := privateDirectory(filepath.Dir(state.ControlSocket), false)
	if err != nil {
		return state, err
	}
	parent.Close()
	info, err := os.Lstat(state.ControlSocket)
	if err != nil {
		return state, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Mode().Perm() != 0600 {
		return state, fmt.Errorf("%w: control leaf must be a private socket", errUnsafePath)
	}
	dialer := net.Dialer{}
	raw, err := dialer.DialContext(ctx, "unix", state.ControlSocket)
	if err != nil {
		return state, fmt.Errorf("Xcode instance is unavailable; closure is unconfirmed: %w", err)
	}
	connection := raw.(*net.UnixConn)
	defer connection.Close()
	deadline, _ := ctx.Deadline()
	connection.SetDeadline(deadline)
	interrupt := context.AfterFunc(ctx, func() { connection.SetDeadline(time.Now()) })
	defer interrupt()
	if err := json.NewEncoder(connection).Encode(controlRequest{Version: lifecycleVersion, InstanceID: state.InstanceID, Command: command}); err != nil {
		return state, err
	}
	connection.CloseWrite()
	var response controlResponse
	decoder := json.NewDecoder(io.LimitReader(connection, maximumStateBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return state, fmt.Errorf("Xcode control response unavailable; closure is unconfirmed: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return state, errors.New("invalid Xcode control response")
	}
	if err := validateState(response.Status); err != nil {
		return state, err
	}
	if response.Status.InstanceID != state.InstanceID || response.Status.Socket != state.Socket || response.Status.ControlSocket != state.ControlSocket || response.Status.Project != state.Project || response.Status.Compatibility != state.Compatibility {
		return state, errors.New("Xcode control instance changed")
	}
	if response.Error != "" {
		return response.Status, errors.New("Xcode cache closure did not complete successfully")
	}
	if command == "stop" && response.Status.Phase != "stopped" {
		return response.Status, errors.New("Xcode cache closure is unconfirmed")
	}
	return response.Status, nil
}

func Status(ctx context.Context, readyFile string) (LifecycleStatus, error) {
	return controlExchange(ctx, readyFile, "status")
}
func Stop(ctx context.Context, readyFile string) (LifecycleStatus, error) {
	return controlExchange(ctx, readyFile, "stop")
}
