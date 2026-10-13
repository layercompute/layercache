package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/config"
)

type turboSyncTeam struct {
	mu         sync.Mutex
	artifacts  map[string][]byte
	puts       int
	headBad    bool
	offline    bool
	maxEntries int
}

func (team *turboSyncTeam) serve(writer http.ResponseWriter, request *http.Request) {
	team.mu.Lock()
	defer team.mu.Unlock()
	if request.Header.Get("Authorization") != "Bearer team-secret" {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	identity := request.Header.Get("X-LayerCache-Compatibility") + ":" + strings.TrimPrefix(request.URL.Path, "/v8/artifacts/")
	if team.offline || strings.HasSuffix(identity, ":unrelated") {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if request.Method == http.MethodPut {
		body, _ := io.ReadAll(request.Body)
		team.puts++
		if previous, exists := team.artifacts[identity]; exists && !bytes.Equal(previous, body) {
			writer.WriteHeader(http.StatusConflict)
			return
		}
		if team.maxEntries > 0 && len(team.artifacts) >= team.maxEntries {
			for key := range team.artifacts {
				if key != identity {
					delete(team.artifacts, key)
				}
			}
		}
		team.artifacts[identity] = body
		writer.WriteHeader(http.StatusOK)
		return
	}
	if request.Method != http.MethodHead {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, exists := team.artifacts[identity]
	if !exists {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	digest := sha256.Sum256(body)
	writer.Header().Set("x-layercache-digest", "sha256:"+hex.EncodeToString(digest[:]))
	writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
	if team.headBad {
		writer.Header().Set("x-layercache-digest", "invalid")
	}
	writer.WriteHeader(http.StatusOK)
}

func newTurboSyncServer(t *testing.T, teamHandler http.Handler) (*Server, config.Config) {
	t.Helper()
	endpoint := httptest.NewServer(teamHandler)
	t.Cleanup(endpoint.Close)
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	cfg.MaxBytes = 1 << 20
	cfg.MinFreeBytes = 5 << 30
	cfg.ProjectID = "github.com/acme/widget"
	cfg.ActionsRepository = "acme/widget"
	cfg.CompatibilityID = "linux-amd64-node@24-schema1"
	cfg.TeamURL = endpoint.URL
	cfg.TeamToken = "team-secret"
	instance, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance, cfg
}

func localTurboSyncArtifact(t *testing.T, server *Server, hash, compatibilityID, contents string) artifact.Entry {
	t.Helper()
	key := artifact.Key{Integration: "turbo", Project: server.config.ProjectID, Compatibility: compatibilityID, Native: hash}
	entry, _, err := server.store.Put(context.Background(), key, artifact.Metadata{}, strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func callTurboSync(t *testing.T, server *Server, token, contents string) (*httptest.ResponseRecorder, turboSyncResult) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/sync/turbo", strings.NewReader(contents))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	var result turboSyncResult
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return response, result
}

func TestTurboSyncRepublishesLocalHitAfterTeamEviction(t *testing.T) {
	team := &turboSyncTeam{artifacts: make(map[string][]byte)}
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(team.serve))
	localTurboSyncArtifact(t, server, "task", cfg.CompatibilityID, "successful-test-output")
	for iteration := 0; iteration < 2; iteration++ {
		team.mu.Lock()
		delete(team.artifacts, cfg.CompatibilityID+":task")
		team.mu.Unlock()
		response, result := callTurboSync(t, server, cfg.LocalToken, `{"hashes":["task","task"],"timeoutMs":500}`)
		if response.Code != http.StatusOK || result != (turboSyncResult{Requested: 1, Ready: 1}) {
			t.Fatalf("sync result = HTTP %d %+v: %s", response.Code, result, response.Body.String())
		}
	}
	team.mu.Lock()
	defer team.mu.Unlock()
	if team.puts != 2 {
		t.Fatalf("Team writes = %d, want one per republished artifact", team.puts)
	}
}

func TestTurboSyncAcceptsExistingTaskDespiteDifferentLogBytesAndUnrelatedQueue(t *testing.T) {
	team := &turboSyncTeam{artifacts: make(map[string][]byte)}
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(team.serve))
	team.artifacts[cfg.CompatibilityID+":task"] = []byte("same-test-task-with-different-logs")
	localTurboSyncArtifact(t, server, "task", cfg.CompatibilityID, "local-logs")
	unrelated := localTurboSyncArtifact(t, server, "unrelated", cfg.CompatibilityID, "pending")
	if err := server.enqueueTurboUpload(context.Background(), unrelated.Key, unrelated); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, result := callTurboSync(t, server, cfg.LocalToken, `{"hashes":["task"],"timeoutMs":1000}`)
	if response.Code != http.StatusOK || result.Ready != 1 || result.Pending != 0 {
		t.Fatalf("sync result = HTTP %d %+v", response.Code, result)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("already-ready task waited on unrelated uploads")
	}
	team.mu.Lock()
	defer team.mu.Unlock()
	if team.puts != 0 {
		t.Fatalf("already present task was uploaded %d times", team.puts)
	}
}

func TestTurboSyncChecksTeamWithoutLocalArtifactAndUsesScopedCompatibility(t *testing.T) {
	team := &turboSyncTeam{artifacts: map[string][]byte{"darwin-arm64-node@24-schema1:task": []byte("existing")}}
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(team.serve))
	token, err := access.MintCapabilityToken(cfg.LocalToken, access.Claims{Subject: "local-writer", Project: cfg.ProjectID, Integration: "turbo", Compatibility: "darwin-arm64-node@24-schema1", Capabilities: []access.Capability{access.CapabilityWrite}, ExpiresAt: time.Now().Add(time.Hour)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	response, result := callTurboSync(t, server, token, `{"hashes":["task"],"timeoutMs":500}`)
	if response.Code != http.StatusOK || result.Ready != 1 {
		t.Fatalf("scoped sync = HTTP %d %+v: %s", response.Code, result, response.Body.String())
	}
}

func TestTurboSyncNeverCountsInvalidTeamMetadataAsReady(t *testing.T) {
	team := &turboSyncTeam{artifacts: make(map[string][]byte), headBad: true}
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(team.serve))
	team.artifacts[cfg.CompatibilityID+":task"] = []byte("test-output")
	localTurboSyncArtifact(t, server, "task", cfg.CompatibilityID, "test-output")
	response, result := callTurboSync(t, server, cfg.LocalToken, `{"hashes":["task"],"timeoutMs":20}`)
	if response.Code != http.StatusOK || result != (turboSyncResult{Requested: 1, Pending: 1}) {
		t.Fatalf("invalid metadata sync = HTTP %d %+v", response.Code, result)
	}
}

func TestTurboSyncRechecksEarlierHitsAfterPublicationCanEvictThem(t *testing.T) {
	team := &turboSyncTeam{artifacts: make(map[string][]byte), maxEntries: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var initialHit, rechecked atomic.Bool
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		team.serve(writer, request)
		if request.Method == http.MethodHead && strings.HasSuffix(request.URL.Path, "/already") {
			team.mu.Lock()
			published := team.puts > 0
			team.mu.Unlock()
			if published {
				// End confirmation only after the earlier hit was checked again
				// following the write that evicted it. Cancellation may also omit
				// the remaining hit, which is conservatively still unconfirmed.
				rechecked.Store(true)
				cancel()
			} else {
				initialHit.Store(true)
			}
		}
	}))
	team.artifacts[cfg.CompatibilityID+":already"] = []byte("earlier-hit")
	localTurboSyncArtifact(t, server, "new", cfg.CompatibilityID, "later-upload")
	request := httptest.NewRequest(http.MethodPost, "/v1/sync/turbo", strings.NewReader(`{"hashes":["already","new"],"timeoutMs":1000}`)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+cfg.LocalToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	var result turboSyncResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !initialHit.Load() || !rechecked.Load() {
		t.Fatal("confirmation did not recheck the initial Team hit after publication")
	}
	if response.Code != http.StatusOK || result.Requested != 2 || result.Ready < 0 || result.Ready > 1 || result.Pending < 1 || result.Ready+result.Pending != 2 {
		t.Fatalf("post-eviction sync = HTTP %d %+v", response.Code, result)
	}
	team.mu.Lock()
	defer team.mu.Unlock()
	if team.puts != 1 || team.artifacts[cfg.CompatibilityID+":already"] != nil || string(team.artifacts[cfg.CompatibilityID+":new"]) != "later-upload" {
		t.Fatalf("publication did not evict the earlier hit: puts=%d artifacts=%v", team.puts, team.artifacts)
	}
}

func TestTurboSyncDeadlineStopsStalledHead(t *testing.T) {
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	started := time.Now()
	response, result := callTurboSync(t, server, cfg.LocalToken, `{"hashes":["task"],"timeoutMs":20}`)
	if response.Code != http.StatusOK || result != (turboSyncResult{Requested: 1, Pending: 1}) {
		t.Fatalf("stalled sync = HTTP %d %+v", response.Code, result)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("stalled HEAD outlived the synchronization deadline")
	}
}

func TestTurboSyncCancelledHeadOrPutQueuesLocalArtifactAcrossRestart(t *testing.T) {
	for _, stalledMethod := range []string{http.MethodHead, http.MethodPut} {
		t.Run(stalledMethod, func(t *testing.T) {
			team := &turboSyncTeam{artifacts: make(map[string][]byte)}
			var stalled atomic.Bool
			stalled.Store(true)
			server, cfg := newTurboSyncServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if stalled.Load() && request.Method == stalledMethod && strings.HasSuffix(request.URL.Path, "/task") {
					// Drain the upload body so the HTTP server can observe a peer
					// cancellation while this handler stalls the response.
					_, _ = io.Copy(io.Discard, request.Body)
					<-request.Context().Done()
					return
				}
				team.serve(writer, request)
			}))
			team.artifacts[cfg.CompatibilityID+":already"] = []byte("valid-team-result")
			localTurboSyncArtifact(t, server, "already", cfg.CompatibilityID, "different-local-logs")
			localTurboSyncArtifact(t, server, "task", cfg.CompatibilityID, "successful-local-test")
			started := time.Now()
			response, result := callTurboSync(t, server, cfg.LocalToken, `{"hashes":["already","task"],"timeoutMs":20}`)
			if response.Code != http.StatusOK || result.Pending == 0 {
				t.Fatalf("cancelled sync = HTTP %d %+v", response.Code, result)
			}
			if time.Since(started) > 1500*time.Millisecond {
				t.Fatal("confirmation and bounded queue cleanup exceeded their budgets")
			}
			jobs, err := server.uploads.Unfinished(context.Background())
			if err != nil || len(jobs) != 1 {
				t.Fatalf("durable retry count = %d, error %v; ready task must not be queued", len(jobs), err)
			}
			_, queuedHash, err := decodeTurboUploadIdentity(jobs[0].Identity, cfg.CompatibilityID)
			if err != nil || queuedHash != "task" {
				t.Fatalf("queued identity did not select only the unconfirmed task: %v", err)
			}
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			stalled.Store(false)
			restarted, err := New(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = restarted.Close() })
			deadline := time.Now().Add(3 * time.Second)
			for {
				team.mu.Lock()
				body := string(team.artifacts[cfg.CompatibilityID+":task"])
				team.mu.Unlock()
				if body == "successful-local-test" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("restarted daemon did not publish the requested Local artifact")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestTurboSyncPollRechecksPreviouslyReadyTasks(t *testing.T) {
	team := &turboSyncTeam{artifacts: make(map[string][]byte)}
	var secondChecks int
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodHead && strings.HasSuffix(request.URL.Path, "/second") {
			team.mu.Lock()
			secondChecks++
			if secondChecks == 2 {
				// A background publisher makes second ready while evicting first
				// just after the initial verification pass observed first.
				compatibilityID := request.Header.Get("X-LayerCache-Compatibility")
				delete(team.artifacts, compatibilityID+":first")
				team.artifacts[compatibilityID+":second"] = []byte("new-result")
				team.mu.Unlock()
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			team.mu.Unlock()
		}
		team.serve(writer, request)
	}))
	team.artifacts[cfg.CompatibilityID+":first"] = []byte("old-result")
	response, result := callTurboSync(t, server, cfg.LocalToken, `{"hashes":["first","second"],"timeoutMs":220}`)
	if response.Code != http.StatusOK || result != (turboSyncResult{Requested: 2, Ready: 1, Pending: 1}) {
		t.Fatalf("eviction between passes produced false readiness: HTTP %d %+v", response.Code, result)
	}
}

func TestTurboSyncOfflinePublicationIsQueuedAndTimeoutRemainsBounded(t *testing.T) {
	team := &turboSyncTeam{artifacts: make(map[string][]byte), offline: true}
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(team.serve))
	localTurboSyncArtifact(t, server, "task", cfg.CompatibilityID, "test-output")
	started := time.Now()
	response, result := callTurboSync(t, server, cfg.LocalToken, `{"hashes":["task","absent"],"timeoutMs":40}`)
	if response.Code != http.StatusOK || result != (turboSyncResult{Requested: 2, Pending: 2}) {
		t.Fatalf("offline sync = HTTP %d %+v", response.Code, result)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("offline sync ignored its overall timeout")
	}
	jobs, err := server.uploads.Unfinished(context.Background())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("pending publication jobs = %d, error %v", len(jobs), err)
	}
	team.mu.Lock()
	team.offline = false
	team.mu.Unlock()
	response, result = callTurboSync(t, server, cfg.LocalToken, `{"hashes":["task"],"timeoutMs":500}`)
	if response.Code != http.StatusOK || result.Ready != 1 {
		t.Fatalf("recovered sync = HTTP %d %+v", response.Code, result)
	}
}

func TestTurboSyncRequeuesCompletedUploadAfterTeamEvictionAndOfflineRestart(t *testing.T) {
	team := &turboSyncTeam{artifacts: make(map[string][]byte)}
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(team.serve))
	entry := localTurboSyncArtifact(t, server, "task", cfg.CompatibilityID, "successful-test-output")
	if err := server.enqueueTurboUpload(context.Background(), entry.Key, entry); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		stats, err := server.uploads.Stats(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if stats.CompletedJobs == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial queued upload did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	team.mu.Lock()
	delete(team.artifacts, cfg.CompatibilityID+":task")
	team.offline = true
	team.mu.Unlock()
	response, result := callTurboSync(t, server, cfg.LocalToken, `{"hashes":["task"],"timeoutMs":40}`)
	if response.Code != http.StatusOK || result != (turboSyncResult{Requested: 1, Pending: 1}) {
		t.Fatalf("offline repair = HTTP %d %+v", response.Code, result)
	}
	jobs, err := server.uploads.Unfinished(context.Background())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("completed upload was not durably requeued: %+v, %v", jobs, err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	team.mu.Lock()
	team.offline = false
	team.mu.Unlock()
	restarted, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	deadline = time.Now().Add(3 * time.Second)
	for {
		team.mu.Lock()
		body := string(team.artifacts[cfg.CompatibilityID+":task"])
		team.mu.Unlock()
		if body == "successful-test-output" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restarted daemon did not repair the evicted Team result")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTurboSyncRejectsReaderWrongIntegrationAndOtherRoles(t *testing.T) {
	team := &turboSyncTeam{artifacts: make(map[string][]byte)}
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(team.serve))
	for _, authority := range []struct {
		integration string
		capability  access.Capability
		project     string
	}{
		{"turbo", access.CapabilityRead, cfg.ProjectID},
		{"actions", access.CapabilityWrite, cfg.ProjectID},
		{"turbo", access.CapabilityWrite, "other-project"},
	} {
		token, err := access.MintCapabilityToken(cfg.LocalToken, access.Claims{Subject: "caller", Project: authority.project, Integration: authority.integration, Compatibility: cfg.CompatibilityID, Capabilities: []access.Capability{authority.capability}, ExpiresAt: time.Now().Add(time.Hour)}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		response, _ := callTurboSync(t, server, token, `{"hashes":["task"],"timeoutMs":1}`)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized authority received HTTP %d", response.Code)
		}
	}
	server.config.Role = "team"
	response, _ := callTurboSync(t, server, cfg.LocalToken, `{"hashes":["task"],"timeoutMs":1}`)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Team role sync received HTTP %d", response.Code)
	}
}

func TestTurboSyncRejectsMalformedBoundsAndHashSelectors(t *testing.T) {
	team := &turboSyncTeam{artifacts: make(map[string][]byte)}
	server, cfg := newTurboSyncServer(t, http.HandlerFunc(team.serve))
	excessiveHashes := strings.Repeat(`"task",`, maximumTurboSyncHashes) + `"extra"`
	for _, contents := range []string{
		`{}`, `{"hashes":[]}`, `{"hashes":[""]}`, `{"hashes":["a/b"]}`, `{"hashes":[".."]}`, `{"hashes":["has space"]}`, `{"hashes":["line\nbreak"]}`,
		`{"hashes":["task"],"timeoutMs":-1}`, `{"hashes":["task"],"timeoutMs":60001}`, `{"hashes":["task"],"timeoutMs":1.5}`,
		`{"hashes":["task"],"endpoint":"https://other-team"}`, `{"hashes":["task"]} {}`, `{"hashes":["` + strings.Repeat("a", 513) + `"]}`,
		`{"hashes":[` + excessiveHashes + `]}`,
	} {
		response, _ := callTurboSync(t, server, cfg.LocalToken, contents)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("malformed request received HTTP %d: %.100s", response.Code, contents)
		}
	}
}
