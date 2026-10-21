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

func cliOperationFixture() measurement.OperationReport {
	end := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Second)
	setup, restore, work, closeMS, publish := int64(10), int64(20), int64(900), int64(30), int64(40)
	return measurement.OperationReport{
		Version: 1, OperationID: "run-cli-operation-000000000000", SessionID: "run-cli-session-000000000000",
		WorkloadDigest: "sha256:" + strings.Repeat("a", 64), CoverageDigest: "sha256:" + strings.Repeat("b", 64),
		EnvironmentDigest: "sha256:" + strings.Repeat("c", 64), OtherCachesDigest: "sha256:" + strings.Repeat("d", 64),
		OutputState: "fresh", CachePolicy: "enabled", CachePreparation: "hit",
		StartedAt: end.Add(-time.Second), FinishedAt: end, ElapsedMS: 1000, Status: "success",
		PhaseTimingMS: measurement.OperationPhaseTiming{Setup: &setup, Restore: &restore, Work: &work, Close: &closeMS, Publish: &publish},
	}
}

func writeCLIOperationFixture(t *testing.T, path string, report measurement.OperationReport) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOperationImportHostedAndLocalReconcileWithoutWorkOrConfigMutation(t *testing.T) {
	for _, remote := range []bool{true, false} {
		t.Run(fmt.Sprintf("remote=%t", remote), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "user-config"))
			t.Setenv("HOME", filepath.Join(root, "home"))
			github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer operation-fixture-secret" {
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
			backend.ProjectID, backend.CompatibilityID = "github.com/acme/widgets", "native-profile-v1"
			backend.ActionsRepository, backend.ActionsRef, backend.ActionsDefaultRef = "acme/widgets", "refs/heads/main", "refs/heads/main"
			backend.GitHubAPIURL = github.URL
			backend.TeamMembers = map[string]string{"developer": "reader"}
			runtime, err := server.New(context.Background(), backend)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			report := cliOperationFixture()
			wantRun, wantWorkspace := operationReportContext(backend.InstallationID, report.SessionID)
			nativeRun, nativeWorkspace := nativeReportContext(backend.ProjectID, backend.CompatibilityID, "xcode", report.SessionID)
			if wantRun == nativeRun || wantWorkspace == nativeWorkspace {
				t.Fatal("operation imports reuse the native invocation identity domain")
			}
			if remote {
				wantRun, wantWorkspace = "github-local:github:developer:"+wantRun, "github-local:github:developer:"+wantWorkspace
			}
			var posts, unrelated atomic.Int64
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path != measurement.OperationPath && r.URL.Path != "/v1/auth/github/exchange" {
					unrelated.Add(1)
				}
				if r.URL.Path == measurement.OperationPath {
					posts.Add(1)
					claims, claimErr := access.ParseCapabilityToken(backend.LocalToken, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), time.Now())
					if claimErr != nil || claims.Integration != "xcode" || claims.RunID != wantRun || claims.WorkspaceID != wantWorkspace || claims.Project != backend.ProjectID || claims.Compatibility != backend.CompatibilityID || !claims.Allows(access.CapabilityRead) || claims.Allows(access.CapabilityWrite) || claims.Allows(access.CapabilityAdmin) {
						t.Error("operation report received wrong or excessive authority")
					}
					if r.Header.Get("X-LayerCache-Project") != backend.ProjectID || r.Header.Get("X-LayerCache-Compatibility") != backend.CompatibilityID {
						t.Error("operation report lost exact project/profile headers")
					}
				}
				runtime.Handler().ServeHTTP(w, r)
			}))
			defer endpoint.Close()
			cfg := backend
			cfg.Role, cfg.RemoteOnly = "local", remote
			if remote {
				cfg.LocalToken = "must-not-sign-hosted-reports"
				cfg.DataDir, cfg.TeamURL = filepath.Join(root, "no-local-store"), endpoint.URL
			} else {
				cfg.Listen = strings.TrimPrefix(endpoint.URL, "http://")
			}
			gh := filepath.Join(root, "gh")
			if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf operation-fixture-secret"), 0700); err != nil {
				t.Fatal(err)
			}
			cfg.GitHubCLIPath = gh
			path, input := filepath.Join(root, "client.json"), filepath.Join(root, "operation.json")
			if err := config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			writeCLIOperationFixture(t, input, report)
			var receiptID string
			for _, repeated := range []bool{false, true} {
				output, diagnostics, err := callCLI("report", "operation", "--config", path, "--input", input, "--integration", "xcode", "--source-commit", strings.Repeat("a", 40), "--json")
				if err != nil {
					t.Fatalf("operation import failed: %v %s", err, diagnostics)
				}
				var receipt measurement.OperationReceipt
				if json.Unmarshal([]byte(output), &receipt) != nil || !receipt.Recorded || receipt.Repeated != repeated || receipt.OperationID != report.OperationID || !operationReceiptID(receipt.ReceiptID) || repeated && receipt.ReceiptID != receiptID {
					t.Fatal("operation import returned wrong immutable receipt")
				}
				receiptID = receipt.ReceiptID
				if strings.Contains(output+diagnostics, "operation-fixture-secret") || strings.Contains(output+diagnostics, cfg.LocalToken) {
					t.Fatal("operation import disclosed a credential")
				}
			}
			if _, _, err := callCLI("report", "operation", "--config", path, "--input", input, "--integration", "xcode", "--source-commit", strings.Repeat("b", 40)); err == nil || !strings.Contains(err.Error(), "409") {
				t.Fatal("same operation accepted a changed source")
			}
			report.ElapsedMS--
			*report.PhaseTimingMS.Work--
			writeCLIOperationFixture(t, input, report)
			if _, _, err := callCLI("report", "operation", "--config", path, "--input", input, "--integration", "xcode", "--source-commit", strings.Repeat("a", 40)); err == nil || !strings.Contains(err.Error(), "409") {
				t.Fatal("same operation accepted changed elapsed evidence")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) || posts.Load() != 4 || unrelated.Load() != 0 {
				t.Fatal("import changed config, performed unrelated work, or retried")
			}
			if remote {
				if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
					t.Fatal("hosted import initialized local artifact storage")
				}
			}
		})
	}
}

func TestOperationImportRejectsUnsafeInputBeforeCredentials(t *testing.T) {
	root := t.TempDir()
	for _, kind := range []string{"unknown-field", "duplicate", "oversize", "public-mode", "hardlink", "symlink", "interval-too-long", "invalid-duration", "source", "missing-session"} {
		t.Run(kind, func(t *testing.T) {
			input := filepath.Join(root, kind+".json")
			report := cliOperationFixture()
			if kind == "interval-too-long" {
				report.StartedAt = report.StartedAt.Add(-48 * time.Hour)
			}
			if kind == "invalid-duration" {
				report.ElapsedMS = 10000
			}
			if kind == "missing-session" {
				report.SessionID = ""
			}
			writeCLIOperationFixture(t, input, report)
			source := strings.Repeat("a", 40)
			switch kind {
			case "unknown-field", "duplicate":
				data, _ := os.ReadFile(input)
				extra := `,"rawPath":"PRIVATE-CANARY"}`
				if kind == "duplicate" {
					extra = `,"status":"failed"}`
				}
				if err := os.WriteFile(input, append(data[:len(data)-1], []byte(extra)...), 0600); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(input, bytes.Repeat([]byte("x"), measurement.OperationMaximumBytes+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "public-mode":
				if err := os.Chmod(input, 0644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(input, input+".link"); err != nil {
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
			output, diagnostics, err := callCLI("report", "operation", "--config", filepath.Join(root, "does-not-exist"), "--input", input, "--integration", "xcode", "--source-commit", source)
			if err == nil || strings.Contains(err.Error(), "configuration") || strings.Contains(output+diagnostics+err.Error(), "PRIVATE-CANARY") {
				t.Fatal("unsafe operation was not rejected before configuration/credentials")
			}
			if kind == "missing-session" && !strings.Contains(err.Error(), "declared sessionId") {
				t.Fatal("missing session was not rejected before configuration/credentials")
			}
		})
	}
}

func TestOperationImportDeclaredSessionSharesQuotaAndPreservesExactRetry(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Role, cfg.DataDir, cfg.MaxBytes = "local", filepath.Join(root, "backend"), 1<<20
	cfg.ProjectID, cfg.CompatibilityID = "github.com/acme/widgets", "native-profile-v1"
	runtime, err := server.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	endpoint := httptest.NewServer(runtime.Handler())
	defer endpoint.Close()
	cfg.Listen = strings.TrimPrefix(endpoint.URL, "http://")
	path, input := filepath.Join(root, "config.json"), filepath.Join(root, "operation.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	source := strings.Repeat("a", 40)
	base := cliOperationFixture()
	var first measurement.OperationReceipt
	var firstReport measurement.OperationReport
	importReport := func(report measurement.OperationReport, source string) (measurement.OperationReceipt, error) {
		t.Helper()
		writeCLIOperationFixture(t, input, report)
		output, _, err := callCLI("report", "operation", "--config", path, "--input", input, "--integration", "gradle", "--source-commit", source, "--json")
		var receipt measurement.OperationReceipt
		if err == nil && json.Unmarshal([]byte(output), &receipt) != nil {
			t.Fatal("invalid operation acknowledgement")
		}
		return receipt, err
	}
	for i := range measurement.OperationMaximumPerJob {
		report := base
		report.OperationID = fmt.Sprintf("run-session-quota-operation-%04d", i)
		receipt, err := importReport(report, source)
		if err != nil || !receipt.Recorded || receipt.Repeated {
			t.Fatalf("operation %d refused before session limit: %+v %v", i, receipt, err)
		}
		if i == 0 {
			first, firstReport = receipt, report
		}
	}
	extra := base
	extra.OperationID = "run-session-quota-extra-000000000000"
	for _, value := range []string{source, strings.Repeat("b", 40)} {
		if _, err := importReport(extra, value); err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("distinct operation/source rotated a full session bucket: %v", err)
		}
	}
	profile := cfg.CompatibilityID
	cfg.CompatibilityID = "another-native-profile-v1"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := importReport(extra, source); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("profile change rotated a full session bucket: %v", err)
	}
	if _, err := importReport(firstReport, source); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("changed profile escaped immutable operation authority: %v", err)
	}
	cfg.CompatibilityID = profile
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	retry, err := importReport(firstReport, source)
	if err != nil || !retry.Repeated || retry.ReceiptID != first.ReceiptID {
		t.Fatalf("full session blocked exact reconciliation: %+v %v", retry, err)
	}
	extra.SessionID = "run-next-declared-session-000000000000"
	next, err := importReport(extra, source)
	if err != nil || !next.Recorded || next.Repeated {
		t.Fatalf("explicit next session did not get its own bucket: %+v %v", next, err)
	}
}

func TestOperationImportRequiresStableInstallationBeforeCredentials(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RemoteOnly, cfg.DataDir = true, filepath.Join(root, "no-local-store")
	cfg.TeamURL = "http://127.0.0.1:1"
	gh, called := filepath.Join(root, "gh"), filepath.Join(root, "credential-read")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\ntouch "+shellQuote(called)+"\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.GitHubCLIPath = gh
	path, input := filepath.Join(root, "config.json"), filepath.Join(root, "operation.json")
	writeCLIOperationFixture(t, input, cliOperationFixture())
	for _, identity := range []string{"", "machine-name", "install-" + strings.Repeat("a", 23), "install-" + strings.Repeat("a", 24) + "/"} {
		cfg.InstallationID = identity
		if err := config.Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		_, _, err := callCLI("report", "operation", "--config", path, "--input", input, "--integration", "gradle")
		if err == nil || !strings.Contains(err.Error(), "stable installation identity") {
			t.Fatal("missing/malformed installation did not stop before credentials")
		}
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatal("invalid installation read a credential")
	}
}

func TestOperationImportReconcilesRetainedReportAfterNewAdmissionWindow(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Role, cfg.DataDir, cfg.MaxBytes = "local", filepath.Join(root, "backend"), 1<<20
	cfg.ProjectID, cfg.CompatibilityID = "github.com/acme/widgets", "native-profile-v1"
	runtime, err := server.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	endpoint := httptest.NewServer(runtime.Handler())
	defer endpoint.Close()
	cfg.Listen = strings.TrimPrefix(endpoint.URL, "http://")
	path, input := filepath.Join(root, "config.json"), filepath.Join(root, "operation.json")
	if err = config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	report := cliOperationFixture()
	report.StartedAt, report.FinishedAt = report.StartedAt.Add(-48*time.Hour), report.FinishedAt.Add(-48*time.Hour)
	runID, workspaceID := operationReportContext(cfg.InstallationID, report.SessionID)
	owner, err := measurement.OpenSQLiteRepository(filepath.Join(cfg.DataDir, "measurements.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	repository, err := owner.OperationRepository(context.Background(), cfg.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	authority := measurement.NativeAuthority{
		Project: cfg.ProjectID, CompatibilityID: cfg.CompatibilityID, Integration: measurement.IntegrationGradle,
		RunID: runID, WorkspaceID: workspaceID, Subject: cfg.InstallationID, Repository: cfg.ActionsRepository,
		SourceCommit: strings.Repeat("a", 40), Kind: "local",
	}
	first, err := repository.RecordOperation(context.Background(), authority, report, report.FinishedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	writeCLIOperationFixture(t, input, report)
	output, diagnostics, err := callCLI("report", "operation", "--config", path, "--input", input, "--integration", "gradle", "--source-commit", authority.SourceCommit, "--json")
	if err != nil {
		t.Fatalf("retained exact reconciliation refused: %v %s", err, diagnostics)
	}
	var receipt measurement.OperationReceipt
	if json.Unmarshal([]byte(output), &receipt) != nil || !receipt.Repeated || receipt.ReceiptID != first.ReceiptID {
		t.Fatal("late CLI retry did not return its original receipt")
	}
	changed := report
	changed.Status = "failed"
	writeCLIOperationFixture(t, input, changed)
	if _, _, err := callCLI("report", "operation", "--config", path, "--input", input, "--integration", "gradle", "--source-commit", authority.SourceCommit); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatal("late changed-content retry did not conflict")
	}
	writeCLIOperationFixture(t, input, report)
	if _, _, err := callCLI("report", "operation", "--config", path, "--input", input, "--integration", "gradle", "--source-commit", strings.Repeat("b", 40)); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatal("late changed-source retry did not conflict")
	}
	report.OperationID = "run-stale-new-operation-000000000000"
	writeCLIOperationFixture(t, input, report)
	if _, _, err := callCLI("report", "operation", "--config", path, "--input", input, "--integration", "gradle", "--source-commit", authority.SourceCommit); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatal("new stale CLI report bypassed admission age")
	}
}

func TestOperationSubmitUnknownAcknowledgementNeverRetriesOrRedirects(t *testing.T) {
	for _, mode := range []string{"truncated", "foreign-operation", "missing-receipt", "invalid-receipt", "oversize", "unknown-field", "error", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var calls, forwarded atomic.Int64
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
			defer foreign.Close()
			report := cliOperationFixture()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				receipt := measurement.OperationReceipt{Version: 1, Recorded: true, OperationID: report.OperationID, ReceiptID: strings.Repeat("a", 64)}
				switch mode {
				case "truncated":
					_, _ = w.Write([]byte(`{"version":1`))
				case "foreign-operation", "missing-receipt", "invalid-receipt":
					if mode == "foreign-operation" {
						receipt.OperationID = "run-other-000000000000"
					} else if mode == "missing-receipt" {
						receipt.ReceiptID = ""
					} else {
						receipt.ReceiptID = strings.Repeat("A", 64)
					}
					_ = json.NewEncoder(w).Encode(receipt)
				case "oversize", "unknown-field":
					data, _ := json.Marshal(receipt)
					if mode == "oversize" {
						data = append(data, bytes.Repeat([]byte(" "), 16<<10)...)
					} else {
						data = append(data[:len(data)-1], []byte(`,"diagnostic":"PRIVATE-CANARY"}`)...)
					}
					_, _ = w.Write(data)
				case "error":
					http.Error(w, "PRIVATE-CANARY", 503)
				case "redirect":
					http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
				}
			}))
			defer origin.Close()
			_, err := submitOperationReport(context.Background(), newCLIHTTPClient(time.Second), origin.URL, "never-forward-credential", "project", "profile", report)
			if err == nil || calls.Load() != 1 || forwarded.Load() != 0 || strings.Contains(err.Error(), "PRIVATE-CANARY") || strings.Contains(err.Error(), "never-forward-credential") {
				t.Fatal("ambiguous acknowledgement retried, redirected, or leaked diagnostics")
			}
			if mode != "redirect" && !strings.Contains(err.Error(), "retry the same input") {
				t.Fatal("ambiguous acknowledgement lacks reconciliation instruction")
			}
		})
	}
}

func TestOperationImportRequiresDiscoveredFeatureAndIgnoresCachedWriter(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProjectID, cfg.CompatibilityID = "github.com/acme/widgets", "native-profile-v1"
	cfg.RemoteOnly, cfg.DataDir = true, filepath.Join(root, "no-local-store")
	var posts atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/github/exchange" {
			posts.Add(1)
			http.Error(w, "not allowed", 403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "ordinary-team-token", "expiresAt": time.Now().Add(time.Hour),
			"runId": "run-exchanged-000000000000", "workspaceId": "sha256:" + strings.Repeat("a", 64),
			"nativeTokens": map[string]string{"xcode": "native-reader-token"},
			// Older servers may support native reports without operation reports.
			"nativeReports": map[string]any{"version": 1, "path": measurement.NativePath},
		})
	}))
	defer endpoint.Close()
	cfg.TeamURL, cfg.TeamToken, cfg.TeamTokenExpiresAt = endpoint.URL, "cached-writer-must-not-be-used", time.Now().Add(time.Hour)
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf operation-fixture-secret"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.GitHubCLIPath = gh
	path, input := filepath.Join(root, "config.json"), filepath.Join(root, "operation.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	writeCLIOperationFixture(t, input, cliOperationFixture())
	output, diagnostics, err := callCLI("report", "operation", "--config", path, "--input", input, "--integration", "xcode", "--json")
	if err == nil || !strings.Contains(err.Error(), "server may need an update") || posts.Load() != 0 || output != "" || strings.Contains(diagnostics+err.Error(), "cached-writer-must-not-be-used") {
		t.Fatal("missing operation discovery fell back to a broader capability or leaked it")
	}
}
