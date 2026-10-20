package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/measurement"
)

// A hosted acknowledgement is only a cache presence observation. It does not
// promise retained storage for required pipeline handoffs.
func syncRemoteTurboResults(ctx context.Context, cfg config.Config, hashes []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := newCLIHTTPClient(timeout)
	for _, hash := range hashes {
		request, err := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimRight(cfg.TeamURL, "/")+"/v8/artifacts/"+url.PathEscape(hash)+"?teamId="+url.QueryEscape(cfg.ProjectID), nil)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+cfg.TeamToken)
		request.Header.Set("X-LayerCache-Project", cfg.ProjectID)
		request.Header.Set("X-LayerCache-Compatibility", cfg.CompatibilityID)
		response, err := client.Do(request)
		if err != nil {
			return errors.New("hosted cache did not confirm task presence")
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("hosted task presence returned HTTP %d", response.StatusCode)
		}
	}
	return nil
}

func reconcileRemoteTurboSummaries(ctx context.Context, cfg config.Config, documents [][]byte) (int, error) {
	body, err := measurement.TurboReportPayload(documents)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.TeamURL, "/")+"/v1/reports/turbo", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.TeamToken)
	request.Header.Set("X-LayerCache-Project", cfg.ProjectID)
	request.Header.Set("Content-Type", "application/json")
	response, err := newCLIHTTPClient(controlRequestTimeout).Do(request)
	if err != nil {
		return 0, errors.New("hosted Turbo reconciliation unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("hosted Turbo reconciliation returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Tasks int `json:"tasks"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil || result.Tasks < 0 {
		return 0, errors.New("invalid hosted Turbo reconciliation")
	}
	return result.Tasks, nil
}

// The caller holds the configuration lock. A cloud-only DataDir is only an
// unused hint, so neither uninstall mode inspects or removes anything there.
func uninstallRemoteOnlyClient(ctx context.Context, path string, cfg config.Config, deleteCredential func(context.Context, string) error) (bool, error) {
	if !cfg.RemoteOnly {
		return false, errors.New("cloud client mode changed before uninstall")
	}
	data, err := readProtectedFile(path)
	if err != nil {
		return false, fmt.Errorf("refusing to remove unprotected cloud client configuration: %w", err)
	}
	var current config.Config
	if json.Unmarshal(data, &current) != nil || !reflect.DeepEqual(current, cfg) {
		// Config.Load applies migrations. Compare against its current projection
		// while the protected file and process lock still identify this client.
		current, err = config.Load(path)
		if err != nil || !reflect.DeepEqual(current, cfg) {
			return false, errors.New("cloud client configuration changed before uninstall")
		}
	}
	if cfg.GitHubCredentialAccount == "" {
		// GitHub CLI owns its own login. Removing this configuration needs no
		// recovery copy because no external credential will be changed.
		if err := os.Remove(path); err != nil {
			return false, fmt.Errorf("remove cloud client configuration: %w", err)
		}
		return false, nil
	}
	if cfg.GitHubCredentialAccount != "" {
		prefix := githubCredentialAccount(cfg)
		owned := cfg.GitHubCredentialAccount == prefix
		if suffix, ok := strings.CutPrefix(cfg.GitHubCredentialAccount, prefix+"-"); ok && len(suffix) == 16 {
			_, decodeErr := hex.DecodeString(suffix)
			owned = decodeErr == nil
		}
		if !owned {
			return false, errors.New("cloud client credential account does not belong to this project and endpoint")
		}
	}
	backup, err := persistDisconnectedClientRecovery(path, data)
	if err != nil {
		return false, err
	}
	cleanupRecovery := func() error {
		if err := os.Remove(backup); err != nil {
			return fmt.Errorf("remove protected cloud client recovery file at %s: %w", backup, err)
		}
		return nil
	}
	// Prove the installed configuration can be removed before changing its
	// credential. The durable recovery copy survives restoration failures.
	if err := os.Remove(path); err != nil {
		return false, errors.Join(fmt.Errorf("remove cloud client configuration: %w", err), cleanupRecovery())
	}
	if cfg.GitHubCredentialAccount != "" {
		if err := deleteCredential(ctx, cfg.GitHubCredentialAccount); err != nil {
			credentialErr := fmt.Errorf("remove cloud client credential: %w", err)
			if restoreErr := restoreDisconnectedClient(path, data); restoreErr != nil {
				return false, errors.Join(credentialErr, restoreErr, fmt.Errorf("original cloud client configuration remains recoverable at %s", backup))
			}
			return false, errors.Join(credentialErr, cleanupRecovery())
		}
		if cleanupErr := cleanupRecovery(); cleanupErr != nil {
			return true, &remoteOnlyRecoveryCleanupPending{Path: backup, Cause: cleanupErr}
		}
		return true, nil
	}
	return false, cleanupRecovery()
}

// This warning is returned only after configuration and credential removal
// both completed. Failed credential deletion or restoration remains an error.
type remoteOnlyRecoveryCleanupPending struct {
	Path  string
	Cause error
}

func (pending *remoteOnlyRecoveryCleanupPending) Error() string {
	return fmt.Sprintf("cloud client disconnected; protected recovery cleanup is pending at %s: %v", pending.Path, pending.Cause)
}

func reportRemoteOnlyUninstall(stdout, stderr io.Writer, jsonOutput bool, credentialRemoved bool, removeErr error) error {
	pending, warning := removeErr.(*remoteOnlyRecoveryCleanupPending)
	if removeErr != nil && !warning {
		return removeErr
	}
	result := map[string]any{"uninstalled": true, "remoteOnly": true, "cachePreserved": true, "localDataTouched": false, "credentialRemoved": credentialRemoved}
	if warning {
		result["pendingRecoveryCleanupPath"] = pending.Path
		_, _ = fmt.Fprintf(stderr, "Layer Cache warning: %s\n", pending.Error())
	}
	return printResult(stdout, jsonOutput, result, "Layer Cache cloud client disconnected; local data was left untouched")
}

// Exclusive creation preserves a configuration another process may have created
// outside the CLI's lock. Restoration never reads or writes the DataDir hint.
func restoreDisconnectedClient(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("restore cloud client configuration after credential cleanup failed: %w", err)
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("restore cloud client configuration after credential cleanup failed: %w", err)
	}
	if err := syncDisconnectedClientDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync restored cloud client configuration: %w", err)
	}
	return nil
}

func persistDisconnectedClientRecovery(path string, data []byte) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve cloud client recovery directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(absolute), ".layercache-disconnect-*.json")
	if err != nil {
		return "", fmt.Errorf("persist cloud client recovery before disconnect: %w", err)
	}
	backup := file.Name()
	writeErr := file.Chmod(0600)
	if writeErr == nil {
		_, writeErr = file.Write(data)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err == nil {
		err = syncDisconnectedClientDirectory(filepath.Dir(backup))
	}
	if err != nil {
		_ = os.Remove(backup)
		return "", fmt.Errorf("persist cloud client recovery before disconnect: %w", err)
	}
	return backup, nil
}

func syncDisconnectedClientDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
