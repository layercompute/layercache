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

// Operation imports carry an already sealed elapsed-time observation. Importing
// does not run the workload, sample the importer's hardware, or mutate its config.
func runOperationReport(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	defaultPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("report operation", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultPath, "project configuration")
	inputPath := flags.String("input", "", "protected bounded sealed operation report JSON file")
	integration := flags.String("integration", "", "native integration: gradle or xcode")
	sourceCommit := flags.String("source-commit", "", "optional caller-declared immutable source commit; not workload attestation")
	jsonOutput := flags.Bool("json", false, "print bounded acknowledgement JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *inputPath == "" || (*integration != "gradle" && *integration != "xcode") || !nativeReportCommit(*sourceCommit) {
		return errors.New("operation report requires --input and --integration gradle|xcode; --source-commit must be an immutable commit")
	}
	// Validate the complete bounded projection before credentials or network use.
	data, err := readProtectedFileUpTo(*inputPath, measurement.OperationMaximumBytes)
	if err != nil {
		return errors.New("operation report input must be a bounded, private, owned regular file")
	}
	report, err := measurement.DecodeOperationReport(bytes.NewReader(data))
	if err != nil || measurement.ValidateOperationReportShape(report) != nil {
		return errors.New("invalid bounded operation report; no report was sent")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return errors.New("load operation report project configuration")
	}
	if cfg.ProjectID == "" || cfg.CompatibilityID == "" {
		return errors.New("operation report requires the producing project's exact compatibility profile")
	}
	runID, workspaceID := operationReportContext(cfg.ProjectID, cfg.CompatibilityID, *integration, report.OperationID)
	endpoint, token := localRuntimeURL(cfg.Listen), ""
	client := newLocalCLIHTTPClient(controlRequestTimeout)
	if cfg.RemoteOnly {
		githubToken, authErr := githubTokenForCapability(ctx, cfg)
		if authErr != nil {
			return errors.New("operation report authentication unavailable; run layercache login")
		}
		exchanged, authErr := exchangeGitHubCapabilityWithContext(ctx, cfg.TeamURL, cfg, githubToken, &localRunContext{
			RunID: runID, WorkspaceID: workspaceID, SourceCommit: strings.ToLower(*sourceCommit),
		})
		if authErr != nil || exchanged.RunID == "" || exchanged.WorkspaceID == "" || exchanged.NativeTokens[*integration] == "" || exchanged.OperationReports == nil || exchanged.OperationReports.Version != measurement.OperationVersion || exchanged.OperationReports.Path != measurement.OperationPath {
			return errors.New("operation report workload authority unavailable; the server may need an update")
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
			return errors.New("operation report local authority unavailable")
		}
	}
	receipt, err := submitOperationReport(ctx, client, endpoint, token, cfg.ProjectID, cfg.CompatibilityID, report)
	if err != nil {
		return err
	}
	return printResult(stdout, *jsonOutput, receipt, "Operation observation recorded. A paired elapsed difference does not establish compute or billed savings.")
}

// Use a separate identity domain from native invocation imports, even if a
// caller accidentally reuses the same operation/invocation string.
func operationReportContext(project, profile, integration, operation string) (string, string) {
	digest := sha256.Sum256([]byte("layercache-operation-import-v1\x00" + project + "\x00" + profile + "\x00" + integration + "\x00" + operation))
	value := hex.EncodeToString(digest[:])
	return "run-operation-" + value, "sha256:" + value
}

func submitOperationReport(ctx context.Context, client *http.Client, endpoint, token, project, profile string, report measurement.OperationReport) (measurement.OperationReceipt, error) {
	body, err := json.Marshal(report)
	if err != nil {
		return measurement.OperationReceipt{}, errors.New("invalid operation report projection")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+measurement.OperationPath, bytes.NewReader(body))
	if err != nil {
		return measurement.OperationReceipt{}, errors.New("operation report endpoint unavailable")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-LayerCache-Project", project)
	request.Header.Set("X-LayerCache-Compatibility", profile)
	response, err := client.Do(request)
	if err != nil {
		return measurement.OperationReceipt{}, errors.New("operation report acknowledgement unknown; retry the same input to reconcile")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 500 {
			return measurement.OperationReceipt{}, fmt.Errorf("operation report returned HTTP %d; retry the same input to reconcile", response.StatusCode)
		}
		return measurement.OperationReceipt{}, fmt.Errorf("operation report returned HTTP %d; the native work result is unchanged", response.StatusCode)
	}
	const maximumAcknowledgementBytes = 16 << 10
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumAcknowledgementBytes+1))
	if err != nil || len(data) > maximumAcknowledgementBytes {
		return measurement.OperationReceipt{}, errors.New("operation report acknowledgement unknown; retry the same input to reconcile")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt measurement.OperationReceipt
	if decoder.Decode(&receipt) != nil || decoder.Decode(&struct{}{}) != io.EOF || receipt.Version != measurement.OperationVersion || receipt.OperationID != report.OperationID || !receipt.Recorded || !operationReceiptID(receipt.ReceiptID) {
		return measurement.OperationReceipt{}, errors.New("operation report acknowledgement unknown; retry the same input to reconcile")
	}
	return receipt, nil
}

func operationReceiptID(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
