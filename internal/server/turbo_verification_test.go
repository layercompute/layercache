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
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/config"
)

func TestCloudTurboReadIsVerifiedBeforeHTTPResponse(t *testing.T) {
	t.Parallel()

	good := []byte("verified-cloud-turbo-artifact")
	digest := sha256.Sum256(good)
	key := artifact.Key{
		Integration: "turbo", Project: "github.com/acme/widgets",
		Compatibility: "linux-amd64-schema1", Native: "task-hash",
	}
	entry := artifact.Entry{Key: key, Digest: hex.EncodeToString(digest[:]), Size: int64(len(good))}
	for _, test := range []struct {
		name         string
		open         func() io.ReadCloser
		wantStatus   int
		wantBody     []byte
		wantDelete   bool
		minFreeBytes int64
	}{
		{
			name:         "disk reserve rejects staging without deleting cloud artifact",
			open:         func() io.ReadCloser { return io.NopCloser(bytes.NewReader(good)) },
			minFreeBytes: math.MaxInt64,
			wantStatus:   http.StatusInsufficientStorage,
			wantBody:     []byte("{\"error\":\"insufficient verification staging space\"}\n"),
		},
		{
			name: "normal verified stream",
			open: func() io.ReadCloser {
				return io.NopCloser(bytes.NewReader(good))
			},
			wantStatus: http.StatusOK, wantBody: good,
		},
		{
			name: "full corrupt stream with late error",
			open: func() io.ReadCloser {
				return &lateCorruptReadCloser{reader: bytes.NewReader(bytes.Repeat([]byte("x"), len(good)))}
			},
			wantStatus: http.StatusNotFound, wantDelete: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			local, err := artifact.Open(context.Background(), root, 1<<20, test.minFreeBytes)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = local.Close() })
			store := &turboStreamingStore{entry: entry, open: test.open}
			instance := &Server{
				config: config.Config{Role: "team", DataDir: root, MaxBytes: 1 << 20},
				store:  store, localStore: local, cloudStore: &cloud.Store{},
			}
			request := httptest.NewRequest(http.MethodGet, "/v8/artifacts/"+key.Native, nil)
			response := httptest.NewRecorder()

			instance.getTurbo(response, request, key, false)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", response.Code, test.wantStatus, response.Body.String())
			}
			if !bytes.Equal(response.Body.Bytes(), test.wantBody) {
				t.Fatalf("body = %q, want %q", response.Body.Bytes(), test.wantBody)
			}
			if store.deleted != test.wantDelete {
				t.Fatalf("deleted = %t, want %t", store.deleted, test.wantDelete)
			}
			stagingFiles, err := os.ReadDir(filepath.Join(root, "staging"))
			if err != nil {
				t.Fatal(err)
			}
			if len(stagingFiles) != 0 {
				t.Fatalf("verification staging retained %d files after response", len(stagingFiles))
			}
		})
	}
}

type turboStreamingStore struct {
	cacheStore
	entry   artifact.Entry
	open    func() io.ReadCloser
	deleted bool
}

func (store *turboStreamingStore) Get(context.Context, artifact.Key) (artifact.Entry, io.ReadCloser, error) {
	return store.entry, store.open(), nil
}

func (store *turboStreamingStore) Delete(context.Context, artifact.Key) error {
	store.deleted = true
	return nil
}

type lateCorruptReadCloser struct {
	reader *bytes.Reader
}

func (reader *lateCorruptReadCloser) Read(buffer []byte) (int, error) {
	count, _ := reader.reader.Read(buffer)
	if reader.reader.Len() == 0 {
		return count, artifact.ErrCorrupt
	}
	return count, nil
}

func (*lateCorruptReadCloser) Close() error { return nil }

func TestCloudTurboHeadAnswersFromMetadataWithoutReading(t *testing.T) {
	t.Parallel()

	key := artifact.Key{
		Integration: "turbo", Project: "github.com/acme/widgets",
		Compatibility: "linux-amd64-schema1", Native: "task-hash",
	}
	digest := sha256.Sum256([]byte("verified-cloud-turbo-artifact"))
	entry := artifact.Entry{Key: key, Digest: hex.EncodeToString(digest[:]), Size: 29, Metadata: artifact.Metadata{DurationMS: 7, Tag: "tag"}}
	for _, test := range []struct {
		name       string
		headErr    error
		wantStatus int
		wantDelete bool
	}{
		{name: "committed entry with matching object metadata", wantStatus: http.StatusOK},
		{name: "missing or mismatched object", headErr: artifact.ErrCorrupt, wantStatus: http.StatusNotFound, wantDelete: true},
		{name: "no committed entry", headErr: artifact.ErrNotFound, wantStatus: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := &turboProbeStore{t: t, entry: entry, headErr: test.headErr}
			instance := &Server{
				config: config.Config{Role: "team", DataDir: root, MaxBytes: 1 << 20},
				store:  store, cloudStore: &cloud.Store{},
			}
			response := httptest.NewRecorder()
			instance.getTurbo(response, httptest.NewRequest(http.MethodHead, "/v8/artifacts/"+key.Native, nil), key, true)

			if response.Code != test.wantStatus || response.Body.Len() != 0 {
				t.Fatalf("HEAD = %d with %d body bytes, want %d", response.Code, response.Body.Len(), test.wantStatus)
			}
			if store.deleted != test.wantDelete {
				t.Fatalf("deleted = %t, want %t", store.deleted, test.wantDelete)
			}
			if test.wantStatus != http.StatusOK {
				return
			}
			for name, want := range map[string]string{
				"Content-Length": "29", "x-layercache-digest": "sha256:" + entry.Digest,
				"x-layercache-source": "team", "x-artifact-duration": "7", "x-artifact-tag": "tag",
			} {
				if got := response.Header().Get(name); got != want {
					t.Fatalf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// turboProbeStore fails the test if a HEAD opens the object body.
type turboProbeStore struct {
	cacheStore
	t       *testing.T
	entry   artifact.Entry
	headErr error
	deleted bool
}

func (store *turboProbeStore) Head(context.Context, artifact.Key) (artifact.Entry, error) {
	if store.headErr != nil {
		return artifact.Entry{}, store.headErr
	}
	return store.entry, nil
}

func (store *turboProbeStore) Get(context.Context, artifact.Key) (artifact.Entry, io.ReadCloser, error) {
	store.t.Error("HEAD opened the cloud object body")
	return artifact.Entry{}, nil, artifact.ErrNotFound
}

func (store *turboProbeStore) Delete(context.Context, artifact.Key) error {
	store.deleted = true
	return nil
}
