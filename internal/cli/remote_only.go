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
	"reflect"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/config"
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
	summaries := make([]json.RawMessage, len(documents))
	for i, document := range documents {
		summaries[i] = document
	}
	body, err := json.Marshal(struct {
		Summaries []json.RawMessage `json:"summaries"`
	}{summaries})
	if err != nil {
		return 0, err
	}
	if len(body) > 8<<20 {
		return 0, errors.New("hosted Turbo summaries exceed 8 MiB")
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
	// Prove the installed configuration can be removed before changing its
	// credential. A directory permission failure leaves the client usable.
	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("remove cloud client configuration: %w", err)
	}
	if cfg.GitHubCredentialAccount != "" {
		if err := deleteCredential(ctx, cfg.GitHubCredentialAccount); err != nil {
			return false, errors.Join(fmt.Errorf("remove cloud client credential: %w", err), restoreDisconnectedClient(path, data))
		}
		return true, nil
	}
	return false, nil
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
	return nil
}
