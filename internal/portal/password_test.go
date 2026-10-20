//go:build linux || darwin

package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sys/unix"
)

const initialTestPassword = "fixture-initial-password-123"
const nextTestPassword = "fixture-replacement-password-456"

var passwordHashOnce sync.Once
var passwordTestHash string

func testPasswordFile(t *testing.T, mustChange bool) (string, []OperatorProject) {
	t.Helper()
	passwordHashOnce.Do(func() {
		hash, err := bcrypt.GenerateFromPassword([]byte(initialTestPassword), passwordCost)
		if err != nil {
			panic(err)
		}
		passwordTestHash = string(hash)
	})
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(directory, "accounts.json")
	data, err := json.Marshal(passwordFile{Accounts: []PasswordAccount{{Email: "alice@example.test", User: User{ID: "1", Login: "alice"}, PasswordHash: passwordTestHash, MustChangePassword: mustChange}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	return filename, []OperatorProject{{Owner: User{ID: "1", Login: "alice"}}}
}
func newPasswordPortal(t *testing.T, mustChange bool) (*portalQA, string, []OperatorProject) {
	t.Helper()
	q := newPortalQA(t)
	filename, owners := testPasswordFile(t, mustChange)
	var err error
	q.p.passwords, err = newPasswordStore(filename, owners)
	if err != nil {
		t.Fatal(err)
	}
	q.p.cfg.GitHubClientID, q.p.cfg.GitHubClientSecret = "", ""
	return q, filename, owners
}
func passwordRequest(q *portalQA, path, email, password string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return q.request("POST", path, string(body), nil, map[string]string{"Origin": q.cfg.Origin, "Content-Type": "application/json"})
}
func passwordBrowser(t *testing.T, q *portalQA, password string) *browserQA {
	t.Helper()
	response := passwordRequest(q, "/auth/password", "alice@example.test", password)
	if response.Code != 200 {
		t.Fatalf("password login failed: %d %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %d", len(cookies))
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge <= 0 || cookie.MaxAge > 43200 {
		t.Fatal("session cookie protections missing")
	}
	browser := &browserQA{cookie: cookie}
	info := q.request("GET", "/api/session", "", browser, nil)
	var session struct {
		CSRF string `json:"csrfToken"`
	}
	if json.Unmarshal(info.Body.Bytes(), &session) != nil || session.CSRF == "" {
		t.Fatal("session info missing csrf")
	}
	browser.csrf = session.CSRF
	return browser
}
func TestPasswordLoginOriginStrictInputAndGenericFailure(t *testing.T) {
	q, _, _ := newPasswordPortal(t, false)
	for _, test := range []struct {
		body, origin, contentType string
		status                    int
	}{
		{`{"email":"alice@example.test","password":"fixture-initial-password-123"}`, "https://foreign.example", "application/json", 403},
		{`{"email":"alice@example.test","password":"fixture-initial-password-123"}`, "", "application/json", 403},
		{`{"email":"alice@example.test","password":"fixture-initial-password-123","extra":true}`, q.cfg.Origin, "application/json", 400},
		{`{"email":"alice@example.test","password":"first-password","password":"fixture-initial-password-123"}`, q.cfg.Origin, "application/json", 400},
		{`{"email":"alice@example.test","password":"fixture-initial-password-123"}{}`, q.cfg.Origin, "application/json", 400},
		{`{"email":"alice@example.test","password":"fixture-initial-password-123"}`, q.cfg.Origin, "text/plain", 400},
		{`{"email":"alice@example.test","password":"` + strings.Repeat("a", 5000) + `"}`, q.cfg.Origin, "application/json", 400},
	} {
		response := q.request("POST", "/auth/password", test.body, nil, map[string]string{"Origin": test.origin, "Content-Type": test.contentType})
		if response.Code != test.status {
			t.Fatalf("status=%d want%d", response.Code, test.status)
		}
	}
	wrong := passwordRequest(q, "/auth/password", "alice@example.test", "wrong-valid-length-password")
	unknown := passwordRequest(q, "/auth/password", "nobody@example.test", "wrong-valid-length-password")
	if wrong.Code != 401 || unknown.Code != 401 || wrong.Body.String() != unknown.Body.String() {
		t.Fatal("unknown and wrong-password failures differ")
	}
	info := q.request("GET", "/api/public-config", "", nil, nil)
	if !strings.Contains(info.Body.String(), `"passwordLogin":true`) || !strings.Contains(info.Body.String(), `"githubClientId":""`) {
		t.Fatal("auth modes are inaccurate")
	}
	for _, path := range []string{"/auth/github", "/auth/callback?state=invalid&code=invalid"} {
		if response := q.request("GET", path, "", nil, nil); response.Code != 404 {
			t.Fatal("password-only mode exposed GitHub sign-in")
		}
	}
}
func TestPasswordRotationInvalidatesPriorSessionsAndSurvivesReload(t *testing.T) {
	q, filename, owners := newPasswordPortal(t, true)
	first := passwordBrowser(t, q, initialTestPassword)
	second := passwordBrowser(t, q, initialTestPassword)
	adopted := operatorTestProject(t)
	if err := q.p.store.adoptOperatorProjects(context.Background(), []OperatorProject{adopted}); err != nil {
		t.Fatal(err)
	}
	q.p.cfg.OperatorProjects = []OperatorProject{adopted}
	team := Team{ID: adopted.TeamID}
	project := Project{ID: adopted.Config.ProjectID}
	info := q.request("GET", "/api/session", "", first, nil)
	if strings.Contains(info.Body.String(), team.ID) || strings.Contains(info.Body.String(), project.ID) || strings.Contains(info.Body.String(), "Private") || strings.Contains(info.Body.String(), "acme/private-repository") || !strings.Contains(info.Body.String(), `"projects":[]`) || !strings.Contains(info.Body.String(), `"teams":[]`) || !strings.Contains(info.Body.String(), `"invitations":[]`) {
		t.Fatal("initial password disclosed workspace metadata")
	}

	if !strings.Contains(info.Body.String(), `"passwordChangeRequired":true`) || !strings.Contains(info.Body.String(), `"canManageGitHub":false`) || !strings.Contains(info.Body.String(), `"canCreateTeam":false`) {
		t.Fatal("initial session capability state incorrect")
	}
	if got := q.request("GET", "/api/teams/anything/usage", "", first, nil).Code; got != 403 {
		t.Fatal("initial password could access workspace")
	}
	currentSession, err := q.p.store.Session(context.Background(), first.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(currentSession.EncryptedToken, "alice@example.test") || strings.Contains(currentSession.EncryptedToken, initialTestPassword) {
		t.Fatal("session credential material is not encrypted")
	}
	body := `{"currentPassword":"` + initialTestPassword + `","newPassword":"` + nextTestPassword + `"}`
	if got := q.request("POST", "/api/password/change", body, first, map[string]string{"Content-Type": "application/json", "X-CSRF-Token": "bad"}).Code; got != 403 {
		t.Fatal("password change accepted bad CSRF")
	}
	wrong := `{"currentPassword":"wrong-current-password","newPassword":"` + nextTestPassword + `"}`
	if got := q.request("POST", "/api/password/change", wrong, first, map[string]string{"Content-Type": "application/json"}).Code; got != 403 {
		t.Fatal("password changed without current password")
	}
	tooLong := `{"currentPassword":"` + initialTestPassword + `","newPassword":"` + strings.Repeat("é", 37) + `"}`
	if got := q.request("POST", "/api/password/change", tooLong, first, map[string]string{"Content-Type": "application/json"}).Code; got != 400 {
		t.Fatal("password accepted beyond bcrypt byte limit")
	}
	changed := q.request("POST", "/api/password/change", body, first, map[string]string{"Content-Type": "application/json"})
	if changed.Code != 200 || len(changed.Result().Cookies()) != 1 {
		t.Fatalf("rotation failed: %d %s", changed.Code, changed.Body.String())
	}
	replacement := &browserQA{cookie: changed.Result().Cookies()[0]}
	for _, old := range []*browserQA{first, second} {
		if got := q.request("GET", "/api/session", "", old, nil).Code; got != 401 {
			t.Fatal("old password session survived rotation")
		}
	}
	if got := passwordRequest(q, "/auth/password", "alice@example.test", initialTestPassword).Code; got != 401 {
		t.Fatal("old password remains valid")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), nextTestPassword) || strings.Contains(string(data), initialTestPassword) {
		t.Fatal("password persisted as plaintext")
	}
	stat, err := os.Stat(filename)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("replacement password file is not private")
	}
	reloaded, err := newPasswordStore(filename, owners)
	if err != nil {
		t.Fatal(err)
	}
	q.p.passwords = reloaded
	restored := q.request("GET", "/api/session", "", replacement, nil)
	if !strings.Contains(restored.Body.String(), project.ID) || !strings.Contains(restored.Body.String(), team.ID) {
		t.Fatal("workspace metadata missing after password rotation")
	}
	if restored.Code != 200 || !strings.Contains(restored.Body.String(), `"passwordChangeRequired":false`) {
		t.Fatal("rotated credential/session did not survive reload")
	}
	passwordBrowser(t, q, nextTestPassword)
	if _, err = q.p.store.db.Exec(`UPDATE lc_portal_sessions SET expires_at=$1 WHERE id=$2`, time.Now().Add(-time.Second).Unix(), tokenHash(replacement.cookie.Value)); err != nil {
		t.Fatal(err)
	}
	if got := q.request("GET", "/api/session", "", replacement, nil).Code; got != 401 {
		t.Fatal("expired password session remains valid")
	}
}
func TestPasswordSessionsCannotUseProviderManagementOrIgnoreRevocation(t *testing.T) {
	q, filename, owners := newPasswordPortal(t, false)
	browser := passwordBrowser(t, q, initialTestPassword)
	for _, test := range []struct{ method, path, body string }{
		{"POST", "/api/teams", `{"name":"No"}`},
		{"POST", "/api/teams/anything/projects", `{"name":"No","repository":"acme/alpha"}`},
		{"GET", "/api/teams/anything/members", ""},
		{"PUT", "/api/teams/anything/members/2", `{"role":"writer"}`},
		{"DELETE", "/api/teams/anything/members/2", ""},
		{"POST", "/api/teams/anything/invitations", `{"login":"bob","role":"reader"}`},
		{"POST", "/api/invitations/anything/accept", ""},
	} {
		response := q.request(test.method, test.path, test.body, browser, nil)
		if response.Code != 403 {
			t.Fatalf("provider management %s %s status%d", test.method, test.path, response.Code)
		}
	}
	q.mu.Lock()
	exchanges := q.exchanges
	q.mu.Unlock()
	if exchanges != 0 {
		t.Fatal("password marker reached OAuth provider")
	}
	// Removing the protected account revokes sessions without waiting for expiry.
	data, _ := json.Marshal(passwordFile{Accounts: []PasswordAccount{{Email: "other@example.test", User: owners[0].Owner, PasswordHash: passwordTestHash}}})
	if err := writePasswordAccountsFile(filename, data); err != nil {
		t.Fatal(err)
	}
	if got := q.request("GET", "/api/session", "", browser, nil).Code; got != 401 {
		t.Fatal("removed account retained session")
	}
}
func TestPasswordAccountFilesFailClosedAndOwnersAreExact(t *testing.T) {
	filename, owners := testPasswordFile(t, false)
	if _, err := newPasswordStore(filename, []OperatorProject{{Owner: User{ID: "1", Login: "different"}}}); err == nil {
		t.Fatal("owner login mismatch accepted")
	}
	if _, err := newPasswordStore(filename, nil); err == nil {
		t.Fatal("unadopted identity accepted")
	}
	if err := os.Chmod(filename, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newPasswordStore(filename, owners); err == nil {
		t.Fatal("public hash file accepted")
	}
	os.Chmod(filename, 0600)
	link := filepath.Join(filepath.Dir(filename), "linked.json")
	if err := os.Link(filename, link); err != nil {
		t.Fatal(err)
	}
	if _, err := newPasswordStore(filename, owners); err == nil {
		t.Fatal("hardlinked hash file accepted")
	}
	os.Remove(link)
	if err := os.Symlink(filename, link); err != nil {
		t.Fatal(err)
	}
	if _, err := newPasswordStore(link, owners); err == nil {
		t.Fatal("symlinked hash file accepted")
	}
	os.Remove(link)
	fifo := filepath.Join(filepath.Dir(filename), "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newPasswordStore(fifo, owners); err == nil {
		t.Fatal("FIFO password file accepted")
	}
	if err := os.Chmod(filepath.Dir(filename), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := newPasswordStore(filename, owners); err == nil {
		t.Fatal("public password directory accepted")
	}
}
func TestPasswordRateLimitsBoundMemoryAndBcryptConcurrency(t *testing.T) {
	filename, owners := testPasswordFile(t, false)
	store, err := newPasswordStore(filename, owners)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "https://cloud.example/auth/password", nil)
	for attempt := range 9 {
		allowed := store.allow(request, "alice@example.test")
		if allowed != (attempt < 8) {
			t.Fatalf("email attempt%d allowed%t", attempt, allowed)
		}
	}
	store.limits = map[string]passwordWindow{}
	store.global = passwordWindow{}
	for i := range 4 {
		request.RemoteAddr = string(rune('a'+i)) + ":123"
		if !store.acquire(httptest.NewRecorder(), request, string(rune('a'+i))+"@example.test") {
			t.Fatal("available bcrypt worker refused")
		}
	}
	request.RemoteAddr = "last:123"
	response := httptest.NewRecorder()
	if store.acquire(response, request, "last@example.test") || response.Code != 429 {
		t.Fatal("bcrypt concurrency exceeded bound")
	}
	for range 4 {
		<-store.work
	}
	store.global = passwordWindow{started: time.Now()}
	store.limits = map[string]passwordWindow{}
	for i := range 2047 {
		store.limits[string(rune(i+1))] = passwordWindow{started: time.Now()}
	}
	if store.allow(request, "new@example.test") || len(store.limits) != 2047 {
		t.Fatal("rate-limit key memory is unbounded")
	}
}

func TestPasswordSessionScopesImportedProjectsWithoutBroadeningGitHubIdentity(t *testing.T) {
	q, _, _ := newPasswordPortal(t, false)
	// The same verified identity can also sign in with GitHub and administer
	// ordinary or personal projects. A password grants only its adopted scope.
	q.p.cfg.GitHubClientID, q.p.cfg.GitHubClientSecret = q.cfg.GitHubClientID, q.cfg.GitHubClientSecret
	oauth := q.login("alice")
	q.login("bob")
	ordinaryTeam := q.team(oauth, "Ordinary team")
	ordinary := q.project(oauth, ordinaryTeam, "acme/ordinary-private")
	personalTeam, err := q.p.store.CreatePersonalCache(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	personal := q.project(oauth, personalTeam, "acme/personal-private")
	own := operatorTestProject(t)
	foreign := operatorTestProject(t)
	foreign.Config.ProjectID, foreign.Config.ActionsRepository = "github.com/acme/foreign-import", "acme/foreign-import"
	foreign.Owner, foreign.TeamID, foreign.TeamName = User{ID: "2", Login: "bob"}, "operator-foreign", "Foreign imported team"
	configured := []OperatorProject{own, foreign}
	if err = q.p.store.adoptOperatorProjects(context.Background(), configured); err != nil {
		t.Fatal(err)
	}
	q.p.cfg.OperatorProjects = configured
	// Native/portal membership alone must not widen the password's operator
	// allowlist, even when that global identity is an administrator.
	if _, err = q.p.store.db.Exec(`INSERT INTO lc_portal_members(team_id,user_id,role) VALUES($1,'1','admin')`, foreign.TeamID); err != nil {
		t.Fatal(err)
	}
	bobTeam, err := q.p.store.CreateTeam(context.Background(), "2", "Invitation-only private team")
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := q.p.store.Invite(context.Background(), "2", bobTeam.ID, "1", "reader")
	if err != nil {
		t.Fatal(err)
	}
	// Defend even against a later unrelated project sharing an allowed team.
	const unconfiguredID = "unconfigured-operator-project"
	if _, err = q.p.store.db.Exec(`INSERT INTO lc_portal_projects(id,team_id,name,repository,repository_id,default_ref,secret) SELECT $1,$2,'Unconfigured private project','acme/unconfigured','456',default_ref,secret FROM lc_portal_projects WHERE id=$3`, unconfiguredID, own.TeamID, ordinary.ID); err != nil {
		t.Fatal(err)
	}
	password := passwordBrowser(t, q, initialTestPassword)
	response := q.request("GET", "/api/session", "", password, nil)
	requireStatus(t, response, 200)
	view := decodeQA[struct {
		Teams       []Team       `json:"teams"`
		Projects    []Project    `json:"projects"`
		Invitations []Invitation `json:"invitations"`
	}](t, response)
	if len(view.Teams) != 1 || view.Teams[0].ID != own.TeamID || len(view.Projects) != 1 || view.Projects[0].ID != own.Config.ProjectID || len(view.Invitations) != 0 {
		t.Fatalf("password scope escaped adopted owner: %+v", view)
	}
	for _, hidden := range []string{ordinary.ID, personal.ID, foreign.Config.ProjectID, foreign.TeamID, unconfiguredID, invitation.ID, bobTeam.ID} {
		if strings.Contains(response.Body.String(), hidden) {
			t.Fatalf("session exposed unrelated resource %q", hidden)
		}
	}
	for _, id := range []string{ordinary.ID, personal.ID, foreign.Config.ProjectID, unconfiguredID} {
		path := "/api/projects/" + url.PathEscape(id)
		requireStatus(t, q.request("GET", path+"/snapshot", "", password, nil), 403)
		requireStatus(t, q.request("POST", path+"/prune", `{"targetBytes":0,"confirm":true}`, password, map[string]string{"Content-Type": "application/json"}), 403)
	}
	for _, id := range []string{ordinaryTeam.ID, personalTeam.ID, foreign.TeamID, bobTeam.ID} {
		requireStatus(t, q.request("GET", "/api/teams/"+id+"/usage", "", password, nil), 403)
		requireStatus(t, q.request("GET", "/api/teams/"+id+"/members", "", password, nil), 403)
	}
	usage := q.request("GET", "/api/teams/"+own.TeamID+"/usage", "", password, nil)
	requireStatus(t, usage, 200)
	if count := decodeQA[struct {
		ProjectCount int `json:"projectCount"`
	}](t, usage).ProjectCount; count != 1 {
		t.Fatalf("allowed team counted unrelated projects: %d", count)
	}
	// The shared guard used before snapshots and pruning still permits the
	// exact configured project. Native SQL authority is tested separately.
	req := httptest.NewRequest("GET", q.cfg.Origin+"/fixture", nil)
	req.SetPathValue("project", own.Config.ProjectID)
	if selected, err := q.p.dashboardProject(req, Session{User: own.Owner, AuthMethod: "password"}); err != nil || selected.ID != own.Config.ProjectID {
		t.Fatal("owned configured project was denied", err)
	}
	// Cookies never substitute for the native GitHub bearer on cache/CLI APIs.
	requireStatus(t, q.request("GET", "/api/cli/projects", "", password, nil), 403)
	requireStatus(t, q.request("GET", "/v1/status", "", password, map[string]string{"X-LayerCache-Project": ordinary.ID}), 401)
	requireStatus(t, q.request("GET", "/v8/artifacts/anything", "", password, map[string]string{"X-LayerCache-Project": ordinary.ID}), 404)
	// The same user's existing OAuth session keeps its ordinary access.
	full := q.request("GET", "/api/session", "", oauth, nil)
	requireStatus(t, full, 200)
	for _, visible := range []string{ordinary.ID, personal.ID, foreign.Config.ProjectID, invitation.ID} {
		if !strings.Contains(full.Body.String(), visible) {
			t.Fatalf("OAuth lost existing resource %q", visible)
		}
	}
	for _, test := range []struct{ path string }{
		{"/api/projects/" + ordinary.ID + "/snapshot"},
		{"/api/projects/" + personal.ID + "/snapshot"},
		{"/api/teams/" + ordinaryTeam.ID + "/usage"},
		{"/api/teams/" + ordinaryTeam.ID + "/members"},
	} {
		requireStatus(t, q.request("GET", test.path, "", oauth, nil), 200)
	}
	token := q.capability(ordinary, "alice")
	nativeHeaders := map[string]string{"Authorization": "Bearer " + token}
	requireStatus(t, q.request("PUT", "/v8/artifacts/password-scope-control", "still-private", nil, nativeHeaders), 200)
	requireStatus(t, q.request("POST", "/api/projects/"+ordinary.ID+"/prune", `{"targetBytes":0,"confirm":true}`, password, map[string]string{"Content-Type": "application/json"}), 403)
	intact := q.request("GET", "/v8/artifacts/password-scope-control", "", nil, nativeHeaders)
	requireStatus(t, intact, 200)
	if intact.Body.String() != "still-private" {
		t.Fatal("denied password prune altered native bytes")
	}
	requireStatus(t, q.request("POST", "/api/projects/"+ordinary.ID+"/prune", `{"targetBytes":0,"confirm":true}`, oauth, map[string]string{"Content-Type": "application/json"}), 200)
	// Membership removal still wins over a static operator allowlist.
	if _, err = q.p.store.db.Exec(`DELETE FROM lc_portal_members WHERE team_id=$1 AND user_id='1'`, own.TeamID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("GET", "/api/teams/"+own.TeamID+"/usage", "", password, nil), 403)
	requireStatus(t, q.request("GET", "/api/projects/"+url.PathEscape(own.Config.ProjectID)+"/snapshot", "", password, nil), 403)
}
