package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/dashboard"
)

func dashboardFixtureConfig(t *testing.T) (config.Config, string) {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = listener.Addr().String()
	listener.Close()
	cfg.DataDir = filepath.Join(t.TempDir(), "cache")
	cfg.ProjectID = "github.com/acme/dashboard-fixture"
	cfg.MaxBytes = 1 << 20
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return cfg, path
}

func launchDashboardFixture(t *testing.T, args ...string) (string, string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	links := make(chan string, 1)
	go func() { done <- runDashboard(ctx, args, writer, io.Discard); writer.Close() }()
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "http://") {
				links <- scanner.Text()
			}
		}
	}()
	var link string
	select {
	case link = <-links:
	case err := <-done:
		cancel()
		reader.Close()
		t.Fatalf("dashboard exited: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		reader.Close()
		t.Fatal("dashboard not ready")
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	token := parsed.Fragment
	parsed.Fragment = ""
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("dashboard shutdown: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("dashboard did not stop")
			}
			reader.Close()
		})
	}
	t.Cleanup(stop)
	return strings.TrimRight(parsed.String(), "/"), token, stop
}

func dashboardFixtureSnapshot(t *testing.T, origin, token string) dashboard.Snapshot {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, origin+"/api/snapshot", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	client := newLocalCLIHTTPClient(5 * time.Second)
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result dashboard.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("snapshot: %d", response.StatusCode)
	}
	return result
}

func TestDashboardServeOwnsBackendAndPreservesSourceAndExistingRuntime(t *testing.T) {
	cfg, path := dashboardFixtureConfig(t)
	origin, token, stop := launchDashboardFixture(t, "--config", path, "--serve", "--allow-prune")
	snapshot := dashboardFixtureSnapshot(t, origin, token)
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].Local.Status == nil || !snapshot.Projects[0].Local.PruneAllowed {
		t.Fatalf("owned runtime unavailable: %#v", snapshot)
	}
	request, _ := http.NewRequest(http.MethodPut, localRuntimeURL(cfg.Listen)+"/v8/artifacts/fixture", strings.NewReader("owned-by-fixture"))
	request.Header.Set("Authorization", "Bearer "+cfg.LocalToken)
	client := newLocalCLIHTTPClient(5 * time.Second)
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("seed: %d", response.StatusCode)
	}
	// A second owner cannot replace the running listener or its runtime lock.
	err = runDashboard(context.Background(), []string{"--config", path, "--serve"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "existing runtime is never replaced") {
		t.Fatalf("conflict result: %v", err)
	}
	if snapshot := dashboardFixtureSnapshot(t, origin, token); snapshot.Projects[0].Local.Status.Artifacts != 1 {
		t.Fatalf("conflict disturbed first runtime: %#v", snapshot)
	}
	readOrigin, readToken, stopRead := launchDashboardFixture(t, "--config", path)
	readSnapshot := dashboardFixtureSnapshot(t, readOrigin, readToken)
	if readSnapshot.Projects[0].Local.PruneAllowed {
		t.Fatal("read-only dashboard advertised maintenance")
	}
	post := func(origin, token, project string) *http.Response {
		t.Helper()
		payload, _ := json.Marshal(dashboard.PruneRequest{ProjectID: project, Cache: "local", TargetBytes: 0, Confirm: true})
		request, _ := http.NewRequest(http.MethodPost, origin+"/api/prune", strings.NewReader(string(payload)))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response = post(readOrigin, readToken, cfg.ProjectID)
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("read-only maintenance status %d", response.StatusCode)
	}
	stopRead()
	response = post(origin, token, "github.com/foreign/project")
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign selector status %d", response.StatusCode)
	}
	response = post(origin, token, cfg.ProjectID)
	var result dashboard.PruneResult
	err = json.NewDecoder(response.Body).Decode(&result)
	response.Body.Close()
	if response.StatusCode != 200 || err != nil || result.BeforeBytes != int64(len("owned-by-fixture")) || result.AfterBytes != 0 || result.AfterEntries != 0 {
		t.Fatalf("real maintenance: %#v %v HTTP %d", result, err, response.StatusCode)
	}
	stop()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatalf("owned listener survived shutdown: %v", err)
	}
	listener.Close()
	current, err := config.Load(path)
	if err != nil || current.ProjectID != cfg.ProjectID || current.DataDir != cfg.DataDir || current.LocalToken != cfg.LocalToken {
		t.Fatal("configuration changed")
	}
	// The runtime lock, too, must have been released by the owner.
	backends, err := startDashboardBackends(context.Background(), []config.Config{cfg})
	if err != nil {
		t.Fatal(err)
	}
	closeDashboardBackends(backends)
}

func TestDashboardServeRollsBackOnlyNewlyStartedBackends(t *testing.T) {
	cfg, _ := dashboardFixtureConfig(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	second := cfg
	second.Listen = busy.Addr().String()
	second.DataDir = filepath.Join(t.TempDir(), "second")
	if _, err := startDashboardBackends(context.Background(), []config.Config{cfg, second}); err == nil {
		t.Fatal("accepted conflicting backend")
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal("partial startup retained first listener")
	}
	listener.Close()
	connection, err := net.DialTimeout("tcp", busy.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("unowned listener was stopped: %v", err)
	}
	connection.Close()
	backends, err := startDashboardBackends(context.Background(), []config.Config{cfg})
	if err != nil {
		t.Fatalf("partial startup retained lock: %v", err)
	}
	closeDashboardBackends(backends)
}

func TestDashboardBackendSelectionIsRemoteOnlyAndCannotRetargetCredentials(t *testing.T) {
	cfg, path := dashboardFixtureConfig(t)
	var remoteCalls, localCalls, foreignCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer team-secret" || r.Header.Get("X-LayerCache-Project") != cfg.ProjectID {
			t.Error("remote authority changed")
		}
		if r.URL.Path == "/v1/status" {
			json.NewEncoder(w).Encode(map[string]any{"projectId": cfg.ProjectID, "role": "team", "usageBytes": 5})
		} else {
			io.WriteString(w, `{"schemaVersion":"2"}`)
		}
	}))
	defer upstream.Close()
	local := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { localCalls.Add(1) }))
	defer local.Close()
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignCalls.Add(1) }))
	defer foreign.Close()
	cfg.Listen = strings.TrimPrefix(local.URL, "http://")
	cfg.TeamURL = upstream.URL
	cfg.TeamToken = "team-secret"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := runDashboard(context.Background(), []string{"--config", path, "--backend", foreign.URL}, io.Discard, io.Discard); err == nil {
		t.Fatal("accepted foreign backend")
	}
	if remoteCalls.Load() != 0 || foreignCalls.Load() != 0 {
		t.Fatal("validation sent credentials")
	}
	origin, token, stop := launchDashboardFixture(t, "--config", path, "--backend", upstream.URL+"/")
	defer stop()
	snapshot := dashboardFixtureSnapshot(t, origin, token)
	if snapshot.Projects[0].Local.State != "not-selected" || snapshot.Projects[0].Team.State != "connected" || localCalls.Load() != 0 {
		t.Fatalf("remote-only selection: %#v", snapshot)
	}
	cfg.TeamURL = foreign.URL
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	snapshot = dashboardFixtureSnapshot(t, origin, token)
	if snapshot.Projects[0].Team.State != "unavailable" || foreignCalls.Load() != 0 || localCalls.Load() != 0 {
		t.Fatalf("retargeted explicit selection: %#v", snapshot)
	}
}

func TestDashboardPruneChecksLiveAuthorityAndNeverFollowsRedirects(t *testing.T) {
	for _, test := range []struct {
		name     string
		allowed  bool
		role     string
		gcStatus int
		want     error
	}{{"reader", false, "team", 200, dashboard.ErrPruneForbidden}, {"public", true, "public", 200, dashboard.ErrPruneForbidden}, {"revoked", true, "team", 403, dashboard.ErrPruneForbidden}, {"uncertain", true, "team", 503, dashboard.ErrPruneUnavailable}, {"redirect", true, "team", 302, dashboard.ErrPruneUnavailable}, {"admin", true, "team", 200, nil}} {
		t.Run(test.name, func(t *testing.T) {
			puts := 0
			foreignCalls := 0
			foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignCalls++ }))
			defer foreign.Close()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/status":
					json.NewEncoder(w).Encode(map[string]any{"projectId": "project", "role": test.role, "canPrune": test.allowed, "maxBytes": 100})
				case "/v1/reports":
					io.WriteString(w, `{"schemaVersion":"2"}`)
				case "/v1/gc":
					puts++
					if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer scoped-token" || r.Header.Get("X-LayerCache-Project") != "project" {
						t.Error("mutation authority changed")
					}
					if test.gcStatus == 302 {
						http.Redirect(w, r, foreign.URL, 302)
						return
					}
					w.WriteHeader(test.gcStatus)
					if test.gcStatus == 200 {
						io.WriteString(w, `{"targetBytes":0,"beforeBytes":8,"afterBytes":0,"freedBytes":8,"beforeEntries":1,"afterEntries":0}`)
					} else {
						io.WriteString(w, "protected-server-details")
					}
				default:
					t.Error("unexpected route")
				}
			}))
			defer upstream.Close()
			result, err := pruneDashboardCache(context.Background(), newCLIHTTPClient(time.Second), upstream.URL, "scoped-token", "project", 0)
			if !errors.Is(err, test.want) || foreignCalls != 0 {
				t.Fatalf("result: %#v %v foreign=%d", result, err, foreignCalls)
			}
			if !test.allowed || test.role == "public" {
				if puts != 0 {
					t.Fatal("unauthorized mutation attempted")
				}
			} else if puts != 1 {
				t.Fatalf("mutations: %d", puts)
			}
		})
	}
}

func TestDashboardDefaultReloadDoesNotForwardOldDiskCapability(t *testing.T) {
	cfg, path := dashboardFixtureConfig(t)
	cfg.TeamURL = "http://127.0.0.1:1"
	cfg.TeamToken = "old-endpoint-capability"
	cfg.TeamTokenExpiresAt = time.Now().Add(time.Hour)
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	var foreignCalls atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignCalls.Add(1) }))
	defer foreign.Close()
	origin, token, _ := launchDashboardFixture(t, "--config", path)
	// The first snapshot must also be safe if configuration changes after startup.
	cfg.TeamURL = foreign.URL
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		snapshot := dashboardFixtureSnapshot(t, origin, token)
		if snapshot.Projects[0].Team.State != "authentication-required" || foreignCalls.Load() != 0 {
			t.Fatalf("old capability retargeted: %#v requests=%d", snapshot, foreignCalls.Load())
		}
	}
}

func TestDashboardRemoteURLNormalizationAndFlagConflicts(t *testing.T) {
	for _, test := range []struct{ input, want string }{{"https://CACHE.example:443/cache/", "https://cache.example/cache"}, {"http://127.0.0.1:80/", "http://127.0.0.1"}, {"http://[::1]:80/", "http://[::1]"}} {
		got, err := normalizeDashboardBackend(test.input)
		if err != nil || got != test.want {
			t.Fatalf("normalize %q: %q %v", test.input, got, err)
		}
	}
	for _, endpoint := range []string{"https://user:token@cache.example", "https://cache.example?token=x", "https://cache.example#secret", "http://cache.example", "https://cache.example?", "https://cache.example/%2f"} {
		if _, err := normalizeDashboardBackend(endpoint); err == nil {
			t.Fatalf("accepted endpoint %q", endpoint)
		}
	}
	_, path := dashboardFixtureConfig(t)
	for _, args := range [][]string{{"--config", path, "--serve", "--backend", "http://127.0.0.1:1"}, {"--config", path, "--config", path, "--backend", "http://127.0.0.1:1"}} {
		if err := runDashboard(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted conflicting flags: %v", args)
		}
	}
}

func TestDashboardOwnedBackendRefreshesInMemoryWithoutChangingConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell credential fixture")
	}
	cfg, path := dashboardFixtureConfig(t)
	credentialProgram := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(credentialProgram, []byte("#!/bin/sh\nprintf 'fixture-github-token\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	const payload = "fresh owned backend output"
	var exchanges, artifactReads atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/github/exchange":
			exchanges.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"teamToken": "refreshed-owned-capability", "expiresAt": time.Now().Add(time.Hour)})
		case "/v8/artifacts/refreshed-fixture":
			artifactReads.Add(1)
			if r.Header.Get("Authorization") != "Bearer refreshed-owned-capability" {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("x-layercache-digest", fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(payload))))
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			if r.Method != http.MethodHead {
				io.WriteString(w, payload)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	cfg.TeamURL = upstream.URL
	cfg.TeamToken = "expired-owned-capability"
	cfg.TeamTokenExpiresAt = time.Now().Add(-time.Hour)
	cfg.GitHubCLIPath = credentialProgram
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, stop := launchDashboardFixture(t, "--config", path, "--serve")
	deadline := time.Now().Add(3 * time.Second)
	for exchanges.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if exchanges.Load() != 1 {
		t.Fatalf("background refreshes=%d", exchanges.Load())
	}
	client := newLocalCLIHTTPClient(5 * time.Second)
	defer client.CloseIdleConnections()
	var body []byte
	var code int
	// Wait for the refresh to finish after its exchange endpoint has been reached.
	for time.Now().Before(deadline) {
		request, _ := http.NewRequest(http.MethodGet, localRuntimeURL(cfg.Listen)+"/v8/artifacts/refreshed-fixture", nil)
		request.Header.Set("Authorization", "Bearer "+cfg.LocalToken)
		response, requestErr := client.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		body, err = io.ReadAll(response.Body)
		response.Body.Close()
		code = response.StatusCode
		if code == http.StatusOK {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || code != http.StatusOK || string(body) != payload || artifactReads.Load() == 0 {
		t.Fatalf("owned backend did not use refreshed authority: HTTP %d body=%q err=%v", code, body, err)
	}
	stop()
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatal("owned refresh changed source configuration")
	}
}
