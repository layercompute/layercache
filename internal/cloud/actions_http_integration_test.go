package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/actionscache"
	"github.com/layercache/layercache/internal/artifact"
)

// The HTTP loop uses real PostgreSQL and the production S3 SDK against a
// disposable in-memory S3 endpoint. No production configuration is loaded.
func TestActionsCommitLookupAgainstPostgresAndMemoryS3(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	for _, size := range []int{64 << 10, 124108800} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			fixture := newActionsHTTPFixture(t, int64(size)*2)
			payload := make([]byte, size)
			for index := range payload {
				payload[index] = byte(index%251 + index/(4<<20))
			}
			id := fixture.publish(t, "required-handoff", payload)
			fixture.restore(t, "required-handoff", id, payload)
			t.Logf("commit HTTP 204 -> lookup HTTP 200 -> verified archive: %d bytes", size)
		})
	}
}

func TestActionsAcknowledgementDoesNotRetainFutureHandoff(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	for _, cause := range []string{"quota", "expiry", "removed-entry", "missing-object", "corrupt-object", "wrong-profile", "wrong-version"} {
		t.Run(cause, func(t *testing.T) {
			fixture := newActionsHTTPFixture(t, 64<<10)
			payload := bytes.Repeat([]byte{0x71}, 32<<10)
			id := fixture.publish(t, "required-handoff", payload)
			key := fixture.artifactKey("required-handoff")
			query := url.Values{"keys": {"required-handoff"}, "version": {"v1"}}
			headers := map[string]string{}
			reason := "no-matching-entry"
			switch cause {
			case "quota":
				_, _, err := fixture.store.Put(context.Background(), artifact.Key{
					Project: key.Project, Integration: "turbo", Compatibility: key.Compatibility, Native: "pressure",
				}, artifact.Metadata{}, bytes.NewReader(bytes.Repeat([]byte{0x72}, 48<<10)))
				if err != nil {
					t.Fatal(err)
				}
			case "expiry":
				fixture.store.config.IdleTTL = time.Hour
				fixture.now.Add(int64(2 * time.Hour))
				if _, err := fixture.store.Maintain(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "removed-entry":
				if err := fixture.store.Delete(context.Background(), key); err != nil {
					t.Fatal(err)
				}
			case "missing-object", "corrupt-object":
				entry, err := fixture.store.Head(context.Background(), key)
				if err != nil {
					t.Fatal(err)
				}
				fixture.objects.mutex.Lock()
				objectKey := fixture.store.blobKey(entry.Digest)
				if cause == "missing-object" {
					delete(fixture.objects.objects, objectKey)
				} else {
					object := fixture.objects.objects[objectKey]
					object.data = append([]byte(nil), object.data...)
					object.data[0] ^= 1
					fixture.objects.objects[objectKey] = object
				}
				fixture.objects.mutex.Unlock()
				reason = "integrity-rejected"
			case "wrong-profile":
				headers["x-layercache-compatibility"] = "different-profile"
				reason = "no-matching-entry"
			case "wrong-version":
				query.Set("version", "v2")
				reason = "no-matching-entry"
			}
			response := fixture.request(t, http.MethodGet, "/_apis/artifactcache/cache?"+query.Encode(), nil, headers, http.StatusNoContent)
			gotReason := response.Header.Get("X-LayerCache-Miss-Reason")
			response.Body.Close()
			if gotReason != reason {
				t.Fatalf("miss diagnostic %q, want %q after acknowledged commit", gotReason, reason)
			}
			if cause == "wrong-profile" || cause == "wrong-version" {
				fixture.restore(t, "required-handoff", id, payload)
			}
		})
	}
}

func TestActionsExplicitPinPreservesAcknowledgedHandoffUnderQuota(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	fixture := newActionsHTTPFixture(t, 64<<10)
	payload := bytes.Repeat([]byte{0x71}, 32<<10)
	id := fixture.publish(t, "required-handoff", payload)
	key := fixture.artifactKey("required-handoff")
	entry, err := fixture.store.Head(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Pin(context.Background(), "test", artifact.Pin{Owner: "consumer", Key: key, Digest: entry.Digest, Size: entry.Size}); err != nil {
		t.Fatal(err)
	}
	_, _, err = fixture.store.Put(context.Background(), artifact.Key{Project: key.Project, Integration: "turbo", Compatibility: key.Compatibility, Native: "pressure"},
		artifact.Metadata{}, bytes.NewReader(bytes.Repeat([]byte{0x72}, 48<<10)))
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("pinned quota admission: %v", err)
	}
	fixture.restore(t, "required-handoff", id, payload)
}

func TestActionsCommitResponseLossStillLeavesReadableEntry(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	fixture := newActionsHTTPFixture(t, 64<<10)
	payload := bytes.Repeat([]byte{0x71}, 32<<10)
	id := fixture.upload(t, "required-handoff", payload)
	commitPath := fmt.Sprintf("/_apis/artifactcache/caches/%d", id)
	handler := fixture.endpoint.Config.Handler
	endpoint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == commitPath {
			writer = &lostCommitResponse{ResponseWriter: writer}
		}
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(endpoint.Close)
	response, err := endpoint.Client().Post(endpoint.URL+commitPath, "application/json",
		bytes.NewReader([]byte(fmt.Sprintf("{\"size\":%d}", len(payload)))))
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("lost acknowledgement: %v, want EOF", err)
	}
	fixture.restore(t, "required-handoff", id, payload)
}

// Drop the response only after the actual commit handler reaches its success
// acknowledgement. Losing a response is distinct from rolling back a commit.
type lostCommitResponse struct{ http.ResponseWriter }

func (writer *lostCommitResponse) WriteHeader(status int) {
	if status != http.StatusNoContent {
		writer.ResponseWriter.WriteHeader(status)
		return
	}
	connection, _, err := writer.ResponseWriter.(http.Hijacker).Hijack()
	if err == nil {
		_ = connection.Close()
	}
}

func TestActionsFailedCommitDoesNotPublishLookupEntry(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	fixture := newActionsHTTPFixture(t, 64<<10)
	payload := bytes.Repeat([]byte{0x71}, 32<<10)
	id := fixture.upload(t, "required-handoff", payload)
	fixture.objects.failCopy.Store(true)
	response := fixture.request(t, http.MethodPost, fmt.Sprintf("/_apis/artifactcache/caches/%d", id),
		bytes.NewReader([]byte(fmt.Sprintf("{\"size\":%d}", len(payload)))), nil, http.StatusInternalServerError)
	response.Body.Close()
	response = fixture.request(t, http.MethodGet, "/_apis/artifactcache/cache?keys=required-handoff&version=v1", nil, nil, http.StatusNoContent)
	response.Body.Close()
	stats, err := fixture.store.Stats(context.Background())
	if err != nil || stats.Entries != 0 || stats.Artifacts != 0 {
		t.Fatalf("failed commit published metadata: %+v, %v", stats, err)
	}
	fixture.objects.failCopy.Store(false)
	response = fixture.request(t, http.MethodPost, fmt.Sprintf("/_apis/artifactcache/caches/%d", id),
		bytes.NewReader([]byte(fmt.Sprintf("{\"size\":%d}", len(payload)))), nil, http.StatusNoContent)
	response.Body.Close()
	fixture.restore(t, "required-handoff", id, payload)
}

func TestActionsLookupRejectsCorruptCandidateAndRestoresHealthyFallback(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	fixture := newActionsHTTPFixture(t, 128<<10)
	payload := bytes.Repeat([]byte{0x71}, 32<<10)
	id := fixture.publish(t, "handoff-healthy", payload)
	fixture.publish(t, "handoff-corrupt", bytes.Repeat([]byte{0x72}, 32<<10))
	entry, err := fixture.store.Head(context.Background(), fixture.artifactKey("handoff-corrupt"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.objects.mutex.Lock()
	delete(fixture.objects.objects, fixture.store.blobKey(entry.Digest))
	fixture.objects.mutex.Unlock()
	fixture.restoreLookup(t, url.Values{"keys": {"handoff-corrupt,handoff-"}, "version": {"v1"}}, "handoff-healthy", id, payload)
}

func TestActionsAcknowledgedLookupWithConcurrentMaintenance(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	fixture := newActionsHTTPFixture(t, 64<<10)
	payload := bytes.Repeat([]byte{0x71}, 32<<10)
	id := fixture.publish(t, "required-handoff", payload)
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	start := make(chan struct{})
	finished := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for range 20 {
			if _, err := fixture.store.Maintain(ctx); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	close(start)
	for range 10 {
		fixture.restore(t, "required-handoff", id, payload)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestActionsMaintenanceProtectsLookupAlreadyReadingArchive(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	fixture := newActionsHTTPFixture(t, 64<<10)
	payload := bytes.Repeat([]byte{0x71}, 32<<10)
	id := fixture.publish(t, "required-handoff", payload)
	entry, err := fixture.store.Head(context.Background(), fixture.artifactKey("required-handoff"))
	if err != nil {
		t.Fatal(err)
	}
	gate := &memoryS3ReadGate{key: fixture.store.blobKey(entry.Digest), entered: make(chan struct{}), released: make(chan struct{})}
	fixture.objects.mutex.Lock()
	fixture.objects.readGate = gate
	fixture.objects.mutex.Unlock()
	fixture.store.config.IdleTTL = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var worker sync.WaitGroup
	defer func() { cancel(); gate.release(); worker.Wait() }()
	finished := make(chan error, 1)
	worker.Add(1)
	go func() {
		defer worker.Done()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			fixture.endpoint.URL+"/_apis/artifactcache/cache?keys=required-handoff&version=v1", nil)
		if err == nil {
			var response *http.Response
			response, err = fixture.endpoint.Client().Do(request)
			if err == nil {
				if response.StatusCode != http.StatusOK {
					err = fmt.Errorf("active lookup HTTP %d", response.StatusCode)
				}
				response.Body.Close()
			}
		}
		finished <- err
	}()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("lookup did not reach archive verification")
	}
	// Make the entry eligible for idle pruning while its real S3 read is held.
	fixture.now.Add(int64(2 * time.Second))
	result, err := fixture.store.Maintain(ctx)
	if err != nil || result.PrunedEntries != 0 {
		t.Fatalf("maintenance during active lookup: %+v, %v", result, err)
	}
	gate.release()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	fixture.restore(t, "required-handoff", id, payload)
}

func TestActionsLookupScopeIsolation(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	fixture := newActionsHTTPFixture(t, 64<<10)
	payload := bytes.Repeat([]byte{0x71}, 32<<10)
	id := fixture.publish(t, "required-handoff", payload)
	for _, field := range []string{"repository", "ref", "project", "key"} {
		t.Run(field, func(t *testing.T) {
			config := actionscache.Config{
				Project: fixture.store.config.Project, Repository: "acme/phone",
				Ref: "refs/heads/main", DefaultRef: "refs/heads/main", Compatibility: "linux-x64-node24",
			}
			store := fixture.store
			key := "required-handoff"
			switch field {
			case "repository":
				config.Repository = "acme/other"
			case "ref":
				config.Ref, config.DefaultRef = "refs/heads/other", "refs/heads/other"
			case "project":
				storeConfig := fixture.store.config
				storeConfig.Project = "github.com/acme/other-private-project"
				var err error
				store, err = Open(context.Background(), storeConfig)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				config.Project = storeConfig.Project
			case "key":
				key = "other-handoff"
			}
			storage, err := NewActionsStorage(store)
			if err != nil {
				t.Fatal(err)
			}
			handler, err := actionscache.NewHandler(config, storage)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := httptest.NewServer(handler)
			t.Cleanup(endpoint.Close)
			response, err := endpoint.Client().Get(endpoint.URL + "/_apis/artifactcache/cache?keys=" + key + "&version=v1")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusNoContent || response.Header.Get("X-LayerCache-Miss-Reason") != "no-matching-entry" {
				t.Fatalf("cross-%s lookup: HTTP %d, reason=%q", field, response.StatusCode, response.Header.Get("X-LayerCache-Miss-Reason"))
			}
		})
	}
	fixture.restore(t, "required-handoff", id, payload)
}

func TestActionsCandidateRemovedAfterSelectionIsDiagnosticMiss(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	fixture := newActionsHTTPFixture(t, 64<<10)
	fixture.publish(t, "required-handoff", bytes.Repeat([]byte{0x71}, 32<<10))
	storage := &ActionsStorage{store: fixture.store}
	request := actionscache.LookupRequest{
		Scope: actionscache.Scope{Project: fixture.store.config.Project, Repository: "acme/phone",
			Ref: "refs/heads/main", DefaultRef: "refs/heads/main", Compatibility: "linux-x64-node24"},
		Keys: []string{"required-handoff"}, Version: "v1",
	}
	candidates, found, err := storage.queryCandidates(context.Background(), request, request.Scope.Ref, request.Keys[0], true, 1)
	if err != nil || !found || len(candidates) != 1 {
		t.Fatalf("select candidate: found=%t, count=%d, error=%v", found, len(candidates), err)
	}
	if err := fixture.store.Delete(context.Background(), fixture.artifactKey("required-handoff")); err != nil {
		t.Fatal(err)
	}
	available, err := storage.actionsEntryAvailable(context.Background(), request, candidates[0])
	if available || !errors.Is(err, actionscache.ErrLookupEntryRemoved) || !errors.Is(err, actionscache.ErrNotFound) {
		t.Fatalf("removed selected candidate: available=%t, error=%v", available, err)
	}
}

type actionsHTTPFixture struct {
	store    *Store
	objects  *memoryS3Endpoint
	endpoint *httptest.Server
	now      atomic.Int64
}

func newActionsHTTPFixture(t *testing.T, quota int64) *actionsHTTPFixture {
	t.Helper()
	ctx := context.Background()
	rawURL := os.Getenv("LAYER_CACHE_QA_POSTGRES_URL")
	admin, err := sql.Open("pgx", rawURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("actions_http_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quotePostgresIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+quotePostgresIdentifier(schema)+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	scopedURL, err := postgresURLWithSearchPath(rawURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &actionsHTTPFixture{}
	fixture.now.Store(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC).UnixNano())
	fixture.objects = newMemoryS3Endpoint(t)
	fixture.store, err = Open(ctx, Config{
		PostgresURL: scopedURL, S3Endpoint: fixture.objects.server.URL,
		S3Bucket: "cache", S3Region: "us-east-1", S3PathStyle: true,
		S3AccessKey: "fixture-access", S3SecretKey: "fixture-secret",
		Project: "github.com/acme/phone", MaxBytes: quota,
		Now: func() time.Time { return time.Unix(0, fixture.now.Load()).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.store.Close() })
	storage, err := NewActionsStorage(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := actionscache.NewHandler(actionscache.Config{
		Project: fixture.store.config.Project, Repository: "acme/phone",
		Ref: "refs/heads/main", DefaultRef: "refs/heads/main",
		Compatibility: "linux-x64-node24", MaxArtifactBytes: quota,
	}, storage)
	if err != nil {
		t.Fatal(err)
	}
	fixture.endpoint = httptest.NewServer(handler)
	t.Cleanup(fixture.endpoint.Close)
	return fixture
}

func (fixture *actionsHTTPFixture) artifactKey(key string) artifact.Key {
	storage := &ActionsStorage{store: fixture.store}
	return storage.artifactKey(cloudActionsReservation{repository: "acme/phone",
		compatibility: "linux-x64-node24", ref: "refs/heads/main", key: key, version: "v1"})
}

func (fixture *actionsHTTPFixture) request(t *testing.T, method, path string, body io.Reader, headers map[string]string, want int) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, fixture.endpoint.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := fixture.endpoint.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		response.Body.Close()
		t.Fatalf("%s %s: HTTP %d, want %d", method, path, response.StatusCode, want)
	}
	return response
}

func (fixture *actionsHTTPFixture) publish(t *testing.T, key string, payload []byte) int64 {
	t.Helper()
	id := fixture.upload(t, key, payload)
	response := fixture.request(t, http.MethodPost, fmt.Sprintf("/_apis/artifactcache/caches/%d", id),
		bytes.NewReader([]byte(fmt.Sprintf("{\"size\":%d}", len(payload)))), nil, http.StatusNoContent)
	response.Body.Close()
	return id
}

func (fixture *actionsHTTPFixture) upload(t *testing.T, key string, payload []byte) int64 {
	t.Helper()
	body, err := json.Marshal(map[string]any{"key": key, "version": "v1", "cacheSize": len(payload)})
	if err != nil {
		t.Fatal(err)
	}
	response := fixture.request(t, http.MethodPost, "/_apis/artifactcache/caches", bytes.NewReader(body), nil, http.StatusCreated)
	var reservation struct {
		CacheID int64 `json:"cacheId"`
	}
	err = json.NewDecoder(response.Body).Decode(&reservation)
	response.Body.Close()
	if err != nil || reservation.CacheID <= 0 {
		t.Fatalf("reservation: %+v, %v", reservation, err)
	}
	path := fmt.Sprintf("/_apis/artifactcache/caches/%d", reservation.CacheID)
	const chunk = 4 << 20
	for start := 0; start < len(payload); start += chunk {
		end := min(start+chunk, len(payload))
		response := fixture.request(t, http.MethodPatch, path, bytes.NewReader(payload[start:end]),
			map[string]string{"Content-Range": fmt.Sprintf("bytes %d-%d/*", start, end-1)}, http.StatusNoContent)
		response.Body.Close()
	}
	return reservation.CacheID
}

func (fixture *actionsHTTPFixture) restore(t *testing.T, key string, id int64, payload []byte) {
	t.Helper()
	query := url.Values{"keys": {key}, "version": {"v1"}}
	fixture.restoreLookup(t, query, key, id, payload)
}

func (fixture *actionsHTTPFixture) restoreLookup(t *testing.T, query url.Values, key string, id int64, payload []byte) {
	t.Helper()
	response := fixture.request(t, http.MethodGet, "/_apis/artifactcache/cache?"+query.Encode(), nil, nil, http.StatusOK)
	var lookup struct {
		CacheKey        string `json:"cacheKey"`
		ArchiveLocation string `json:"archiveLocation"`
	}
	err := json.NewDecoder(response.Body).Decode(&lookup)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	location, parseErr := url.Parse(lookup.ArchiveLocation)
	if parseErr != nil || lookup.CacheKey != key || !strings.HasSuffix(location.Path, fmt.Sprintf("/caches/%d/archive", id)) {
		t.Fatal("lookup identity differs from the acknowledged reservation")
	}
	response, err = fixture.endpoint.Client().Get(lookup.ArchiveLocation)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("archive HTTP %d", response.StatusCode)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, response.Body)
	wantDigest := sha256.Sum256(payload)
	if err != nil || size != int64(len(payload)) || !bytes.Equal(hash.Sum(nil), wantDigest[:]) {
		t.Fatalf("archive verification: bytes=%d, expected=%d, error=%v", size, len(payload), err)
	}
}
