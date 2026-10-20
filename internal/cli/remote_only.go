package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
