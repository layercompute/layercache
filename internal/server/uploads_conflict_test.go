package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/remote"
	"github.com/layercache/layercache/internal/uploadqueue"
)

func conflictUploadFixture(t *testing.T, handler http.Handler) (*Server, uploadqueue.Lease, artifact.Entry) {
	t.Helper()
	ctx := context.Background()
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	root := t.TempDir()
	store, err := artifact.Open(ctx, root, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	queue, err := uploadqueue.Open(uploadqueue.Config{
		Path: filepath.Join(root, "team-uploads.db"), MaxQueuedBytes: 4,
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close() })
	key := artifact.Key{Integration: "turbo", Project: "github.com/acme/widget", Compatibility: "linux-amd64-node@24-schema1", Native: "same-task"}
	entry, _, err := store.Put(ctx, key, artifact.Metadata{}, bytes.NewBufferString("keep"))
	if err != nil {
		t.Fatal(err)
	}
	currentCompatibility := "darwin-arm64-node@24-schema1"
	client, err := remote.NewTurboClientForCompatibility(endpoint.URL, "team-token", currentCompatibility)
	if err != nil {
		t.Fatal(err)
	}
	instance := &Server{
		config: config.Config{Role: "local", DataDir: root, MaxBytes: 4, TeamURL: endpoint.URL, TeamToken: "team-token", ProjectID: key.Project, CompatibilityID: currentCompatibility},
		store:  store, uploads: queue, team: client, teamToken: "team-token", uploadContext: ctx, uploadWake: make(chan struct{}, 1),
	}
	if err := instance.enqueueTurboUpload(ctx, key, entry); err != nil {
		t.Fatal(err)
	}
	lease, err := queue.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return instance, lease, entry
}

func TestTurboConflictRequiresValidScopedMetadataBeforeReleasingRetention(t *testing.T) {
	for _, test := range []struct {
		name                  string
		putStatus, headStatus int
		digest, length        string
		alreadyPresent        bool
		wantError             string
	}{
		{name: "different-log-bytes", putStatus: 409, headStatus: 200, digest: "different", length: "4", alreadyPresent: true},
		{name: "same-digest", putStatus: 409, headStatus: 200, digest: "same", length: "4", alreadyPresent: true},
		{name: "missing-digest", putStatus: 409, headStatus: 200, length: "4", wantError: "metadata_invalid"},
		{name: "malformed-digest", putStatus: 409, headStatus: 200, digest: "invalid", length: "4", wantError: "metadata_invalid"},
		{name: "missing-size", putStatus: 409, headStatus: 200, digest: "different", wantError: "metadata_invalid"},
		{name: "missing-entry", putStatus: 409, headStatus: 404, wantError: "remote_unavailable"},
		{name: "metadata-outage", putStatus: 409, headStatus: 503, wantError: "remote_unavailable"},
		{name: "metadata-unauthorized", putStatus: 409, headStatus: 401, wantError: "unauthorized"},
		{name: "upload-outage", putStatus: 503, headStatus: 200, digest: "different", length: "4", wantError: "remote_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			var methodsMu sync.Mutex
			var methods []string
			instance, lease, entry := conflictUploadFixture(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				methodsMu.Lock()
				methods = append(methods, request.Method)
				methodsMu.Unlock()
				if request.Header.Get("Authorization") != "Bearer team-token" || request.Header.Get("X-LayerCache-Compatibility") != "linux-amd64-node@24-schema1" || request.URL.Path != "/v8/artifacts/same-task" {
					t.Errorf("conflict check escaped its queued project credential, compatibility or native key")
					writer.WriteHeader(http.StatusForbidden)
					return
				}
				if request.Method == http.MethodPut {
					body, _ := io.ReadAll(request.Body)
					if string(body) != "keep" {
						t.Errorf("queued upload sent different Local bytes: %q", body)
					}
					writer.WriteHeader(test.putStatus)
					return
				}
				digest := test.digest
				if digest == "same" || digest == "different" {
					body := "keep"
					if digest == "different" {
						body = "logs"
					}
					hash := sha256.Sum256([]byte(body))
					digest = "sha256:" + hex.EncodeToString(hash[:])
				}
				if digest != "" {
					writer.Header().Set("x-layercache-digest", digest)
				}
				if test.length != "" {
					writer.Header().Set("Content-Length", test.length)
				}
				writer.WriteHeader(test.headStatus)
			}))
			otherKey := entry.Key
			otherKey.Native = "useful-other-task"
			if _, _, err := instance.store.Put(ctx, otherKey, artifact.Metadata{}, bytes.NewBufferString("work")); !errors.Is(err, artifact.ErrQuota) {
				t.Fatalf("pending conflict was not pinned before handling: %v", err)
			}
			instance.processUpload(lease)
			methodsMu.Lock()
			observedMethods := append([]string(nil), methods...)
			methodsMu.Unlock()
			job, err := instance.uploads.Get(ctx, lease.Job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if job.Digest != entry.Digest || job.Size != entry.Size {
				t.Fatalf("remote metadata overwrote the queued Local archive identity: %+v", job)
			}
			if test.putStatus == 409 && (len(observedMethods) != 2 || observedMethods[0] != http.MethodPut || observedMethods[1] != http.MethodHead) {
				t.Fatalf("conflict was not independently checked: %v", observedMethods)
			}
			if test.putStatus != 409 && (len(observedMethods) != 1 || observedMethods[0] != http.MethodPut) {
				t.Fatalf("an outage was treated as a verified conflict: %v", observedMethods)
			}
			response := httptest.NewRecorder()
			instance.status(response, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
			var status struct {
				PendingUploads     int64 `json:"pendingUploads"`
				PendingUploadBytes int64 `json:"pendingUploadBytes"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil {
				t.Fatalf("upload status failed: HTTP %d %s", response.Code, response.Body.String())
			}
			if test.alreadyPresent {
				if job.State != uploadqueue.StateCompleted || job.Outcome != uploadqueue.OutcomeAlreadyPresent || status.PendingUploads != 0 || status.PendingUploadBytes != 0 {
					t.Fatalf("verified existing archive remained pending or claimed upload: %+v, status=%+v", job, status)
				}
				if _, _, err := instance.store.Put(ctx, otherKey, artifact.Metadata{}, bytes.NewBufferString("work")); err != nil {
					t.Fatalf("terminal conflict pin blocked useful cache admission: %v", err)
				}
				if _, _, err := instance.store.Get(ctx, entry.Key); !errors.Is(err, artifact.ErrNotFound) {
					t.Fatalf("terminal conflict retained its old archive under quota pressure: %v", err)
				}
			} else {
				if job.State != uploadqueue.StatePending || job.Outcome != "" || job.LastError != test.wantError || status.PendingUploads != 1 || status.PendingUploadBytes != 4 {
					t.Fatalf("unverified conflict lost retry or truthful status: %+v, status=%+v", job, status)
				}
				if err := instance.store.GC(ctx, 0); !errors.Is(err, artifact.ErrQuota) {
					t.Fatalf("unverified conflict lost its retention pin: %v", err)
				}
			}
		})
	}
}

func TestUnverifiedTurboConflictSurvivesRestartAndCompletesAfterMetadataRecovery(t *testing.T) {
	ctx := context.Background()
	var available atomic.Bool
	instance, lease, entry := conflictUploadFixture(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer team-token" || request.Header.Get("X-LayerCache-Compatibility") != "linux-amd64-node@24-schema1" || request.URL.Path != "/v8/artifacts/same-task" {
			t.Error("restarted conflict verification escaped its original namespace")
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		if request.Method == http.MethodPut {
			_, _ = io.Copy(io.Discard, request.Body)
			writer.WriteHeader(http.StatusConflict)
			return
		}
		if !available.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		digest := sha256.Sum256([]byte("logs"))
		writer.Header().Set("x-layercache-digest", "sha256:"+hex.EncodeToString(digest[:]))
		writer.Header().Set("Content-Length", "4")
		writer.WriteHeader(http.StatusOK)
	}))
	instance.processUpload(lease)
	if err := instance.uploads.Close(); err != nil {
		t.Fatal(err)
	}
	if err := instance.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := artifact.Open(ctx, instance.config.DataDir, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Advance beyond the fixture's fixed retry clock without a scheduling sleep.
	queue, err := uploadqueue.Open(uploadqueue.Config{Path: filepath.Join(instance.config.DataDir, "team-uploads.db"), MaxQueuedBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close() })
	restarted := &Server{
		config: instance.config, store: store, uploads: queue, team: instance.team,
		teamToken: "team-token", uploadContext: ctx,
	}
	if err := restarted.reconcileUploadPins(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.GC(ctx, 0); !errors.Is(err, artifact.ErrQuota) {
		t.Fatalf("unverified conflict lost its pin across restart: %v", err)
	}
	available.Store(true)
	recoveredLease, err := queue.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restarted.processUpload(recoveredLease)
	job, err := queue.Get(ctx, lease.Job.ID)
	if err != nil || job.State != uploadqueue.StateCompleted || job.Outcome != uploadqueue.OutcomeAlreadyPresent {
		t.Fatalf("metadata recovery did not terminate the retained conflict: %+v, %v", job, err)
	}
	if err := store.GC(ctx, 0); err != nil {
		t.Fatalf("recovered conflict retained its pin: %v", err)
	}
	if _, _, err := store.Get(ctx, entry.Key); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("recovered conflict archive remained unevictable: %v", err)
	}
}

func TestRestartReleasesPinLeftAfterAlreadyPresentCompletion(t *testing.T) {
	ctx := context.Background()
	instance, lease, entry := conflictUploadFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("restart cleanup must not perform remote publication")
	}))
	if _, err := instance.uploads.CompleteAlreadyPresent(ctx, lease); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the terminal queue commit and before unpinning.
	if err := instance.store.GC(ctx, 0); !errors.Is(err, artifact.ErrQuota) {
		t.Fatalf("fixture did not retain the crash-window pin: %v", err)
	}
	if err := instance.uploads.Close(); err != nil {
		t.Fatal(err)
	}
	if err := instance.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := artifact.Open(ctx, instance.config.DataDir, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	queue, err := uploadqueue.Open(uploadqueue.Config{Path: filepath.Join(instance.config.DataDir, "team-uploads.db"), MaxQueuedBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close() })
	restarted := &Server{config: instance.config, store: store, uploads: queue}
	if err := restarted.reconcileUploadPins(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := queue.Get(ctx, lease.Job.ID)
	if err != nil || job.Outcome != uploadqueue.OutcomeAlreadyPresent {
		t.Fatalf("restart lost the already-present outcome: %+v, %v", job, err)
	}
	if err := store.GC(ctx, 0); err != nil {
		t.Fatalf("completed conflict retained a pin across restart: %v", err)
	}
	if _, _, err := store.Get(ctx, entry.Key); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("completed conflict was not evictable after restart: %v", err)
	}
}
