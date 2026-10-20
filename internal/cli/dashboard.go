package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/dashboard"
	"github.com/layercache/layercache/internal/measurement"
	cacheserver "github.com/layercache/layercache/internal/server"
)

type dashboardCredentials struct {
	source    config.Config
	token     string
	expiresAt time.Time
}

func (cached dashboardCredentials) matches(cfg config.Config) bool {
	return sameCredentialScope(cached.source, cfg) &&
		cached.source.TeamToken == cfg.TeamToken && cached.source.TeamTokenExpiresAt.Equal(cfg.TeamTokenExpiresAt) &&
		cached.source.PublicAccessToken == cfg.PublicAccessToken && cached.source.PublicAccessTokenExpiresAt.Equal(cfg.PublicAccessTokenExpiresAt)
}

func runDashboard(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	defaultPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var paths []string
	flags.Func("config", "project configuration; repeat for multiple projects", func(path string) error { paths = append(paths, path); return nil })
	listen := flags.String("listen", "127.0.0.1:0", "loopback dashboard address")
	allowPrune := flags.Bool("allow-prune", false, "allow confirmed administrator cache maintenance")
	costFile := flags.String("cost-file", "", "bounded operator cost ledger")
	backend := flags.String("backend", "", "select the configured remote cache URL without running a Local backend")
	serve := flags.Bool("serve", false, "run the configured Local backend(s) until this dashboard stops")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("dashboard accepts flags only")
	}
	if len(paths) == 0 {
		paths = []string{defaultPath}
	}
	if len(paths) > 32 {
		return errors.New("dashboard supports at most 32 project configurations")
	}
	if *backend != "" && (*serve || len(paths) != 1) {
		return errors.New("--backend requires exactly one configuration and cannot be combined with --serve")
	}
	selectedBackend := ""
	if *backend != "" {
		selectedBackend, err = normalizeDashboardBackend(*backend)
		if err != nil {
			return err
		}
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		return errors.New("dashboard must listen on 127.0.0.1 or ::1")
	}
	ids := make([]string, len(paths))
	sources := make([]config.Config, len(paths))
	seen := map[string]bool{}
	for i, path := range paths {
		cfg, err := config.Load(path)
		if err != nil {
			return fmt.Errorf("load dashboard configuration %d: %w", i+1, err)
		}
		if cfg.Role != "local" {
			return errors.New("dashboard requires local CLI configurations; connect Team Cache with layercache login")
		}
		if seen[cfg.ProjectID] {
			return fmt.Errorf("duplicate dashboard project %s", cfg.ProjectID)
		}
		if selectedBackend != "" {
			configured, normalizeErr := normalizeDashboardBackend(cfg.TeamURL)
			if normalizeErr != nil || configured != selectedBackend {
				return errors.New("--backend must match the remote cache URL already configured for this project")
			}
		}
		seen[cfg.ProjectID], ids[i], sources[i] = true, cfg.ProjectID, cfg
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ownedContext, cancelOwned := context.WithCancel(ctx)
	defer cancelOwned()
	var owned []*dashboardBackend
	var refreshWork sync.WaitGroup
	if *serve {
		owned, err = startDashboardBackends(ownedContext, sources)
		if err != nil {
			return err
		}
		defer func() {
			cancelOwned()
			refreshWork.Wait()
			closeDashboardBackends(owned)
		}()
	}
	// Serialize refreshes: credential managers and config refresh must not race
	// when several browser tabs refresh together.
	gate := make(chan struct{}, 1)
	credentials := make([]dashboardCredentials, len(paths))
	for i, source := range sources {
		credentials[i] = dashboardCredentials{source: source, token: source.TeamToken, expiresAt: source.TeamTokenExpiresAt}
	}
	for i, backend := range owned {
		refreshWork.Add(1)
		go func(i int, backend *dashboardBackend) {
			defer refreshWork.Done()
			maintainDashboardBackendCredentials(ownedContext, paths[i], sources[i], backend.runtime, gate, &credentials[i])
		}(i, backend)
	}
	options := dashboard.Options{}
	options.Snapshot = func(ctx context.Context, period time.Duration) dashboard.Snapshot {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		case <-ctx.Done():
			return dashboard.Snapshot{}
		}
		now := time.Now().UTC()
		result := dashboard.Snapshot{Projects: make([]dashboard.Project, len(paths)), UpdatedAt: now, From: now.Add(-period), To: now}
		var work sync.WaitGroup
		for i, path := range paths {
			work.Add(1)
			go func(i int, path string) {
				defer work.Done()
				project := dashboard.Project{ID: ids[i], Local: dashboard.Cache{State: "unavailable", ReportState: "unavailable"}, Team: dashboard.Cache{State: "unavailable", ReportState: "unavailable"}, Cost: dashboard.ReadCost(*costFile, "project", ids[i])}
				defer func() { result.Projects[i] = project }()
				cfg, err := config.Load(path)
				if err != nil || !dashboardConfigurationMatches(sources[i], cfg, selectedBackend, *serve) {
					return
				}
				if selectedBackend == "" {
					project.Local = readDashboardCache(ctx, newLocalCLIHTTPClient(5*time.Second), localRuntimeURL(cfg.Listen), cfg.LocalToken, cfg.ProjectID, result.From, result.To)
					project.Local.PruneAllowed = *allowPrune && project.Local.Status != nil && project.Local.Status.CanPrune && project.Local.Status.Role != "public"
				} else {
					project.Local.State = "not-selected"
				}
				if cfg.TeamURL == "" {
					project.Team = dashboard.Cache{State: "not-connected", ReportState: "unavailable"}
					return
				}
				cfg, err = dashboardTeamCredentials(ctx, cfg, &credentials[i])
				if err != nil {
					project.Team.State = "authentication-required"
					return
				}
				project.Team = readDashboardCache(ctx, newCLIHTTPClient(5*time.Second), cfg.TeamURL, cfg.TeamToken, cfg.ProjectID, result.From, result.To)
				project.Team.PruneAllowed = *allowPrune && project.Team.Status != nil && project.Team.Status.CanPrune && project.Team.Status.Role != "public"
			}(i, path)
		}
		work.Wait()
		return result
	}
	if *allowPrune {
		options.Prune = func(ctx context.Context, request dashboard.PruneRequest) (dashboard.PruneResult, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			select {
			case gate <- struct{}{}:
				defer func() { <-gate }()
			case <-ctx.Done():
				return dashboard.PruneResult{}, dashboard.ErrPruneUnavailable
			}
			if !request.Confirm || request.TargetBytes < 0 || request.Cache != "local" && request.Cache != "team" {
				return dashboard.PruneResult{}, dashboard.ErrPruneInvalid
			}
			index := -1
			for i, id := range ids {
				if id == request.ProjectID {
					index = i
					break
				}
			}
			if index < 0 || selectedBackend != "" && request.Cache == "local" {
				return dashboard.PruneResult{}, dashboard.ErrPruneForbidden
			}
			cfg, err := config.Load(paths[index])
			if err != nil || !dashboardConfigurationMatches(sources[index], cfg, selectedBackend, *serve) {
				return dashboard.PruneResult{}, dashboard.ErrPruneUnavailable
			}
			endpoint, credential := localRuntimeURL(cfg.Listen), cfg.LocalToken
			client := newLocalCLIHTTPClient(30 * time.Second)
			if request.Cache == "team" {
				if cfg.TeamURL == "" {
					return dashboard.PruneResult{}, dashboard.ErrPruneForbidden
				}
				cfg, err = dashboardTeamCredentials(ctx, cfg, &credentials[index])
				if err != nil {
					return dashboard.PruneResult{}, dashboard.ErrPruneForbidden
				}
				endpoint, credential, client = cfg.TeamURL, cfg.TeamToken, newCLIHTTPClient(30*time.Second)
			}
			return pruneDashboardCache(ctx, client, endpoint, credential, cfg.ProjectID, request.TargetBytes)
		}
	}
	handler := dashboard.NewWithOptions(listener.Addr().String(), token, options)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, WriteTimeout: 40 * time.Second}
	go func() { <-ctx.Done(); _ = server.Close() }()
	if _, err := fmt.Fprintf(stdout, "Layer Cache dashboard\nhttp://%s/#%s\nConnected to %d project(s). Keep this process running; press Ctrl-C to stop.\n", listener.Addr(), token, len(paths)); err != nil {
		return err
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func readDashboardCache(ctx context.Context, client *http.Client, endpoint, token, project string, from, to time.Time) dashboard.Cache {
	defer client.CloseIdleConnections()
	result := dashboard.Cache{State: "unavailable", ReportState: "unavailable"}
	if token == "" {
		result.State = "authentication-required"
		return result
	}
	read := func(path string, target any) string {
		request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(endpoint, "/")+path, nil)
		if err != nil {
			return "unavailable"
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-LayerCache-Project", project)
		response, err := client.Do(request)
		if err != nil {
			return "unavailable"
		}
		defer response.Body.Close()
		if response.StatusCode == 401 || response.StatusCode == 403 {
			return "authentication-required"
		}
		if response.StatusCode != 200 {
			return "unavailable"
		}
		limited := &io.LimitedReader{R: response.Body, N: (1 << 20) + 1}
		decoder := json.NewDecoder(limited)
		if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF || limited.N == 0 {
			return "unavailable"
		}
		return "connected"
	}
	var status dashboard.Status
	result.State = read("/v1/status", &status)
	if result.State != "connected" {
		return result
	}
	if status.ProjectID != project {
		result.State = "unavailable"
		return result
	}
	result.Status = &status
	var report measurement.PeriodReport
	query := url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}
	result.ReportState = read("/v1/reports?"+query.Encode(), &report)
	if result.ReportState == "connected" && report.SchemaVersion == measurement.SchemaVersion {
		result.Report = &report
	} else if result.ReportState == "connected" {
		result.ReportState = "unavailable"
	}
	return result
}

// An explicit remote selector stays bound to the configured URL. An owned
// backend also keeps the identity and paths it opened until this process exits.
// Default dashboards retain project-scoped live configuration reloads.
func dashboardConfigurationMatches(original, current config.Config, selectedBackend string, owned bool) bool {
	if current.Role != "local" || current.ProjectID != original.ProjectID {
		return false
	}
	if selectedBackend != "" {
		configured, err := normalizeDashboardBackend(current.TeamURL)
		if err != nil || configured != selectedBackend {
			return false
		}
	}
	return !owned || original.Listen == current.Listen && original.DataDir == current.DataDir && original.LocalToken == current.LocalToken && sameCredentialScope(original, current)
}

func normalizeDashboardBackend(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" || parsed.RawPath != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || strings.TrimSpace(endpoint) != endpoint {
		return "", errors.New("--backend requires the configured HTTP(S) cache URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme == "http" && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return "", errors.New("--backend requires HTTPS except on loopback")
	}
	port := parsed.Port()
	if port == "443" && parsed.Scheme == "https" || port == "80" && parsed.Scheme == "http" {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
		if strings.Contains(host, ":") {
			parsed.Host = "[" + host + "]"
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func dashboardTeamCredentials(ctx context.Context, cfg config.Config, cached *dashboardCredentials) (config.Config, error) {
	disk := cfg
	if cached.source.TeamURL != "" && cached.source.TeamURL != cfg.TeamURL && (cfg.TeamToken == cached.source.TeamToken || cfg.TeamToken == cached.token) {
		// A changed endpoint may obtain a new capability, but must never receive a
		// capability captured for the previous endpoint, including a disk copy.
		cfg.TeamToken, cfg.TeamTokenExpiresAt = "", time.Time{}
	}
	if cached.matches(disk) {
		cfg.TeamToken, cfg.TeamTokenExpiresAt = cached.token, cached.expiresAt
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := refreshTeamCapability(refreshCtx, &cfg); err != nil {
		if cfg.TeamToken == "" || !cfg.TeamTokenExpiresAt.After(time.Now()) {
			return cfg, dashboard.ErrPruneForbidden
		}
	}
	*cached = dashboardCredentials{source: disk, token: cfg.TeamToken, expiresAt: cfg.TeamTokenExpiresAt}
	return cfg, nil
}

func pruneDashboardCache(ctx context.Context, client *http.Client, endpoint, token, project string, target int64) (dashboard.PruneResult, error) {
	defer client.CloseIdleConnections()
	now := time.Now().UTC()
	status := readDashboardCache(ctx, client, endpoint, token, project, now.Add(-time.Hour), now)
	if status.Status == nil || status.Status.Role == "public" || !status.Status.CanPrune {
		return dashboard.PruneResult{}, dashboard.ErrPruneForbidden
	}
	if target < 0 || status.Status.MaxBytes != nil && target > *status.Status.MaxBytes {
		return dashboard.PruneResult{}, dashboard.ErrPruneInvalid
	}
	body, _ := json.Marshal(map[string]int64{"targetBytes": target})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/gc", bytes.NewReader(body))
	if err != nil {
		return dashboard.PruneResult{}, dashboard.ErrPruneUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-LayerCache-Project", project)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return dashboard.PruneResult{}, dashboard.ErrPruneUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return dashboard.PruneResult{}, dashboard.ErrPruneForbidden
	}
	if response.StatusCode != http.StatusOK {
		return dashboard.PruneResult{}, dashboard.ErrPruneUnavailable
	}
	var result dashboard.PruneResult
	limited := &io.LimitedReader{R: response.Body, N: (64 << 10) + 1}
	decoder := json.NewDecoder(limited)
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || limited.N == 0 || result.TargetBytes != target {
		return dashboard.PruneResult{}, dashboard.ErrPruneUnavailable
	}
	return result, nil
}

type dashboardBackend struct {
	runtime  *cacheserver.Server
	server   *http.Server
	listener net.Listener
	done     chan error
}

func startDashboardBackends(ctx context.Context, configurations []config.Config) ([]*dashboardBackend, error) {
	var owned []*dashboardBackend
	for _, cfg := range configurations {
		listener, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			closeDashboardBackends(owned)
			return nil, errors.New("dashboard --serve cannot bind a configured Local backend; an existing runtime is never replaced")
		}
		runtime, err := cacheserver.New(ctx, cfg)
		if err != nil {
			_ = listener.Close()
			closeDashboardBackends(owned)
			return nil, fmt.Errorf("dashboard --serve cannot open its Local backend: %w", err)
		}
		backend := &dashboardBackend{runtime: runtime, listener: listener, done: make(chan error, 1)}
		backend.server = &http.Server{Handler: runtime.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10}
		owned = append(owned, backend)
		go func() { backend.done <- backend.server.Serve(backend.listener) }()
	}
	return owned, nil
}

func closeDashboardBackends(backends []*dashboardBackend) {
	for _, backend := range backends {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = backend.server.Shutdown(ctx)
		cancel()
		_ = backend.server.Close()
		<-backend.done
		_ = backend.runtime.Close()
	}
}

// Refresh only the connection this process owns. The same gate protects browser
// refreshes and mutations from racing a single-use credential rotation.
func maintainDashboardBackendCredentials(ctx context.Context, path string, original config.Config, runtime *cacheserver.Server, gate chan struct{}, credentials *dashboardCredentials) {
	cfg := original
	retry := false
	for {
		delay, refreshable := nextCapabilityRefresh(cfg, time.Now().UTC())
		if !refreshable {
			return
		}
		if retry {
			delay = capabilityRefreshRetry
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		select {
		case <-ctx.Done():
			return
		case gate <- struct{}{}:
		}
		current, err := config.Load(path)
		if err == nil && !dashboardConfigurationMatches(original, current, "", true) {
			<-gate
			return
		}
		if err == nil {
			var refreshed config.Config
			refreshed, err = dashboardTeamCredentials(ctx, current, credentials)
			if err == nil && refreshed.TeamToken != "" {
				err = runtime.UpdateTeamToken(refreshed.TeamToken)
			}
			if err == nil {
				cfg = refreshed
			}
		}
		<-gate
		retry = err != nil
	}
}
