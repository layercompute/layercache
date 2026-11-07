package actionscache_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/actionscache"
	"github.com/layercache/layercache/internal/directtransfer"
)

// directMemoryStorage offers direct reads of its archives from a separate
// object server, the way hosted storage does.
type directMemoryStorage struct {
	*actionscache.MemoryStorage
	objects  *httptest.Server
	payload  []byte
	served   []byte
	notAfter time.Time

	mu          sync.Mutex
	objectGets  int
	headers     []http.Header
	completions []string
}

func (storage *directMemoryStorage) IssueDirectArchive(
	_ context.Context,
	_ actionscache.OpenRequest,
	notAfter time.Time,
) (directtransfer.Descriptor, error) {
	storage.mu.Lock()
	storage.notAfter = notAfter
	storage.mu.Unlock()
	sum := sha256.Sum256(storage.payload)
	return directtransfer.Descriptor{
		Transfer: directtransfer.Kind, Method: http.MethodGet,
		URL:    storage.objects.URL + "/object?X-Amz-Signature=secret",
		Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(storage.payload)),
		ExpiresAt: notAfter, Lease: "read-lease",
	}, nil
}

func (storage *directMemoryStorage) CompleteDirectArchive(
	_ context.Context,
	_ actionscache.OpenRequest,
	lease, digest string,
	size int64,
) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()
	for _, completed := range storage.completions {
		if completed == lease {
			return actionscache.ErrNotFound
		}
	}
	storage.completions = append(storage.completions, lease)
	return nil
}

func newDirectActionsFixture(t *testing.T, payload []byte) (*directMemoryStorage, actionscache.Scope, *actionscache.RemoteStorage) {
	t.Helper()
	scope := actionscache.Scope{
		Repository: "acme/widgets", Ref: "refs/heads/main", DefaultRef: "refs/heads/main",
		Compatibility: "linux-x64-node24",
	}
	storage := &directMemoryStorage{MemoryStorage: actionscache.NewMemoryStorage(), payload: payload, served: payload}
	storage.objects = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		storage.mu.Lock()
		storage.objectGets++
		storage.headers = append(storage.headers, request.Header.Clone())
		served := storage.served
		storage.mu.Unlock()
		writer.Header().Set("Content-Length", strconv.Itoa(len(served)))
		_, _ = writer.Write(served)
	}))
	t.Cleanup(storage.objects.Close)
	putArchive(t, storage, scope, "direct-key", "v1", payload)
	handler, err := actionscache.NewHandler(actionscache.Config{
		Repository: scope.Repository, Ref: scope.Ref, DefaultRef: scope.DefaultRef, Compatibility: scope.Compatibility,
	}, storage)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(handler)
	t.Cleanup(gateway.Close)
	client, err := actionscache.NewRemoteStorage(actionscache.RemoteStorageConfig{Endpoint: gateway.URL, Token: "team-token"})
	if err != nil {
		t.Fatal(err)
	}
	actionscache.SetRemoteDirectBreaker(client, directtransfer.NewBreaker(3, 10*time.Minute, nil))
	return storage, scope, client
}

func TestRemoteStorageReadsArchivesDirectlyAndReportsTheVerifiedRead(t *testing.T) {
	payload := bytes.Repeat([]byte("actions-direct"), 1024)
	storage, scope, client := newDirectActionsFixture(t, payload)
	result, err := client.Lookup(context.Background(), actionscache.LookupRequest{Scope: scope, Keys: []string{"direct-key"}, Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := client.Open(context.Background(), actionscache.OpenRequest{Scope: scope, ID: result.Entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(archive.Body)
	_ = archive.Body.Close()
	if err != nil || !bytes.Equal(contents, payload) || archive.Entry.Size != int64(len(payload)) {
		t.Fatalf("direct archive: %d bytes, err %v", len(contents), err)
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if storage.objectGets != 1 || len(storage.completions) != 1 {
		t.Fatalf("object GETs %d, completions %d", storage.objectGets, len(storage.completions))
	}
	if storage.headers[0].Get("Authorization") != "" || storage.headers[0].Get("X-LayerCache-Compatibility") != "" {
		t.Fatal("the object request carried LayerCache credentials")
	}
	// The descriptor never outlives the signed archive URL whose outcome its
	// completion reuses.
	if storage.notAfter.IsZero() || storage.notAfter.After(time.Now().Add(10*time.Minute)) {
		t.Fatalf("descriptor cap = %v", storage.notAfter)
	}
}

func TestRemoteStorageRejectsMismatchedDirectBytesAndRepeatsOnceProxied(t *testing.T) {
	payload := bytes.Repeat([]byte("actions-fallback"), 512)
	storage, scope, client := newDirectActionsFixture(t, payload)
	storage.served = bytes.Repeat([]byte("x"), len(payload))
	result, err := client.Lookup(context.Background(), actionscache.LookupRequest{Scope: scope, Keys: []string{"direct-key"}, Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	request := actionscache.OpenRequest{Scope: scope, ID: result.Entry.ID}
	archive, err := client.Open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(archive.Body)
	_ = archive.Body.Close()
	if !errors.Is(err, directtransfer.ErrFailed) {
		t.Fatalf("mismatched direct read = %v", err)
	}
	// The repeat downloads through the gateway, verified against the same
	// declared digest and size, without retrying the URL.
	archive, err = client.Open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(archive.Body)
	_ = archive.Body.Close()
	if err != nil || !bytes.Equal(contents, payload) {
		t.Fatalf("proxied repeat: %d bytes, err %v", len(contents), err)
	}
	if storage.objectGets != 1 || len(storage.completions) != 0 {
		t.Fatalf("object GETs %d, completions %d", storage.objectGets, len(storage.completions))
	}
}

func TestCacheChainRepeatsAFailedDirectWarmOnce(t *testing.T) {
	payload := bytes.Repeat([]byte("chain-fallback"), 512)
	storage, scope, client := newDirectActionsFixture(t, payload)
	storage.served = payload[:len(payload)-3]
	chain, err := actionscache.NewCacheChain(actionscache.NewMemoryStorage(), client)
	if err != nil {
		t.Fatal(err)
	}
	result, err := chain.Lookup(context.Background(), actionscache.LookupRequest{Scope: scope, Keys: []string{"direct-key"}, Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := chain.Open(context.Background(), actionscache.OpenRequest{Scope: scope, ID: result.Entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(archive.Body)
	_ = archive.Body.Close()
	if err != nil || !bytes.Equal(contents, payload) {
		t.Fatalf("warmed archive: %d bytes, err %v", len(contents), err)
	}
	if storage.objectGets != 1 || len(storage.completions) != 0 {
		t.Fatalf("object GETs %d, completions %d", storage.objectGets, len(storage.completions))
	}
}
