package xcodecache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSuccessfulActionRetainsGraphDuringConcurrentQuotaWrites(t *testing.T) {
	ctx := context.Background()
	store, err := artifact.Open(ctx, t.TempDir(), 1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(store, "project", "toolchain")
	if err != nil {
		t.Fatal(err)
	}
	client, actions := connect(t, service)
	leaf, err := client.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(bytes.Repeat([]byte("l"), 200))}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := client.Put(ctx, &cas.CASPutRequest{Data: &cas.CASObject{Blob: blob([]byte("root")), References: []*cas.CASDataID{leaf.GetCasId()}}})
	if err != nil {
		t.Fatal(err)
	}
	if response, err := actions.PutValue(ctx, &kv.PutValueRequest{Key: []byte("action"), Value: &kv.Value{Entries: map[string][]byte{"CASID": root.GetCasId().Id}}}); err != nil || response.GetError() != nil {
		t.Fatalf("save action: %v %v", response, err)
	}
	hit, err := actions.GetValue(ctx, &kv.GetValueRequest{Key: []byte("action")})
	if err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS {
		t.Fatalf("complete action: %v %v", hit, err)
	}
	// The original 906-byte incoming object cannot coexist with the leased
	// graph inside 1024 bytes. Quota wins over a new write, never a promised read.
	if _, err := client.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(bytes.Repeat([]byte("n"), 900))}}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversubscribed write = %v", err)
	}
	var writers sync.WaitGroup
	errors := make(chan error, 32)
	for writer := 0; writer < 4; writer++ {
		writers.Add(1)
		go func(writer int) {
			defer writers.Done()
			for object := 0; object < 8; object++ {
				data := append([]byte(fmt.Sprintf("%d-%d-", writer, object)), bytes.Repeat([]byte("output"), 20)...)
				_, err := client.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(data)}})
				if err != nil {
					errors <- err
				}
			}
		}(writer)
	}
	writers.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("bounded concurrent write failed: %v", err)
	}
	if usage, err := store.Usage(ctx); err != nil || usage > 1024 {
		t.Fatalf("committed quota: %d %v", usage, err)
	}
	manifest, err := client.Get(ctx, &cas.CASGetRequest{CasId: root.GetCasId()})
	if err != nil || manifest.GetOutcome() != cas.CASGetResponse_SUCCESS || len(manifest.GetData().GetReferences()) != 1 {
		t.Fatalf("promised root lost: %v %v", manifest, err)
	}
	output, err := client.Load(ctx, &cas.CASLoadRequest{CasId: manifest.GetData().References[0]})
	if err != nil || output.GetOutcome() != cas.CASLoadResponse_SUCCESS || !bytes.Equal(output.GetData().GetBlob().GetData(), bytes.Repeat([]byte("l"), 200)) {
		t.Fatalf("promised output lost: %v %v", output, err)
	}
	stats := service.Stats()
	if stats.ActionHits != 1 || stats.GraphChecks != 1 || stats.GraphObjectsChecked != 2 || stats.GraphLookupNanos == 0 {
		t.Fatalf("graph qualification counters: %+v", stats)
	}
}

func TestCorruptGraphMissesAndCanBeRepairedWithoutReplacingLease(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	service := openService(t, root, "project", "toolchain")
	object, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("object code"))}})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("action")
	if _, err := service.PutValue(ctx, &kv.PutValueRequest{Key: key, Value: &kv.Value{Entries: map[string][]byte{"CASID": object.GetCasId().Id}}}); err != nil {
		t.Fatal(err)
	}
	if hit, err := service.GetValue(ctx, &kv.GetValueRequest{Key: key}); err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS {
		t.Fatalf("initial hit: %v %v", hit, err)
	}
	entry, err := service.store.Head(ctx, service.key("cas", object.GetCasId().Id))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "blobs", "sha256", entry.Digest[:2], entry.Digest[2:])
	if err := os.WriteFile(path, []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if miss, err := service.GetValue(ctx, &kv.GetValueRequest{Key: key}); err != nil || miss.GetOutcome() != kv.GetValueResponse_KEY_NOT_FOUND {
		t.Fatalf("corrupt graph promised: %v %v", miss, err)
	}
	if miss, err := service.Load(ctx, &cas.CASLoadRequest{CasId: object.GetCasId()}); err != nil || miss.GetOutcome() != cas.CASLoadResponse_OBJECT_NOT_FOUND {
		t.Fatalf("corrupt bytes replayed: %v %v", miss, err)
	}
	if _, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("object code"))}}); err != nil {
		t.Fatal(err)
	}
	if hit, err := service.GetValue(ctx, &kv.GetValueRequest{Key: key}); err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS {
		t.Fatalf("repaired immutable graph missed: %v %v", hit, err)
	}
}

func TestReadLeaseOwnersSeparateProjectsAndToolchains(t *testing.T) {
	ctx := context.Background()
	first := openService(t, t.TempDir(), "project-one", "toolchain-one")
	for _, identity := range [][2]string{{"project-one", "toolchain-one"}, {"project-two", "toolchain-one"}, {"project-one", "toolchain-two"}} {
		service, err := New(first.store, identity[0], identity[1])
		if err != nil {
			t.Fatal(err)
		}
		object, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("shared bytes"))}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.PutValue(ctx, &kv.PutValueRequest{Key: []byte("action"), Value: &kv.Value{Entries: map[string][]byte{"CASID": object.GetCasId().Id}}}); err != nil {
			t.Fatal(err)
		}
		if hit, err := service.GetValue(ctx, &kv.GetValueRequest{Key: []byte("action")}); err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS {
			t.Fatalf("scoped lease admission: %v %v", hit, err)
		}
	}
}

func TestExclusiveRuntimeClearsInterruptedLeasesAfterRestart(t *testing.T) {
	ctx := context.Background()
	options := reviewOptions(t)
	store, err := artifact.Open(ctx, options.DataDir, options.MaxBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	key := artifact.Key{Integration: "xcode", Project: options.Project, Compatibility: options.Compatibility, Native: "abandoned"}
	entry, _, err := store.Put(ctx, key, artifact.Metadata{}, bytes.NewBufferString("abandoned output"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RetainForRead(ctx, readLeaseNamespace, "interrupted", []artifact.Entry{entry}, artifact.MaximumReadLeaseDuration); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAYERCACHE_XCODE_CHILD", "1")
	var output bytes.Buffer
	if err := Run(ctx, options, compilerCommand(), &output, &output); err != nil {
		t.Fatalf("restart compiler: %v %s", err, &output)
	}
	store, err = artifact.Open(ctx, options.DataDir, options.MaxBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.GC(ctx, 0); err != nil {
		t.Fatalf("interrupted lease survived exclusive runtime restart: %v", err)
	}
}

func BenchmarkGraphLookupAndLease(b *testing.B) {
	ctx := context.Background()
	store, err := artifact.Open(ctx, b.TempDir(), 16<<20, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	service, err := New(store, "project", "toolchain")
	if err != nil {
		b.Fatal(err)
	}
	var references []*cas.CASDataID
	for object := 0; object < 100; object++ {
		leaf, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte(fmt.Sprintf("object-%d", object)))}})
		if err != nil {
			b.Fatal(err)
		}
		references = append(references, leaf.GetCasId())
	}
	root, err := service.Put(ctx, &cas.CASPutRequest{Data: &cas.CASObject{Blob: blob([]byte("manifest")), References: references}})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := service.PutValue(ctx, &kv.PutValueRequest{Key: []byte("action"), Value: &kv.Value{Entries: map[string][]byte{"CASID": root.GetCasId().Id}}}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		hit, err := service.GetValue(ctx, &kv.GetValueRequest{Key: []byte("action")})
		if err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS {
			b.Fatalf("leased graph: %v %v", hit, err)
		}
	}
}
