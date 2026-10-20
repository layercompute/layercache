package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/measurement"
)

// A report import is an authenticated member's observation of an external
// invocation. It does not observe the importing machine as the build machine,
// change saved configuration, or transfer cache publication credentials.
func runNativeReport(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	defaultPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("report native", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultPath, "project configuration")
	inputPath := flags.String("input", "", "protected bounded native report JSON file")
	integration := flags.String("integration", "", "native integration: gradle or xcode")
	sourceCommit := flags.String("source-commit", "", "optional caller-declared immutable source commit; not workload attestation")
	jsonOutput := flags.Bool("json", false, "print bounded acknowledgement JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *inputPath == "" || (*integration != "gradle" && *integration != "xcode") || !nativeReportCommit(*sourceCommit) {
		return errors.New("native report requires --input and --integration gradle|xcode; --source-commit must be an immutable commit")
	}
	// Reject paths, free-form diagnostics, unknown fields and oversized input
	// before looking up a credential or sending any bytes to the backend.
	data, err := readProtectedFileUpTo(*inputPath, measurement.NativeMaximumBytes)
	if err != nil {
		return errors.New("native report input must be a bounded, private, owned regular file")
	}
	report, err := measurement.DecodeNativeReport(bytes.NewReader(data))
	if err != nil || measurement.ValidateNativeReport(report, time.Now().UTC()) != nil {
		return errors.New("invalid bounded native report; no report was sent")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return errors.New("load native report project configuration")
	}
	if cfg.ProjectID == "" || cfg.CompatibilityID == "" {
		return errors.New("native report requires the producing project's exact compatibility profile")
	}
	runID, workspaceID := nativeReportContext(cfg.ProjectID, cfg.CompatibilityID, *integration, report.InvocationID)
	endpoint, token := localRuntimeURL(cfg.Listen), ""
	client := newLocalCLIHTTPClient(controlRequestTimeout)
	if cfg.RemoteOnly {
		githubToken, authErr := githubTokenForCapability(ctx, cfg)
		if authErr != nil {
			return errors.New("native report authentication unavailable; run layercache login")
		}
		exchanged, authErr := exchangeGitHubCapabilityWithContext(ctx, cfg.TeamURL, cfg, githubToken, &localRunContext{
			RunID: runID, WorkspaceID: workspaceID, SourceCommit: strings.ToLower(*sourceCommit),
		})
		if authErr != nil || exchanged.RunID == "" || exchanged.WorkspaceID == "" || exchanged.NativeTokens[*integration] == "" || exchanged.NativeReports == nil || exchanged.NativeReports.Version != measurement.NativeVersion || exchanged.NativeReports.Path != measurement.NativePath {
			return errors.New("native report workload authority unavailable; the server may need an update")
		}
		endpoint, token = strings.TrimRight(cfg.TeamURL, "/"), exchanged.NativeTokens[*integration]
		client = newCLIHTTPClient(controlRequestTimeout)
	} else {
		now := time.Now().UTC()
		claims := access.Claims{
			Subject: cfg.InstallationID, Project: cfg.ProjectID, Compatibility: cfg.CompatibilityID,
			Integration: *integration, RunID: runID, WorkspaceID: workspaceID,
			Repository: cfg.ActionsRepository, SourceCommit: strings.ToLower(*sourceCommit),
			Capabilities: []access.Capability{access.CapabilityRead}, ExpiresAt: now.Add(5 * time.Minute),
		}
		token, err = access.MintCapabilityToken(cfg.LocalToken, claims, now)
		if err != nil {
			return errors.New("native report local authority unavailable")
		}
	}
	receipt, err := submitNativeReport(ctx, client, endpoint, token, cfg.ProjectID, cfg.CompatibilityID, report)
	if err != nil {
		return err
	}
	return printResult(stdout, *jsonOutput, receipt, "Native report recorded. Reporting does not establish cache retention or billed savings.")
}

func nativeReportContext(project, profile, integration, invocation string) (string, string) {
	digest := sha256.Sum256([]byte("layercache-native-import-v1\x00" + project + "\x00" + profile + "\x00" + integration + "\x00" + invocation))
	value := hex.EncodeToString(digest[:])
	return "run-native-" + value, "sha256:" + value
}

func nativeReportCommit(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func submitNativeReport(ctx context.Context, client *http.Client, endpoint, token, project, profile string, report measurement.NativeReport) (measurement.NativeReceipt, error) {
	// Re-encoding the strict projection prevents unrecognized input data from
	// leaking even when a caller supplies a valid-looking JSON document.
	body, err := json.Marshal(report)
	if err != nil {
		return measurement.NativeReceipt{}, errors.New("invalid native report projection")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+measurement.NativePath, bytes.NewReader(body))
	if err != nil {
		return measurement.NativeReceipt{}, errors.New("native report endpoint unavailable")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-LayerCache-Project", project)
	request.Header.Set("X-LayerCache-Compatibility", profile)
	response, err := client.Do(request)
	if err != nil {
		return measurement.NativeReceipt{}, errors.New("native report acknowledgement unknown; retry the same input to reconcile")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 500 {
			return measurement.NativeReceipt{}, fmt.Errorf("native report returned HTTP %d; retry the same input to reconcile", response.StatusCode)
		}
		return measurement.NativeReceipt{}, fmt.Errorf("native report returned HTTP %d; the native build result is unchanged", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<10))
	var receipt measurement.NativeReceipt
	if decoder.Decode(&receipt) != nil || decoder.Decode(&struct{}{}) != io.EOF || receipt.Version != measurement.NativeVersion || receipt.InvocationID != report.InvocationID || receipt.Tasks != len(report.Tasks) || !receipt.Recorded {
		return measurement.NativeReceipt{}, errors.New("native report acknowledgement unknown; retry the same input to reconcile")
	}
	return receipt, nil
}
