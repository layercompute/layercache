package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

const cliTestRepository = "https://github.com/acme/widgets.git"

func TestDiscoverConfiguredProjectRepositoryBinding(t *testing.T) {
	t.Setenv("GITHUB_REF", "")
	clone := filepath.Join(t.TempDir(), "clone")
	commit := initCLIRepository(t, clone, "feature/current", "repository binding")
	for _, test := range []struct {
		name, project, repository, origin string
		valid                             bool
	}{
		{name: "transferred repository", project: "github.com/old-owner/old-name", repository: "acme/widgets", origin: cliTestRepository, valid: true},
		{name: "transferred repository mixed case", project: "github.com/old-owner/old-name", repository: "Acme/Widgets", origin: cliTestRepository, valid: true},
		{name: "transferred repository SSH", project: "github.com/old-owner/old-name", repository: "acme/widgets", origin: "git@github.com:acme/widgets.git", valid: true},
		{name: "opaque project", project: "project-widgets", repository: "acme/widgets", origin: cliTestRepository, valid: true},
		{name: "matching project without binding", project: "github.com/acme/widgets", origin: cliTestRepository, valid: true},
		{name: "transferred repository without binding", project: "github.com/old-owner/old-name", origin: cliTestRepository},
		{name: "opaque project without binding", project: "project-widgets", origin: cliTestRepository},
		{name: "unrelated repository", project: "github.com/old-owner/old-name", repository: "acme/other", origin: cliTestRepository},
		{name: "matching project cannot override repository binding", project: "github.com/acme/widgets", repository: "acme/other", origin: cliTestRepository},
		{name: "local project with repository binding", project: "local-123456789012", repository: "acme/widgets", origin: cliTestRepository},
		{name: "foreign HTTPS origin", project: "github.com/old-owner/old-name", repository: "acme/widgets", origin: "https://example.com/acme/widgets.git"},
		{name: "insecure HTTP origin", project: "github.com/old-owner/old-name", repository: "acme/widgets", origin: "http://github.com/acme/widgets.git"},
		{name: "file origin", project: "github.com/old-owner/old-name", repository: "acme/widgets", origin: "file:///acme/widgets.git"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runCLIGit(t, clone, "remote", "set-url", "origin", test.origin)
			cfg := config.Config{ProjectID: test.project, ActionsRepository: test.repository, ProjectRoot: clone}
			for _, allowOutside := range []bool{false, true} {
				identity, err := discoverConfiguredProject(context.Background(), cfg, clone, allowOutside)
				if (err == nil) != test.valid {
					t.Fatalf("allowOutsideRepository=%v: identity=%+v error=%v, want valid=%v", allowOutside, identity, err, test.valid)
				}
				if err == nil && (identity.Project != "github.com/acme/widgets" || identity.ActionsRepository != "acme/widgets" || identity.Commit != commit || identity.Ref != "refs/heads/feature/current") {
					t.Fatalf("current checkout identity was not preserved: %+v", identity)
				}
			}
		})
	}
}

func TestDiscoverConfiguredProjectPreservesLocalIdentity(t *testing.T) {
	t.Setenv("GITHUB_REF", "")
	clone := filepath.Join(t.TempDir(), "clone")
	initCLIRepository(t, clone, "main", "local identity")
	runCLIGit(t, clone, "remote", "remove", "origin")
	const localProject = "local-123456789012"
	runCLIGit(t, clone, "config", "--local", "layercache.project-id", localProject)
	for _, project := range []string{localProject, "local-987654321098", "github.com/old-owner/old-name"} {
		cfg := config.Config{ProjectID: project, ActionsRepository: "acme/widgets", ProjectRoot: clone}
		identity, err := discoverConfiguredProject(context.Background(), cfg, clone, true)
		if (err == nil) != (project == localProject) {
			t.Fatalf("project=%q: identity=%+v error=%v", project, identity, err)
		}
		if err == nil && (identity.Project != localProject || identity.ActionsRepository != "") {
			t.Fatalf("local identity was not preserved: %+v", identity)
		}
	}
}

func TestRemoteOnlyConnectRunPreservesTransferredProjectIdentity(t *testing.T) {
	t.Setenv("GITHUB_REF", "")
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	clone := filepath.Join(root, "clone")
	commit := initCLIRepository(t, clone, "feature/transferred", "transferred repository")
	t.Chdir(clone)
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf github-fixture-secret"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer github-fixture-secret" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "developer", "id": 12345})
	}))
	defer github.Close()
	backend, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	backend.Role, backend.ProjectID, backend.ActionsRepository = "team", "github.com/old-owner/old-name", "acme/widgets"
	backend.DataDir, backend.MaxBytes = filepath.Join(root, "backend"), 1<<20
	backend.ActionsRef, backend.ActionsDefaultRef = "refs/heads/main", "refs/heads/main"
	backend.GitHubAPIURL, backend.TeamMembers = github.URL, map[string]string{"developer": "writer"}
	runtime, err := server.New(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var exchanges atomic.Int64
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/cli/projects" {
			if r.Header.Get("Authorization") != "Bearer github-fixture-secret" {
				http.Error(w, "denied", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(cloudProjects{Teams: []portal.Team{{ID: "team"}}, Projects: []portal.Project{{ID: backend.ProjectID, TeamID: "team", Name: "Widgets", Repository: backend.ActionsRepository, DefaultRef: backend.ActionsDefaultRef}}})
			return
		}
		if r.URL.Path == "/v1/auth/github/exchange" {
			exchanges.Add(1)
		}
		runtime.Handler().ServeHTTP(w, r)
	}))
	defer service.Close()
	output, diagnostics, err := callCLI("connect", "--cloud", service.URL, "--project", backend.ProjectID, "--github-cli", "--remote-only", "--json")
	if err != nil {
		t.Fatalf("transferred project connect failed: %v %s", err, diagnostics)
	}
	var connected struct{ Config string }
	if err = json.Unmarshal([]byte(output), &connected); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(connected.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != backend.ProjectID || cfg.ActionsRepository != backend.ActionsRepository {
		t.Fatal("connect changed the logical project or repository binding")
	}
	before, err := os.ReadFile(connected.Config)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAYERCACHE_GRADLE_CHILD", "1")
	output, diagnostics, err = callCLI("run", "--config", connected.Config, "--", os.Args[0], "-test.run=^TestGradleConnectionChild$")
	if err != nil {
		t.Fatalf("transferred project run failed: %v %s", err, diagnostics)
	}
	var observed map[string]string
	if err = json.Unmarshal([]byte(output), &observed); err != nil {
		t.Fatal(err)
	}
	claims, err := access.ParseCapabilityToken(backend.LocalToken, observed["token"], time.Now())
	if err != nil {
		t.Fatal("run did not receive hosted authority", err)
	}
	if observed["url"] != service.URL+"/v1/gradle-cache/" || claims.Project != backend.ProjectID || claims.Repository != backend.ActionsRepository || claims.SourceCommit != commit || claims.Ref != "refs/heads/feature/transferred" || claims.Integration != "gradle" || exchanges.Load() != 2 {
		t.Fatalf("run lost transferred project authority: %+v, exchanges=%d", claims, exchanges.Load())
	}
	for _, origin := range []string{"https://github.com/other/widgets.git", "https://github.com/old-owner/old-name.git"} {
		runCLIGit(t, clone, "remote", "set-url", "origin", origin)
		output, diagnostics, err = callCLI("run", "--config", connected.Config, "--", os.Args[0], "-test.run=^TestGradleConnectionChild$")
		if err != nil {
			t.Fatalf("scope bypass failed: %v %s", err, diagnostics)
		}
		if err = json.Unmarshal([]byte(output), &observed); err != nil {
			t.Fatal(err)
		}
		if observed["url"] != "" || observed["token"] != "" || exchanges.Load() != 2 || !strings.Contains(diagnostics, "does not match configured project") {
			t.Fatal("unrelated checkout received hosted authority")
		}
	}
	after, err := os.ReadFile(connected.Config)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("run changed the saved project identity", err)
	}
	if _, err = os.Stat(cfg.DataDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("cloud-only connection created Local artifacts", err)
	}
}

func TestIntegrationsRejectChangedRepositoryOrigin(t *testing.T) {
	t.Setenv("GITHUB_REF", "")
	clone := filepath.Join(t.TempDir(), "clone")
	initCLIRepository(t, clone, "main", "setup")
	t.Chdir(clone)
	configPath, _ := setupLocalConfig(t)
	// An existing unexpired handoff must not skip current repository validation.
	runCLI(t, "integration", "local-ci", "--config", configPath, "--apply", "--json")
	runCLIGit(t, clone, "remote", "set-url", "origin", "https://github.com/fork/widgets.git")

	for _, integration := range []string{"turbo", "local-ci"} {
		t.Run(integration, func(t *testing.T) {
			_, _, err := callCLI("integration", integration, "--config", configPath, "--apply", "--json")
			if err == nil || !strings.Contains(err.Error(), "does not match configured project") {
				t.Fatalf("changed-origin %s integration error = %v", integration, err)
			}
		})
	}
}

func TestVMRouteUsesCurrentCloneActionsScope(t *testing.T) {
	t.Setenv("GITHUB_REF", "")
	root := canonicalTestTempDir(t)
	setupClone := filepath.Join(root, "setup-clone")
	featureClone := filepath.Join(root, "feature-clone")
	initCLIRepository(t, setupClone, "main", "setup")
	featureCommit := initCLIRepository(t, featureClone, "feature/cache-scope", "feature")

	t.Chdir(setupClone)
	configPath, _ := setupLocalConfig(t)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectRoot != setupClone || cfg.ActionsRef != "refs/heads/main" {
		t.Fatalf("setup source scope = root %q ref %q", cfg.ProjectRoot, cfg.ActionsRef)
	}

	t.Chdir(featureClone)
	stdout, _ := runCLI(t,
		"vm-route", "issue", "--config", configPath,
		"--endpoint", "http://127.0.0.1:7437", "--integration", "actions", "--json",
	)
	var response vmRouteResponse
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("decode VM route: %v\n%s", err, stdout)
	}
	claims, err := access.ParseCapabilityToken(
		cfg.LocalToken, response.Environment["ACTIONS_RUNTIME_TOKEN"], time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Repository != "acme/widgets" || claims.Ref != "refs/heads/feature/cache-scope" ||
		claims.DefaultRef != "refs/heads/main" || claims.SourceCommit != featureCommit {
		t.Fatalf("VM Actions scope = %+v", claims)
	}
	if response.Environment["TURBO_TOKEN"] != "" {
		t.Fatal("Actions-only VM route included a Turbo credential")
	}
}

func TestVMRouteUsesTransferredRepositoryBinding(t *testing.T) {
	t.Setenv("GITHUB_REF", "")
	clone := filepath.Join(t.TempDir(), "clone")
	commit := initCLIRepository(t, clone, "feature/transferred", "transferred VM route")
	t.Chdir(clone)
	configPath, _ := setupLocalConfig(t)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProjectID = "github.com/old-owner/old-name"
	if err = config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	stdout, _ := runCLI(t, "vm-route", "issue", "--config", configPath,
		"--endpoint", "http://127.0.0.1:7437", "--integration", "actions", "--json")
	var response vmRouteResponse
	if err = json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatal(err)
	}
	claims, err := access.ParseCapabilityToken(cfg.LocalToken, response.Environment["ACTIONS_RUNTIME_TOKEN"], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claims.Project != cfg.ProjectID || claims.Repository != "acme/widgets" || claims.Ref != "refs/heads/feature/transferred" || claims.DefaultRef != "refs/heads/main" || claims.SourceCommit != commit {
		t.Fatalf("VM route lost transferred project authority: %+v", claims)
	}
	for _, origin := range []string{"https://github.com/other/widgets.git", "https://github.com/old-owner/old-name.git"} {
		runCLIGit(t, clone, "remote", "set-url", "origin", origin)
		_, _, err = callCLI("vm-route", "issue", "--config", configPath,
			"--endpoint", "http://127.0.0.1:7437", "--integration", "actions", "--json")
		if err == nil || !strings.Contains(err.Error(), "does not match configured project") {
			t.Fatalf("unrelated checkout received VM route authority: %v", err)
		}
	}
}

func TestVMRouteRequiresExplicitImmutableActionsScopeOutsideRepository(t *testing.T) {
	t.Setenv("GITHUB_REF", "")
	root := t.TempDir()
	setupClone := filepath.Join(root, "setup-clone")
	initCLIRepository(t, setupClone, "main", "setup")
	t.Chdir(setupClone)
	configPath, _ := setupLocalConfig(t)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(outside)
	_, _, err = callCLI(
		"vm-route", "issue", "--config", configPath,
		"--endpoint", "http://127.0.0.1:7437", "--integration", "actions", "--json",
	)
	if err == nil || !strings.Contains(err.Error(), "current Git checkout") ||
		!strings.Contains(err.Error(), "--actions-source-commit") {
		t.Fatalf("unscoped Actions VM route error = %v", err)
	}

	commit := strings.Repeat("a", 40)
	stdout, _ := runCLI(t,
		"vm-route", "issue", "--config", configPath,
		"--endpoint", "http://127.0.0.1:7437", "--integration", "actions",
		"--actions-repository", "acme/widgets",
		"--actions-ref", "refs/pull/17/merge",
		"--actions-default-ref", "refs/heads/main",
		"--actions-source-commit", commit,
		"--json",
	)
	var response vmRouteResponse
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatal(err)
	}
	claims, err := access.ParseCapabilityToken(
		cfg.LocalToken, response.Environment["ACTIONS_RUNTIME_TOKEN"], time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Repository != "acme/widgets" || claims.Ref != "refs/pull/17/merge" ||
		claims.DefaultRef != "refs/heads/main" || claims.SourceCommit != commit {
		t.Fatalf("explicit VM Actions scope = %+v", claims)
	}

	_, _, err = callCLI(
		"vm-route", "issue", "--config", configPath,
		"--endpoint", "http://127.0.0.1:7437", "--integration", "actions",
		"--actions-repository", "acme/widgets",
		"--json",
	)
	if err == nil || !strings.Contains(err.Error(), "must be provided together") {
		t.Fatalf("partial explicit Actions scope error = %v", err)
	}
	_, _, err = callCLI(
		"vm-route", "issue", "--config", configPath,
		"--endpoint", "http://127.0.0.1:7437", "--integration", "actions",
		"--actions-repository", "acme/other",
		"--actions-ref", "refs/heads/main",
		"--actions-default-ref", "refs/heads/main",
		"--actions-source-commit", commit,
		"--json",
	)
	if err == nil || !strings.Contains(err.Error(), "does not match configured repository") {
		t.Fatalf("mismatched explicit Actions repository error = %v", err)
	}
}

func TestVMRouteRejectsUnprovenDefaultRef(t *testing.T) {
	t.Setenv("GITHUB_REF", "")
	root := t.TempDir()
	clone := filepath.Join(root, "clone")
	initCLIRepository(t, clone, "main", "fixture")
	t.Chdir(clone)
	configPath, _ := setupLocalConfig(t)
	runCLIGit(t, clone, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")

	_, _, err := callCLI(
		"vm-route", "issue", "--config", configPath,
		"--endpoint", "http://127.0.0.1:7437", "--integration", "actions",
		"--json",
	)
	if err == nil || !strings.Contains(err.Error(), "origin/HEAD") ||
		!strings.Contains(err.Error(), "--actions-source-commit") {
		t.Fatalf("unproven default ref error = %v", err)
	}
}

func TestTurboIntegrationPrefersMatchingCurrentClone(t *testing.T) {
	t.Setenv("GITHUB_REF", "")
	root := canonicalTestTempDir(t)
	setupClone := filepath.Join(root, "setup-clone")
	currentClone := filepath.Join(root, "current-clone")
	initCLIRepository(t, setupClone, "main", "setup")
	initCLIRepository(t, currentClone, "feature/worktree", "current")

	t.Chdir(setupClone)
	configPath, _ := setupLocalConfig(t)
	t.Chdir(currentClone)
	runCLI(t, "integration", "turbo", "--config", configPath, "--apply", "--json")
	setupConfig := filepath.Join(setupClone, ".turbo", "config.json")
	currentConfig := filepath.Join(currentClone, ".turbo", "config.json")
	if _, err := os.Stat(currentConfig); err != nil {
		t.Fatalf("current clone did not receive Turbo integration: %v", err)
	}
	if _, err := os.Stat(setupConfig); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("persisted setup clone was modified: %v", err)
	}

	t.Chdir(setupClone)
	_, _, err := callCLI("integration", "turbo", "--config", configPath, "--apply", "--json")
	if err == nil || !strings.Contains(err.Error(), currentConfig) ||
		!strings.Contains(err.Error(), setupConfig) || !strings.Contains(err.Error(), "uninstall") {
		t.Fatalf("owned clone retarget error = %v", err)
	}
	runCLI(t, "uninstall", "--config", configPath, "--preserve-cache", "--json")
	if _, err := os.Stat(currentConfig); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("uninstall left current clone integration: %v", err)
	}
}

func initCLIRepository(t *testing.T, root, branch, contents string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, root, "init", "-b", branch)
	runCLIGit(t, root, "config", "user.email", "qa@layercache.dev")
	runCLIGit(t, root, "config", "user.name", "Layer Cache QA")
	runCLIGit(t, root, "remote", "add", "origin", cliTestRepository)
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte(contents+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, root, "add", "fixture.txt")
	runCLIGit(t, root, "commit", "-m", contents)
	runCLIGit(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return runCLIGit(t, root, "rev-parse", "HEAD")
}

func runCLIGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}
