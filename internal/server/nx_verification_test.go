package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/config"
)

func TestCloudNxReadVerifiesCompleteArchiveBeforeResponse(t *testing.T) {
	t.Parallel()
	good := []byte("verified opaque Nx archive")
	digest := sha256.Sum256(good)
	key := artifact.Key{Integration: "nx", Project: "github.com/acme/widgets", Compatibility: "linux-amd64-schema1", Native: "native-hash"}
	entry := artifact.Entry{Key: key, Digest: hex.EncodeToString(digest[:]), Size: int64(len(good))}
	for _, test := range []struct {
		name    string
		open    func() io.ReadCloser
		minimum int64
		want    int
		deleted bool
	}{
		{name: "valid", open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(good)) }, want: 200},
		{name: "wrong digest", open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), len(good)))) }, want: 404, deleted: true},
		{name: "late cloud checksum failure", open: func() io.ReadCloser { return &lateCorruptReadCloser{reader: bytes.NewReader(good)} }, want: 404, deleted: true},
		{name: "short stream", open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(good[:len(good)-1])) }, want: 404, deleted: true},
		{name: "trailing bytes", open: func() io.ReadCloser { return io.NopCloser(strings.NewReader(string(good) + "extra")) }, want: 404, deleted: true},
		{name: "staging reserve exhausted", open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(good)) }, minimum: math.MaxInt64, want: 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			local, err := artifact.Open(context.Background(), root, 1<<20, test.minimum)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = local.Close() })
			store := &turboStreamingStore{entry: entry, open: test.open}
			instance := &Server{config: config.Config{Role: "team", DataDir: root, MaxBytes: 1 << 20}, store: store, localStore: local, cloudStore: &cloud.Store{}}
			request := httptest.NewRequest(http.MethodGet, "/v1/cache/"+key.Native, nil)
			request.SetPathValue("hash", key.Native)
			request = request.WithContext(access.WithClaims(request.Context(), access.Claims{Project: key.Project, Compatibility: key.Compatibility}))
			response := httptest.NewRecorder()
			instance.nxArtifact(response, request)
			if response.Code != test.want {
				t.Fatalf("HTTP %d, want %d; body=%q", response.Code, test.want, response.Body.String())
			}
			if test.want == 200 && !bytes.Equal(response.Body.Bytes(), good) {
				t.Fatalf("restored bytes = %q", response.Body.Bytes())
			}
			if test.want != 200 && (bytes.Contains(response.Body.Bytes(), good) || response.Header().Get("x-layercache-digest") != "") {
				t.Fatal("unverified archive escaped the HTTP boundary")
			}
			if store.deleted != test.deleted {
				t.Fatalf("deleted=%v, want %v", store.deleted, test.deleted)
			}
			files, err := os.ReadDir(filepath.Join(root, "staging"))
			if err != nil || len(files) != 0 {
				t.Fatalf("staging not reclaimed: %v, %v", files, err)
			}
		})
	}
}

func TestProjectGatewayNxCapacityRejectionDeclinesPublication(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "upfront reserve exhausted"
		if streaming {
			name = "streaming capacity probe unavailable"
		}
		t.Run(name, func(t *testing.T) {
			gateway, projects := gatewayFixture(t, nil)
			probe := filepath.Join(t.TempDir(), "capacity-probe")
			if err := os.Mkdir(probe, 0700); err != nil {
				t.Fatal(err)
			}
			gateway.pool = StoragePoolConfig{Path: probe, MinFreeBytes: math.MaxInt64}
			if streaming {
				gateway.pool.MinFreeBytes = 0
			}
			body := &nxDrainingBody{reader: strings.NewReader(strings.Repeat("x", 4096))}
			if streaming {
				body.afterFirst = func() {
					if err := os.Remove(probe); err != nil {
						t.Fatal(err)
					}
				}
			}
			auth := "Bearer " + gatewayToken(t, projects["alpha"], "github:alice", "nx", access.CapabilityWrite)
			request := httptest.NewRequest(http.MethodPut, "/v1/cache/capacity-test", body)
			request.ContentLength = 4096
			request.Header.Set("Authorization", auth)
			response := httptest.NewRecorder()
			gateway.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") {
				t.Fatalf("capacity must decline native publication, HTTP %d: %s", response.Code, response.Body.String())
			}
			if streaming && !body.closed {
				t.Fatal("streaming request body remained open")
			}
			if response := gatewayRequest(gateway, "GET", "/v1/cache/capacity-test", auth, "", nil); response.Code != 404 {
				t.Fatalf("rejected upload was committed: HTTP %d", response.Code)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			stats, err := gateway.projects[projects["alpha"].ProjectID].store.Stats(ctx)
			if err != nil || stats.UsageBytes != 0 {
				t.Fatalf("rejected upload retained bytes: %+v, %v", stats, err)
			}
		})
	}
}

func TestProjectGatewayCapacityResponsePreservesAdministrativeRoutes(t *testing.T) {
	gateway, projects := gatewayFixture(t, nil)
	auth := "Bearer " + gatewayToken(t, projects["alpha"], "github:alice", "nx", access.CapabilityWrite)
	// GET pins belongs to administration. Its identifier cannot be an Nx
	// round-trip key, even though PUT would otherwise match the generic route.
	if response := gatewayRequest(gateway, http.MethodPut, "/v1/cache/pins", auth, "bytes", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("reserved identifier accepted as an Nx artifact: HTTP %d", response.Code)
	}
	gateway.pool = StoragePoolConfig{Path: t.TempDir(), MinFreeBytes: math.MaxInt64}
	for _, path := range []string{"/v1/cache/quota", "/v1/cache/pins", "/v1/cache/pins/owner", "/v1/cache/nested/key", "/v8/artifacts/task"} {
		response := gatewayRequest(gateway, http.MethodPut, path, auth, "bytes", nil)
		if response.Code != http.StatusInsufficientStorage {
			t.Fatalf("%s classified as a native Nx archive: HTTP %d", path, response.Code)
		}
	}
}

type nxDrainingBody struct {
	reader     io.Reader
	afterFirst func()
	closed     bool
}

func (body *nxDrainingBody) Read(buffer []byte) (int, error) {
	count, err := body.reader.Read(buffer[:min(len(buffer), 8)])
	if body.afterFirst != nil {
		after := body.afterFirst
		body.afterFirst = nil
		after()
	}
	return count, err
}
func (body *nxDrainingBody) Close() error { body.closed = true; return nil }
