package xcodecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net"
	"os"
	"path/filepath"
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
	request := &kv.PutValueRequest{Key: []byte("action"), Value: &kv.Value{Entries: map[string][]byte{"result": []byte("first")}}}
	if response, err := producer.PutValue(ctx, request); err != nil || response.GetError() != nil {
		t.Fatalf("first save: %v %v", response, err)
	}
	request.Value.Entries["result"] = []byte("replacement")
	if response, err := producer.PutValue(ctx, request); err != nil || response.GetError() == nil {
		t.Fatalf("collision must be rejected: %v %v", response, err)
	}
	got, err := producer.GetValue(ctx, &kv.GetValueRequest{Key: request.Key})
	if err != nil || string(got.GetValue().Entries["result"]) != "first" {
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
