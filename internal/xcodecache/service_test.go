package xcodecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func blob(data []byte) *cas.CASBytes { return &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: data}} }
func openService(t *testing.T, root, project, abi string) *Service {
	t.Helper()
	store, err := artifact.Open(context.Background(), root, 32<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s, err := New(store, project, abi)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func connect(t *testing.T, s *Service) (cas.CASDBServiceClient, kv.KeyValueDBClient) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	cas.RegisterCASDBServiceServer(server, s)
	kv.RegisterKeyValueDBServer(server, s)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///cache", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return cas.NewCASDBServiceClient(conn), kv.NewKeyValueDBClient(conn)
}

func TestProtocolSurvivesClosedProducerAndFreshConsumer(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	producer := openService(t, root, "github.com/test/app", "xcode-test-1")
	c, k := connect(t, producer)
	empty, err := c.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(nil)}})
	if err != nil || len(empty.GetCasId().GetId()) != sha256.Size {
		t.Fatalf("LLVM empty-object handshake: %v %v", empty, err)
	}
	leaf, err := c.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("object code"))}})
	if err != nil {
		t.Fatal(err)
	}
	object, err := c.Put(ctx, &cas.CASPutRequest{Data: &cas.CASObject{Blob: blob([]byte("compiler manifest")), References: []*cas.CASDataID{leaf.GetCasId()}}})
	if err != nil {
		t.Fatal(err)
	}
	value := &kv.Value{Entries: map[string][]byte{"CASID": object.GetCasId().Id, "other": []byte("metadata")}}
	if _, err = k.PutValue(ctx, &kv.PutValueRequest{Key: []byte("compile action"), Value: value}); err != nil {
		t.Fatal(err)
	}
	if err = producer.store.Close(); err != nil {
		t.Fatal(err)
	}
	consumer := openService(t, root, "github.com/test/app", "xcode-test-1")
	c, k = connect(t, consumer)
	action, err := k.GetValue(ctx, &kv.GetValueRequest{Key: []byte("compile action")})
	if err != nil || action.GetOutcome() != kv.GetValueResponse_SUCCESS {
		t.Fatalf("warm action: %v %v", action, err)
	}
	got, err := c.Get(ctx, &cas.CASGetRequest{CasId: &cas.CASDataID{Id: action.GetValue().Entries["CASID"]}, WriteToDisk: true})
	if err != nil || got.GetOutcome() != cas.CASGetResponse_SUCCESS || string(got.GetData().GetBlob().GetData()) != "compiler manifest" {
		t.Fatalf("warm manifest: %v %v", got, err)
	}
	loaded, err := c.Load(ctx, &cas.CASLoadRequest{CasId: got.GetData().References[0], WriteToDisk: true})
	if err != nil || !bytes.Equal(loaded.GetData().GetBlob().GetData(), []byte("object code")) {
		t.Fatalf("warm output: %v %v", loaded, err)
	}
	if stats := consumer.Stats(); stats.ActionHits != 1 || stats.CASHits != 2 {
		t.Fatalf("real consumer hits: %+v", stats)
	}
}

func TestProjectToolchainMissAndImmutableActionCollision(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	producer := openService(t, root, "project-a", "toolchain-a")
	original := nativeTestNode([]byte("first"))
	request := &kv.PutValueRequest{Key: []byte("action"), Value: &kv.Value{Entries: map[string][]byte{"value": original}}}
	if response, err := producer.PutValue(ctx, request); err != nil || response.GetError() != nil {
		t.Fatalf("first save: %v %v", response, err)
	}
	request.Value.Entries["value"] = nativeTestNode([]byte("replacement"))
	if response, err := producer.PutValue(ctx, request); err != nil || response.GetError() == nil {
		t.Fatalf("collision must be rejected: %v %v", response, err)
	}
	got, err := producer.GetValue(ctx, &kv.GetValueRequest{Key: request.Key})
	if err != nil || !bytes.Equal(got.GetValue().Entries["value"], original) {
		t.Fatalf("original preserved: %v %v", got, err)
	}
	for _, identity := range [][2]string{{"project-b", "toolchain-a"}, {"project-a", "toolchain-b"}} {
		consumer, err := New(producer.store, identity[0], identity[1])
		if err != nil {
			t.Fatal(err)
		}
		miss, err := consumer.GetValue(ctx, &kv.GetValueRequest{Key: request.Key})
		if err != nil || miss.GetOutcome() != kv.GetValueResponse_KEY_NOT_FOUND {
			t.Fatalf("identity must miss: %v %v", miss, err)
		}
	}
}

func TestFileAndInlineObjectsHaveSameIdentity(t *testing.T) {
	ctx := context.Background()
	s := openService(t, t.TempDir(), "project", "toolchain")
	data := []byte("compiler output")
	path := filepath.Join(t.TempDir(), "object.o")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := s.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: &cas.CASBytes{Contents: &cas.CASBytes_FilePath{FilePath: path}}}})
	if err != nil {
		t.Fatal(err)
	}
	inline, err := s.Put(ctx, &cas.CASPutRequest{Data: &cas.CASObject{Blob: blob(data)}})
	if err != nil || !bytes.Equal(file.GetCasId().Id, inline.GetCasId().Id) {
		t.Fatalf("canonical bytes: %v %v", inline, err)
	}
}

func TestActionMissesWhenAnIndirectCASOutputIsEvicted(t *testing.T) {
	ctx := context.Background()
	s := openService(t, t.TempDir(), "project", "toolchain")
	c, k := connect(t, s)
	leaf, err := c.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("output"))}})
	if err != nil {
		t.Fatal(err)
	}
	middle, err := c.Put(ctx, &cas.CASPutRequest{Data: &cas.CASObject{Blob: blob([]byte("middle")), References: []*cas.CASDataID{leaf.GetCasId()}}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := c.Put(ctx, &cas.CASPutRequest{Data: &cas.CASObject{Blob: blob([]byte("root")), References: []*cas.CASDataID{middle.GetCasId()}}})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("graph-action")
	if _, err = k.PutValue(ctx, &kv.PutValueRequest{Key: key, Value: &kv.Value{Entries: map[string][]byte{"CASID": root.GetCasId().Id}}}); err != nil {
		t.Fatal(err)
	}
	warm, err := k.GetValue(ctx, &kv.GetValueRequest{Key: key})
	if err != nil || warm.GetOutcome() != kv.GetValueResponse_SUCCESS {
		t.Fatalf("complete graph: %v %v", warm, err)
	}
	if err = s.store.Delete(ctx, s.key("cas", leaf.GetCasId().Id)); !errors.Is(err, artifact.ErrPinned) {
		t.Fatalf("successful action did not protect its descendant: %v", err)
	}
	// Simulate a consumer which has stopped. A fresh action lookup must miss
	// once an unprotected descendant is collected.
	if err = s.store.ResetReadLeases(ctx, readLeaseNamespace); err != nil {
		t.Fatal(err)
	}
	if err = s.store.Delete(ctx, s.key("cas", leaf.GetCasId().Id)); err != nil {
		t.Fatal(err)
	}
	miss, err := k.GetValue(ctx, &kv.GetValueRequest{Key: key})
	if err != nil || miss.GetOutcome() != kv.GetValueResponse_KEY_NOT_FOUND {
		t.Fatalf("missing descendant must miss: %v %v", miss, err)
	}
	if stats := s.Stats(); stats.ActionHits != 1 || stats.CASReads != 0 || stats.CASHits != 0 {
		t.Fatalf("presence checks are not compiler RPCs: %+v", stats)
	}
}

func TestMaximumFileBlobIsRejectedBeforeReturningAnUnreplayableID(t *testing.T) {
	s := openService(t, t.TempDir(), "project", "toolchain")
	path := filepath.Join(t.TempDir(), "maximum-object")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(MaxMessageBytes); err != nil {
		t.Fatal(err)
	}
	file.Close()
	response, err := s.Save(context.Background(), &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: &cas.CASBytes{Contents: &cas.CASBytes_FilePath{FilePath: path}}}})
	if err == nil || response != nil || !strings.Contains(err.Error(), "encoded CAS object exceeds") {
		t.Fatalf("unreplayable object accepted: %v %v", response, err)
	}
	if stats := s.Stats(); stats.CASWrites != 0 || stats.CacheErrors != 0 {
		t.Fatalf("oversize request reached storage: %+v", stats)
	}
}
