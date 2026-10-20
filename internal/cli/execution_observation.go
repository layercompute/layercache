package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/execution"
	"github.com/layercache/layercache/internal/executioncollect"
)

const clientObservationTimeout = 2 * time.Second
const standaloneObservationScope = "https://standalone.layercache.invalid"

type clientExecutionObservation struct {
	endpoint string
	token    string
	project  string
	identity executioncollect.Identity
	roots    []executioncollect.Root
	client   *http.Client
	collect  func([]executioncollect.Root, func(string) string) execution.Sample
}

// Optional metadata must not hold the command on a stalled filesystem call.
// Each phase starts at most one finite-purpose worker. A late worker observes
// cancellation before sending data; the CLI process never waits for it on exit.
func optionalObservation[T any](ctx context.Context, work func(context.Context) T) T {
	var unavailable T
	if ctx.Err() != nil {
		return unavailable
	}
	bounded, cancel := context.WithTimeout(ctx, clientObservationTimeout)
	defer cancel()
	result := make(chan T, 1)
	go func() { result <- work(bounded) }()
	select {
	case value := <-result:
		return value
	case <-bounded.Done():
		return unavailable
	}
}

// This seam is reached only after a run-scoped human exchange succeeds. Login,
// doctor and old services do not create an installation or report execution.
func startClientExecutionObservation(ctx context.Context, cfg config.Config, exchanged capabilityExchange, workspace string) *clientExecutionObservation {
	capability := exchanged.ExecutionObservations
	if capability == nil || capability.Version != execution.Version || capability.Path != execution.Path || exchanged.RunID == "" || exchanged.WorkspaceID == "" {
		return nil
	}
	endpoint, ok := observationEndpoint(cfg.TeamURL)
	if !ok {
		return nil
	}
	token := exchanged.NativeTokens["turbo"]
	if token == "" {
		token = exchanged.TeamToken
	}
	if token == "" {
		token = exchanged.Token
	}
	if token == "" {
		return nil
	}
	return optionalObservation(ctx, func(bounded context.Context) *clientExecutionObservation {
		identity, err := executioncollect.LoadIdentity(bounded, cfg.TeamURL)
		if err != nil || bounded.Err() != nil {
			return nil
		}
		observation := &clientExecutionObservation{endpoint: endpoint, token: token, project: cfg.ProjectID, identity: identity, roots: executionObservationRoots(cfg, workspace), client: newCLIHTTPClient(clientObservationTimeout)}
		observation.reportNow(bounded, "start")
		return observation
	})
}

func prepareLocalExecutionObservation(ctx context.Context, cfg config.Config, capability *execution.Capability, workspace string) *clientExecutionObservation {
	if cfg.RemoteOnly || cfg.Role != "local" || capability == nil || capability.Version != execution.Version || capability.Path != execution.Path {
		return nil
	}
	endpoint, ok := observationEndpoint(localRuntimeURL(cfg.Listen))
	if !ok {
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(parsed.Hostname())
	if parsed.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil
	}
	return optionalObservation(ctx, func(bounded context.Context) *clientExecutionObservation {
		identity, err := executioncollect.LoadIdentity(bounded, standaloneObservationScope)
		if err != nil || bounded.Err() != nil {
			return nil
		}
		return &clientExecutionObservation{endpoint: endpoint, project: cfg.ProjectID, identity: identity, roots: executionObservationRoots(cfg, workspace), client: newLocalCLIHTTPClient(clientObservationTimeout)}
	})
}

func (observation *clientExecutionObservation) localContext(runID string) *execution.Context {
	return execution.ClientContext("local:"+observation.identity.InstallationID(), runID)
}

func executionObservationRoots(cfg config.Config, workspace string) []executioncollect.Root {
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	roots := []executioncollect.Root{}
	if workspace != "" {
		roots = append(roots, executioncollect.Root{Path: workspace, Role: "workspace"})
	}
	if !cfg.RemoteOnly && cfg.DataDir != "" {
		roots = append(roots, executioncollect.Root{Path: cfg.DataDir, Role: "localCache"})
	}
	return roots
}

func observationEndpoint(base string) (string, bool) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" || parsed.Opaque != "" {
		return "", false
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", false
	}
	parsed.Path, parsed.RawPath = execution.Path, ""
	return parsed.String(), true
}

func (observation *clientExecutionObservation) report(ctx context.Context, phase string) {
	if observation == nil || phase != "start" && phase != "finish" {
		return
	}
	_ = optionalObservation(ctx, func(bounded context.Context) bool { observation.reportNow(bounded, phase); return true })
}

func (observation *clientExecutionObservation) reportNow(ctx context.Context, phase string) {
	if ctx.Err() != nil {
		return
	}
	collect := observation.collect
	if collect == nil {
		collect = executioncollect.Collect
	}
	sample := collect(observation.roots, observation.identity.FilesystemID)
	if ctx.Err() != nil {
		return
	}
	input := execution.ObservationRequest{Version: execution.Version, Phase: phase, InstallationID: observation.identity.InstallationID(), Provider: "unknown", Sample: sample}
	body, err := json.Marshal(input)
	if err != nil || len(body) > execution.MaximumRequestBytes {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, observation.endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+observation.token)
	request.Header.Set("X-LayerCache-Project", observation.project)
	response, err := observation.client.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	// The response is optional, contains no authority and is never displayed.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
}

func readDashboardExecutions(ctx context.Context, client *http.Client, endpoint, token, project string, from, to time.Time) *execution.Inventory {
	if token == "" {
		return nil
	}
	target, ok := observationEndpoint(endpoint)
	if !ok {
		return nil
	}
	period := "24h"
	if to.Sub(from) > 24*time.Hour {
		period = "7d"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target+"?period="+period, nil)
	if err != nil {
		return nil
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-LayerCache-Project", project)
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var result execution.Inventory
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(&result); err != nil || result.Version != execution.Version || result.From.IsZero() || result.To.IsZero() || result.UpdatedAt.IsZero() || result.RetentionDays <= 0 || result.StaleAfterSeconds <= 0 || result.Installations == nil || result.Jobs == nil {
		return nil
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil
	}
	// Do not turn unsupported/failed endpoints into an invented empty inventory.
	return &result
}

// Successful inventories describe observed installations, not physical hosts.
// Preserve whole newest samples and union identities without adding capacities.
func mergeDashboardExecutions(left, right *execution.Inventory) *execution.Inventory {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	result := *left
	result.From, result.To = minTime(left.From, right.From), maxTime(left.To, right.To)
	result.UpdatedAt = maxTime(left.UpdatedAt, right.UpdatedAt)
	result.RetentionDays = min(left.RetentionDays, right.RetentionDays)
	result.StaleAfterSeconds = min(left.StaleAfterSeconds, right.StaleAfterSeconds)
	result.HistoryTruncated = left.HistoryTruncated || right.HistoryTruncated
	result.ListTruncated = left.ListTruncated || right.ListTruncated || left.JobCount > len(left.Jobs) || right.JobCount > len(right.Jobs)
	merge := func(first, second []execution.Entry) []execution.Entry {
		entries := map[string]execution.Entry{}
		for _, values := range [][]execution.Entry{first, second} {
			for _, value := range values {
				current, found := entries[value.ID]
				observed := value.CacheActivity == "observed" || current.CacheActivity == "observed"
				if !found || value.LastObservedAt.After(current.LastObservedAt) {
					current = value
				}
				if observed {
					current.CacheActivity = "observed"
				}
				entries[value.ID] = current
			}
		}
		merged := make([]execution.Entry, 0, len(entries))
		for _, entry := range entries {
			merged = append(merged, entry)
		}
		sort.Slice(merged, func(i, j int) bool {
			if merged[i].LastObservedAt.Equal(merged[j].LastObservedAt) {
				return merged[i].ID < merged[j].ID
			}
			return merged[i].LastObservedAt.After(merged[j].LastObservedAt)
		})
		return merged
	}
	result.Installations, result.Jobs = merge(left.Installations, right.Installations), merge(left.Jobs, right.Jobs)
	result.JobCount = len(result.Jobs)
	return &result
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}
func maxTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}
