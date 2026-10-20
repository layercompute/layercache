package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/portal"
	"github.com/layercache/layercache/internal/server"
)

func TestRemoteOnlyNativeRunUsesHostedMemberAuthorityWithoutLocalStore(t *testing.T) {
	t.Setenv("GITHUB_REF", "")
	root := t.TempDir()
	clone := filepath.Join(root, "clone")
	commit := initCLIRepository(t, clone, "feature/hosted", "hosted")
	t.Chdir(clone)
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf github-fixture-secret"), 0700); err != nil {
		t.Fatal(err)
	}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer github-fixture-secret" {
			http.Error(w, "denied", 401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "developer", "id": 12345})
	}))
	defer github.Close()
	backend, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	backend.Role = "team"
	backend.DataDir = filepath.Join(root, "backend")
	backend.MaxBytes = 1 << 20
	backend.ProjectID = "github.com/acme/widgets"
	backend.ActionsRepository = "acme/widgets"
	backend.ActionsRef = "refs/heads/main"
	backend.ActionsDefaultRef = "refs/heads/main"
	backend.LocalToken = "real-hosted-signing-key"
	backend.GitHubAPIURL = github.URL
	backend.TeamMembers = map[string]string{"developer": "writer"}
	runtime, err := server.New(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var puts atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
		}
		runtime.Handler().ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	cfg := backend
	cfg.Role = "local"
	cfg.RemoteOnly = true
	cfg.LocalToken = "must-never-sign-hosted-requests"
	cfg.DataDir = filepath.Join(root, "no-local-store")
	cfg.ProjectRoot = clone
	cfg.GitHubAPIURL = ""
	cfg.GitHubCLIPath = gh
	cfg.TeamURL = endpoint.URL
	cfg.TeamToken = "expired-token"
	cfg.TeamTokenExpiresAt = time.Now().Add(-time.Hour)
	path := filepath.Join(root, "client.json")
	if err = config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	t.Setenv("LAYERCACHE_REMOTE_ONLY_CHILD", "write")
	output, diagnostics, err := callCLI("run", "--config", path, "--", os.Args[0], "-test.run=^TestRemoteOnlyNativeChild$")
	if err != nil {
		t.Fatalf("hosted child failed: %v %s", err, diagnostics)
	}
	var observed struct {
		Token, Endpoint string
		Codes           map[string]int
		Bodies          map[string]string
	}
	if err = json.Unmarshal([]byte(output), &observed); err != nil {
		t.Fatal(err)
	}
	claims, err := access.ParseCapabilityToken(backend.LocalToken, observed.Token, time.Now())
	if err != nil {
		t.Fatal("native token was not signed by the hosted service", err)
	}
	if _, err = access.ParseCapabilityToken(cfg.LocalToken, observed.Token, time.Now()); err == nil {
		t.Fatal("hosted run minted a local signing token")
	}
	if claims.Project != cfg.ProjectID || claims.Repository != "acme/widgets" || claims.Ref != "refs/heads/feature/hosted" || claims.DefaultRef != "refs/heads/main" || claims.SourceCommit != commit || claims.Compatibility != cfg.CompatibilityID || claims.Integration != "turbo" || !strings.HasPrefix(claims.RunID, "github-local:github:developer:run-") || !strings.HasPrefix(claims.WorkspaceID, "github-local:github:developer:sha256:") || claims.Allows(access.CapabilityAdmin) {
		t.Fatalf("incorrect hosted run claims: %+v", claims)
	}
	for _, key := range []string{"turbo-put", "turbo-get", "gradle-put", "gradle-get", "nx-put", "nx-get"} {
		if observed.Codes[key] != http.StatusOK {
			t.Fatalf("%s returned %d", key, observed.Codes[key])
		}
	}
	if observed.Codes["changed"] != 404 || observed.Codes["actions"] != 204 {
		t.Fatalf("changed/native Actions outcomes: %+v", observed.Codes)
	}
	if puts.Load() != 3 {
		t.Fatalf("writer PUTs=%d", puts.Load())
	}
	if err = syncTurboTeamResults(context.Background(), config.Config{RemoteOnly: true, TeamURL: endpoint.URL, TeamToken: observed.Token, ProjectID: cfg.ProjectID, CompatibilityID: cfg.CompatibilityID}, []string{"native-turbo"}, time.Second); err != nil {
		t.Fatal("remote sync did not confirm existing artifact", err)
	}
	if err = syncTurboTeamResults(context.Background(), config.Config{RemoteOnly: true, TeamURL: endpoint.URL, TeamToken: observed.Token, ProjectID: cfg.ProjectID, CompatibilityID: cfg.CompatibilityID}, []string{"changed-input"}, time.Second); err == nil {
		t.Fatal("remote sync confirmed a missing artifact")
	}
	t.Setenv("LAYERCACHE_REMOTE_ONLY_CHILD", "read")
	output, diagnostics, err = callCLI("run", "--config", path, "--", os.Args[0], "-test.run=^TestRemoteOnlyNativeChild$")
	if err != nil {
		t.Fatalf("independent read child failed: %v %s", err, diagnostics)
	}
	if err = json.Unmarshal([]byte(output), &observed); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"turbo-get", "gradle-get", "nx-get"} {
		if observed.Codes[key] != 200 || observed.Bodies[key] != "exact-native-bytes" {
			t.Fatalf("%s restore failed (%d)", key, observed.Codes[key])
		}
	}
	if puts.Load() != 3 {
		t.Fatal("independent read consumer published unexpectedly")
	}
	for _, args := range [][]string{{"status", "--config", path, "--json"}, {"doctor", "--config", path, "--json"}, {"report", "--config", path, "--from", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "--to", time.Now().UTC().Format(time.RFC3339), "--json"}, {"repair", "--config", path, "--json"}} {
		if _, _, err = callCLI(args...); err != nil {
			t.Fatalf("read-only hosted command %s failed: %v", args[0], err)
		}
	}
	for _, args := range [][]string{{"start", "--config", path}, {"serve", "--config", path}, {"dashboard", "--config", path, "--serve"}, {"integration", "turbo", "--config", path, "--apply"}, {"integration", "local-ci", "--config", path, "--apply"}} {
		if _, _, err = callCLI(args...); err == nil {
			t.Fatalf("Local operation accepted: %s", args[0])
		}
	}
	if _, err = os.Stat(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatalf("hosted operations created Local artifact storage: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("per-run authority overwrote saved configuration")
	}
	if strings.Contains(diagnostics, "github-fixture-secret") || strings.Contains(diagnostics, observed.Token) {
		t.Fatal("credentials appeared in CLI diagnostics")
	}
}

func TestRemoteOnlyNativeChild(t *testing.T) {
	mode := os.Getenv("LAYERCACHE_REMOTE_ONLY_CHILD")
	if mode == "" {
		return
	}
	endpoint := os.Getenv("TURBO_API")
	token := os.Getenv("TURBO_TOKEN")
	codes := map[string]int{}
	bodies := map[string]string{}
	request := func(name, method, target string) {
		r, err := http.NewRequest(method, target, strings.NewReader("exact-native-bytes"))
		if err != nil {
			t.Fatal(err)
		}
		selectedToken := token
		if strings.HasPrefix(name, "nx-") {
			selectedToken = os.Getenv("NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN")
		}
		if name == "actions" {
			selectedToken = os.Getenv("ACTIONS_RUNTIME_TOKEN")
		}
		r.Header.Set("Authorization", "Bearer "+selectedToken)
		if strings.HasPrefix(name, "gradle-") {
			r.SetBasicAuth("layercache", os.Getenv("LAYER_CACHE_GRADLE_TOKEN"))
		}
		response, err := newCLIHTTPClient(time.Second * 5).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		codes[name] = response.StatusCode
		bodies[name] = string(body)
	}
	paths := map[string]string{"turbo": endpoint + "/v8/artifacts/native-turbo?teamId=" + os.Getenv("TURBO_TEAM"), "gradle": os.Getenv("LAYER_CACHE_GRADLE_URL") + strings.Repeat("a", 64), "nx": os.Getenv("NX_SELF_HOSTED_REMOTE_CACHE_SERVER") + "/v1/cache/native-nx"}
	for name, path := range paths {
		if mode == "write" {
			request(name+"-put", http.MethodPut, path)
		}
		request(name+"-get", http.MethodGet, path)
	}
	request("changed", http.MethodGet, endpoint+"/v8/artifacts/changed-input")
	request("actions", http.MethodGet, os.Getenv("ACTIONS_CACHE_URL")+"_apis/artifactcache/cache?keys=unavailable&version=native-v1")
	request("status", http.MethodGet, endpoint+"/v1/status")
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"Token": token, "Endpoint": endpoint, "Codes": codes, "Bodies": bodies})
	os.Exit(0)
}

func TestRemoteOnlyConnectImportsLegacyIDWithoutLocalArtifacts(t *testing.T) {
	root := t.TempDir()
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf fixture-secret"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/projects":
			_ = json.NewEncoder(w).Encode(cloudProjects{Teams: []portal.Team{{ID: "team"}}, Projects: []portal.Project{{ID: "github.com/acme/widgets", TeamID: "team", Name: "Widgets", Repository: "acme/widgets", DefaultRef: "refs/heads/main"}}})
		case "/v1/auth/github/exchange":
			_ = json.NewEncoder(w).Encode(capabilityExchange{Token: "fixture-capability", ExpiresAt: time.Now().Add(time.Hour)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()
	stdout, stderr, err := callCLI("connect", "--cloud", service.URL, "--github-cli", "--remote-only", "--json")
	if err != nil {
		t.Fatalf("remote connect failed: %v %s", err, stderr)
	}
	var connected struct {
		Config string `json:"config"`
	}
	if err = json.Unmarshal([]byte(stdout), &connected); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(connected.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RemoteOnly || cfg.ProjectID != "github.com/acme/widgets" || len(filepath.Base(connected.Config)) != 37 {
		t.Fatal("legacy ID was not imported into a hashed config filename")
	}
	if _, err = os.Stat(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatal("connect created a Local artifact directory")
	}
}

func TestRemoteOnlyRunClearsInheritedCredentialsWhenAuthenticationOrScopeFails(t *testing.T) {
	root := t.TempDir()
	clone := filepath.Join(root, "clone")
	initCLIRepository(t, clone, "main", "failure")
	t.Chdir(clone)
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RemoteOnly = true
	cfg.ProjectID = "github.com/acme/widgets"
	cfg.ActionsRepository = "acme/widgets"
	cfg.ProjectRoot = clone
	cfg.TeamURL = "https://unavailable.example"
	cfg.DataDir = filepath.Join(root, "absent")
	path := filepath.Join(root, "config.json")
	if err = config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAYERCACHE_GRADLE_CHILD", "1")
	t.Setenv("LAYER_CACHE_GRADLE_URL", "https://foreign.example")
	t.Setenv("LAYER_CACHE_GRADLE_TOKEN", "foreign-token")
	for _, wrongScope := range []bool{false, true} {
		if wrongScope {
			runCLIGit(t, clone, "remote", "set-url", "origin", "https://github.com/other/repo.git")
		}
		stdout, stderr, err := callCLI("run", "--config", path, "--", os.Args[0], "-test.run=^TestGradleConnectionChild$")
		if err != nil {
			t.Fatalf("fallback failed: %v %s", err, stderr)
		}
		var observed map[string]string
		if err = json.Unmarshal([]byte(stdout), &observed); err != nil {
			t.Fatal(err)
		}
		if observed["url"] != "" || observed["token"] != "" {
			t.Fatal("fallback leaked inherited credentials")
		}
	}
	if _, err = os.Stat(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatal("failed authentication created Local storage")
	}
}

func TestRemoteOnlySetupAndBuildxPlanDoNotInitializeStorage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	data := filepath.Join(root, "no-store")
	if _, stderr, err := callCLI("setup", "--non-interactive", "--remote-only", "--config", path, "--data-dir", data, "--team-url", "https://cache.example", "--json"); err != nil {
		t.Fatalf("setup: %v %s", err, stderr)
	}
	cfg, err := config.Load(path)
	if err != nil || !cfg.RemoteOnly {
		t.Fatalf("remote setup invalid: %v", err)
	}
	cfg.BuildkitTeamRepository = "registry.example/acme/widgets"
	cfg.TeamToken = "fixture-token"
	cfg.TeamTokenExpiresAt = time.Now().Add(time.Hour)
	if err = config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := callCLI("buildx", "plan", "--config", path, "--team-import", "main", root)
	if err != nil {
		t.Fatalf("registry plan: %v %s", err, stderr)
	}
	if !strings.Contains(stdout, "registry.example/acme/widgets/linux-amd64:main") {
		t.Fatal("existing authenticated registry import was dropped")
	}
	if _, err = os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("setup or BuildKit plan initialized Local storage")
	}
}

func TestRemoteOnlyExchangeRejectsRedirectWithoutCredentialReplay(t *testing.T) {
	var forwarded atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1); w.WriteHeader(200) }))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	_, err := exchangeGitHubCapabilityWithContext(context.Background(), origin.URL, config.Config{ProjectID: "github.com/acme/widgets", CompatibilityID: "linux-amd64-node24", ActionsRepository: "acme/widgets", ActionsRef: "refs/heads/main", ActionsDefaultRef: "refs/heads/main"}, "never-forward-this", &localRunContext{RunID: "run-local-nonce-12345", WorkspaceID: "sha256:" + strings.Repeat("a", 64)})
	if err == nil || forwarded.Load() != 0 {
		t.Fatal("GitHub exchange followed a redirect")
	}
	if strings.Contains(err.Error(), "never-forward-this") {
		t.Fatal("exchange error disclosed credential")
	}
}
