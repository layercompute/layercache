// Package portal owns self-serve teams and projects. Cache byte protocols remain
// in server; this module supplies their durable membership authority.
package portal

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/dashboard"
	"github.com/layercache/layercache/internal/execution"
	"github.com/layercache/layercache/internal/githubauth"
	"github.com/layercache/layercache/internal/measurement"
	"github.com/layercache/layercache/internal/server"
)

//go:embed assets/*
var assets embed.FS

type Config struct {
	PasswordAccountsFile    string
	DashboardCostsFile      string
	Origin                  string
	DataDir                 string
	PostgresURL             string
	GitHubClientID          string
	GitHubClientSecret      string
	SessionKey              []byte
	GitHubAPIURL            string
	GitHubAuthorizeURL      string
	GitHubTokenURL          string
	TeamCreatorIDs          []string
	ProjectTemplate         config.Config
	StoragePool             server.StoragePoolConfig
	HostedStoragePools      map[string]config.Config
	StoragePlans            map[string]StoragePlan
	DefaultStoragePlan      string
	TeamCreatorStoragePlans map[string]string
	OperatorProjects        []OperatorProject
	RegistryURL             string
	RegistryUsername        string
	RegistryPassword        string
}
type Portal struct {
	passwords *passwordStore
	cfg       Config
	store     *Store
	gateway   *server.ProjectGateway
	aead      cipher.AEAD
	client    *http.Client
	mux       *http.ServeMux
	authority string
	secure    bool
	creators  map[string]struct{}
}

// ValidateTeamCreatorIDs keeps beta admission bound to GitHub's immutable
// numeric identity, not a username that another account can later claim.
func ValidateTeamCreatorIDs(ids []string) error {
	if len(ids) == 0 {
		return errors.New("cloud requires at least one teamCreatorIds GitHub user ID")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		parsed, err := strconv.ParseInt(id, 10, 64)
		if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != id {
			return errors.New("teamCreatorIds must contain canonical positive numeric GitHub user IDs")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("teamCreatorIds must not contain duplicate GitHub user IDs")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func ValidateOrigin(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost"))) {
		return errors.New("cloud origin must be HTTPS, or loopback HTTP, without a path")
	}
	return nil
}
func New(ctx context.Context, cfg Config) (*Portal, error) {
	if err := ValidateOrigin(cfg.Origin); err != nil {
		return nil, err
	}
	if err := ValidateTeamCreatorIDs(cfg.TeamCreatorIDs); err != nil {
		return nil, err
	}
	if err := validateOperatorProjects(cfg.OperatorProjects); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(cfg.DataDir) || len(cfg.SessionKey) != 32 {
		return nil, errors.New("cloud requires an absolute data directory and 32-byte session key")
	}
	if (cfg.GitHubClientID == "") != (cfg.GitHubClientSecret == "") || (cfg.GitHubClientID == "" && cfg.PasswordAccountsFile == "") {
		return nil, errors.New("cloud requires complete GitHub OAuth credentials or protected operator password accounts")
	}
	var passwords *passwordStore
	var err error
	if cfg.PasswordAccountsFile != "" {
		passwords, err = newPasswordStore(cfg.PasswordAccountsFile, cfg.OperatorProjects)
		if err != nil {
			return nil, err
		}
	}
	if cfg.GitHubAPIURL == "" {
		cfg.GitHubAPIURL = githubauth.DefaultAPIURL
	}
	if cfg.GitHubAuthorizeURL == "" {
		cfg.GitHubAuthorizeURL = "https://github.com/login/oauth/authorize"
	}
	if cfg.GitHubTokenURL == "" {
		cfg.GitHubTokenURL = githubauth.DefaultTokenEndpoint
	}
	for _, endpoint := range []string{cfg.GitHubAPIURL, cfg.GitHubAuthorizeURL, cfg.GitHubTokenURL} {
		u, err := url.Parse(endpoint)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, ErrInvalid
		}
		origin := u.Scheme + "://" + u.Host
		if err := ValidateOrigin(origin); err != nil {
			return nil, err
		}
	}
	storage, err := newStoragePolicy(cfg)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, errors.New("create cloud data directory")
	}
	if cfg.StoragePool.Path != "" {
		root, rootErr := filepath.EvalSymlinks(cfg.StoragePool.Path)
		data, dataErr := filepath.EvalSymlinks(cfg.DataDir)
		relative, relativeErr := filepath.Rel(root, data)
		if rootErr != nil || dataErr != nil || relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return nil, errors.New("cloud data must be inside its bounded storage pool")
		}
	}
	block, err := aes.NewCipher(cfg.SessionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(ctx, cfg.PostgresURL, filepath.Join(cfg.DataDir, "portal.db"))
	if err != nil {
		return nil, err
	}
	if err := store.configureStorage(ctx, storage); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("restore managed storage assignments: %w", err)
	}
	if err := store.adoptOperatorProjects(ctx, cfg.OperatorProjects); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("adopt operator projects: %w", err)
	}
	origin, _ := url.Parse(cfg.Origin)
	creators := make(map[string]struct{}, len(cfg.TeamCreatorIDs))
	for _, id := range cfg.TeamCreatorIDs {
		creators[id] = struct{}{}
	}
	p := &Portal{passwords: passwords, cfg: cfg, store: store, aead: aead, authority: origin.Host, secure: origin.Scheme == "https", creators: creators, mux: http.NewServeMux(), client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	p.gateway, err = server.NewProjectGateway(ctx, server.ProjectGatewayConfig{Origin: cfg.Origin, Authority: store, StoragePool: cfg.StoragePool, RegistryURL: cfg.RegistryURL, RegistryUsername: cfg.RegistryUsername, RegistryPassword: cfg.RegistryPassword})
	if err != nil {
		store.Close()
		return nil, err
	}
	projects, err := store.AllProjects(ctx)
	if err != nil {
		p.Close()
		return nil, err
	}
	for _, project := range projects {
		if !validRepositoryID(project.RepositoryID) {
			p.Close()
			return nil, fmt.Errorf("managed project %s lacks its original GitHub repository ID; back up portal metadata, then restore the original numeric ID from a trusted record or provision a fresh portal database (never infer it from the current repository name)", project.ID)
		}
		if err = p.ensureProject(ctx, project); err != nil {
			p.Close()
			return nil, errors.New("restore managed project runtime")
		}
	}
	p.routes()
	return p, nil
}
func (p *Portal) Close() error { return errors.Join(p.gateway.Close(), p.store.Close()) }
func (p *Portal) ensureProject(ctx context.Context, project Project) error {
	if project.OperatorManaged {
		for _, adopted := range p.cfg.OperatorProjects {
			if adopted.Config.ProjectID == project.ID {
				return p.gateway.AddStandaloneProject(ctx, operatorAlias(adopted), adopted.Config)
			}
		}
		return errors.New("operator project configuration is missing")
	}
	cfg, err := p.store.projectTemplate(ctx, project)
	if err != nil {
		return err
	}
	cfg.Role, cfg.ProjectID, cfg.LocalToken = "team", project.ID, project.Secret
	cfg.DataDir = filepath.Join(p.cfg.DataDir, "projects", project.ID)
	cfg.ActionsRepository, cfg.ActionsDefaultRef, cfg.ActionsRef = project.Repository, project.DefaultRef, project.DefaultRef
	cfg.GitHubAPIURL = p.cfg.GitHubAPIURL
	cfg.TeamMembers = nil
	cfg.TeamURL, cfg.TeamToken, cfg.PublicURL, cfg.PublicAccessToken = "", "", "", ""
	cfg.GitHubCredentialAccount, cfg.GitHubCLIPath = "", ""
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return errors.New("create project cache directory")
	}
	return p.gateway.AddProject(ctx, project.ID, cfg)
}
func (p *Portal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.Host != p.authority {
		http.Error(w, "invalid cloud host", 403)
		return
	}
	path := r.URL.Path
	if strings.HasPrefix(path, "/v2/") || strings.HasPrefix(path, "/v8/") || strings.HasPrefix(path, "/v1/cache/") || strings.HasPrefix(path, "/v1/gradle-cache/") || strings.HasPrefix(path, "/v1/bazel-cache/") || strings.HasPrefix(path, "/v1/members/") || strings.HasPrefix(path, "/v1/buildkit/promotion-leases/") || path == "/v1/audit" || path == "/v1/sync/turbo" || path == execution.Path || path == "/v1/retained-artifacts" || strings.HasPrefix(path, "/v1/retained-artifacts/") || strings.HasPrefix(path, "/_apis/artifactcache/") || strings.HasPrefix(path, "/_layercache/compatibility/") || path == "/v1/status" || path == "/v1/gc" || strings.HasPrefix(path, "/v1/reports") || path == "/v1/auth/github/exchange" || path == "/v1/auth/github-oidc/exchange" || path == "/v1/auth/snapshot/exchange" {
		p.gateway.ServeHTTP(w, r)
		return
	}
	p.mux.ServeHTTP(w, r)
}
func (p *Portal) routes() {
	files, _ := fs.Sub(assets, "assets")
	static := http.FileServer(http.FS(files))
	p.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { static.ServeHTTP(w, r) })
	// Deep links load the same public shell. Each data request still requires
	// a live session and current project authority. Unknown paths stay 404.
	projectPage := func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("page") {
		case "overview", "activity", "infrastructure", "storage", "settings":
			http.ServeFileFS(w, r, files, "index.html")
		default:
			http.NotFound(w, r)
		}
	}
	p.mux.HandleFunc("GET /projects/{project}/{page}", projectPage)
	p.mux.HandleFunc("GET /teams/{team}/settings", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, files, "index.html")
	})
	for _, path := range []string{"/app.js", "/style.css"} {
		p.mux.Handle("GET "+path, static)
	}
	p.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, map[string]string{"status": "ok"}) })
	p.mux.HandleFunc("GET /api/public-config", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{"githubClientId": p.cfg.GitHubClientID, "passwordLogin": p.passwords != nil})
	})
	p.mux.HandleFunc("POST /auth/password", p.passwordLogin)
	p.mux.Handle("POST /api/password/change", p.auth(p.changePassword))
	p.mux.HandleFunc("GET /auth/github", p.beginLogin)
	p.mux.HandleFunc("GET /auth/callback", p.finishLogin)
	p.mux.HandleFunc("GET /api/cli/projects", p.cliProjects)
	p.mux.Handle("GET /api/session", p.auth(p.sessionInfo))
	p.mux.Handle("POST /api/logout", p.auth(p.logout))
	p.mux.Handle("POST /api/teams", p.auth(p.createTeam))
	p.mux.Handle("POST /api/teams/{team}/projects", p.auth(p.createProject))
	p.mux.Handle("GET /api/teams/{team}/members", p.auth(p.members))
	p.mux.Handle("PUT /api/teams/{team}/members/{user}", p.auth(p.changeMember))
	p.mux.Handle("DELETE /api/teams/{team}/members/{user}", p.auth(p.changeMember))
	p.mux.Handle("POST /api/teams/{team}/invitations", p.auth(p.invite))
	p.mux.Handle("POST /api/invitations/{invite}/accept", p.auth(p.accept))
	p.mux.Handle("GET /api/projects/{project}/snapshot", p.auth(p.projectSnapshot))
	p.mux.Handle("GET /api/teams/{team}/usage", p.auth(p.teamUsage))
	p.mux.Handle("POST /api/projects/{project}/prune", p.auth(p.pruneProject))
	p.mux.Handle("GET /api/projects/{project}/capacity", p.auth(p.projectCapacity))
	p.mux.Handle("PUT /api/projects/{project}/capacity", p.auth(p.changeProjectCapacity))
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, err error) {
	status, message := 500, "cloud request failed"
	switch {
	case errors.Is(err, ErrDenied):
		status, message = 403, "access denied"
	case errors.Is(err, ErrInvalid):
		status, message = 400, "invalid input"
	case errors.Is(err, ErrConflict):
		status, message = 409, err.Error()
	case errors.Is(err, ErrLimit):
		status, message = 409, err.Error()
	}
	jsonResponse(w, status, map[string]string{"error": message})
}
func readJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func (p *Portal) cookieName(suffix string) string {
	if p.secure {
		return "__Host-layercache_" + suffix
	}
	return "layercache_" + suffix
}
func (p *Portal) cookie(w http.ResponseWriter, suffix, value string, seconds int) {
	http.SetCookie(w, &http.Cookie{Name: p.cookieName(suffix), Value: value, Path: "/", MaxAge: seconds, HttpOnly: true, Secure: p.secure, SameSite: http.SameSiteLaxMode})
}
func (p *Portal) auth(next func(http.ResponseWriter, *http.Request, Session)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(p.cookieName("session"))
		if err != nil || len(cookie.Value) != 48 {
			jsonResponse(w, 401, map[string]string{"error": "sign in required"})
			return
		}
		session, err := p.store.Session(r.Context(), cookie.Value)
		if err != nil {
			jsonResponse(w, 401, map[string]string{"error": "sign in required"})
			return
		}
		if err := p.authenticatePasswordSession(&session); err != nil {
			jsonResponse(w, 401, map[string]string{"error": "sign in required"})
			return
		}
		if r.Method != "GET" && (r.Header.Get("Origin") != p.cfg.Origin || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1) {
			fail(w, ErrDenied)
			return
		}
		if session.PasswordChangeRequired && r.URL.Path != "/api/session" && r.URL.Path != "/api/password/change" && r.URL.Path != "/api/logout" {
			jsonResponse(w, 403, map[string]string{"error": "change your initial password before using the dashboard"})
			return
		}
		next(w, r, session)
	})
}
func (p *Portal) encrypt(value string) string {
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic("system random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(p.aead.Seal(nonce, nonce, []byte(value), []byte(p.cfg.Origin)))
}
func (p *Portal) decrypt(value string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) < p.aead.NonceSize() {
		return "", ErrDenied
	}
	decoded, err := p.aead.Open(nil, data[:p.aead.NonceSize()], data[p.aead.NonceSize():], []byte(p.cfg.Origin))
	return string(decoded), err
}
func (p *Portal) beginLogin(w http.ResponseWriter, r *http.Request) {
	if p.cfg.GitHubClientID == "" {
		http.NotFound(w, r)
		return
	}
	state, verifier := randomID(""), randomID("")
	if err := p.store.SaveOAuth(r.Context(), state, verifier); err != nil {
		fail(w, err)
		return
	}
	p.cookie(w, "oauth", state, 600)
	hash := sha256.Sum256([]byte(verifier))
	u, _ := url.Parse(p.cfg.GitHubAuthorizeURL)
	query := url.Values{"client_id": {p.cfg.GitHubClientID}, "redirect_uri": {p.cfg.Origin + "/auth/callback"}, "scope": {"read:user repo"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}}
	u.RawQuery = query.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}
func (p *Portal) finishLogin(w http.ResponseWriter, r *http.Request) {
	if p.cfg.GitHubClientID == "" {
		http.NotFound(w, r)
		return
	}
	states, codes := r.URL.Query()["state"], r.URL.Query()["code"]
	cookie, err := r.Cookie(p.cookieName("oauth"))
	p.cookie(w, "oauth", "", -1)
	if err != nil || len(states) != 1 || len(codes) != 1 || len(codes[0]) > 1024 || len(states[0]) != 48 || subtle.ConstantTimeCompare([]byte(states[0]), []byte(cookie.Value)) != 1 {
		fail(w, ErrDenied)
		return
	}
	verifier, err := p.store.ConsumeOAuth(r.Context(), states[0])
	if err != nil {
		fail(w, ErrDenied)
		return
	}
	form := url.Values{"client_id": {p.cfg.GitHubClientID}, "client_secret": {p.cfg.GitHubClientSecret}, "code": {codes[0]}, "redirect_uri": {p.cfg.Origin + "/auth/callback"}, "code_verifier": {verifier}}
	request, err := http.NewRequestWithContext(r.Context(), "POST", p.cfg.GitHubTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		fail(w, err)
		return
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	tokenExchangeStartedAt := time.Now()
	response, err := p.client.Do(request)
	if err != nil {
		fail(w, ErrDenied)
		return
	}
	defer response.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   *int64 `json:"expires_in"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&token) != nil || token.AccessToken == "" {
		fail(w, ErrDenied)
		return
	}
	// GitHub OAuth Apps can issue eight-hour access tokens. A browser session
	// must not remain valid after its stored provider token has expired.
	sessionSeconds := int64(12 * 60 * 60)
	if token.ExpiresIn != nil {
		const expiryMarginSeconds = int64(60)
		if *token.ExpiresIn <= expiryMarginSeconds {
			fail(w, ErrDenied)
			return
		}
		if remaining := *token.ExpiresIn - expiryMarginSeconds; remaining < sessionSeconds {
			sessionSeconds = remaining
		}
	}
	// Start the lifetime before the exchange so provider and database latency
	// cannot extend it. SQLite and PostgreSQL persist expiry at second precision.
	sessionDeadline := tokenExchangeStartedAt.Add(time.Duration(sessionSeconds) * time.Second).Truncate(time.Second)
	identity, err := githubauth.VerifyUser(r.Context(), p.cfg.GitHubAPIURL, token.AccessToken)
	if err != nil {
		fail(w, ErrDenied)
		return
	}
	user := User{ID: userID(identity.ID), Login: identity.Login}
	if err = p.store.UpsertUser(r.Context(), user); err != nil {
		fail(w, err)
		return
	}
	cookieSeconds := int(time.Until(sessionDeadline) / time.Second)
	if cookieSeconds <= 0 {
		fail(w, ErrDenied)
		return
	}
	sessionToken := randomID("")
	session := Session{User: user, CSRF: randomID(""), EncryptedToken: p.encrypt(token.AccessToken), ExpiresAt: sessionDeadline.Unix()}
	if err = p.store.SaveSession(r.Context(), sessionToken, session); err != nil {
		fail(w, err)
		return
	}
	cookieSeconds = int(time.Until(sessionDeadline) / time.Second)
	if cookieSeconds <= 0 {
		_ = p.store.DeleteSession(r.Context(), sessionToken)
		fail(w, ErrDenied)
		return
	}
	p.cookie(w, "session", sessionToken, cookieSeconds)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func (p *Portal) sessionInfo(w http.ResponseWriter, r *http.Request, session Session) {
	if session.PasswordChangeRequired {
		// First-login access is limited to identity and password rotation.
		// Do not query or disclose workspace metadata before that gate passes.
		jsonResponse(w, 200, map[string]any{"user": session.User, "csrfToken": session.CSRF, "authMethod": session.AuthMethod, "passwordChangeRequired": true, "canChangePassword": true, "canManageGitHub": false, "canCreateTeam": false, "teams": []Team{}, "projects": []Project{}, "invitations": []Invitation{}})
		return
	}
	teams, err := p.dashboardTeams(r.Context(), session)
	if err != nil {
		fail(w, err)
		return
	}
	projects, err := p.dashboardProjects(r.Context(), session)
	if err != nil {
		fail(w, err)
		return
	}
	invitations := []Invitation{}
	if session.AuthMethod != "password" {
		invitations, err = p.store.Invitations(r.Context(), session.User.ID)
		if err != nil {
			fail(w, err)
			return
		}
	}
	_, canCreateTeam := p.creators[session.User.ID]
	jsonResponse(w, 200, map[string]any{"user": session.User, "csrfToken": session.CSRF, "teams": teams, "projects": projects, "invitations": invitations, "cloudOrigin": p.cfg.Origin, "canCreateTeam": canCreateTeam && session.AuthMethod != "password", "authMethod": session.AuthMethod, "passwordChangeRequired": session.PasswordChangeRequired, "canChangePassword": session.AuthMethod == "password", "canManageGitHub": session.AuthMethod != "password"})
}
func (p *Portal) cliProjects(w http.ResponseWriter, r *http.Request) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || len(values[0]) > 16<<10 {
		fail(w, ErrDenied)
		return
	}
	identity, err := githubauth.VerifyUser(r.Context(), p.cfg.GitHubAPIURL, strings.TrimPrefix(values[0], "Bearer "))
	if err != nil {
		fail(w, ErrDenied)
		return
	}
	user := User{ID: userID(identity.ID), Login: identity.Login}
	if err = p.store.UpsertUser(r.Context(), user); err != nil {
		fail(w, err)
		return
	}
	projects, err := p.store.Projects(r.Context(), user.ID)
	if err != nil {
		fail(w, err)
		return
	}
	teams, err := p.store.Teams(r.Context(), user.ID)
	if err != nil {
		fail(w, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"user": user, "teams": teams, "projects": projects})
}
func (p *Portal) logout(w http.ResponseWriter, r *http.Request, session Session) {
	cookie, _ := r.Cookie(p.cookieName("session"))
	if err := p.store.DeleteSession(r.Context(), cookie.Value); err != nil {
		fail(w, err)
		return
	}
	p.cookie(w, "session", "", -1)
	w.WriteHeader(204)
}
func (p *Portal) createTeam(w http.ResponseWriter, r *http.Request, session Session) {
	if !passwordProviderAllowed(w, session) {
		return
	}
	if _, allowed := p.creators[session.User.ID]; !allowed {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "team creation is limited to invited beta creators"})
		return
	}
	var input struct {
		Name string `json:"name"`
		Kind string `json:"kind,omitempty"`
	}
	if err := readJSON(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	var team Team
	var err error
	switch input.Kind {
	case "", "team":
		team, err = p.store.CreateTeam(r.Context(), session.User.ID, strings.TrimSpace(input.Name))
	case "personal":
		team, err = p.store.CreatePersonalCache(r.Context(), session.User.ID)
	default:
		err = ErrInvalid
	}
	if err != nil {
		fail(w, err)
		return
	}
	jsonResponse(w, 201, team)
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9_.-]{1,100}$`)
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func (p *Portal) githubGET(ctx context.Context, token, path string, result any) error {
	request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(p.cfg.GitHubAPIURL, "/")+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := p.client.Do(request)
	if err != nil {
		return ErrDenied
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(result) != nil {
		return ErrDenied
	}
	return nil
}
func (p *Portal) createProject(w http.ResponseWriter, r *http.Request, session Session) {
	if !passwordProviderAllowed(w, session) {
		return
	}
	var input struct {
		Name       string `json:"name"`
		Repository string `json:"repository"`
	}
	if err := readJSON(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	input.Repository = strings.ToLower(strings.TrimSpace(input.Repository))
	if !repositoryPattern.MatchString(input.Repository) || strings.Contains(input.Repository, "..") {
		fail(w, ErrInvalid)
		return
	}
	token, err := p.decrypt(session.EncryptedToken)
	if err != nil {
		fail(w, ErrDenied)
		return
	}
	var repository struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Permissions   struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	if err = p.githubGET(r.Context(), token, "/repos/"+input.Repository, &repository); err != nil || repository.ID <= 0 || !repository.Permissions.Admin || !strings.EqualFold(repository.FullName, input.Repository) || repository.DefaultBranch == "" || strings.ContainsAny(repository.DefaultBranch, "\x00\r\n") {
		jsonResponse(w, 403, map[string]string{"error": "GitHub repository administrator access is required; sign in again if access changed"})
		return
	}
	project, err := p.store.CreateProject(r.Context(), session.User.ID, r.PathValue("team"), strings.TrimSpace(input.Name), input.Repository, strconv.FormatInt(repository.ID, 10), "refs/heads/"+repository.DefaultBranch)
	if err != nil {
		fail(w, err)
		return
	}
	if err = p.ensureProject(r.Context(), project); err != nil {
		jsonResponse(w, 503, map[string]string{"error": "project saved; cache provisioning will retry when you open it"})
		return
	}
	jsonResponse(w, 201, project)
}
func (p *Portal) members(w http.ResponseWriter, r *http.Request, session Session) {
	if !passwordProviderAllowed(w, session) {
		return
	}
	members, err := p.store.Members(r.Context(), session.User.ID, r.PathValue("team"))
	if err != nil {
		fail(w, err)
		return
	}
	jsonResponse(w, 200, members)
}
func (p *Portal) changeMember(w http.ResponseWriter, r *http.Request, session Session) {
	if !passwordProviderAllowed(w, session) {
		return
	}
	var input struct {
		Role string `json:"role"`
	}
	if r.Method != "DELETE" {
		if err := readJSON(w, r, &input); err != nil {
			fail(w, err)
			return
		}
		if !validRole(input.Role) {
			fail(w, ErrInvalid)
			return
		}
	}
	if err := p.store.ChangeMember(r.Context(), session.User.ID, r.PathValue("team"), r.PathValue("user"), input.Role); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
func (p *Portal) invite(w http.ResponseWriter, r *http.Request, session Session) {
	if !passwordProviderAllowed(w, session) {
		return
	}
	var input struct {
		Login string `json:"login"`
		Role  string `json:"role"`
	}
	if err := readJSON(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	if !loginPattern.MatchString(input.Login) || !validRole(input.Role) {
		fail(w, ErrInvalid)
		return
	}
	token, err := p.decrypt(session.EncryptedToken)
	if err != nil {
		fail(w, ErrDenied)
		return
	}
	var target githubauth.User
	if err = p.githubGET(r.Context(), token, "/users/"+url.PathEscape(input.Login), &target); err != nil || target.ID <= 0 {
		fail(w, ErrDenied)
		return
	}
	invitation, err := p.store.Invite(r.Context(), session.User.ID, r.PathValue("team"), strconv.FormatInt(target.ID, 10), input.Role)
	if err != nil {
		fail(w, err)
		return
	}
	jsonResponse(w, 201, invitation)
}
func (p *Portal) accept(w http.ResponseWriter, r *http.Request, session Session) {
	if !passwordProviderAllowed(w, session) {
		return
	}
	if err := p.store.Accept(r.Context(), session.User.ID, r.PathValue("invite")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}

type capture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *capture) Header() http.Header { return c.header }
func (c *capture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}
func (c *capture) Write(data []byte) (int, error) {
	if c.status == 0 {
		c.status = 200
	}
	if c.body.Len()+len(data) > 1<<20 {
		return 0, errors.New("snapshot exceeds limit")
	}
	return c.body.Write(data)
}
func (p *Portal) dashboardProject(r *http.Request, session Session) (*Project, error) {
	projects, err := p.dashboardProjects(r.Context(), session)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if projects[i].ID == r.PathValue("project") {
			return &projects[i], nil
		}
	}
	return nil, ErrDenied
}

func (p *Portal) dashboardTeam(r *http.Request, session Session, id string) (*Team, error) {
	teams, err := p.dashboardTeams(r.Context(), session)
	if err != nil {
		return nil, err
	}
	for i := range teams {
		if teams[i].ID == id {
			return &teams[i], nil
		}
	}
	return nil, ErrDenied
}

func (p *Portal) projectSnapshot(w http.ResponseWriter, r *http.Request, session Session) {
	selected, err := p.dashboardProject(r, session)
	if err != nil {
		fail(w, err)
		return
	}
	team, err := p.dashboardTeam(r, session, selected.TeamID)
	if err != nil {
		fail(w, err)
		return
	}
	if err = p.ensureProject(r.Context(), *selected); err != nil {
		jsonResponse(w, 503, map[string]string{"error": "project cache is unavailable"})
		return
	}
	now := time.Now().UTC()
	subject := "github-id:" + session.User.ID
	capabilities := []access.Capability{access.CapabilityRead}
	if selected.OperatorManaged {
		subject = "github:" + strings.ToLower(session.User.Login)
		if selected.Role == "admin" {
			capabilities = []access.Capability{access.CapabilityAdmin}
		}
	}
	token, err := access.MintCapabilityToken(selected.Secret, access.Claims{Subject: subject, Project: selected.ID, Capabilities: capabilities, ExpiresAt: now.Add(time.Minute)}, now)
	if err != nil {
		fail(w, err)
		return
	}
	read := func(path string, target any) bool {
		req, _ := http.NewRequestWithContext(r.Context(), "GET", p.cfg.Origin+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		out := &capture{header: http.Header{}}
		p.gateway.ServeHTTP(out, req)
		return out.status == 200 && json.Unmarshal(out.body.Bytes(), target) == nil
	}
	cache := dashboard.Cache{State: "unavailable", ReportState: "unavailable"}
	var status dashboard.Status
	if read("/v1/status", &status) {
		cache.State = "connected"
		if !selected.OperatorManaged {
			status.CanPrune = selected.Role == "admin"
		}
		cache.Status = &status
	}
	period := 24 * time.Hour
	if r.URL.Query().Get("period") == "7d" {
		period = 7 * 24 * time.Hour
	} else if value := r.URL.Query().Get("period"); value != "" && value != "24h" {
		fail(w, ErrInvalid)
		return
	}
	var report measurement.PeriodReport
	query := url.Values{"from": {now.Add(-period).Format(time.RFC3339Nano)}, "to": {now.Format(time.RFC3339Nano)}}
	if read("/v1/reports?"+query.Encode(), &report) {
		cache.Report = &report
		cache.ReportState = "connected"
	}
	projection := map[string]any{"project": selected, "scope": team.Kind, "cache": cache, "cost": dashboard.ReadCost(p.cfg.DashboardCostsFile, "project", selected.ID), "updatedAt": now}
	var inventory execution.Inventory
	if read(execution.Path+"?period="+url.QueryEscape(r.URL.Query().Get("period")), &inventory) {
		projection["executions"] = inventory
	}
	jsonResponse(w, 200, projection)
}

// teamUsage projects only the current member's team allocation. Artifact and
// measurement observations can overlap, so they are not summed into team usage.
func (p *Portal) teamUsage(w http.ResponseWriter, r *http.Request, session Session) {
	team, err := p.dashboardTeam(r, session, r.PathValue("team"))
	if err != nil {
		fail(w, err)
		return
	}
	projects, err := p.dashboardProjects(r.Context(), session)
	if err != nil {
		fail(w, err)
		return
	}
	count, allocated := 0, int64(0)
	complete := true
	const maximumSafeAllocation = int64(1<<53 - 1)
	for _, project := range projects {
		if project.TeamID != team.ID {
			continue
		}
		count++
		// Legacy runtime capacities are not reserved storage allocations. A
		// partial or JavaScript-unsafe sum must not appear as the team's total.
		if !validStorageID(project.StoragePoolID) || project.AllocatedCacheBytes <= 0 || project.AllocatedCacheBytes > maximumSafeAllocation-allocated {
			complete = false
			continue
		}
		allocated += project.AllocatedCacheBytes
	}
	var total *int64
	if complete {
		total = &allocated
	}
	jsonResponse(w, 200, map[string]any{
		"teamId": team.ID, "scope": team.Kind, "projectCount": count,
		"allocatedCacheBytes": total, "allocationComplete": complete, "cost": dashboard.ReadCost(p.cfg.DashboardCostsFile, "team", team.ID),
	})
}

func (p *Portal) pruneProject(w http.ResponseWriter, r *http.Request, session Session) {
	selected, err := p.dashboardProject(r, session)
	if err != nil {
		fail(w, err)
		return
	}
	if selected.Role != "admin" {
		fail(w, ErrDenied)
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		fail(w, ErrInvalid)
		return
	}
	defer r.Body.Close()
	var input struct {
		TargetBytes *int64 `json:"targetBytes"`
		Confirm     bool   `json:"confirm"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.TargetBytes == nil || *input.TargetBytes < 0 || !input.Confirm {
		fail(w, ErrInvalid)
		return
	}
	if err := p.ensureProject(r.Context(), *selected); err != nil {
		jsonResponse(w, 503, map[string]string{"error": "project cache is unavailable"})
		return
	}
	now := time.Now().UTC()
	subject := "github-id:" + session.User.ID
	if selected.OperatorManaged {
		subject = "github:" + strings.ToLower(session.User.Login)
	}
	token, err := access.MintCapabilityToken(selected.Secret, access.Claims{
		Subject: subject, Project: selected.ID,
		Capabilities: []access.Capability{access.CapabilityAdmin}, ExpiresAt: now.Add(time.Minute),
	}, now)
	if err != nil {
		jsonResponse(w, 503, map[string]string{"error": "cache pruning could not be confirmed; refresh cache status"})
		return
	}
	body, _ := json.Marshal(map[string]int64{"targetBytes": *input.TargetBytes})
	request, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, p.cfg.Origin+"/v1/gc", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	out := &capture{header: http.Header{}}
	p.gateway.ServeHTTP(out, request)
	var result dashboard.PruneResult
	if out.status == http.StatusBadRequest {
		fail(w, ErrInvalid)
		return
	}
	if out.status != http.StatusOK || json.Unmarshal(out.body.Bytes(), &result) != nil {
		jsonResponse(w, 503, map[string]string{"error": "cache pruning could not be confirmed; refresh cache status"})
		return
	}
	jsonResponse(w, http.StatusOK, result)
}
