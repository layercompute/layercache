package xcodelifecycle

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	project = "github.com/layercache/xcode-lifecycle-qa"
	profile = "xcode-lifecycle-protocol-v1"
	key     = "xcode-snapshots/generation-1"
	budget  = "16777216"
)

// This is an opt-in public-interface qualifier. It starts the supplied real Go
// executable and invokes the supplied built native CLI, without rebuilding either.
func TestScopedClosedStoreRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Xcode adapter requires Unix domain sockets")
	}
	binary, nativeCLI := os.Getenv("LAYERCACHE_BIN"), os.Getenv("NATIVE_CACHE_CLI")
	if binary == "" || nativeCLI == "" {
		t.Skip("set absolute LAYERCACHE_BIN and NATIVE_CACHE_CLI paths for the opt-in actual-client qualifier")
	}
	f := newFixture(t, binary, nativeCLI)
	grantPath := filepath.Join(f.root, "snapshot-grants.json")
	writer, reader := newGrant("fixture-writer", "writer"), newGrant("fixture-reader", "reader")
	writeGrants(t, grantPath, writer.grant, reader.grant)
	writerFile, readerFile := filepath.Join(f.root, "writer.credential"), filepath.Join(f.root, "reader.credential")
	writePrivate(t, writerFile, []byte(writer.token+"\n"))
	writePrivate(t, readerFile, []byte(reader.token+"\n"))

	address := freeAddress(t)
	configPath := filepath.Join(f.root, "team.json")
	f.command("setup Team server", f.binary, "setup", "--config", configPath, "--data-dir", filepath.Join(f.root, "team-store"),
		"--listen", address, "--role", "team", "--project", project, "--compatibility-id", profile,
		"--max-size", budget, "--non-interactive", "--json")
	var config map[string]json.RawMessage
	decodeFile(t, configPath, &config)
	config["snapshotCredentialsFile"], _ = json.Marshal(grantPath)
	writeJSON(t, configPath, config)
	team := f.start("Team server", f.binary, "serve", "--config", configPath)
	f.healthy("http://" + address)
	audit := newAudit(t, "http://"+address)
	f.endpoint = audit.server.URL

	producerStore := filepath.Join(f.root, "producer-store")
	producer := f.adapter("producer", producerStore)
	objects, actions, connection := connect(t, producer.socket)
	leaf, err := objects.Save(f.ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: data([]byte("synthetic compiled object\n"))}})
	if err != nil || len(leaf.GetCasId().GetId()) != sha256.Size {
		t.Fatal("producer could not save fixture CAS bytes")
	}
	manifest, err := objects.Put(f.ctx, &cas.CASPutRequest{Data: &cas.CASObject{Blob: data([]byte("synthetic manifest\n")), References: []*cas.CASDataID{leaf.GetCasId()}}})
	if err != nil || len(manifest.GetCasId().GetId()) != sha256.Size {
		t.Fatal("producer could not save fixture CAS graph")
	}
	_, err = actions.PutValue(f.ctx, &kv.PutValueRequest{Key: []byte("fixture compile action"), Value: &kv.Value{Entries: map[string][]byte{"CASID": manifest.GetCasId().Id}}})
	if err != nil {
		t.Fatal("producer could not save fixture action")
	}
	_ = connection.Close()
	graph := graphIDs{bytes.Clone(leaf.GetCasId().Id), bytes.Clone(manifest.GetCasId().Id)}
	f.require(audit.puts() == 0, "no archive publication while producer is open")
	f.stopAdapter(producer)
	f.require(noSockets(producerStore), "closed store contains no lifecycle sockets")

	// A separately launched adapter must see the checkpointed graph before any
	// archive publication. This also proves ownership was released by stop.
	restarted := f.adapter("producer-restart", producerStore)
	verifyGraph(t, f.ctx, restarted.socket, graph)
	f.stopAdapter(restarted)
	f.require(true, "closed producer restart preserves graph")

	audit.phase("writer")
	published := f.native("trusted writer", "save", writerFile, producerStore, filepath.Join(f.root, "writer-cache"), project, profile, key, true)
	publishedDigest, digestErr := hex.DecodeString(published.Digest)
	t.Logf("writer result: hit=%t source=%s bytes=%d validSHA256=%t archivePUTs=%d", published.Hit, published.Source, published.Bytes, digestErr == nil && len(publishedDigest) == sha256.Size, audit.count("writer", http.MethodPut))
	f.require(!published.Hit && published.Source == "team" && published.Bytes > 0 && digestErr == nil && len(publishedDigest) == sha256.Size, "writer receives Team publication acknowledgement")
	f.require(audit.count("writer", http.MethodPut) == 1, "writer publishes one archive")

	// Restart the real Team runtime before the independent archive consumer.
	team.interrupt(t)
	team = f.start("restarted Team server", f.binary, "serve", "--config", configPath)
	f.healthy("http://" + address)
	_ = team
	audit.phase("reader")
	consumerStore := filepath.Join(f.root, "fresh-consumer-store")
	restored := f.native("read-only consumer", "restore", readerFile, consumerStore, filepath.Join(f.root, "reader-cache"), project, profile, key, true)
	f.require(restored.Hit && restored.Source == "team" && restored.Digest == published.Digest && restored.Bytes == published.Bytes, "fresh read-only client restores exact Team archive after restart")
	f.require(audit.count("reader", http.MethodGet) == 1 && audit.count("reader", http.MethodPut) == 0, "read-only archive restore sends zero PUT requests")
	// The generic archive contract preserves the saved directory's top level;
	// Result.path is the extraction parent, not the nested compiler store.
	restoredStore := filepath.Join(consumerStore, filepath.Base(producerStore))
	metadata, metadataErr := os.Stat(filepath.Join(restoredStore, "metadata.db"))
	f.require(metadataErr == nil && metadata.Mode().IsRegular() && metadata.Size() > 0, "restored compiler metadata exists before adapter startup")
	consumer := f.adapter("fresh-consumer", restoredStore)
	verifyGraph(t, f.ctx, consumer.socket, graph)
	// A signal is the managed-build cancellation path. The final receipt must
	// still acknowledge closed storage, not merely a missing process or socket.
	consumer.process.interrupt(t)
	var cancelled lifecycle
	decodeFile(t, consumer.ready, &cancelled)
	f.require(cancelled.Phase == "stopped" && cancelled.StoreClosed && cancelled.OwnershipReleased && cancelled.InstanceID == consumer.instance && cancelled.Historical != nil && *cancelled.Historical, "cancelled exact consumer leaves a historical closed receipt")

	audit.phase("reader-write-denied")
	f.native("reader cannot publish", "save", readerFile, producerStore, filepath.Join(f.root, "denied-writer-cache"), project, profile, key, false)
	f.require(audit.exchangeDenied("reader-write-denied") && audit.count("reader-write-denied", http.MethodPut) == 0, "reader write exchange fails before archive PUT")
	for _, control := range []struct{ name, project, profile, key string }{
		{"foreign project", project + "-foreign", profile, key},
		{"foreign profile", project, profile + "-foreign", key},
		{"foreign namespace", project, profile, "foreign/generation-1"},
	} {
		audit.phase(control.name)
		f.native(control.name, "restore", readerFile, filepath.Join(f.root, strings.ReplaceAll(control.name, " ", "-")), filepath.Join(f.root, strings.ReplaceAll(control.name, " ", "-")+"-cache"), control.project, control.profile, control.key, false)
		f.require(audit.exchangeDenied(control.name), control.name+" rejected by real exchange")
	}
	missPath := filepath.Join(f.root, "changed-key-output")
	miss := f.native("changed key", "restore", readerFile, missPath, filepath.Join(f.root, "changed-key-cache"), project, profile, "xcode-snapshots/generation-2", true)
	f.require(!miss.Hit && miss.Source == "miss" && absent(missPath), "new generation is a clean archive miss")

	capability := exchange(t, f.endpoint, reader.token, project, profile, key, "read")
	f.require(capability.Version == 1 && capability.Mode == "read" && len(capability.Capabilities) == 1 && capability.Capabilities[0] == "read" && capability.ExpiresAt.After(time.Now()) && capability.ExpiresAt.Before(time.Now().Add(15*time.Minute+time.Second)), "real exchange returns bounded read-only capability")
	f.require(artifactRequest(t, f.endpoint, capability, http.MethodHead, capability.NativeKey) == http.StatusOK, "exchanged capability reads its exact archive")
	f.require(denied(artifactRequest(t, f.endpoint, capability, http.MethodPut, capability.NativeKey)), "read capability cannot upload")
	// Use a second valid capability against the already proven existing first
	// archive. A concealed 404 here cannot be a natural miss for the target.
	other := exchange(t, f.endpoint, reader.token, project, profile, "xcode-snapshots/generation-2", "read")
	f.require(other.NativeKey != capability.NativeKey && artifactRequest(t, f.endpoint, other, http.MethodHead, capability.NativeKey) == http.StatusNotFound, "read capability cannot address another existing artifact")

	for _, fault := range []string{"corrupt", "partial"} {
		audit.phase(fault)
		audit.fault(fault)
		destination, cache := filepath.Join(f.root, fault+"-output"), filepath.Join(f.root, fault+"-cache")
		f.native(fault+" HTTP bytes", "restore", readerFile, destination, cache, project, profile, key, false)
		f.require(audit.count(fault, http.MethodGet) == 1 && audit.modified(fault) == 1 && absent(destination) && noArchives(cache), fault+" bytes install neither output nor cached archive")
		audit.fault("")
	}
	writeGrants(t, grantPath) // Revoke already exchanged tokens, without restart.
	f.require(artifactRequest(t, f.endpoint, capability, http.MethodHead, capability.NativeKey) == http.StatusNotFound, "grant revocation invalidates previously exchanged capability")
	audit.phase("revoked bootstrap")
	f.native("revoked bootstrap", "restore", readerFile, filepath.Join(f.root, "revoked-output"), filepath.Join(f.root, "revoked-cache"), project, profile, key, false)
	f.require(audit.exchangeDenied("revoked bootstrap"), "revoked bootstrap cannot obtain another capability")
	f.writeReport(published.Bytes)
	t.Logf("qualified %d checks: real lifecycle, scoped exchange and native archive transport; synthetic CAS only", len(f.checks))
}

type fixture struct {
	t                                                *testing.T
	ctx                                              context.Context
	root, sockets, binary, nativeCLI, node, endpoint string
	env, checks                                      []string
}

func newFixture(t *testing.T, binary, nativeCLI string) *fixture {
	t.Helper()
	for _, path := range []string{binary, nativeCLI} {
		info, err := os.Stat(path)
		if !filepath.IsAbs(path) || err != nil || !info.Mode().IsRegular() {
			t.Fatal("qualifier requires absolute existing executable and native bundle paths")
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node 24+ is required")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	sockets, err := os.MkdirTemp("/tmp", "lc-xqa-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockets) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	f := &fixture{t: t, ctx: ctx, root: root, sockets: sockets, binary: binary, nativeCLI: nativeCLI, node: node}
	f.env = fixtureEnvironment(root)
	return f
}

func fixtureEnvironment(root string) []string {
	// No inherited cloud, Actions, native-cache or Node injection credentials.
	environment := []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "GOMAXPROCS=2", "NODE_NO_WARNINGS=1", "TMPDIR=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_STATE_HOME=" + filepath.Join(root, "state")}
	// Darwin's Go defaults require HOME even when every CLI path is explicit;
	// unlike Linux, UserCacheDir/UserConfigDir do not consult the XDG variables.
	// Preserve the caller's value unchanged. Never redirect or write its home.
	if home, exists := os.LookupEnv("HOME"); exists {
		environment = append(environment, "HOME="+home)
	}
	return environment
}

func TestFixtureEnvironmentPreservesCallerHomeWithoutCredentials(t *testing.T) {
	allowed := map[string]bool{"PATH": true, "LANG": true, "GOMAXPROCS": true, "NODE_NO_WARNINGS": true, "TMPDIR": true,
		"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_STATE_HOME": true, "HOME": true}
	actual := make(map[string]string)
	for _, entry := range fixtureEnvironment(t.TempDir()) {
		key, value, found := strings.Cut(entry, "=")
		if !found || !allowed[key] {
			t.Fatal("fixture environment inherited an undeclared field")
		}
		actual[key] = value
	}
	expected, expectedPresent := os.LookupEnv("HOME")
	home, present := actual["HOME"]
	if present != expectedPresent || home != expected {
		t.Fatal("fixture must preserve the caller's HOME presence and value unchanged")
	}
}

func (f *fixture) require(ok bool, name string) {
	f.t.Helper()
	if !ok {
		f.t.Fatal(name)
	}
	f.checks = append(f.checks, name)
}

type limitedBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("fixture command output exceeded bound")
	}
	return b.Buffer.Write(p)
}
func (b *limitedBuffer) value() []byte { b.Lock(); defer b.Unlock(); return bytes.Clone(b.Bytes()) }

func (f *fixture) run(program string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, program, args...)
	command.Env, command.Dir = f.env, f.root
	var stdout, stderr limitedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return stdout.value(), fmt.Errorf("%w; %s", ctx.Err(), safeCommandStderr(stderr.value()))
	}
	if err != nil {
		return stdout.value(), fmt.Errorf("%w; %s", err, safeCommandStderr(stderr.value()))
	}
	return stdout.value(), err
}

// CLI setup can print configuration to stdout. Never attach stdout or raw
// stderr to a public CI failure: emit only fixed diagnostic classifications.
func safeCommandStderr(stderr []byte) string {
	text := string(stderr)
	for _, diagnostic := range []struct{ contains, classification string }{
		{"find user cache directory: $HOME is not defined", "user cache defaults require HOME"},
		{"find user config directory: $HOME is not defined", "user config defaults require HOME"},
		{"neither $XDG_CACHE_HOME nor $HOME are defined", "user cache defaults require XDG_CACHE_HOME or HOME"},
		{"neither $XDG_CONFIG_HOME nor $HOME are defined", "user config defaults require XDG_CONFIG_HOME or HOME"},
		{"permission denied", "filesystem permission denied"},
		{"no space left on device", "filesystem has no free space"},
		{"insufficient free space", "filesystem free-space reservation rejected"},
		{"path is not private", "private path validation rejected"},
		{"symlink", "symbolic path validation rejected"},
		{"invalid setup", "setup configuration validation rejected"},
		{"Snapshot credential exchange returned HTTP", "snapshot credential exchange rejected"},
		{"Snapshot archive operation failed", "snapshot archive operation rejected"},
	} {
		if strings.Contains(text, diagnostic.contains) {
			return fmt.Sprintf("stderr classification: %s (%d bytes withheld)", diagnostic.classification, len(stderr))
		}
	}
	return fmt.Sprintf("stderr classification: unknown (%d bytes withheld)", len(stderr))
}

func TestCommandDiagnosticsDoNotExposeCapturedOutput(t *testing.T) {
	secret := "fixture-sensitive-output-that-must-not-appear"
	for _, test := range []struct{ stderr, want string }{
		{secret + "\nfind user cache directory: $HOME is not defined\n" + secret, "user cache defaults require HOME"},
		{secret + "\npermission denied\n" + secret, "filesystem permission denied"},
		{secret + ` {"localToken":"` + secret + `"}`, "classification: unknown"},
	} {
		actual := safeCommandStderr([]byte(test.stderr))
		if strings.Contains(actual, secret) || !strings.Contains(actual, test.want) || len(actual) > 160 {
			t.Fatal("command failure diagnostic disclosed captured bytes or lost its bounded classification")
		}
	}
}
func (f *fixture) command(label, program string, args ...string) []byte {
	f.t.Helper()
	output, err := f.run(program, args...)
	if err != nil {
		f.t.Fatalf("%s failed: %v (command output withheld)", label, err)
	}
	return output
}

type process struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
}

func (f *fixture) start(label, program string, args ...string) *process {
	f.t.Helper()
	command := exec.CommandContext(f.ctx, program, args...)
	command.Env, command.Dir = f.env, f.root
	command.Stdout, command.Stderr = &limitedBuffer{}, &limitedBuffer{}
	if err := command.Start(); err != nil {
		f.t.Fatalf("%s did not start: %v", label, err)
	}
	p := &process{command: command, done: make(chan struct{})}
	go func() { p.err = command.Wait(); close(p.done) }()
	f.t.Cleanup(func() { p.cleanup() })
	return p
}
func (p *process) cleanup() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.command.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
		return
	case <-time.After(3 * time.Second):
	}
	_ = p.command.Process.Kill()
	<-p.done
}
func (p *process) interrupt(t *testing.T) {
	t.Helper()
	if err := p.command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal("fixture process did not accept cancellation")
	}
	select {
	case <-p.done:
		if p.err != nil {
			t.Fatal("cancelled fixture process did not exit cleanly")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fixture process cancellation timed out")
	}
}
func (f *fixture) healthy(endpoint string) {
	f.t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	f.t.Fatal("real Team server did not become healthy")
}

type lifecycle struct {
	Version           int    `json:"version"`
	InstanceID        string `json:"instanceId"`
	Phase             string `json:"phase"`
	StoreClosed       bool   `json:"storeClosed"`
	OwnershipReleased bool   `json:"ownershipReleased"`
	Historical        *bool  `json:"historical"`
}
type adapter struct {
	process                 *process
	socket, ready, instance string
}

func (f *fixture) adapter(name, store string) adapter {
	f.t.Helper()
	socket, ready := filepath.Join(f.sockets, name+".sock"), filepath.Join(f.sockets, name+".json")
	p := f.start(name, f.binary, "xcode", "serve", "--data-dir", store, "--socket", socket, "--project", project, "--compatibility", profile, "--max-size", budget, "--stats-file", filepath.Join(f.sockets, name+"-stats.json"), "--ready-file", ready)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if absent(ready) {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		output, err := f.run(f.binary, "xcode", "status", "--ready-file", ready)
		var state lifecycle
		if err == nil && json.Unmarshal(output, &state) == nil && state.Phase == "ready" && !state.StoreClosed && !state.OwnershipReleased && state.Historical != nil && !*state.Historical && len(state.InstanceID) == 64 {
			return adapter{p, socket, ready, state.InstanceID}
		}
		time.Sleep(25 * time.Millisecond)
	}
	f.t.Fatal("Xcode service did not produce an authenticated ready status")
	return adapter{}
}
func (f *fixture) stopAdapter(a adapter) {
	f.t.Helper()
	output := f.command("close exact Xcode instance", f.binary, "xcode", "stop", "--ready-file", a.ready)
	var state lifecycle
	if json.Unmarshal(output, &state) != nil || state.Phase != "stopped" || !state.StoreClosed || !state.OwnershipReleased || state.InstanceID != a.instance || state.Historical == nil || *state.Historical {
		f.t.Fatal("stop did not acknowledge exact closed instance")
	}
	select {
	case <-a.process.done:
		if a.process.err != nil {
			f.t.Fatal("stopped Xcode process failed")
		}
	case <-time.After(5 * time.Second):
		f.t.Fatal("stopped Xcode process remained running")
	}
	f.require(absent(a.socket), "closed adapter removes RPC socket")
}

type result struct {
	Hit    bool   `json:"hit"`
	Source string `json:"source"`
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}

func (f *fixture) native(label, operation, credential, path, cache, identity, compatibility, key string, success bool) result {
	f.t.Helper()
	output, err := f.run(f.node, f.nativeCLI, operation, "--endpoint", f.endpoint, "--credential-file", credential, "--project", identity, "--compatibility", compatibility, "--key", key, "--path", path, "--cache-dir", cache, "--max-bytes", budget)
	if !success {
		if err == nil {
			f.t.Fatalf("%s unexpectedly succeeded", label)
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() <= 0 {
			f.t.Fatalf("%s failed through infrastructure rather than a normal client rejection", label)
		}
		return result{}
	}
	if err != nil {
		f.t.Fatalf("%s failed: %v (command output withheld)", label, err)
	}
	var r result
	if json.Unmarshal(output, &r) != nil {
		f.t.Fatalf("%s returned invalid result JSON", label)
	}
	return r
}

func connect(t *testing.T, socket string) (cas.CASDBServiceClient, kv.KeyValueDBClient, *grpc.ClientConn) {
	t.Helper()
	connection, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return cas.NewCASDBServiceClient(connection), kv.NewKeyValueDBClient(connection), connection
}
func data(value []byte) *cas.CASBytes {
	return &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: value}}
}

type graphIDs struct{ leaf, manifest []byte }

func verifyGraph(t *testing.T, ctx context.Context, socket string, expected graphIDs) {
	t.Helper()
	objects, actions, connection := connect(t, socket)
	defer connection.Close()
	action, err := actions.GetValue(ctx, &kv.GetValueRequest{Key: []byte("fixture compile action")})
	if err != nil || action.GetOutcome() != kv.GetValueResponse_SUCCESS || !bytes.Equal(action.GetValue().Entries["CASID"], expected.manifest) {
		t.Fatal("fresh adapter cannot restore fixture action")
	}
	manifest, err := objects.Get(ctx, &cas.CASGetRequest{CasId: &cas.CASDataID{Id: action.GetValue().Entries["CASID"]}})
	if err != nil || manifest.GetOutcome() != cas.CASGetResponse_SUCCESS || string(manifest.GetData().GetBlob().GetData()) != "synthetic manifest\n" || len(manifest.GetData().GetReferences()) != 1 || !bytes.Equal(manifest.GetData().References[0].Id, expected.leaf) {
		t.Fatal("fresh adapter cannot restore exact fixture manifest")
	}
	object, err := objects.Load(ctx, &cas.CASLoadRequest{CasId: manifest.GetData().References[0]})
	if err != nil || object.GetOutcome() != cas.CASLoadResponse_SUCCESS || string(object.GetData().GetBlob().GetData()) != "synthetic compiled object\n" {
		t.Fatal("fresh adapter cannot restore exact transitive CAS bytes")
	}
}

type grant struct {
	ID            string    `json:"id"`
	SecretSHA256  string    `json:"secretSha256"`
	Project       string    `json:"project"`
	Compatibility string    `json:"compatibility"`
	KeyPrefix     string    `json:"keyPrefix"`
	Role          string    `json:"role"`
	ExpiresAt     time.Time `json:"expiresAt"`
}
type bootstrap struct {
	token string
	grant grant
}

func newGrant(id, role string) bootstrap {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(err)
	}
	token := "lcs1." + id + "." + hex.EncodeToString(secret)
	sum := sha256.Sum256([]byte(token))
	return bootstrap{token, grant{id, hex.EncodeToString(sum[:]), project, profile, "xcode-snapshots/", role, time.Now().UTC().Add(time.Hour)}}
}
func writeGrants(t *testing.T, path string, grants ...grant) {
	if grants == nil {
		grants = []grant{}
	}
	writeJSON(t, path, struct {
		Version     int     `json:"version"`
		Credentials []grant `json:"credentials"`
	}{1, grants})
}
func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writePrivate(t, path, append(encoded, '\n'))
}
func writePrivate(t *testing.T, path string, value []byte) {
	t.Helper()
	temporary := path + ".new"
	if err := os.WriteFile(temporary, value, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
}
func decodeFile(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(encoded, value) != nil {
		t.Fatal("invalid fixture state file")
	}
}
func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}
func absent(path string) bool { _, err := os.Lstat(path); return errors.Is(err, os.ErrNotExist) }
func noSockets(root string) bool {
	ok := true
	err := filepath.WalkDir(root, func(_ string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type()&os.ModeSocket != 0 {
			ok = false
		}
		return nil
	})
	return err == nil && ok
}
func noArchives(root string) bool {
	ok := true
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".tgz") || strings.HasSuffix(path, ".partial") {
			ok = false
		}
		return nil
	})
	return err == nil && ok
}

type capability struct {
	Version      int       `json:"version"`
	Token        string    `json:"token"`
	NativeKey    string    `json:"nativeKey"`
	Mode         string    `json:"mode"`
	Capabilities []string  `json:"capabilities"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

func exchange(t *testing.T, endpoint, bootstrap, project, profile, key, mode string) capability {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"project": project, "compatibility": profile, "key": key, "mode": mode})
	request, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/auth/snapshot/exchange", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+bootstrap)
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("real capability exchange failed")
	}
	defer response.Body.Close()
	var result capability
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&result) != nil || result.Token == "" || !strings.HasPrefix(result.NativeKey, "native-v1-") {
		t.Fatal("real capability exchange rejected valid reader")
	}
	return result
}
func artifactRequest(t *testing.T, endpoint string, cap capability, method, key string) int {
	t.Helper()
	u := endpoint + "/v8/artifacts/" + key + "?teamId=" + url.QueryEscape(project)
	request, _ := http.NewRequest(method, u, strings.NewReader("forbidden replacement"))
	request.Header.Set("Authorization", "Bearer "+cap.Token)
	request.Header.Set("X-LayerCache-Compatibility", profile)
	client := http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("scoped artifact control failed")
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return response.StatusCode
}
func denied(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

type observedRequest struct{ phase, method string }
type observedReply struct {
	phase, path string
	status      int
}
type auditProxy struct {
	sync.Mutex
	server                     *httptest.Server
	currentPhase, currentFault string
	requests                   []observedRequest
	replies                    []observedReply
	modifications              map[string]int
}

func newAudit(t *testing.T, endpoint string) *auditProxy {
	t.Helper()
	target, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	a := &auditProxy{modifications: make(map[string]int)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{DisableKeepAlives: true}
	proxy.ModifyResponse = func(response *http.Response) error {
		a.Lock()
		fault := a.currentFault
		a.replies = append(a.replies, observedReply{a.currentPhase, response.Request.URL.Path, response.StatusCode})
		a.Unlock()
		if response.StatusCode != 200 || response.Request.Method != http.MethodGet || !strings.HasPrefix(response.Request.URL.Path, "/v8/artifacts/") || fault == "" {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
		_ = response.Body.Close()
		if err != nil || len(body) < 2 {
			return errors.New("invalid fixture response")
		}
		if fault == "corrupt" {
			body[len(body)/2] ^= 1
		} else {
			body = body[:len(body)/2]
		}
		a.Lock()
		a.modifications[fault]++
		a.Unlock()
		response.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	a.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.Lock()
		if strings.HasPrefix(r.URL.Path, "/v8/artifacts/") {
			a.requests = append(a.requests, observedRequest{a.currentPhase, r.Method})
		}
		a.Unlock()
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(a.server.Close)
	return a
}
func (a *auditProxy) phase(phase string) { a.Lock(); a.currentPhase = phase; a.Unlock() }
func (a *auditProxy) fault(fault string) { a.Lock(); a.currentFault = fault; a.Unlock() }
func (a *auditProxy) count(phase, method string) int {
	a.Lock()
	defer a.Unlock()
	n := 0
	for _, r := range a.requests {
		if r.phase == phase && r.method == method {
			n++
		}
	}
	return n
}
func (a *auditProxy) puts() int {
	a.Lock()
	defer a.Unlock()
	n := 0
	for _, r := range a.requests {
		if r.method == http.MethodPut {
			n++
		}
	}
	return n
}

func (a *auditProxy) exchangeDenied(phase string) bool {
	a.Lock()
	defer a.Unlock()
	for _, reply := range a.replies {
		if reply.phase == phase && reply.path == "/v1/auth/snapshot/exchange" && (reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden) {
			return true
		}
	}
	return false
}

func (a *auditProxy) modified(fault string) int {
	a.Lock()
	defer a.Unlock()
	return a.modifications[fault]
}

func (f *fixture) writeReport(archiveBytes int64) {
	path := os.Getenv("LAYER_CACHE_XCODE_LIFECYCLE_REPORT")
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) {
		f.t.Fatal("receipt path must be absolute")
	}
	hash := func(path string) string {
		encoded, err := os.ReadFile(path)
		if err != nil {
			f.t.Fatal("cannot hash qualifier input")
		}
		digest := sha256.Sum256(encoded)
		return hex.EncodeToString(digest[:])
	}
	report := struct {
		Version            int      `json:"version"`
		Passed             bool     `json:"passed"`
		Checks             []string `json:"checks"`
		GoBinarySHA256     string   `json:"goBinarySha256"`
		NativeBundleSHA256 string   `json:"nativeBundleSha256"`
		ArchiveBytes       int64    `json:"archiveBytes"`
		Scope              string   `json:"scope"`
		NativeCompilation  bool     `json:"nativeCompilation"`
	}{1, true, f.checks, hash(f.binary), hash(f.nativeCLI), archiveBytes, "loopback actual Go CLI/Team server, bundled native CLI, synthetic Xcode CAS graph", false}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		f.t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		f.t.Fatal("cannot create new private qualifier receipt")
	}
	_, writeErr := fmt.Fprintln(file, string(encoded))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		f.t.Fatal("cannot finish qualifier receipt")
	}
}
