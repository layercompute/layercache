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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/measurement"
	"github.com/layercache/layercache/internal/portal"
)

func TestRemoteOnlyDoctorUsesHostedReportWithoutLocalRuntime(t *testing.T) {
	for _, test := range []struct {
		name                         string
		available, degraded, healthy bool
		reportBody                   string
	}{
		{name: "healthy hosted report", available: true, healthy: true},
		{name: "degraded hosted report", available: true, degraded: true},
		{name: "unavailable hosted report"},
		{name: "empty hosted report", available: true, reportBody: `{}`},
		{name: "missing degradation observation", available: true, reportBody: `{"schemaVersion":"2"}`},
		{name: "null degradation observation", available: true, reportBody: `{"schemaVersion":"2","degraded":null}`},
		{name: "negative observed runs", available: true, reportBody: `{"schemaVersion":"2","degraded":false,"runs":-1}`},
		{name: "unsupported hosted report", available: true, reportBody: `{"schemaVersion":"future","degraded":false}`},
		{name: "trailing hosted report", available: true, reportBody: `{"schemaVersion":"2","degraded":false} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := config.Defaults()
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			cfg.RemoteOnly = true
			cfg.DataDir = filepath.Join(root, "absent")
			cfg.ProjectID = "github.com/acme/widgets"
			cfg.TeamToken = "fixture-reader"
			cfg.TeamTokenExpiresAt = time.Now().Add(time.Hour)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-reader" {
					w.WriteHeader(401)
					return
				}
				switch r.URL.Path {
				case "/v1/status":
					_ = json.NewEncoder(w).Encode(map[string]any{"projectId": cfg.ProjectID, "usageBytes": 42, "maxBytes": 4096, "role": "team"})
				case "/v1/reports":
					if !test.available {
						w.WriteHeader(503)
						return
					}
					if test.reportBody != "" {
						_, _ = w.Write([]byte(test.reportBody))
						return
					}
					_ = json.NewEncoder(w).Encode(measurement.PeriodReport{SchemaVersion: measurement.SchemaVersion, Degraded: test.degraded})
				default:
					t.Errorf("unexpected hosted request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer endpoint.Close()
			cfg.TeamURL = endpoint.URL
			path := filepath.Join(root, "client.json")
			if err = config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, err := callCLI("doctor", "--config", path, "--json")
			if err != nil {
				t.Fatalf("doctor failed: %v %s", err, stderr)
			}
			var result struct {
				Healthy bool                       `json:"healthy"`
				Status  operationalStatus          `json:"status"`
				Checks  map[string]diagnosticCheck `json:"checks"`
			}
			if err = json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatal(err)
			}
			if result.Healthy != test.healthy || result.Checks["degraded"].OK != test.healthy || result.Status.Running {
				t.Fatalf("doctor classification healthy=%v degradedCheck=%+v running=%v", result.Healthy, result.Checks["degraded"], result.Status.Running)
			}
			if _, err = os.Stat(cfg.DataDir); !os.IsNotExist(err) {
				t.Fatal("doctor initialized Local storage")
			}
		})
	}
}

func TestRemoteOnlyUninstallRemovesOnlyProtectedClientConfiguration(t *testing.T) {
	for _, mode := range []string{"--preserve-cache", "--delete-cache"} {
		for _, hint := range []string{"absent", "foreign directory", "symlink", "regular file"} {
			t.Run(mode+"/"+hint, func(t *testing.T) {
				root := t.TempDir()
				cfg, err := config.Defaults()
				if err != nil {
					t.Fatal(err)
				}
				cfg.RemoteOnly = true
				cfg.TeamURL = "https://cache.example"
				cfg.DataDir = filepath.Join(root, "unused-hint")
				preserved := filepath.Join(root, "foreign-data")
				if err = os.Mkdir(preserved, 0700); err != nil {
					t.Fatal(err)
				}
				sentinel := filepath.Join(preserved, "keep.txt")
				if err = os.WriteFile(sentinel, []byte("unowned bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				switch hint {
				case "foreign directory":
					cfg.DataDir = preserved
				case "symlink":
					if err = os.Symlink(preserved, cfg.DataDir); err != nil {
						t.Fatal(err)
					}
				case "regular file":
					if err = os.WriteFile(cfg.DataDir, []byte("path hint is not owned"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(root, "client.json")
				if err = config.Save(path, cfg); err != nil {
					t.Fatal(err)
				}
				unrelated := filepath.Join(root, "other-client.json")
				if err = os.WriteFile(unrelated, []byte("unrelated config"), 0600); err != nil {
					t.Fatal(err)
				}
				args := []string{"uninstall", "--config", path, mode, "--json"}
				if mode == "--delete-cache" {
					args = append(args, "--yes")
				}
				stdout, stderr, err := callCLI(args...)
				if err != nil {
					t.Fatalf("cloud uninstall: %v %s", err, stderr)
				}
				var receipt struct{ Uninstalled, RemoteOnly, CachePreserved, LocalDataTouched bool }
				if err = json.Unmarshal([]byte(stdout), &receipt); err != nil {
					t.Fatal(err)
				}
				if !receipt.Uninstalled || !receipt.RemoteOnly || !receipt.CachePreserved || receipt.LocalDataTouched {
					t.Fatal("uninstall did not report preserved data")
				}
				if _, err = os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Fatal("client config remains")
				}
				assertFileContents(t, sentinel, []byte("unowned bytes"))
				assertFileContents(t, unrelated, []byte("unrelated config"))
				switch hint {
				case "absent":
					if _, err = os.Lstat(cfg.DataDir); !os.IsNotExist(err) {
						t.Fatal("uninstall initialized the unused data path")
					}
				case "symlink":
					if target, err := os.Readlink(cfg.DataDir); err != nil || target != preserved {
						t.Fatal("uninstall changed unowned symlink")
					}
				case "regular file":
					assertFileContents(t, cfg.DataDir, []byte("path hint is not owned"))
				}
			})
		}
	}
}

func TestRemoteOnlyUninstallOwnsCredentialAndPreservesConfigurationOnFailure(t *testing.T) {
	for _, test := range []struct {
		name             string
		foreign, failing bool
	}{
		{name: "owned credential"}, {name: "foreign credential", foreign: true}, {name: "credential deletion failed", failing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			cfg, err := config.Defaults()
			if err != nil {
				t.Fatal(err)
			}
			cfg.RemoteOnly = true
			cfg.TeamURL = "https://cache.example"
			cfg.DataDir = filepath.Join(root, "unused")
			cfg.GitHubCredentialAccount, err = newGitHubCredentialAccount(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if test.foreign {
				cfg.GitHubCredentialAccount = "github-foreign-account"
			}
			path := filepath.Join(root, "client.json")
			if err = config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			var calls []string
			unlock, err := lockConfiguration(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			removed, err := uninstallRemoteOnlyClient(context.Background(), path, cfg, func(_ context.Context, account string) error {
				calls = append(calls, account)
				if test.failing {
					return errors.New("fixture credential manager unavailable")
				}
				return nil
			})
			if test.foreign || test.failing {
				if err == nil || removed {
					t.Fatal("unsafe/failed credential removal succeeded")
				}
				assertFileContents(t, path, before)
			} else {
				if err != nil || !removed || len(calls) != 1 || calls[0] != cfg.GitHubCredentialAccount {
					t.Fatalf("owned credential removal failed: %v", err)
				}
				if _, err = os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("disconnected client config remains")
				}
			}
			if backups, globErr := filepath.Glob(filepath.Join(root, ".layercache-disconnect-*.json")); globErr != nil || len(backups) != 0 {
				t.Fatal("completed disconnect/restoration retained unnecessary recovery copy")
			}
			if test.foreign && len(calls) != 0 {
				t.Fatal("attempted to delete a foreign credential")
			}
			if _, err = os.Stat(cfg.DataDir); !os.IsNotExist(err) {
				t.Fatal("credential cleanup created unused data storage")
			}
		})
	}
}

func TestRemoteOnlyUninstallRejectsUnprotectedConfiguration(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			cfg, err := config.Defaults()
			if err != nil {
				t.Fatal(err)
			}
			cfg.RemoteOnly = true
			cfg.TeamURL = "https://cache.example"
			cfg.DataDir = filepath.Join(root, "unused")
			path := filepath.Join(root, "client.json")
			if err = config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			target := filepath.Join(root, "original.json")
			switch kind {
			case "symlink":
				if err = os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err = os.Link(path, target); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err = os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err = callCLI("uninstall", "--config", path, "--delete-cache", "--yes"); err == nil {
				t.Fatal("uninstall accepted unsafe client config")
			}
			assertFileContents(t, path, before)
			if kind != "permissions" {
				assertFileContents(t, target, before)
			}
		})
	}
}

func TestRemoteOnlyConnectPreservesMixedCaseImportedRepositoryIdentity(t *testing.T) {
	root := t.TempDir()
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf fixture-secret"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	const projectID = "github.com/Acme/Widgets"
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/projects":
			_ = json.NewEncoder(w).Encode(cloudProjects{Teams: []portal.Team{{ID: "team"}}, Projects: []portal.Project{{ID: projectID, TeamID: "team", Name: "Widgets", Repository: "acme/widgets", DefaultRef: "refs/heads/main"}}})
		case "/v1/auth/github/exchange":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["project"] != projectID || body["repository"] != "acme/widgets" {
				t.Error("imported identity changed during exchange")
			}
			_ = json.NewEncoder(w).Encode(capabilityExchange{Token: "fixture-capability", ExpiresAt: time.Now().Add(time.Hour)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()
	stdout, stderr, err := callCLI("connect", "--cloud", service.URL, "--github-cli", "--remote-only", "--json")
	if err != nil {
		t.Fatalf("mixed-case import: %v %s", err, stderr)
	}
	var connected struct{ Config string }
	if err = json.Unmarshal([]byte(stdout), &connected); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(connected.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != projectID || len(filepath.Base(connected.Config)) != 37 || strings.Contains(filepath.Base(connected.Config), "Acme") {
		t.Fatal("mixed-case legacy ID was not preserved safely")
	}
	clone := filepath.Join(root, "clone")
	initCLIRepository(t, clone, "main", "mixed-case import")
	t.Chdir(clone)
	if identity, scopeErr := discoverConfiguredProject(context.Background(), cfg, "", false); scopeErr != nil || identity.Project != "github.com/acme/widgets" || cfg.ProjectID != projectID {
		t.Fatalf("mixed-case repository binding failed: %v", scopeErr)
	}
	runCLIGit(t, clone, "remote", "set-url", "origin", "https://github.com/other/repository.git")
	if _, scopeErr := discoverConfiguredProject(context.Background(), cfg, "", false); scopeErr == nil {
		t.Fatal("case-insensitive binding accepted a different checkout")
	}
	if validCloudProjectID(projectID, "other/repository") {
		t.Fatal("case-insensitive binding accepted a different repository")
	}
}

func TestRemoteOnlySetupRejectsOwnedAndPartialLocalIntegrations(t *testing.T) {
	for _, integration := range []string{"turbo", "local-ci", "buildkit", "partial"} {
		t.Run(integration, func(t *testing.T) {
			path, _ := setupConfigWithAddress(t, "127.0.0.1:1", "local-owner")
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			var target string
			switch integration {
			case "turbo":
				root := t.TempDir()
				runCLI(t, "integration", "turbo", "--config", path, "--root", root, "--apply")
				target = filepath.Join(root, ".turbo", "config.json")
			case "local-ci":
				runCLI(t, "integration", "local-ci", "--config", path, "--apply")
				target = filepath.Join(cfg.DataDir, "integrations", "local-ci.env")
			default:
				target = filepath.Join(cfg.DataDir, "native-owned-config")
				if err = os.WriteFile(target, []byte("owned config"), 0600); err != nil {
					t.Fatal(err)
				}
				state, err := loadIntegrationState(cfg)
				if err != nil {
					t.Fatal(err)
				}
				state.Records[integration] = integrationRecord{Name: integration, State: "active", Path: target, OwnershipCaptured: true}
				if integration == "partial" {
					state.Records[integration] = integrationRecord{Name: integration, State: "applying", Path: target}
				}
				if err = saveIntegrationState(cfg, state); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			targetBefore, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = callCLI("setup", "--config", path, "--remote-only", "--team-url", "https://cache.example", "--non-interactive")
			if err == nil || !strings.Contains(err.Error(), "owns Local integrations") || !strings.Contains(err.Error(), integration) {
				t.Fatalf("owned transition was accepted: %v", err)
			}
			assertFileContents(t, path, before)
			assertFileContents(t, target, targetBefore)
		})
	}
}

func TestRemoteOnlyTransitionSerializesIntegrationOwnershipDiscovery(t *testing.T) {
	path, _ := setupConfigWithAddress(t, "127.0.0.1:1", "local-owner")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	unlock, err := lockIntegrationState(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var release sync.Once
	defer release.Do(unlock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var out bytes.Buffer
		done <- runSetup(ctx, []string{"--config", path, "--remote-only", "--team-url", "https://cache.example", "--non-interactive"}, &out, &out)
	}()
	// The transition must hold the configuration lock while it waits for the
	// integration application to finish, then inspect the newly written state.
	deadline := time.Now().Add(time.Second)
	locked := false
	for time.Now().Before(deadline) {
		probe, probeCancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
		releaseConfig, probeErr := lockConfiguration(probe, path)
		probeCancel()
		if probeErr != nil {
			locked = true
			break
		}
		releaseConfig()
	}
	if !locked {
		t.Fatal("setup did not hold config lock while waiting for integration ownership")
	}
	state, err := loadIntegrationState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state.Records["turbo"] = integrationRecord{Name: "turbo", State: "active", OwnershipCaptured: true}
	if err = saveIntegrationState(cfg, state); err != nil {
		t.Fatal(err)
	}
	release.Do(unlock)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "owns Local integrations") {
			t.Fatalf("transition bypassed concurrent integration ownership: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("setup did not finish after integration unlock")
	}
	assertFileContents(t, path, before)
}

func TestRemoteOnlyUninstallKeepsCredentialWhenConfigurationRemovalFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permission denial requires an unprivileged process")
	}
	root := t.TempDir()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RemoteOnly = true
	cfg.TeamURL = "https://cache.example"
	cfg.DataDir = filepath.Join(root, "unused")
	cfg.GitHubCredentialAccount, err = newGitHubCredentialAccount(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "client.json")
	if err = config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	unlock, err := lockConfiguration(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(root, 0700) }()
	credentialCalls := 0
	remove := func(context.Context, string) error { credentialCalls++; return nil }
	removed, err := uninstallRemoteOnlyClient(context.Background(), path, cfg, remove)
	if err == nil || removed || credentialCalls != 0 {
		t.Fatalf("configuration removal failure touched credential: removed=%v calls=%d error=%v", removed, credentialCalls, err)
	}
	assertFileContents(t, path, before)
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	removed, err = uninstallRemoteOnlyClient(context.Background(), path, cfg, remove)
	if err != nil || !removed || credentialCalls != 1 {
		t.Fatalf("uninstall retry failed: removed=%v calls=%d error=%v", removed, credentialCalls, err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("successful retry left installed client")
	}
}

func TestRemoteOnlyUninstallCanRetryUnknownCredentialDeleteCommit(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RemoteOnly = true
	cfg.TeamURL = "https://cache.example"
	cfg.DataDir = filepath.Join(root, "unused")
	cfg.GitHubCredentialAccount, err = newGitHubCredentialAccount(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "client.json")
	if err = config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	unlock, err := lockConfiguration(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	present := true
	attempts := 0
	deleteCredential := func(_ context.Context, account string) error {
		attempts++
		if account != cfg.GitHubCredentialAccount {
			t.Fatal("unexpected credential account")
		}
		if present {
			present = false
			return errors.New("credential deletion committed; acknowledgement lost")
		}
		// Store.Delete normalizes native missing-item responses to success.
		return nil
	}
	if removed, err := uninstallRemoteOnlyClient(context.Background(), path, cfg, deleteCredential); err == nil || removed || present {
		t.Fatal("unknown credential commit was not reported")
	}
	assertFileContents(t, path, before)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("restored client config lost protected permissions")
	}
	if removed, err := uninstallRemoteOnlyClient(context.Background(), path, cfg, deleteCredential); err != nil || !removed || attempts != 2 {
		t.Fatalf("missing credential blocked retry: %v", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("completed retry left installed client")
	}
}

func TestRemoteOnlyUninstallPreservesDurableRecoveryWhenRestorationFails(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RemoteOnly = true
	cfg.TeamURL = "https://cache.example"
	cfg.DataDir = filepath.Join(root, "unused")
	cfg.GitHubCredentialAccount, err = newGitHubCredentialAccount(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "client.json")
	if err = config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockConfiguration(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	calls := 0
	removed, err := uninstallRemoteOnlyClient(context.Background(), path, cfg, func(_ context.Context, account string) error {
		calls++
		if account != cfg.GitHubCredentialAccount {
			t.Fatal("wrong credential account")
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatal("credential deletion preceded configuration removal")
		}
		// Simulate another process claiming the old pathname while the native
		// credential manager reports failure. Restoration must not overwrite it.
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "foreign.txt"), []byte("keep foreign replacement"), 0600); err != nil {
			t.Fatal(err)
		}
		return errors.New("fixture credential manager unavailable")
	})
	if err == nil || removed || calls != 1 {
		t.Fatalf("restoration failure not reported: removed=%v calls=%d error=%v", removed, calls, err)
	}
	backups, globErr := filepath.Glob(filepath.Join(root, ".layercache-disconnect-*.json"))
	if globErr != nil || len(backups) != 1 {
		t.Fatal("durable recovery copy was not retained")
	}
	backup := backups[0]
	if !filepath.IsAbs(backup) || !strings.Contains(err.Error(), backup) {
		t.Fatal("restoration failure omitted exact recovery path")
	}
	data, readErr := readProtectedFile(backup)
	if readErr != nil || !bytes.Equal(data, before) {
		t.Fatal("recovery did not preserve exact protected connection bytes")
	}
	if info, statErr := os.Stat(backup); statErr != nil || info.Mode().Perm() != 0600 {
		t.Fatal("recovery configuration is not protected")
	}
	if recovered, loadErr := config.Load(backup); loadErr != nil || recovered.TeamURL != cfg.TeamURL || recovered.ProjectID != cfg.ProjectID || recovered.GitHubCredentialAccount != cfg.GitHubCredentialAccount {
		t.Fatal("recovery copy cannot restore original cloud connection")
	}
	assertFileContents(t, filepath.Join(path, "foreign.txt"), []byte("keep foreign replacement"))
	if _, statErr := os.Stat(cfg.DataDir); !os.IsNotExist(statErr) {
		t.Fatal("recovery touched the unused data directory")
	}
}

func TestRemoteOnlyCompletedDisconnectReportsPendingRecoveryCleanupAsSuccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permission denial requires an unprivileged process")
	}
	root := t.TempDir()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RemoteOnly = true
	cfg.TeamURL = "https://cache.example"
	cfg.DataDir = filepath.Join(root, "unused")
	cfg.GitHubCredentialAccount, err = newGitHubCredentialAccount(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "client.json")
	if err = config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	unlock, err := lockConfiguration(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	defer func() { _ = os.Chmod(root, 0700) }()
	credentialRemoved, removeErr := uninstallRemoteOnlyClient(context.Background(), path, cfg, func(context.Context, string) error {
		// Credential removal completed, then permissions changed before the
		// remaining recovery copy could be unlinked.
		if err := os.Chmod(root, 0500); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if !credentialRemoved {
		t.Fatal("credential deletion was not completed")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("completed disconnect left installed configuration")
	}
	var stdout, stderr bytes.Buffer
	if err = reportRemoteOnlyUninstall(&stdout, &stderr, true, credentialRemoved, removeErr); err != nil {
		t.Fatalf("completed disconnect reported false failure: %v", err)
	}
	var receipt struct {
		Uninstalled, CredentialRemoved bool
		PendingRecoveryCleanupPath     string
	}
	if err = json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Uninstalled || !receipt.CredentialRemoved || receipt.PendingRecoveryCleanupPath == "" || !strings.Contains(stderr.String(), receipt.PendingRecoveryCleanupPath) {
		t.Fatal("completed disconnect omitted pending cleanup receipt/warning")
	}
	data, readErr := readProtectedFile(receipt.PendingRecoveryCleanupPath)
	if readErr != nil || !bytes.Equal(data, before) {
		t.Fatal("pending cleanup path does not retain protected original connection bytes")
	}
	// A credential failure joined with cleanup failure must still fail.
	var ignored bytes.Buffer
	if err = reportRemoteOnlyUninstall(&ignored, &ignored, true, false, errors.Join(errors.New("credential removal failed"), removeErr)); err == nil {
		t.Fatal("credential failure was hidden by cleanup warning")
	}
}
