package portal

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePersonalCacheOwnerIsolationAndMembershipMutations(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	personal, err := s.CreatePersonalCache(ctx, "1")
	if err != nil || personal.Kind != "personal" {
		t.Fatalf("create personal cache: %+v, %v", personal, err)
	}
	project := mustProject(t, s, "1", personal.ID, "example/repo")
	shared := mustTeam(t, s, "1")
	mustJoin(t, s, "1", shared.ID, "2", "admin")
	sharedProject := mustProject(t, s, "1", shared.ID, "example/repo")
	if sharedProject.ID == project.ID || sharedProject.Secret == project.Secret {
		t.Fatal("personal and shared projects reused identity or credentials")
	}
	_, err = s.CreatePersonalCache(ctx, "1")
	requireErr(t, err, ErrConflict)
	_, err = s.Invite(ctx, "1", personal.ID, "2", "admin")
	requireErr(t, err, ErrDenied)
	requireErr(t, s.ChangeMember(ctx, "1", personal.ID, "1", "reader"), ErrDenied)
	// Owner identity remains authoritative even if a stray member/invitation
	// record appears. Such records must not turn a personal cache into a team.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO lc_portal_members(team_id,user_id,role) VALUES($1,'2','admin')`, personal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO lc_portal_invites(id,team_id,user_id,role,expires_at) VALUES('stray',$1,'3','admin',$2)`, personal.ID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	projects, err := s.Projects(ctx, "2")
	if err != nil || len(projects) != 1 || projects[0].ID != sharedProject.ID {
		t.Fatalf("non-owner projects: %+v, %v", projects, err)
	}
	teams, err := s.Teams(ctx, "2")
	if err != nil || len(teams) != 1 || teams[0].ID != shared.ID {
		t.Fatalf("non-owner scopes: %+v, %v", teams, err)
	}
	_, err = s.MemberRole(ctx, project.ID, "github-id:2")
	requireErr(t, err, ErrDenied)
	_, err = s.Members(ctx, "2", personal.ID)
	requireErr(t, err, ErrDenied)
	_, err = s.CreateProject(ctx, "2", personal.ID, "Forbidden", "example/private", "123", "refs/heads/main")
	requireErr(t, err, ErrDenied)
	members, err := s.Members(ctx, "1", personal.ID)
	if err != nil || len(members) != 1 || members[0].UserID != "1" {
		t.Fatalf("personal members: %+v, %v", members, err)
	}
	invitations, err := s.Invitations(ctx, "3")
	if err != nil || len(invitations) != 0 {
		t.Fatalf("personal invitations: %+v, %v", invitations, err)
	}
	requireErr(t, s.Accept(ctx, "3", "stray"), ErrDenied)
	for _, actor := range []string{"", "2", "01", "1.0"} {
		_, err := s.GitHubActionsRole(ctx, project.ID, actor)
		requireErr(t, err, ErrDenied)
	}
	if role, err := s.GitHubActionsRole(ctx, project.ID, "1"); err != nil || role != "writer" {
		t.Fatalf("owner CI: %q, %v", role, err)
	}
	if role, err := s.GitHubActionsRole(ctx, sharedProject.ID, ""); err != nil || role != "writer" {
		t.Fatalf("existing team CI: %q, %v", role, err)
	}
}

func TestPersonalCacheAPIAndSameRepositoryArtifactsRemainPrivate(t *testing.T) {
	q := newPortalQA(t)
	alice, bob := q.login("alice"), q.login("bob")
	requireStatus(t, q.request("POST", "/api/teams", `{"kind":"personal"}`, bob, nil), 403)
	requireStatus(t, q.request("POST", "/api/teams", `{"kind":"public"}`, alice, nil), 400)
	response := q.request("POST", "/api/teams", `{"kind":"personal"}`, alice, nil)
	requireStatus(t, response, 201)
	personal := decodeQA[Team](t, response)
	privateProject := q.project(alice, personal, "acme/alpha")
	shared := q.team(alice, "Shared")
	sharedProject := q.project(alice, shared, "acme/alpha")
	mustJoin(t, q.p.store, "1", shared.ID, "2", "writer")
	requireStatus(t, q.request("POST", "/api/teams", `{"kind":"personal"}`, alice, nil), 409)
	requireStatus(t, q.request("POST", "/api/teams/"+personal.ID+"/invitations", `{"login":"bob","role":"reader"}`, alice, nil), 403)
	privateToken := q.capability(privateProject, "alice")
	sharedToken := q.capability(sharedProject, "bob")
	paths := []string{"/v8/artifacts/full-context", "/v1/cache/full-context", "/v1/gradle-cache/full-context"}
	for _, path := range paths {
		requireStatus(t, q.request("PUT", path, "private-output", nil, personalNativeCacheHeaders(path, privateToken)), 200)
		requireStatus(t, q.request("GET", path, "", nil, personalNativeCacheHeaders(path, sharedToken)), 404)
		requireStatus(t, q.request("PUT", path, "shared-output", nil, personalNativeCacheHeaders(path, sharedToken)), 200)
	}
	if err := q.p.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	q.p, err = New(context.Background(), q.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		for _, tc := range []struct{ token, expected string }{{privateToken, "private-output"}, {sharedToken, "shared-output"}} {
			response := q.request("GET", path, "", nil, personalNativeCacheHeaders(path, tc.token))
			requireStatus(t, response, 200)
			if response.Body.String() != tc.expected {
				t.Fatalf("%s artifact after restart = %q; want %q", path, response.Body.String(), tc.expected)
			}
		}
	}
	requireStatus(t, q.request("GET", "/api/projects/"+privateProject.ID+"/snapshot", "", bob, nil), 403)
	body, err := json.Marshal(map[string]string{"project": privateProject.ID, "githubToken": "github-bob", "compatibility": "linux-amd64-schema1", "repository": "acme/alpha", "ref": "refs/heads/main", "defaultRef": "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("POST", "/v1/auth/github/exchange", string(body), nil, nil), 401)
}

func TestPersonalCacheOIDCRequiresOwnerAndPreservesPRReaderPolicy(t *testing.T) {
	q := newPortalQA(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "portal-qa", "kty": "RSA", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}})
	}))
	defer issuer.Close()
	q.cfg.ProjectTemplate.GitHubOIDCIssuer = issuer.URL
	if err := q.p.Close(); err != nil {
		t.Fatal(err)
	}
	q.p, err = New(context.Background(), q.cfg)
	if err != nil {
		t.Fatal(err)
	}
	alice := q.login("alice")
	response := q.request("POST", "/api/teams", `{"kind":"personal"}`, alice, nil)
	requireStatus(t, response, 201)
	project := q.project(alice, decodeQA[Team](t, response), "acme/alpha")
	exchange := func(integration, actorID, event, ref string) *httptest.ResponseRecorder {
		t.Helper()
		now := time.Now().UTC()
		claims := map[string]any{
			"iss": issuer.URL, "aud": "layercache:" + project.ID,
			"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
			"sub": "repo:acme/alpha:ref:" + ref, "repository": "acme/alpha", "repository_id": "123",
			"ref": ref, "sha": strings.Repeat("a", 40), "actor_id": actorID,
			"event_name": event, "run_id": "123", "run_attempt": "1", "check_run_id": "456",
		}
		body, err := json.Marshal(map[string]string{"project": project.ID, "compatibility": "linux-amd64-schema1", "integration": integration, "idToken": signPortalOIDCTestToken(t, key, claims)})
		if err != nil {
			t.Fatal(err)
		}
		return q.request("POST", "/v1/auth/github-oidc/exchange", string(body), nil, nil)
	}
	type issuedCapability struct {
		token, path string
		deniedRead  int
	}
	var issued []issuedCapability
	for _, protocol := range []struct {
		integration, path       string
		deniedRead, deniedWrite int
	}{{"turbo", "/v8/artifacts/", 404, 401}, {"nx", "/v1/cache/", 401, 403}, {"gradle", "/v1/gradle-cache/", 401, 403}} {
		t.Run(protocol.integration, func(t *testing.T) {
			for _, actor := range []string{"", "2", "invalid"} {
				requireStatus(t, exchange(protocol.integration, actor, "push", "refs/heads/main"), 401)
			}
			response := exchange(protocol.integration, "1", "push", "refs/heads/main")
			requireStatus(t, response, 200)
			writer := decodeQA[struct {
				Token string `json:"token"`
			}](t, response).Token
			headers := personalNativeCacheHeaders(protocol.path, writer)
			path := protocol.path + "owner-ci"
			requireStatus(t, q.request("PUT", path, "owner-output", nil, headers), 200)
			response = exchange(protocol.integration, "1", "pull_request", "refs/pull/1/merge")
			requireStatus(t, response, 200)
			reader := decodeQA[struct {
				Token string `json:"token"`
			}](t, response).Token
			headers = personalNativeCacheHeaders(protocol.path, reader)
			requireStatus(t, q.request("GET", path, "", nil, headers), 200)
			requireStatus(t, q.request("PUT", protocol.path+"owner-pr", "forbidden", nil, headers), protocol.deniedWrite)
			for _, token := range []string{writer, reader} {
				issued = append(issued, issuedCapability{token, path, protocol.deniedRead})
			}
		})
	}
	// Recheck the personal delegation on every request, including issued tokens.
	if _, err := q.p.store.db.Exec(`UPDATE lc_portal_teams SET created_by='2' WHERE id=$1`, project.TeamID); err != nil {
		t.Fatal(err)
	}
	for _, capability := range issued {
		requireStatus(t, q.request("GET", capability.path, "", nil, personalNativeCacheHeaders(capability.path, capability.token)), capability.deniedRead)
	}
	if _, err := q.p.store.GitHubActionsRole(context.Background(), project.ID, "1"); !errors.Is(err, ErrDenied) {
		t.Fatalf("former owner CI = %v", err)
	}
}

func personalNativeCacheHeaders(path, token string) map[string]string {
	authorization := "Bearer " + token
	if strings.HasPrefix(path, "/v1/gradle-cache/") {
		authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte("layercache:"+token))
	}
	return map[string]string{"Authorization": authorization}
}

func TestLegacyTeamKindMigrationPreservesMembershipAndRestart(t *testing.T) {
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE lc_portal_teams (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_by TEXT NOT NULL, revision BIGINT NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO lc_portal_teams(id,name,created_by) VALUES('existing','Existing','1')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(ctx, "", filename)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []User{{"1", "owner"}, {"2", "member"}} {
		if err := store.UpsertUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO lc_portal_members(team_id,user_id,role) VALUES('existing','1','admin'),('existing','2','reader')`); err != nil {
		t.Fatal(err)
	}
	personal, err := store.CreatePersonalCache(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(ctx, "", filename)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, user := range []string{"1", "2"} {
		teams, err := store.Teams(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		foundShared, foundPersonal := false, false
		for _, team := range teams {
			foundShared = foundShared || team.ID == "existing" && team.Kind == "team"
			foundPersonal = foundPersonal || team.ID == personal.ID && team.Kind == "personal"
		}
		if !foundShared || foundPersonal != (user == "1") {
			t.Fatalf("migrated scopes for %s: %+v", user, teams)
		}
	}
}
