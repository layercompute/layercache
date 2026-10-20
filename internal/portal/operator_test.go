package portal

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/server"
)

func operatorTestProject(t *testing.T) OperatorProject {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Role, cfg.ProjectID, cfg.DataDir = "team", "github.com/acme/alpha", t.TempDir()
	cfg.ActionsRepository, cfg.ActionsRef, cfg.ActionsDefaultRef = "acme/alpha", "refs/heads/main", "refs/heads/main"
	cfg.CloudPostgresURL = "postgres://fixture:fixture-password@127.0.0.1/cache?search_path=public&sslmode=disable"
	cfg.CloudS3Endpoint, cfg.CloudS3Bucket, cfg.CloudS3Region = "https://example.r2.cloudflarestorage.com", "fixture-cache", "auto"
	cfg.CloudS3AccessKey, cfg.CloudS3SecretKey = "fixture-key", "fixture-secret"
	cfg.TeamMembers = map[string]string{"alice": "admin", "bob": "writer"}
	return OperatorProject{Config: cfg, Name: "Example app", RepositoryID: "123", Owner: User{ID: "1", Login: "alice"}, TeamID: "operator-example", TeamName: "Example projects"}
}

func TestStoreOperatorAdoptionPreservesIdentityAndNeverRegrantsMembership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := operatorTestProject(t)
	for range 2 {
		if err := s.adoptOperatorProjects(ctx, []OperatorProject{p}); err != nil {
			t.Fatal(err)
		}
	}
	projects, err := s.Projects(ctx, "1")
	if err != nil || len(projects) != 1 {
		t.Fatal("missing adopted project", err)
	}
	got := projects[0]
	if got.ID != p.Config.ProjectID || got.Secret != p.Config.LocalToken || got.RepositoryID != "123" || !got.OperatorManaged {
		t.Fatal("adoption changed original identity")
	}
	teams, err := s.Teams(ctx, "1")
	if err != nil || len(teams) != 1 || !teams[0].OperatorManaged {
		t.Fatal("operator team is not projected", err)
	}
	if _, err := s.CreateProject(ctx, "1", p.TeamID, "New", "acme/new", "456", "refs/heads/main"); !errors.Is(err, ErrDenied) {
		t.Fatal("operator team allowed unmanaged project creation", err)
	}
	if err := s.ChangeMember(ctx, "1", p.TeamID, "1", "reader"); !errors.Is(err, ErrDenied) {
		t.Fatal("operator authority could be edited in the portal", err)
	}
	// Simulate removal by the separate owner of dashboard membership. Restart
	// must not recreate an enrollment simply because the config still exists.
	if _, err := s.db.Exec(`DELETE FROM lc_portal_members WHERE team_id=$1 AND user_id='1'`, p.TeamID); err != nil {
		t.Fatal(err)
	}
	if err := s.adoptOperatorProjects(ctx, []OperatorProject{p}); err != nil {
		t.Fatal(err)
	}
	projects, err = s.Projects(ctx, "1")
	if err != nil || len(projects) != 0 {
		t.Fatal("restart resurrected removed dashboard membership", err)
	}
	var events int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM lc_portal_audit WHERE action='project.operator-adopt'`).Scan(&events); err != nil || events != 1 {
		t.Fatal("adoption was not idempotent", events, err)
	}
}

func TestStoreOperatorAdoptionRejectsDriftAndAllowsCredentialRotation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := operatorTestProject(t)
	if err := s.adoptOperatorProjects(ctx, []OperatorProject{p}); err != nil {
		t.Fatal(err)
	}
	rotated := p
	rotated.Config.CloudPostgresURL = "postgres://new-role:new-password@127.0.0.1/cache?search_path=public&sslmode=disable"
	rotated.Config.CloudS3AccessKey, rotated.Config.CloudS3SecretKey = "rotated-key", "rotated-secret"
	rotated.Config.TeamMembers = map[string]string{"nobody": "admin"}
	if err := s.adoptOperatorProjects(ctx, []OperatorProject{rotated}); err != nil {
		t.Fatal("same backend credential rotation rejected", err)
	}
	for _, mutate := range []func(*OperatorProject){
		func(p *OperatorProject) { p.Config.LocalToken = "replacement-signing-key" },
		func(p *OperatorProject) { p.Config.CloudS3Bucket = "other-bucket" },
		func(p *OperatorProject) { p.RepositoryID = "456" },
		func(p *OperatorProject) { p.Config.MaxBytes++ },
		func(p *OperatorProject) { p.Owner.ID = "2" },
		func(p *OperatorProject) { p.TeamID = "other-team" },
	} {
		changed := p
		mutate(&changed)
		if err := s.adoptOperatorProjects(ctx, []OperatorProject{changed}); err == nil {
			t.Fatal("accepted changed operator binding")
		}
	}
	if err := s.adoptOperatorProjects(ctx, nil); err == nil {
		t.Fatal("silently removed adopted runtime configuration")
	}
	if err := s.adoptOperatorProjects(ctx, []OperatorProject{p, p}); err == nil {
		t.Fatal("duplicate operator identities accepted")
	}
}

func TestStoreOperatorAdoptionPreservesRepositoryCase(t *testing.T) {
	s := testStore(t)
	p := operatorTestProject(t)
	p.Config.ActionsRepository = "ExampleOrg/App"
	if err := s.adoptOperatorProjects(context.Background(), []OperatorProject{p}); err != nil {
		t.Fatal(err)
	}
	projects, err := s.Projects(context.Background(), "1")
	if err != nil || len(projects) != 1 || projects[0].Repository != "ExampleOrg/App" {
		t.Fatal("adoption rewrote the original repository selector", err)
	}
}

func TestStoreOperatorRuntimePreservesNativeBytesAndSQLRevocation(t *testing.T) {
	postgresURL := os.Getenv("LAYERCACHE_PORTAL_TEST_POSTGRES_URL")
	if postgresURL == "" {
		t.Skip("requires the disposable portal PostgreSQL fixture")
	}
	metadata := testStore(t)
	var schema string
	if err := metadata.db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(postgresURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	s3 := newPortalS3(t)
	q := newPortalQA(t)
	p := operatorTestProject(t)
	p.Config.CloudPostgresURL = parsed.String()
	p.Config.CloudS3Endpoint, p.Config.CloudS3UsePathStyle = s3.server.URL, true
	p.Config.GitHubAPIURL = q.github.URL
	ctx := context.Background()
	original, err := server.New(ctx, p.Config)
	if err != nil {
		t.Fatal(err)
	}
	mint := func(login string, capability access.Capability) string {
		now := time.Now().UTC()
		token, err := access.MintCapabilityToken(p.Config.LocalToken, access.Claims{Subject: "github:" + login, Project: p.Config.ProjectID, Compatibility: "linux-amd64-schema1", Capabilities: []access.Capability{capability}, ExpiresAt: now.Add(time.Hour)}, now)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	writer, reader := mint("alice", access.CapabilityAdmin), mint("bob", access.CapabilityRead)
	request := httptest.NewRequest("PUT", "https://original.example/v8/artifacts/reused-key", strings.NewReader("original-immutable-bytes"))
	request.Header.Set("Authorization", "Bearer "+writer)
	response := httptest.NewRecorder()
	original.Handler().ServeHTTP(response, request)
	requireStatus(t, response, 200)
	cloudConfig := cloud.Config{PostgresURL: parsed.String(), S3Endpoint: s3.server.URL, S3Bucket: p.Config.CloudS3Bucket, S3Region: "auto", S3AccessKey: p.Config.CloudS3AccessKey, S3SecretKey: p.Config.CloudS3SecretKey, S3PathStyle: true, Project: p.Config.ProjectID, MaxBytes: p.Config.MaxBytes}
	store, err := cloud.Open(ctx, cloudConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveMember(ctx, "fixture-operator", "github:bob"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	// The gateway-only rollout uses the identical preservation constructor before
	// browser OAuth is configured. Original capabilities keep their exact keys.
	gateway, err := server.NewProjectGateway(ctx, server.ProjectGatewayConfig{Origin: q.cfg.Origin, Projects: map[string]config.Config{"alpha": p.Config}, PreserveExistingMemberships: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token  string
		status int
	}{{writer, 200}, {reader, 404}} {
		r := httptest.NewRequest("GET", q.cfg.Origin+"/v8/artifacts/reused-key", nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		out := httptest.NewRecorder()
		gateway.ServeHTTP(out, r)
		requireStatus(t, out, tc.status)
	}
	gateway.Close()
	q.p.Close()
	q.cfg.OperatorProjects = []OperatorProject{p}
	q.p, err = New(ctx, q.cfg)
	if err != nil {
		t.Fatal(err)
	}
	alice, bob := q.login("alice"), q.login("bob")
	path := "/api/projects/" + url.PathEscape(p.Config.ProjectID) + "/snapshot"
	for range 2 {
		for _, tc := range []struct {
			token  string
			status int
		}{{writer, 200}, {reader, 404}} {
			got := q.request("GET", "/v8/artifacts/reused-key", "", nil, map[string]string{"Authorization": "Bearer " + tc.token})
			requireStatus(t, got, tc.status)
			if tc.status == 200 && got.Body.String() != "original-immutable-bytes" {
				t.Fatal("adoption changed cache bytes")
			}
		}
		snapshot := q.request("GET", path, "", alice, nil)
		requireStatus(t, snapshot, 200)
		if !strings.Contains(snapshot.Body.String(), `"state":"connected"`) || !strings.Contains(snapshot.Body.String(), `"canPrune":true`) {
			t.Fatal("encoded legacy ID dashboard failed", snapshot.Body.String())
		}
		requireStatus(t, q.request("GET", path, "", bob, nil), 403)
		requireStatus(t, q.request("POST", "/api/teams/"+p.TeamID+"/invitations", `{"login":"bob","role":"admin"}`, alice, nil), 403)
		q.p.Close()
		q.p, err = New(ctx, q.cfg)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Portal administrator enrollment is not native cache authority. A native
	// downgrade and then revocation take effect without changing portal roles.
	store, err = cloud.OpenExisting(ctx, cloudConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMember(ctx, "fixture-operator", cloud.Member{Project: p.Config.ProjectID, Subject: "github:alice", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	snapshot := q.request("GET", path, "", alice, nil)
	requireStatus(t, snapshot, 200)
	if !strings.Contains(snapshot.Body.String(), `"state":"connected"`) || !strings.Contains(snapshot.Body.String(), `"canPrune":false`) {
		t.Fatal("portal role overrode native downgrade", snapshot.Body.String())
	}
	requireStatus(t, q.request("POST", "/api/projects/"+url.PathEscape(p.Config.ProjectID)+"/prune", `{"targetBytes":0,"confirm":true}`, alice, map[string]string{"Content-Type": "application/json"}), 503)
	if err := store.RemoveMember(ctx, "fixture-operator", "github:alice"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	snapshot = q.request("GET", path, "", alice, nil)
	requireStatus(t, snapshot, 200)
	if strings.Contains(snapshot.Body.String(), `"state":"connected"`) {
		t.Fatal("portal owner bypassed native revocation", snapshot.Body.String())
	}
	q.p.Close()
	missing := p.Config
	missing.ProjectID, missing.DataDir = "github.com/acme/missing", t.TempDir()
	if candidate, err := server.NewWithExistingMembership(ctx, missing); err == nil {
		candidate.Close()
		t.Fatal("adoption initialized an absent project")
	}
	var count int
	if err := metadata.db.QueryRow(`SELECT COUNT(*) FROM layercache_projects_v1 WHERE project_id=$1`, missing.ProjectID).Scan(&count); err != nil || count != 0 {
		t.Fatal("missing project was created", count, err)
	}
}

func TestPortalRegistryUsesExistingGatewayAuthentication(t *testing.T) {
	requests := 0
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		user, password, ok := r.BasicAuth()
		if !ok || user != "backend" || password != "fixture-registry-secret" {
			t.Error("missing protected backend authentication")
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		_, _ = w.Write([]byte(`{"schemaVersion":2}`))
	}))
	defer registry.Close()
	q := newPortalQA(t, func(cfg *Config) {
		cfg.RegistryURL, cfg.RegistryUsername, cfg.RegistryPassword = registry.URL, "backend", "fixture-registry-secret"
	})
	alice := q.login("alice")
	team := q.team(alice, "Team")
	p := q.project(alice, team, "acme/alpha")
	token := q.capability(p, "alice")
	path := "/v2/" + p.ID + "/manifests/fixture"
	requireStatus(t, q.request("GET", path, "", nil, nil), 401)
	if requests != 0 {
		t.Fatal("unauthenticated portal registry request reached backend")
	}
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(p.ID+":"+token))
	requireStatus(t, q.request("GET", path, "", nil, map[string]string{"Authorization": auth}), 200)
	if requests != 1 {
		t.Fatal("authorized native registry request was not forwarded")
	}
	requireStatus(t, q.request("GET", "/v2/foreign/manifests/fixture", "", nil, map[string]string{"Authorization": auth}), 401)
	if requests != 1 {
		t.Fatal("foreign registry path reached backend")
	}
}
