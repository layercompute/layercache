package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
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
	"google.golang.org/protobuf/encoding/protowire"
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

func TestNativePressureAllowsUnconsumedFirstRootWhenGraphRemainsAvailable(t *testing.T) {
	fixture := newNativePressureFixture(t)
	objects, actions := connect(t, fixture.proxy)
	ctx := context.Background()
	if hit, err := actions.GetValue(ctx, &kv.GetValueRequest{Key: []byte("native-action")}); err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS {
		t.Fatalf("native action promise: %v %v", hit, err)
	}
	// The native node is an unused first output. Consuming its promised leaf
	// still proves real replay; both objects were verified after eviction.
	if output, err := objects.Load(ctx, &cas.CASLoadRequest{CasId: fixture.leaf}); err != nil || output.GetOutcome() != cas.CASLoadResponse_SUCCESS {
		t.Fatalf("consumed native output: %v %v", output, err)
	}
	stats := fixture.proxy.snapshot()
	if err := stats.validate(); err != nil || stats.CompilerRootReads != 0 || stats.CompilerGraphReads != 1 || stats.AvailablePromisedObjects != 2 || stats.FetchedPromisedObjects != 1 || len(stats.UnfetchedPromisedObjects) != 1 {
		t.Fatalf("unused optional output was confused with lost output: %v %+v", err, stats)
	}
}

type unleasedActions struct {
	kv.KeyValueDBClient
	value *kv.Value
}

func (s unleasedActions) GetValue(context.Context, *kv.GetValueRequest, ...grpc.CallOption) (*kv.GetValueResponse, error) {
	return &kv.GetValueResponse{Outcome: kv.GetValueResponse_SUCCESS, Contents: &kv.GetValueResponse_Value{Value: s.value}}, nil
}

func TestNativePressureRejectsEvictedUnconsumedDescendantBeforeForwardingHit(t *testing.T) {
	fixture := newNativePressureFixture(t)
	ctx := context.Background()
	key := func(id []byte) artifact.Key {
		return artifact.Key{Integration: "xcode", Project: "qa-project", Compatibility: "qa-toolchain", Native: "cas-" + hex.EncodeToString(id), Version: "1"}
	}
	root, err := fixture.store.Head(ctx, key(fixture.node.Id))
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce a root-only retention bug. The first inspection sees a healthy
	// complete graph, then real quota writes evict its unleased leaf.
	if err := fixture.store.RetainForRead(ctx, "qa-root-only", "consumer", []artifact.Entry{root}, time.Minute); err != nil {
		t.Fatal(err)
	}
	fixture.proxy.actions = unleasedActions{value: &kv.Value{Entries: map[string][]byte{"value": fixture.value}}}
	_, actions := connect(t, fixture.proxy)
	if hit, err := actions.GetValue(ctx, &kv.GetValueRequest{Key: []byte("native-action")}); err == nil {
		t.Fatalf("unconsumed evicted descendant survived QA validation: %v", hit)
	}
	stats := fixture.proxy.snapshot()
	if stats.GraphObjectsInspected != 2 || stats.AvailablePromisedObjects != 0 || stats.PressureWrites == 0 || stats.EvictedBytes <= 0 || stats.PressureError == "" {
		t.Fatalf("test did not exercise eviction between graph inspections: %+v", stats)
	}
	if _, err := fixture.store.Head(ctx, key(fixture.node.Id)); err != nil {
		t.Fatalf("promised root was not retained in root-only fixture: %v", err)
	}
	if _, err := fixture.store.Head(ctx, key(fixture.leaf.Id)); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("fixture did not evict the unconsumed descendant: %v", err)
	}
}

func qaNativeNode(data []byte, id []byte, node bool) []byte {
	return qaNativeRoots(data, xcodecache.GraphReference{ID: id, NativeNode: node})
}

func qaNativeRoots(data []byte, refs ...xcodecache.GraphReference) []byte {
	field := func(number protowire.Number, value []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, number, protowire.BytesType), value)
	}
	encoded := field(1, field(1, data))
	for _, ref := range refs {
		descriptor := append(field(1, make([]byte, 65)), field(2, ref.ID)...)
		if ref.NativeNode {
			descriptor = protowire.AppendVarint(protowire.AppendTag(descriptor, 3, protowire.VarintType), 1)
		}
		encoded = append(encoded, field(2, field(2, descriptor))...)
	}
	return encoded
}

func TestNativePressureSelectsDistinctExternalRootsWithoutConsumingEarlierHit(t *testing.T) {
	fixture := newNativePressureFixture(t)
	fixture.proxy.minNativeRoots = 2
	objects, actions := connect(t, fixture.proxy)
	ctx := context.Background()
	if hit, err := actions.GetValue(ctx, &kv.GetValueRequest{Key: []byte("native-action")}); err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS || !bytes.Equal(hit.GetValue().Entries["value"], fixture.value) {
		t.Fatalf("earlier single-root hit was not forwarded normally: %v %v", hit, err)
	}
	if output, err := objects.Load(ctx, &cas.CASLoadRequest{CasId: fixture.leaf}); err != nil || output.GetOutcome() != cas.CASLoadResponse_SUCCESS {
		t.Fatalf("earlier ordinary compiler read: %v %v", output, err)
	}
	if stats := fixture.proxy.snapshot(); stats.PressureWrites != 0 || stats.PromisedRoot != "" || stats.validate() == nil {
		t.Fatalf("single-root-only producer falsely qualified: %+v", stats)
	}
	if _, err := fixture.actions.PutValue(ctx, &kv.PutValueRequest{Key: []byte("relay-root"), Value: &kv.Value{Entries: map[string][]byte{"CASID": fixture.node.Id}}}); err != nil {
		t.Fatal(err)
	}
	if hit, err := actions.GetValue(ctx, &kv.GetValueRequest{Key: []byte("relay-root")}); err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS || fixture.proxy.snapshot().PressureWrites != 0 {
		t.Fatalf("relay root consumed the native multiple-root attempt: %v %v", hit, err)
	}
	nodeRef := xcodecache.GraphReference{ID: fixture.node.Id, NativeNode: true}
	for _, item := range []struct {
		key  string
		refs []xcodecache.GraphReference
	}{
		{"duplicate-root", []xcodecache.GraphReference{nodeRef, nodeRef}},
		{"multiple-roots", []xcodecache.GraphReference{nodeRef, {ID: fixture.leaf.Id}}},
	} {
		value := qaNativeRoots([]byte(item.key), item.refs...)
		if response, err := fixture.actions.PutValue(ctx, &kv.PutValueRequest{Key: []byte(item.key), Value: &kv.Value{Entries: map[string][]byte{"value": value}}}); err != nil || response.GetError() != nil {
			t.Fatalf("store %s action: %v %v", item.key, response, err)
		}
		if response, err := actions.GetValue(ctx, &kv.GetValueRequest{Key: []byte(item.key)}); err != nil || response.GetOutcome() != kv.GetValueResponse_SUCCESS {
			t.Fatalf("lookup %s action: %v %v", item.key, response, err)
		}
		if item.key == "duplicate-root" && fixture.proxy.snapshot().PressureWrites != 0 {
			t.Fatal("duplicate descriptors substituted for two distinct external roots")
		}
	}
	if output, err := objects.Load(ctx, &cas.CASLoadRequest{CasId: fixture.leaf}); err != nil || output.GetOutcome() != cas.CASLoadResponse_SUCCESS {
		t.Fatalf("selected action output lost after pressure: %v %v", output, err)
	}
	stats := fixture.proxy.snapshot()
	if err := stats.validate(); err != nil || stats.NativeExternalRoots != 2 || stats.PromisedObjects != 2 || stats.AvailablePromisedObjects != 2 || stats.CompilerGraphReads != 1 {
		t.Fatalf("multiple-root pressure qualification: %v %+v", err, stats)
	}
}

type nativePressureFixture struct {
	store                     *artifact.Store
	service                   *xcodecache.Service
	objects                   cas.CASDBServiceClient
	actions                   kv.KeyValueDBClient
	proxy                     *pressureProxy
	leaf, node                *cas.CASDataID
	leafData, nodeData, value []byte
}

func newNativePressureFixture(t *testing.T) nativePressureFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	const quota = 4096
	store, err := artifact.Open(ctx, root, quota, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	service, err := xcodecache.New(store, "qa-project", "qa-toolchain")
	if err != nil {
		t.Fatal(err)
	}
	objects, actions := connect(t, service)
	leafData := bytes.Repeat([]byte("native-output"), 20)
	leaf, err := objects.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: leafData}}}})
	if err != nil {
		t.Fatal(err)
	}
	nodeData := qaNativeNode([]byte("native manifest"), leaf.GetCasId().Id, false)
	node, err := objects.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: nodeData}}}})
	if err != nil {
		t.Fatal(err)
	}
	value := qaNativeNode([]byte("inline native result"), node.GetCasId().Id, true)
	if response, err := actions.PutValue(ctx, &kv.PutValueRequest{Key: []byte("native-action"), Value: &kv.Value{Entries: map[string][]byte{"value": value}}}); err != nil || response.GetError() != nil {
		t.Fatalf("native action store: %v %v", response, err)
	}
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(root, "metadata.db"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	proxy := newProxy(objects, actions, db, quota)
	proxy.storeRoot = root
	return nativePressureFixture{store, service, objects, actions, proxy, leaf.GetCasId(), node.GetCasId(), leafData, nodeData, value}
}

func TestNativeCompilerLoadRestoresCompleteGraphAfterPressure(t *testing.T) {
	ctx := context.Background()
	fixture := newNativePressureFixture(t)
	proxy, service, leaf, node := fixture.proxy, fixture.service, fixture.leaf, fixture.node
	leafData, nodeData := fixture.leafData, fixture.nodeData
	compilerObjects, compilerActions := connect(t, proxy)
	hit, err := compilerActions.GetValue(ctx, &kv.GetValueRequest{Key: []byte("native-action")})
	if err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS {
		t.Fatalf("native action promise: %v %v; %+v", hit, err, proxy.snapshot())
	}
	refs, err := xcodecache.ActionReferences(hit.GetValue())
	if err != nil || len(refs) != 1 {
		t.Fatalf("native inline references: %+v %v", refs, err)
	}
	manifest, err := compilerObjects.Load(ctx, &cas.CASLoadRequest{CasId: &cas.CASDataID{Id: refs[0].ID}})
	if err != nil || manifest.GetOutcome() != cas.CASLoadResponse_SUCCESS || !bytes.Equal(manifest.GetData().GetBlob().GetData(), nodeData) {
		t.Fatalf("native manifest lost after promise: %v %v; %+v", manifest, err, proxy.snapshot())
	}
	// Read-only inspection proves the unfetched descendant is still available.
	// Actual compiler reads remain a separate count, including repeated Loads.
	if _, err := compilerObjects.Load(ctx, &cas.CASLoadRequest{CasId: node}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.snapshot().validate(); err != nil {
		t.Fatalf("healthy optional output need not be consumed: %v %+v", err, proxy.snapshot())
	}
	output, err := compilerObjects.Load(ctx, &cas.CASLoadRequest{CasId: leaf})
	if err != nil || output.GetOutcome() != cas.CASLoadResponse_SUCCESS || !bytes.Equal(output.GetData().GetBlob().GetData(), leafData) {
		t.Fatalf("native output lost after promise: %v %v", output, err)
	}
	stats := proxy.snapshot()
	if err := stats.validate(); err != nil {
		t.Fatalf("native pressure qualification: %v %+v", err, stats)
	}
	if !stats.InlineActionRoot || stats.CompilerRootGets != 0 || stats.GraphObjectsInspected != 2 || stats.PromisedObjects != 2 || stats.FetchedPromisedObjects != 2 || stats.CompilerGraphReads != 3 {
		t.Fatalf("native graph proof or actual compiler counts: %+v", stats)
	}
	if actual := service.Stats(); actual.CASReads != 3 || actual.CASHits != 3 || actual.GraphChecks != 1 || actual.GraphObjectsChecked != 2 {
		t.Fatalf("inspection inflated RPC hit counters: %+v", actual)
	}
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
	proxy.storeRoot = root
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
	valid := observations{QuotaBytes: 1024, PressurePayloadBytes: 2048, EvictedBytes: 512, MaxObservedUsageBytes: 1000, PromisedRoot: "root", CompilerRootReads: 1, CompilerRootGets: 1, CompilerGraphReads: 2, PromisedObjects: 2, GraphObjectsInspected: 2, AvailablePromisedObjects: 2, FetchedPromisedObjects: 2, QueryToFirstGraphReadNanos: 1, QueryToLastGraphReadNanos: 2}
	for name, mutate := range map[string]func(*observations){
		"no pressure":        func(s *observations) { s.PressurePayloadBytes = 1024 },
		"no eviction":        func(s *observations) { s.EvictedBytes = 0 },
		"quota exceeded":     func(s *observations) { s.MaxObservedUsageBytes = 1025 },
		"no real fetch":      func(s *observations) { s.CompilerGraphReads = 0 },
		"uninspected graph":  func(s *observations) { s.GraphObjectsInspected = 0 },
		"unavailable leaf":   func(s *observations) { s.AvailablePromisedObjects = 1 },
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
		t.Fatal("root-only reads passed without verifying the discovered leaf remained available")
	}
}

func TestPressureValidationRejectsLoadWithoutGraphInspection(t *testing.T) {
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
		t.Fatal("Load-only root passed without complete graph inspection")
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

func TestPressureValidationReportsUnfetchedGrandchildDiscoveredOutOfOrder(t *testing.T) {
	proxy := newProxy(nil, nil, nil, 1024)
	root, child, grandchild := []byte("root"), []byte("child"), []byte("grandchild")
	proxy.root, proxy.graph[string(root)], proxy.done = string(root), true, true
	proxy.queried, proxy.finished = time.Now(), time.Now()
	proxy.stats.PromisedRoot = "root"
	proxy.stats.PressurePayloadBytes = 2048
	proxy.stats.EvictedBytes = 512
	proxy.stats.MaxObservedUsageBytes = 1000
	proxy.stats.GraphObjectsInspected, proxy.stats.AvailablePromisedObjects = 3, 3
	proxy.recordRead(time.Now(), child, true, []*cas.CASDataID{{Id: grandchild}}, true)
	proxy.recordRead(time.Now(), root, true, []*cas.CASDataID{{Id: child}}, true)
	partial := proxy.snapshot()
	if err := partial.validate(); err != nil || partial.PromisedObjects != 3 || partial.FetchedPromisedObjects != 2 || len(partial.UnfetchedPromisedObjects) != 1 {
		t.Fatalf("out-of-order consumption hid the available unfetched grandchild: %v %+v", err, partial)
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
