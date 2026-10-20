//go:build linux || darwin

package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/execution"
	"github.com/layercache/layercache/internal/executioncollect"
	"github.com/layercache/layercache/internal/server"
)

func executionObservationHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	return root
}

func executionObservationExchange() capabilityExchange {
	return capabilityExchange{RunID: "authenticated-run", WorkspaceID: "authenticated-workspace", TeamToken: "team-token", NativeTokens: map[string]string{"turbo": "native-token"}, ExecutionObservations: execution.Supported()}
}

func TestClientObservationRequiresAdvertisedRunContext(t *testing.T) {
	executionObservationHome(t)
	var calls atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(201) }))
	defer endpoint.Close()
	cfg := config.Config{RemoteOnly: true, TeamURL: endpoint.URL}
	for _, name := range []string{"old-server", "unknown-version", "foreign-path", "login", "missing-workspace"} {
		exchanged := executionObservationExchange()
		switch name {
		case "old-server":
			exchanged.ExecutionObservations = nil
		case "unknown-version":
			exchanged.ExecutionObservations = &execution.Capability{Version: 2, Path: execution.Path}
		case "foreign-path":
			exchanged.ExecutionObservations = &execution.Capability{Version: 1, Path: "https://foreign.example/observations"}
		case "login":
			exchanged.RunID = ""
		case "missing-workspace":
			exchanged.WorkspaceID = ""
		}
		if startClientExecutionObservation(context.Background(), cfg, exchanged, "") != nil {
			t.Fatalf("observation activated without supported execution context: %s", name)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported service received observation traffic")
	}
	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "layercache")); !os.IsNotExist(err) {
		t.Fatal("old/unsupported service created client identity state")
	}
}

func TestClientObservationsHaveStablePrivateIdentityAndNoKeyInputs(t *testing.T) {
	root := executionObservationHome(t)
	workspace := filepath.Join(root, "private-workspace-not-for-upload")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	var lock sync.Mutex
	var reports []execution.ObservationRequest
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != execution.Path || r.Header.Get("Authorization") != "Bearer native-token" {
			t.Error("incorrect observation endpoint or authority")
		}
		if project := r.Header.Get("X-LayerCache-Project"); project != "github.com/acme/project" && project != "github.com/acme/other" {
			t.Error("observation omitted authenticated project routing")
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, execution.MaximumRequestBytes+1))
		if err != nil || len(body) > execution.MaximumRequestBytes {
			t.Error("unbounded observation")
			return
		}
		for _, forbidden := range []string{workspace, "retained-local-secret", "github-secret", "projectRoot", "githubToken", "hostname", "environment", "sourceCommit"} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("private/key material appeared in observation: %s", forbidden)
			}
		}
		var input execution.ObservationRequest
		if err := json.Unmarshal(body, &input); err != nil {
			t.Error(err)
			return
		}
		authority := execution.Authority{Context: *execution.ClientContext("github-id:123", "fixture"), Project: "github.com/acme/project", RunID: "authenticated-run", WorkspaceID: "authenticated-workspace"}
		if err := input.Validate(authority); err != nil {
			t.Error("actual sample does not satisfy service contract", err)
		}
		lock.Lock()
		reports = append(reports, input)
		lock.Unlock()
		w.WriteHeader(201)
	}))
	defer endpoint.Close()
	cfg := config.Config{RemoteOnly: true, TeamURL: endpoint.URL, ProjectID: "github.com/acme/project", ProjectRoot: workspace, DataDir: filepath.Join(root, "unused-cache"), LocalToken: "retained-local-secret", CompatibilityID: "native-profile-unchanged"}
	original := cfg
	exchanged := executionObservationExchange()
	originalNativeToken := exchanged.NativeTokens["turbo"]
	first := startClientExecutionObservation(context.Background(), cfg, exchanged, workspace)
	if first == nil {
		t.Fatal("supported run did not begin observation")
	}
	first.report(context.Background(), "finish")
	cfg.ProjectID, cfg.ProjectRoot = "github.com/acme/other", filepath.Join(root, "another-worktree")
	second := startClientExecutionObservation(context.Background(), cfg, exchanged, workspace)
	if second == nil {
		t.Fatal("second project did not begin observation")
	}
	lock.Lock()
	defer lock.Unlock()
	if len(reports) != 3 || reports[0].Phase != "start" || reports[1].Phase != "finish" || reports[2].Phase != "start" || reports[0].InstallationID != reports[2].InstallationID {
		t.Fatal("project/workspace aliases were not one installation")
	}
	if len(reports[0].Sample.Filesystems) != 1 || len(reports[0].Sample.Filesystems[0].Roles) != 1 || reports[0].Sample.Filesystems[0].Roles[0] != "workspace" {
		t.Fatal("cloud-only client invented local cache residency")
	}
	if original.CompatibilityID != cfg.CompatibilityID || original.LocalToken != cfg.LocalToken || originalNativeToken != exchanged.NativeTokens["turbo"] {
		t.Fatal("observations changed native identity or credentials")
	}
	if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatal("observation created local artifact storage")
	}
}

func TestObservationFailureDoesNotChangeAuthBodyOrFollowRedirects(t *testing.T) {
	executionObservationHome(t)
	var foreign atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreign.Add(1) }))
	defer other.Close()
	var authKeys []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/github/exchange" {
			var request map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			for key := range request {
				authKeys = append(authKeys, key)
			}
			result := executionObservationExchange()
			result.ExpiresAt = time.Now().Add(time.Hour)
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		http.Redirect(w, r, other.URL+execution.Path, http.StatusTemporaryRedirect)
	}))
	defer endpoint.Close()
	cfg := config.Config{RemoteOnly: true, TeamURL: endpoint.URL, ProjectID: "github.com/acme/project", CompatibilityID: "native-profile", ActionsRepository: "acme/project", ActionsRef: "refs/heads/main", ActionsDefaultRef: "refs/heads/main"}
	exchanged, err := exchangeGitHubCapabilityWithContext(context.Background(), endpoint.URL, cfg, "github-secret", &localRunContext{RunID: "run", WorkspaceID: "workspace", SourceCommit: strings.Repeat("a", 40)})
	if err != nil {
		t.Fatal("optional observations broke authentication", err)
	}
	sort.Strings(authKeys)
	expected := []string{"compatibility", "defaultRef", "githubToken", "project", "ref", "repository", "runId", "sourceCommit", "workspaceId"}
	if !reflect.DeepEqual(authKeys, expected) {
		t.Fatal("resource fields entered the native auth/key input contract", authKeys)
	}
	observation := startClientExecutionObservation(context.Background(), cfg, exchanged, "")
	if observation == nil {
		t.Fatal("optional unavailable reporting disabled the authenticated run")
	}
	observation.report(context.Background(), "finish")
	if foreign.Load() != 0 {
		t.Fatal("observation replayed authority to another origin")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observation.report(ctx, "finish")
}

func TestReadDashboardExecutionsPreservesUnavailable(t *testing.T) {
	now := time.Now().UTC()
	valid := execution.Inventory{Version: 1, From: now.Add(-24 * time.Hour), To: now, UpdatedAt: now, RetentionDays: 30, StaleAfterSeconds: 86400, Installations: []execution.Entry{}, Jobs: []execution.Entry{}}
	for _, name := range []string{"supported", "old-server", "forbidden", "invalid", "incomplete", "trailing"} {
		t.Run(name, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != execution.Path || r.URL.Query().Get("period") != "7d" || r.Header.Get("Authorization") != "Bearer read-token" || r.Header.Get("X-LayerCache-Project") != "github.com/acme/project" {
					t.Error("incorrect inventory request")
				}
				switch name {
				case "old-server":
					w.WriteHeader(404)
				case "forbidden":
					w.WriteHeader(403)
				case "invalid":
					_, _ = w.Write([]byte("invalid"))
				case "incomplete":
					_, _ = w.Write([]byte(`{"version":1}`))
				default:
					_ = json.NewEncoder(w).Encode(valid)
					if name == "trailing" {
						_, _ = w.Write([]byte(`{}`))
					}
				}
			}))
			defer endpoint.Close()
			result := readDashboardExecutions(context.Background(), newCLIHTTPClient(time.Second), endpoint.URL, "read-token", "github.com/acme/project", now.Add(-7*24*time.Hour), now)
			if (result != nil) != (name == "supported") {
				t.Fatal("unsupported inventory became an empty successful sample")
			}
		})
	}
}

func TestFailedClientObservationPreservesCommandFailureAndConfiguration(t *testing.T) {
	root := executionObservationHome(t)
	t.Setenv("GITHUB_REF", "")
	workspace := filepath.Join(root, "workspace")
	initCLIRepository(t, workspace, "feature/observations", "observations")
	t.Chdir(workspace)
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf fixture-github-secret"), 0o700); err != nil {
		t.Fatal(err)
	}
	var observations atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/github/exchange" {
			value := executionObservationExchange()
			value.NativeTokens["actions"], value.NativeTokens["nx"], value.NativeTokens["gradle"] = "actions-token", "nx-token", "gradle-token"
			value.ExpiresAt = time.Now().Add(time.Hour)
			_ = json.NewEncoder(w).Encode(value)
			return
		}
		if r.URL.Path == execution.Path {
			observations.Add(1)
		}
		http.Error(w, "unavailable", 500)
	}))
	defer endpoint.Close()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RemoteOnly, cfg.TeamURL, cfg.GitHubCLIPath, cfg.ProjectRoot = true, endpoint.URL, gh, workspace
	cfg.ProjectID, cfg.ActionsRepository = "github.com/acme/widgets", "acme/widgets"
	cfg.DataDir = filepath.Join(root, "no-artifact-store")
	configuration := filepath.Join(root, "client.json")
	if err := config.Save(configuration, cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configuration)
	if err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := callCLI("run", "--config", configuration, "--", "/bin/sh", "-c", "exit 37")
	if err == nil || ExitCode(err) != 37 {
		t.Fatalf("optional observation changed task failure: %v", err)
	}
	if observations.Load() != 2 {
		t.Fatal("failed command did not preserve start/finish reporting attempts")
	}
	after, err := os.ReadFile(configuration)
	if err != nil || string(before) != string(after) {
		t.Fatal("observation changed project configuration")
	}
	if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatal("cloud observation created a Local artifact store")
	}
	for _, secret := range []string{"fixture-github-secret", "native-token", workspace} {
		if strings.Contains(output+diagnostics, secret) {
			t.Fatal("optional observation exposed protected material")
		}
	}
}

func TestStandaloneExecutionObservationNegotiatesBeforeAddingClaims(t *testing.T) {
	root := executionObservationHome(t)
	t.Setenv("GITHUB_REF", "")
	t.Setenv("LAYERCACHE_EXECUTION_CHILD", "1")
	workspace := filepath.Join(root, "workspace")
	initCLIRepository(t, workspace, "feature/old-daemon", "old")
	t.Chdir(workspace)
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProjectID, cfg.ActionsRepository, cfg.ProjectRoot = "github.com/acme/widgets", "acme/widgets", workspace
	cfg.DataDir = filepath.Join(root, "old-daemon-cache")
	var posts atomic.Int64
	oldDaemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		if r.URL.Path == "/v1/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{"running": true, "projectId": cfg.ProjectID, "role": "local"})
			return
		}
		claims, err := access.ParseCapabilityToken(cfg.LocalToken, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), time.Now())
		if err != nil || claims.Execution != nil {
			t.Error("old daemon received a new execution claim", err)
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(200)
	}))
	defer oldDaemon.Close()
	cfg.Listen = strings.TrimPrefix(oldDaemon.URL, "http://")
	path := filepath.Join(root, "old-client.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := callCLI("run", "--config", path, "--", os.Args[0], "-test.run=^TestExecutionObservationNativeChild$")
	if err != nil {
		t.Fatalf("old daemon lost native compatibility: %v %s", err, diagnostics)
	}
	var child struct {
		NativeCode int
		Token      string
	}
	if err := json.Unmarshal([]byte(output), &child); err != nil || child.NativeCode != 200 {
		t.Fatal("old native read did not execute", err)
	}
	claims, err := access.ParseCapabilityToken(cfg.LocalToken, child.Token, time.Now())
	if err != nil || claims.Execution != nil || posts.Load() != 0 {
		t.Fatal("old capability shape changed", err)
	}
	directory, _ := os.UserConfigDir()
	if _, err := os.Stat(filepath.Join(directory, "layercache", "client-identity")); !os.IsNotExist(err) {
		t.Fatal("old daemon caused a client identity write")
	}
}

func TestStandaloneExecutionObservationDeduplicatesAcrossProjectPorts(t *testing.T) {
	root := executionObservationHome(t)
	t.Setenv("GITHUB_REF", "")
	t.Setenv("LAYERCACHE_EXECUTION_CHILD", "1")
	installation := ""
	for _, name := range []string{"first", "second"} {
		workspace := filepath.Join(root, name+"-workspace")
		initCLIRepository(t, workspace, "feature/"+name, name)
		t.Chdir(workspace)
		cfg, err := config.Defaults()
		if err != nil {
			t.Fatal(err)
		}
		cfg.ProjectID, cfg.ActionsRepository, cfg.ProjectRoot = "github.com/acme/widgets", "acme/widgets", workspace
		cfg.DataDir = filepath.Join(root, name+"-cache")
		runtime, err := server.New(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		var githubCalls atomic.Int64
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/auth/github/exchange" {
				githubCalls.Add(1)
			}
			runtime.Handler().ServeHTTP(w, r)
		}))
		defer endpoint.Close()
		cfg.Listen = strings.TrimPrefix(endpoint.URL, "http://")
		path := filepath.Join(root, name+"-client.json")
		if err := config.Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		output, diagnostics, err := callCLI("run", "--config", path, "--", os.Args[0], "-test.run=^TestExecutionObservationNativeChild$")
		if err != nil {
			t.Fatalf("standalone run failed: %v %s", err, diagnostics)
		}
		var child struct {
			NativeCode int
			Token      string
		}
		if err := json.Unmarshal([]byte(output), &child); err != nil || child.NativeCode != 404 {
			t.Fatal("standalone native miss did not execute", err)
		}
		claims, err := access.ParseCapabilityToken(cfg.LocalToken, child.Token, time.Now())
		if err != nil || claims.Execution == nil || !strings.HasPrefix(claims.Execution.OwnerID, "local:") {
			t.Fatal("standalone signed run context unavailable", err)
		}
		if githubCalls.Load() != 0 {
			t.Fatal("standalone run required GitHub")
		}
		now := time.Now().UTC()
		inventory := readDashboardExecutions(context.Background(), newLocalCLIHTTPClient(time.Second), endpoint.URL, cfg.LocalToken, cfg.ProjectID, now.Add(-24*time.Hour), now)
		if inventory == nil || len(inventory.Installations) != 1 || inventory.Installations[0].Phase != "finish" {
			t.Fatal("standalone inventory was not available after completed run")
		}
		entry := inventory.Installations[0]
		if claims.Execution.OwnerID != "local:"+entry.InstallationID {
			t.Fatal("local owner was not bound to the reporting installation")
		}
		if installation == "" {
			installation = entry.InstallationID
		} else if installation != entry.InstallationID {
			t.Fatal("project ports created distinct local installations")
		}
		if len(entry.Sample.Filesystems) != 1 || !reflect.DeepEqual(entry.Sample.Filesystems[0].Roles, []string{"localCache", "workspace"}) {
			t.Fatal("local filesystem aliases were counted separately")
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("standalone observation modified project configuration")
		}
	}
	cloud, err := executioncollect.LoadIdentity(context.Background(), "https://cloud.example")
	if err != nil || cloud.InstallationID() == installation {
		t.Fatal("standalone and cloud identity namespaces were linked", err)
	}
}

func TestExecutionObservationNativeChild(t *testing.T) {
	if os.Getenv("LAYERCACHE_EXECUTION_CHILD") != "1" {
		return
	}
	request, err := http.NewRequest(http.MethodGet, os.Getenv("TURBO_API")+"/v8/artifacts/execution-observation-fixture?teamId="+url.QueryEscape(os.Getenv("TURBO_TEAM")), nil)
	if err != nil {
		t.Fatal(err)
	}
	token := os.Getenv("TURBO_TOKEN")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := newCLIHTTPClient(time.Second).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"NativeCode": response.StatusCode, "Token": token}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestDashboardExecutionUnionKeepsNewestWholeSampleAndUniqueJobs(t *testing.T) {
	now := time.Now().UTC()
	old := execution.Entry{ID: "same", LastObservedAt: now.Add(-time.Minute), CacheActivity: "observed", Sample: execution.Sample{OS: "linux"}}
	newer := execution.Entry{ID: "same", LastObservedAt: now, CacheActivity: "unconfirmed", Sample: execution.Sample{OS: "darwin"}}
	left := &execution.Inventory{Version: 1, From: now.Add(-time.Hour), To: now, UpdatedAt: now, RetentionDays: 30, StaleAfterSeconds: 86400, Installations: []execution.Entry{old}, Jobs: []execution.Entry{{ID: "job"}}, JobCount: 1}
	right := &execution.Inventory{Version: 1, From: now.Add(-2 * time.Hour), To: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute), RetentionDays: 30, StaleAfterSeconds: 86400, Installations: []execution.Entry{newer}, Jobs: []execution.Entry{{ID: "job"}}, JobCount: 3, ListTruncated: true}
	result := mergeDashboardExecutions(left, right)
	if len(result.Installations) != 1 || result.Installations[0].Sample.OS != "darwin" || result.Installations[0].CacheActivity != "observed" || result.JobCount != 1 || !result.ListTruncated || result.From != right.From || result.To != right.To {
		t.Fatal("inventory union summed or replaced observed facts incorrectly")
	}
	if mergeDashboardExecutions(nil, nil) != nil || mergeDashboardExecutions(left, nil) != left {
		t.Fatal("inventory unavailability was fabricated")
	}
	if left.Installations[0].Sample.OS != "linux" {
		t.Fatal("merging mutated the source snapshot")
	}
}

func TestOptionalObservationReturnsWhileResourceProbeIsBlocked(t *testing.T) {
	executionObservationHome(t)
	var posts atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1); w.WriteHeader(201) }))
	defer endpoint.Close()
	identity, err := executioncollect.LoadIdentity(context.Background(), endpoint.URL)
	if err != nil {
		t.Fatal(err)
	}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	observation := &clientExecutionObservation{endpoint: endpoint.URL + execution.Path, token: "fixture", identity: identity, client: newCLIHTTPClient(time.Second), collect: func([]executioncollect.Root, func(string) string) execution.Sample {
		close(started)
		<-release // Models a stalled stat/statfs call that cannot observe context.
		close(finished)
		return execution.Sample{}
	}}
	commandErr := exec.Command("/bin/sh", "-c", "exit 29").Run()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	begin := time.Now()
	observation.report(ctx, "finish")
	if time.Since(begin) > 500*time.Millisecond || ExitCode(commandErr) != 29 {
		t.Fatal("optional resource probe held or replaced the command result")
	}
	select {
	case <-started:
	default:
		t.Fatal("blocking resource probe did not actually start")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("fixture resource probe did not finish after release")
	}
	if posts.Load() != 0 {
		t.Fatal("late canceled resource collection sent data")
	}
}

func TestOptionalIdentityWorkCanFinishLateWithoutWriting(t *testing.T) {
	release, finished := make(chan struct{}), make(chan struct{})
	var writes atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	begin := time.Now()
	value := optionalObservation(ctx, func(worker context.Context) int {
		<-release
		if worker.Err() == nil {
			writes.Add(1)
		}
		close(finished)
		return 42
	})
	if value != 0 || time.Since(begin) > 500*time.Millisecond {
		t.Fatal("optional identity work did not time out independently")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("fixture identity work did not finish after release")
	}
	if writes.Load() != 0 {
		t.Fatal("late canceled identity work attempted a write")
	}
}
