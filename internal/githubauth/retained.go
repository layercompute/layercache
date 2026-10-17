package githubauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// VerifySameRepositoryRun proves that a pull_request execution belongs to the
// repository whose signed OIDC identity we verified. The OIDC SHA, which can be
// a synthetic merge commit, remains authoritative; head_sha is not that SHA.
// The supplied job token is used once and is never returned or persisted.
func VerifySameRepositoryRun(ctx context.Context, apiURL, token string, identity ActionsIdentity) error {
	if token == "" || strings.ContainsAny(token, "\x00\r\n") || identity.EventName != "pull_request" {
		return errors.New("same-repository run verification unavailable")
	}
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname()))) {
		return errors.New("invalid GitHub API origin")
	}
	parts := strings.Split(identity.Repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(identity.Repository, "? #\\\r\n\t") {
		return errors.New("invalid GitHub repository identity")
	}
	endpoint := strings.TrimRight(apiURL, "/") + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/actions/runs/" + identity.RunID
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("invalid GitHub run request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("GitHub API redirects are disabled") }}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("GitHub run verification failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("GitHub run verification failed")
	}
	var run struct {
		ID         uint64 `json:"id"`
		Attempt    uint64 `json:"run_attempt"`
		Event      string `json:"event"`
		Repository struct {
			ID uint64 `json:"id"`
		} `json:"repository"`
		HeadRepository struct {
			ID uint64 `json:"id"`
		} `json:"head_repository"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&run); err != nil ||
		strconv.FormatUint(run.ID, 10) != identity.RunID || strconv.FormatUint(run.Attempt, 10) != identity.RunAttempt ||
		run.Event != identity.EventName || strconv.FormatUint(run.Repository.ID, 10) != identity.RepositoryID ||
		run.HeadRepository.ID != run.Repository.ID {
		return errors.New("GitHub run is not an eligible same-repository execution")
	}
	return nil
}
