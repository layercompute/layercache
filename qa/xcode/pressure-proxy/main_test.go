package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/xcodecache"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func connect(t *testing.T, service interface {
	cas.CASDBServiceServer
	kv.KeyValueDBServer
}) (cas.CASDBServiceClient, kv.KeyValueDBClient) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.MaxRecvMsgSize(messageLimit), grpc.MaxSendMsgSize(messageLimit), grpc.WaitForHandlers(true))
	cas.RegisterCASDBServiceServer(server, service)
	kv.RegisterKeyValueDBServer(server, service)
	go func() { _ = server.Serve(listener) }()
	connection, err := grpc.NewClient("passthrough:///pressure-qa", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close(); server.Stop(); listener.Close() })
	return cas.NewCASDBServiceClient(connection), kv.NewKeyValueDBClient(connection)
}

// This same test fails against the pre-lease implementation: GetValue promises
// SUCCESS, then pressure evicts its graph before the client's first fetch.
func TestCompilerFetchSurvivesPressureAfterSuccessfulAction(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	const quota = 4096
	store, err := artifact.Open(ctx, root, quota, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := xcodecache.New(store, "qa-project", "qa-toolchain")
	if err != nil {
		t.Fatal(err)
	}
	objects, actions := connect(t, service)
	leafData := bytes.Repeat([]byte("compiled-object"), 20)
	leaf, err := objects.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: leafData}}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := objects.Put(ctx, &cas.CASPutRequest{Data: &cas.CASObject{Blob: &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: []byte("manifest")}}, References: []*cas.CASDataID{leaf.GetCasId()}}})
	if err != nil {
		t.Fatal(err)
	}
	if response, err := actions.PutValue(ctx, &kv.PutValueRequest{Key: []byte("compiler-action"), Value: &kv.Value{Entries: map[string][]byte{"CASID": manifest.GetCasId().GetId()}}}); err != nil || response.GetError() != nil {
		t.Fatalf("store action: %v %v", response, err)
	}
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(root, "metadata.db"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	proxy := newProxy(objects, actions, db, quota)
	compilerObjects, compilerActions := connect(t, proxy)
	hit, err := compilerActions.GetValue(ctx, &kv.GetValueRequest{Key: []byte("compiler-action")})
	if err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS {
		t.Fatalf("compiler action hit: %v %v", hit, err)
	}
	restored, err := compilerObjects.Get(ctx, &cas.CASGetRequest{CasId: &cas.CASDataID{Id: hit.GetValue().GetEntries()["CASID"]}})
	if err != nil || restored.GetOutcome() != cas.CASGetResponse_SUCCESS {
		t.Fatalf("promised root lost after pressure: %v %v; evidence %+v", restored, err, proxy.snapshot())
	}
	if len(restored.GetData().GetReferences()) != 1 {
		t.Fatalf("restored references: %v", restored)
	}
	output, err := compilerObjects.Load(ctx, &cas.CASLoadRequest{CasId: restored.GetData().GetReferences()[0]})
	if err != nil || output.GetOutcome() != cas.CASLoadResponse_SUCCESS || !bytes.Equal(output.GetData().GetBlob().GetData(), leafData) {
		t.Fatalf("promised output lost after pressure: %v %v", output, err)
	}
	stats := proxy.snapshot()
	if err := stats.validate(); err != nil {
		t.Fatalf("pressure evidence: %v; %+v", err, stats)
	}
	if stats.CompilerGraphReads != 2 || stats.CompilerRootReads != 1 || stats.CompilerRootGets != 1 || stats.PromisedObjects != 2 || stats.FetchedPromisedObjects != 2 {
		t.Fatalf("fixture writes inflated compiler reads: %+v", stats)
	}
}

func TestPressureValidationRequiresEvictionAndPromisedFetch(t *testing.T) {
	valid := observations{QuotaBytes: 1024, PressurePayloadBytes: 2048, EvictedBytes: 512, MaxObservedUsageBytes: 1000, PromisedRoot: "root", CompilerRootReads: 1, CompilerRootGets: 1, CompilerGraphReads: 2, PromisedObjects: 2, FetchedPromisedObjects: 2}
	for name, mutate := range map[string]func(*observations){
		"no pressure":        func(s *observations) { s.PressurePayloadBytes = 1024 },
		"no eviction":        func(s *observations) { s.EvictedBytes = 0 },
		"quota exceeded":     func(s *observations) { s.MaxObservedUsageBytes = 1025 },
		"no real fetch":      func(s *observations) { s.CompilerRootReads = 0 },
		"load-only root":     func(s *observations) { s.CompilerRootGets = 0 },
		"unfetched leaf":     func(s *observations) { s.UnfetchedPromisedObjects = []string{"leaf"}; s.FetchedPromisedObjects = 1 },
		"missing descendant": func(s *observations) { s.MissingPromisedObjects = []string{"leaf"} },
	} {
		t.Run(name, func(t *testing.T) {
			stats := valid
			mutate(&stats)
			if stats.validate() == nil {
				t.Fatalf("invalid pressure evidence accepted: %+v", stats)
			}
		})
	}
}

func TestPressureValidationRejectsRootOnlyRepeatedReads(t *testing.T) {
	proxy := newProxy(nil, nil, nil, 1024)
	root, leaf := []byte("root"), []byte("leaf")
	proxy.root, proxy.graph[string(root)], proxy.done = string(root), true, true
	proxy.queried, proxy.finished = time.Now(), time.Now()
	proxy.stats.PromisedRoot = "root"
	proxy.stats.PressurePayloadBytes = 2048
	proxy.stats.EvictedBytes = 512
	proxy.stats.MaxObservedUsageBytes = 1000
	for read := 0; read < 2; read++ {
		proxy.recordRead(time.Now(), root, true, []*cas.CASDataID{{Id: leaf}}, true)
	}
	if err := proxy.snapshot().validate(); err == nil {
		t.Fatal("root-only reads passed without fetching the discovered leaf")
	}
}

func TestPressureValidationRejectsLoadOnlyRoot(t *testing.T) {
	proxy := newProxy(nil, nil, nil, 1024)
	root := []byte("root")
	proxy.root, proxy.graph[string(root)], proxy.done = string(root), true, true
	proxy.queried, proxy.finished = time.Now(), time.Now()
	proxy.stats.PromisedRoot = "root"
	proxy.stats.PressurePayloadBytes = 2048
	proxy.stats.EvictedBytes = 512
	proxy.stats.MaxObservedUsageBytes = 1000
	proxy.recordRead(time.Now(), root, true, nil, false)
	if err := proxy.snapshot().validate(); err == nil {
		t.Fatal("Load-only root passed without discovering its references")
	}
}

func TestPressureValidationKeepsFailureBeforeReferenceDiscovery(t *testing.T) {
	proxy := newProxy(nil, nil, nil, 1024)
	root, leaf := []byte("root"), []byte("leaf")
	proxy.root, proxy.graph[string(root)], proxy.done = string(root), true, true
	proxy.queried, proxy.finished = time.Now(), time.Now()
	proxy.stats.PromisedRoot = "root"
	proxy.stats.PressurePayloadBytes = 2048
	proxy.stats.EvictedBytes = 512
	proxy.stats.MaxObservedUsageBytes = 1000
	proxy.recordRead(time.Now(), leaf, false, nil, false)
	proxy.recordRead(time.Now(), leaf, true, nil, false)
	proxy.recordRead(time.Now(), root, true, []*cas.CASDataID{{Id: leaf}}, true)
	stats := proxy.snapshot()
	if stats.FetchedPromisedObjects != 2 || len(stats.MissingPromisedObjects) != 1 || stats.validate() == nil {
		t.Fatalf("successful retries masked a promised-object failure: %+v", stats)
	}
}

func TestPressureValidationRejectsUnfetchedGrandchildDiscoveredOutOfOrder(t *testing.T) {
	proxy := newProxy(nil, nil, nil, 1024)
	root, child, grandchild := []byte("root"), []byte("child"), []byte("grandchild")
	proxy.root, proxy.graph[string(root)], proxy.done = string(root), true, true
	proxy.queried, proxy.finished = time.Now(), time.Now()
	proxy.stats.PromisedRoot = "root"
	proxy.stats.PressurePayloadBytes = 2048
	proxy.stats.EvictedBytes = 512
	proxy.stats.MaxObservedUsageBytes = 1000
	proxy.recordRead(time.Now(), child, true, []*cas.CASDataID{{Id: grandchild}}, true)
	proxy.recordRead(time.Now(), root, true, []*cas.CASDataID{{Id: child}}, true)
	if err := proxy.snapshot().validate(); err == nil {
		t.Fatal("child-before-parent reads passed without fetching the discovered grandchild")
	}
	proxy.recordRead(time.Now(), grandchild, true, nil, false)
	stats := proxy.snapshot()
	if err := stats.validate(); err != nil || stats.PromisedObjects != 3 || stats.FetchedPromisedObjects != 3 || stats.CompilerGraphReads != 3 {
		t.Fatalf("complete out-of-order graph rejected: %v %+v", err, stats)
	}
}

func TestWrapperPreservesCompilerExitAndClosesProxySocket(t *testing.T) {
	// Keep the socket short independently of the store/test temporary path.
	sockets, err := os.MkdirTemp("/tmp", "lc-pressure-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	root := t.TempDir()
	socket := filepath.Join(sockets, "proxy.sock")
	stats := filepath.Join(root, "stats.json")
	err = run(context.Background(), []string{
		"--socket", socket, "--store", root, "--quota", "4096", "--stats", stats,
		"--", "sh", "-c", `test "$1" = "COMPILATION_CACHE_REMOTE_SERVICE_PATH=$2" || exit 99; exit 7`, "qa-child", "COMPILATION_CACHE_REMOTE_SERVICE_PATH=" + filepath.Join(sockets, "backend.sock"), socket,
	})
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("compiler exit was replaced or socket argument was not rewritten: %v", err)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proxy socket survived child exit: %v", err)
	}
	if _, err := os.Stat(stats); err != nil {
		t.Fatalf("child failure lost pressure evidence: %v", err)
	}
}
