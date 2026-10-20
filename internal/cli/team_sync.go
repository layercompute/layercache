package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/measurement"
)

func confirmTurboTeamResults(ctx context.Context, cfg config.Config, documents [][]byte, startedAt, finishedAt time.Time, timeout time.Duration, stderr io.Writer) {
	hashes, err := measurement.SuccessfulTurboArtifactHashes(documents, startedAt, finishedAt)
	if err == nil && len(hashes) == 0 {
		err = errors.New("no successful cacheable Turbo tasks were reported")
	}
	if err == nil {
		err = syncTurboTeamResults(ctx, cfg, hashes, timeout)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Layer Cache warning: Team Cache publication unconfirmed: %v; CI may recompute matching tasks\n", err)
		return
	}
	_, _ = fmt.Fprintf(stderr, "Team Cache ready: %d/%d successful tasks\n", len(hashes), len(hashes))
}

func syncTurboTeamResults(ctx context.Context, cfg config.Config, hashes []string, timeout time.Duration) error {
	if cfg.TeamURL == "" || cfg.TeamToken == "" {
		return errors.New("this Local Cache has no authenticated Team Cache configured")
	}
	if cfg.RemoteOnly {
		return syncRemoteTurboResults(ctx, cfg, hashes, timeout)
	}
	body, err := json.Marshal(struct {
		Hashes    []string `json:"hashes"`
		TimeoutMS int64    `json:"timeoutMs"`
	}{hashes, timeout.Milliseconds()})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, localRuntimeURL(cfg.Listen)+"/v1/sync/turbo", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.LocalToken)
	request.Header.Set("X-LayerCache-Compatibility", cfg.CompatibilityID)
	request.Header.Set("Content-Type", "application/json")
	// A publication can run longer than the standard control-header timeout.
	client := newLocalCLIHTTPClient(timeout + 2*time.Second)
	client.Transport.(*http.Transport).ResponseHeaderTimeout = timeout + 2*time.Second
	response, err := client.Do(request)
	if err != nil {
		return errors.New("the local daemon did not confirm publication")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the local daemon returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Requested int `json:"requested"`
		Ready     int `json:"ready"`
		Pending   int `json:"pending"`
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(contents) > 64<<10 {
		return errors.New("the local daemon returned invalid publication status")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return errors.New("the local daemon returned invalid publication status")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("the local daemon returned invalid publication status")
	}
	if result.Requested != len(hashes) || result.Ready < 0 || result.Pending < 0 || result.Ready+result.Pending != result.Requested {
		return errors.New("the local daemon returned inconsistent publication status")
	}
	if result.Pending != 0 {
		return fmt.Errorf("%d/%d successful tasks are available in Team Cache", result.Ready, result.Requested)
	}
	return nil
}
