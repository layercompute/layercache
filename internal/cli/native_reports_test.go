package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/layercache/layercache/internal/measurement"
	"github.com/layercache/layercache/internal/server"
)

func cliNativeFixture() measurement.NativeReport {
	end := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Second)
	zero, one, duration := int64(0), int64(1), int64(100)
	return measurement.NativeReport{
		Version: 1, InvocationID: "run-cli-fixture-000000000000", StartedAt: end.Add(-time.Second), FinishedAt: end,
		Status: "success", Tasks: []measurement.NativeTask{},
		Counters: &measurement.NativeCounters{Compiler: &measurement.NativeCompilerCounters{
			Scope: "clang-pch", Coverage: "complete", Reason: "none", Hits: &one, Misses: &zero,
		}}, TimingMS: &measurement.NativePhaseTiming{Compile: &duration},
	}
}

func writeCLINativeFixture(t *testing.T, path string, report measurement.NativeReport) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNativeImportHostedAndLocalReplayDoNotObserveImporterOrChangeConfig(t *testing.T) {
	for _, remote := range []bool{true, false} {
		t.Run(fmt.Sprintf("remote=%t", remote), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "user-config"))
			t.Setenv("HOME", filepath.Join(root, "home"))
			github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer native-fixture-secret" {
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
			backend.Role = "local"
			if remote {
				backend.Role = "team"
			}
			backend.DataDir = filepath.Join(root, "backend")
			backend.MaxBytes = 1 << 20
			backend.ProjectID = "github.com/acme/widgets"
			backend.CompatibilityID = "native-profile-v1"
			backend.ActionsRepository = "acme/widgets"
			backend.ActionsRef = "refs/heads/main"
			backend.ActionsDefaultRef = "refs/heads/main"
			backend.GitHubAPIURL = github.URL
			backend.TeamMembers = map[string]string{"developer": "reader"}
			runtime, err := server.New(context.Background(), backend)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			var posts, samples atomic.Int64
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/executions") {
					samples.Add(1)
				}
				if r.URL.Path == measurement.NativePath {
					posts.Add(1)
					claims, claimErr := access.ParseCapabilityToken(backend.LocalToken, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), time.Now())
					if claimErr != nil || claims.Integration != "xcode" || !claims.Allows(access.CapabilityRead) || claims.Allows(access.CapabilityWrite) || claims.Allows(access.CapabilityAdmin) {
						t.Error("native report received wrong authority")
					}
				}
				runtime.Handler().ServeHTTP(w, r)
			}))
			defer endpoint.Close()
			cfg := backend
			cfg.Role, cfg.RemoteOnly = "local", remote
			if remote {
				cfg.LocalToken = "must-not-sign-hosted-reports"
				cfg.DataDir = filepath.Join(root, "no-local-store")
				cfg.TeamURL = endpoint.URL
			} else {
				cfg.Listen = strings.TrimPrefix(endpoint.URL, "http://")
			}
			gh := filepath.Join(root, "gh")
			if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf native-fixture-secret"), 0700); err != nil {
				t.Fatal(err)
			}
			cfg.GitHubCLIPath = gh
			path := filepath.Join(root, "client.json")
			if err := config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			input := filepath.Join(root, "native.json")
			report := cliNativeFixture()
			writeCLINativeFixture(t, input, report)
			for _, repeated := range []bool{false, true} {
				output, diagnostics, err := callCLI("report", "native", "--config", path, "--input", input, "--integration", "xcode", "--source-commit", strings.Repeat("a", 40), "--json")
				if err != nil {
					t.Fatalf("native import failed: %v %s", err, diagnostics)
				}
				var receipt measurement.NativeReceipt
				if json.Unmarshal([]byte(output), &receipt) != nil || !receipt.Recorded || receipt.Repeated != repeated || receipt.InvocationID != report.InvocationID {
					t.Fatal("native import returned wrong receipt")
				}
				if strings.Contains(output+diagnostics, "native-fixture-secret") || strings.Contains(output+diagnostics, cfg.LocalToken) {
					t.Fatal("native import disclosed a credential")
				}
			}
			if _, _, err := callCLI("report", "native", "--config", path, "--input", input, "--integration", "xcode", "--source-commit", strings.Repeat("b", 40)); err == nil || !strings.Contains(err.Error(), "409") {
				t.Fatal("same invocation silently accepted a different source")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) || posts.Load() != 3 || samples.Load() != 0 {
				t.Fatal("import changed configuration, retried, or observed importer hardware")
			}
			if remote {
				if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
					t.Fatal("hosted import initialized local artifact storage")
				}
			}
		})
	}
}

func TestNativeImportRejectsUnsafeInputBeforeCredentialsOrNetwork(t *testing.T) {
	root := t.TempDir()
	for _, kind := range []string{"unknown-field", "oversize", "public-mode", "symlink", "stale", "source"} {
		t.Run(kind, func(t *testing.T) {
			input := filepath.Join(root, kind+".json")
			report := cliNativeFixture()
			if kind == "stale" {
				report.StartedAt, report.FinishedAt = report.StartedAt.Add(-48*time.Hour), report.FinishedAt.Add(-48*time.Hour)
			}
			writeCLINativeFixture(t, input, report)
			source := strings.Repeat("a", 40)
			switch kind {
			case "unknown-field":
				data, _ := os.ReadFile(input)
				data = append(data[:len(data)-1], []byte(`,"rawPath":"PRIVATE-CANARY"}`)...)
				if err := os.WriteFile(input, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(input, bytes.Repeat([]byte("x"), measurement.NativeMaximumBytes+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "public-mode":
				if err := os.Chmod(input, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := input + ".link"
				if err := os.Symlink(input, link); err != nil {
					t.Fatal(err)
				}
				input = link
			case "source":
				source = "mutable-branch"
			}
			output, diagnostics, err := callCLI("report", "native", "--config", filepath.Join(root, "does-not-exist"), "--input", input, "--integration", "xcode", "--source-commit", source)
			if err == nil || strings.Contains(err.Error(), "configuration") || strings.Contains(output+diagnostics+err.Error(), "PRIVATE-CANARY") {
				t.Fatal("unsafe report was not rejected before configuration and credentials")
			}
		})
	}
}

func TestNativeSubmitUnknownAcknowledgementIsBoundedAndNeverRetriesOrRedirects(t *testing.T) {
	for _, mode := range []string{"truncated", "foreign-invocation", "wrong-count", "error", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var calls, forwarded atomic.Int64
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
			defer foreign.Close()
			report := cliNativeFixture()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "truncated":
					_, _ = w.Write([]byte(`{"version":1`))
				case "foreign-invocation", "wrong-count":
					receipt := measurement.NativeReceipt{Version: 1, Recorded: true, InvocationID: report.InvocationID, Tasks: 0}
					if mode == "foreign-invocation" {
						receipt.InvocationID = "run-other-000000000000"
					} else {
						receipt.Tasks = 1
					}
					_ = json.NewEncoder(w).Encode(receipt)
				case "error":
					http.Error(w, "PRIVATE-CANARY", 503)
				case "redirect":
					http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
				}
			}))
			defer origin.Close()
			_, err := submitNativeReport(context.Background(), newCLIHTTPClient(time.Second), origin.URL, "never-forward-credential", "project", "profile", report)
			if err == nil || calls.Load() != 1 || forwarded.Load() != 0 || strings.Contains(err.Error(), "PRIVATE-CANARY") || strings.Contains(err.Error(), "never-forward-credential") {
				t.Fatal("ambiguous acknowledgement retried, redirected, or leaked diagnostics")
			}
			if (mode == "truncated" || mode == "foreign-invocation" || mode == "wrong-count" || mode == "error") && !strings.Contains(err.Error(), "retry the same input") {
				t.Fatal("ambiguous acknowledgement lacks reconciliation instruction")
			}
		})
	}
}
