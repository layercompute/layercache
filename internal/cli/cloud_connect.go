package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/credentials"
	"github.com/layercache/layercache/internal/githubauth"
	"github.com/layercache/layercache/internal/portal"
)

type cloudProjects struct {
	Teams    []portal.Team    `json:"teams"`
	Projects []portal.Project `json:"projects"`
}

func cloudGET(ctx context.Context, origin, path, token string, target any) error {
	request, err := http.NewRequestWithContext(ctx, "GET", origin+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := newCLIHTTPClient(controlRequestTimeout).Do(request)
	if err != nil {
		return errors.New("cloud service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("cloud request failed (HTTP %d); sign in and check team membership", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target)
}

func selectCloudProject(list cloudProjects, team, project string) (portal.Project, error) {
	matches := []portal.Project{}
	for _, p := range list.Projects {
		teamMatches := team == "" || p.TeamID == team
		for _, t := range list.Teams {
			if t.ID == p.TeamID && t.Name == team {
				teamMatches = true
			}
		}
		if teamMatches && (project == "" || p.ID == project || p.Name == project) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return portal.Project{}, errors.New("no matching project; create a project or accept your invitation in the cloud dashboard")
	}
	if len(matches) != 1 {
		return portal.Project{}, errors.New("multiple projects match; use --team and --project with IDs from --list")
	}
	p := matches[0]
	if !validCloudProjectID(p.ID) || !validCloudRepository(p.Repository) || !strings.HasPrefix(p.DefaultRef, "refs/heads/") {
		return portal.Project{}, errors.New("cloud returned invalid project metadata")
	}
	return p, nil
}

// Project IDs retain their provisioned spelling when a repository is transferred.
// The service returns the current repository binding separately.
func validCloudProjectID(id string) bool {
	if strings.HasPrefix(id, "github.com/") {
		return validCloudRepository(strings.TrimPrefix(id, "github.com/"))
	}
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, "/\\. %?#:\t\r\n\x00")
}

// GitHub treats ASCII repository spelling case-insensitively. Validate only
// its syntax projection; keep the imported ID and display spelling unchanged.
func validCloudRepository(repository string) bool {
	for index := 0; index < len(repository); index++ {
		if repository[index] >= 128 {
			return false
		}
	}
	return validVMActionsRepository(strings.ToLower(repository))
}

func runConnect(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	origin := flags.String("cloud", "", "cloud dashboard origin (HTTPS, or HTTP loopback)")
	gh := flags.Bool("github-cli", false, "use your existing gh login")
	remoteOnly := flags.Bool("remote-only", false, "use hosted storage directly without a Local daemon or artifact store")
	team := flags.String("team", "", "team ID or name")
	project := flags.String("project", "", "project ID or name")
	path := flags.String("config", "", "new configuration path (existing files are never replaced)")
	list := flags.Bool("list", false, "list accessible teams and projects without writing configuration")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("connect accepts no positional arguments")
	}
	*origin = strings.TrimRight(*origin, "/")
	if err := portal.ValidateOrigin(*origin); err != nil {
		return err
	}
	var token, executable string
	var session githubauth.Session
	if *gh {
		var err error
		executable, err = exec.LookPath("gh")
		if err != nil {
			return errors.New("install GitHub CLI and run gh auth login")
		}
		executable, err = filepath.Abs(executable)
		if err != nil {
			return err
		}
		token, err = githubCLIToken(ctx, executable)
		if err != nil {
			return err
		}
	} else {
		var public struct {
			ClientID string `json:"githubClientId"`
		}
		if err := cloudGET(ctx, *origin, "/api/public-config", "", &public); err != nil {
			return err
		}
		if strings.TrimSpace(public.ClientID) == "" {
			return errors.New("cloud GitHub device login is not configured; use --github-cli after gh auth login. A dashboard password is not a native GitHub credential")
		}
		client := githubauth.DeviceClient{ClientID: public.ClientID, DeviceEndpoint: githubauth.DefaultDeviceEndpoint, TokenEndpoint: githubauth.DefaultTokenEndpoint, HTTPClient: newCLIHTTPClient(controlRequestTimeout)}
		device, err := client.Begin(ctx)
		if err != nil {
			return cloudDeviceError(ctx, "start", err)
		}
		fmt.Fprintf(stderr, "Open %s and enter code %s\n", device.VerificationURI, device.UserCode)
		session, err = client.Poll(ctx, device)
		if err != nil {
			return cloudDeviceError(ctx, "complete", err)
		}
		token = session.AccessToken
	}
	var projects cloudProjects
	if err := cloudGET(ctx, *origin, "/api/cli/projects", token, &projects); err != nil {
		return err
	}
	if *list {
		return json.NewEncoder(stdout).Encode(projects)
	}
	selected, err := selectCloudProject(projects, *team, *project)
	if err != nil {
		return err
	}
	cfg, err := config.Defaults()
	if err != nil {
		return err
	}
	cfg.ProjectID = selected.ID
	cfg.RemoteOnly = *remoteOnly
	cfg.TeamURL = *origin
	cfg.ActionsRepository = selected.Repository
	cfg.ActionsRef = selected.DefaultRef
	cfg.ActionsDefaultRef = selected.DefaultRef
	cfg.GitHubCLIPath = executable
	exchange, err := exchangeGitHubCapability(ctx, cfg, token)
	if err != nil {
		return err
	}
	cfg.TeamToken = exchange.TeamToken
	cfg.TeamTokenExpiresAt = exchange.ExpiresAt
	if *path == "" {
		base, err := config.DefaultPath()
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(*origin))
		projectSum := sha256.Sum256([]byte(selected.ID))
		*path = filepath.Join(filepath.Dir(base), "cloud", hex.EncodeToString(sum[:8]), hex.EncodeToString(projectSum[:16])+".json")
	}
	*path, err = filepath.Abs(*path)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(*path), 0700); err != nil {
		return err
	}
	unlock, err := lockConfiguration(ctx, *path)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = os.Lstat(*path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("configuration already exists or cannot be inspected; choose a new --config path or use login to refresh")
	}
	committed := false
	if *remoteOnly {
		// Reserved location hint only. Cloud-only paths never create this directory.
		sum := sha256.Sum256([]byte(*path))
		cfg.DataDir = filepath.Join(filepath.Dir(*path), "unused-"+hex.EncodeToString(sum[:8]))
	} else {
		// Every Local connection owns a new directory; never touch an existing runtime.
		cfg.DataDir, err = os.MkdirTemp(filepath.Dir(*path), "cache-")
		if err != nil {
			return err
		}
		defer func() {
			if !committed {
				_ = os.RemoveAll(cfg.DataDir)
			}
		}()
		listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
		if listenErr != nil {
			return listenErr
		}
		cfg.Listen = listener.Addr().String()
		_ = listener.Close()
	}
	if !*gh {
		cfg.GitHubCredentialAccount, err = newGitHubCredentialAccount(cfg)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(session)
		if err != nil {
			return err
		}
		if err = (credentials.Store{}).Put(ctx, cfg.GitHubCredentialAccount, string(encoded)); err != nil {
			return fmt.Errorf("cannot protect GitHub session: %w", err)
		}
		defer func() {
			if !committed {
				_ = cleanupFailedLogin(ctx, cfg.GitHubCredentialAccount, errors.New("connect failed"))
			}
		}()
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	if !cfg.RemoteOnly {
		if err = ensureOwnershipMarker(cfg, *path); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(encoded)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(*path)
		return err
	}
	committed = true
	if cfg.RemoteOnly {
		return printResult(stdout, *jsonOutput, map[string]any{"connected": true, "remoteOnly": true, "config": *path, "project": selected.ID, "team": selected.TeamID}, fmt.Sprintf("Connected to %s using hosted storage. Run commands with layercache run --config %s -- COMMAND", selected.Name, shellQuote(*path)))
	}
	return printResult(stdout, *jsonOutput, map[string]any{"connected": true, "config": *path, "project": selected.ID, "team": selected.TeamID}, fmt.Sprintf("Connected to %s. Start Local Cache: layercache start --config %s\nOpen dashboard: layercache dashboard --config %s", selected.Name, shellQuote(*path), shellQuote(*path)))
}

// Provider errors can contain echoed authorization material. Report the failed
// step without forwarding response bodies or token endpoint diagnostics.
func cloudDeviceError(ctx context.Context, step string, cause error) error {
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("could not %s GitHub device authorization; retry or use --github-cli (the OAuth app must enable device flow)", step)
}
