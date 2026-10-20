package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/execution"
	"github.com/layercache/layercache/internal/measurement"
)

type executionMembership struct{ role string }

func (a *executionMembership) MemberRole(context.Context, string, string) (string, error) {
	return a.role, nil
}
func (a *executionMembership) RepositoryID(context.Context, string) (string, error) {
	return "123", nil
}
func (a *executionMembership) GitHubActionsRole(context.Context, string, string) (string, error) {
	return a.role, nil
}

func executionRouteFixture(t *testing.T) (*Server, *measurement.SQLiteRepository, access.Claims) {
	t.Helper()
	server, repo, claims := turboReportFixture(t)
	var err error
	server.executions, err = repo.ExecutionRepository(context.Background(), server.config.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	claims.Execution = &execution.Context{Kind: "githubActions", OwnerID: "github-id:42", ExecutionID: "github-actions:123:456:1:456", RunnerEnvironment: "self-hosted"}
	return server, repo, claims
}
func executionRequestFixture() execution.ObservationRequest {
	return execution.ObservationRequest{Version: 1, Phase: "start", Provider: "ubicloud", Sample: execution.Sample{OS: "linux", Arch: "amd64", CPU: execution.CPU{LimitKnowledge: "unknown"}, Memory: execution.Memory{LimitKnowledge: "unknown"}, Filesystems: []execution.Filesystem{}}}
}
func executionCall(t *testing.T, server *Server, claims access.Claims, method string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	token, err := access.MintCapabilityToken(server.config.LocalToken, claims, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, execution.Path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	server.Handler().ServeHTTP(out, request)
	return out
}

func TestExecutionReadOnlyJobReportsOnlyOwnAuthorityAndPreservesNativeContexts(t *testing.T) {
	server, repo, claims := executionRouteFixture(t)
	request := executionRequestFixture()
	body, _ := json.Marshal(request)
	for range 2 {
		out := executionCall(t, server, claims, "POST", body)
		if out.Code != 201 {
			t.Fatalf("readonly report %d %s", out.Code, out.Body.String())
		}
	}
	// A later Actions setup in this same signed job has a workflow-level native
	// run ID. It must not erase the earlier Turbo check-scoped association.
	now := time.Now().UTC().Add(-time.Second)
	if err := repo.ObserveTurbo(measurement.TurboObservation{RunID: claims.RunID, ArtifactID: "opaque-artifact", Result: measurement.ResultMiss, Source: measurement.SourceNone, StartedAt: now, FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	actions := claims
	actions.Integration = "actions"
	actions.RunID = "github-actions:acme/widget:123:1"
	if out := executionCall(t, server, actions, "POST", body); out.Code != 201 {
		t.Fatalf("second integration %d %s", out.Code, out.Body.String())
	}
	out := executionCall(t, server, claims, "GET", nil)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var inventory execution.Inventory
	if err := json.Unmarshal(out.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Jobs) != 1 || inventory.Jobs[0].CacheActivity != "observed" || inventory.Jobs[0].Provider != "ubicloud" || inventory.Jobs[0].ProviderEvidence != "declared" {
		t.Fatalf("job inventory %+v", inventory)
	}
	for _, forbidden := range []string{claims.RunID, claims.WorkspaceID, server.config.LocalToken, "opaque-artifact"} {
		if strings.Contains(out.Body.String(), forbidden) {
			t.Fatalf("inventory leaked private transport field %q", forbidden)
		}
	}
	// The observation route never turns a read-only PR credential into a writer.
	token, _ := access.MintCapabilityToken(server.config.LocalToken, claims, time.Now())
	put := httptest.NewRequest("PUT", "/v8/artifacts/key", strings.NewReader("untrusted"))
	put.Header.Set("Authorization", "Bearer "+token)
	denied := httptest.NewRecorder()
	server.Handler().ServeHTTP(denied, put)
	if denied.Code < 400 {
		t.Fatal("readonly observation credential published artifact")
	}
	foreign := claims
	foreign.Execution = &execution.Context{Kind: "githubActions", OwnerID: "github-id:43", ExecutionID: claims.Execution.ExecutionID, RunnerEnvironment: "self-hosted"}
	if out := executionCall(t, server, foreign, "POST", body); out.Code != 409 {
		t.Fatalf("foreign overwrite %d", out.Code)
	}
}

func TestExecutionObservationRejectsForgedFieldsOldTokensAndRevokedMembership(t *testing.T) {
	server, _, claims := executionRouteFixture(t)
	base := executionRequestFixture()
	raw, _ := json.Marshal(base)
	for name, body := range map[string][]byte{"owner": []byte(strings.TrimSuffix(string(raw), "}") + `,"ownerId":"github-id:1"}`), "unknown metric": []byte(strings.Replace(string(raw), `"os":"linux"`, `"os":"linux","hostname":"private-box"`, 1)), "oversize": append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), execution.MaximumRequestBytes)...), "trailing": append(append([]byte{}, raw...), []byte(`{}`)...)} {
		t.Run(name, func(t *testing.T) {
			if out := executionCall(t, server, claims, "POST", body); out.Code != 400 {
				t.Fatalf("invalid report %d %s", out.Code, out.Body.String())
			}
		})
	}
	base.InstallationID = strings.Repeat("a", 64)
	body, _ := json.Marshal(base)
	if out := executionCall(t, server, claims, "POST", body); out.Code != 400 {
		t.Fatal("CI caller supplied persistent identity")
	}
	old := claims
	old.Execution = nil
	if out := executionCall(t, server, old, "POST", raw); out.Code != 401 {
		t.Fatal("old token fabricated inventory authority")
	}
	if out := executionCall(t, server, old, "GET", nil); out.Code != 200 {
		t.Fatal("old read credential lost inventory read compatibility")
	}
	membership := &executionMembership{role: "writer"}
	server.projectAuthority = membership
	if out := executionCall(t, server, claims, "POST", raw); out.Code != 201 {
		t.Fatal("authorized readonly PR report rejected", out.Code)
	}
	membership.role = ""
	if out := executionCall(t, server, claims, "POST", raw); out.Code != 401 {
		t.Fatal("revoked member report accepted")
	}
	if out := executionCall(t, server, claims, "GET", nil); out.Code != 401 {
		t.Fatal("revoked member inventory visible")
	}
}

func TestExecutionHumanExchangeAdvertisesWithoutChangingOldAuthBody(t *testing.T) {
	server, repo, _ := executionRouteFixture(t)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer fixture-secret" {
			http.Error(w, "denied", 401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "developer", "id": 42})
	}))
	defer github.Close()
	server.config.GitHubAPIURL = github.URL
	server.config.ActionsRepository = "acme/widget"
	server.config.ActionsDefaultRef = "refs/heads/main"
	server.config.TeamMembers = map[string]string{"developer": "reader"}
	input := map[string]string{"project": server.config.ProjectID, "repository": "acme/widget", "ref": "refs/heads/main", "defaultRef": "refs/heads/main", "compatibility": "linux-node24", "githubToken": "fixture-secret", "runId": "run-local-nonce-12345", "workspaceId": "sha256:" + strings.Repeat("a", 64)}
	body, _ := json.Marshal(input)
	call := func() map[string]json.RawMessage {
		out := httptest.NewRecorder()
		server.githubCapabilityExchange(out, httptest.NewRequest("POST", "/v1/auth/github/exchange", bytes.NewReader(body)))
		if out.Code != 200 {
			t.Fatalf("auth %d %s", out.Code, out.Body.String())
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := call()
	var supported execution.Capability
	if err := json.Unmarshal(result["executionObservations"], &supported); err != nil || supported.Path != execution.Path || supported.Version != 1 {
		t.Fatal("missing additive discovery", err)
	}
	var token string
	_ = json.Unmarshal(result["teamToken"], &token)
	claims, err := access.ParseCapabilityToken(server.config.LocalToken, token, time.Now())
	if err != nil || claims.Execution == nil || claims.Execution.OwnerID != "github-id:42" || claims.Execution.Kind != "client" {
		t.Fatalf("human authority %+v %v", claims.Execution, err)
	}
	// Optional reporting database failure must not reject authentication.
	_ = repo.Close()
	_ = call()
	server.executions = nil
	result = call()
	if _, exists := result["executionObservations"]; exists {
		t.Fatal("unavailable store advertised")
	}
}

func TestExecutionStandaloneWritesNeedNoAccountAndStayLocal(t *testing.T) {
	server, _, claims := executionRouteFixture(t)
	server.config.Role = "local"
	installation := strings.Repeat("a", 64)
	claims.Subject = "standalone-runtime"
	claims.Execution = execution.ClientContext("local:"+installation, "standalone-command")
	request := executionRequestFixture()
	request.InstallationID = installation
	request.Provider = "unknown"
	body, _ := json.Marshal(request)
	if out := executionCall(t, server, claims, "POST", body); out.Code != 201 {
		t.Fatalf("standalone write %d %s", out.Code, out.Body.String())
	}
	out := executionCall(t, server, claims, "GET", nil)
	var inventory execution.Inventory
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &inventory) != nil || len(inventory.Installations) != 1 || inventory.Installations[0].OwnerID != "local:"+installation {
		t.Fatalf("standalone dashboard %d %s", out.Code, out.Body.String())
	}
	// A local-only owner context is not hosted authority, even if accidentally
	// minted with a Team signing key by another local integration.
	server.config.Role = "team"
	if out = executionCall(t, server, claims, "POST", body); out.Code != 401 {
		t.Fatal("hosted route accepted standalone owner")
	}
	if out = executionCall(t, server, claims, "GET", nil); out.Code != 401 {
		t.Fatal("hosted inventory accepted standalone owner")
	}
}

func TestExecutionOIDCExchangeUsesSignedJobAndRunnerEnvironment(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "execution-oidc-key"
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"keys": []map[string]string{{"kid": keyID, "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes())}}})
	}))
	defer issuer.Close()
	cfg := config.Config{Version: 1, Role: "team", DataDir: t.TempDir(), Listen: "127.0.0.1:7437", MaxBytes: 1 << 20, ProjectID: "github.com/acme/widget", LocalToken: "execution-fixture-secret", CompatibilityID: "linux-node24", ActionsRepository: "acme/widget", ActionsRef: "refs/heads/main", ActionsDefaultRef: "refs/heads/main", BuildkitBuilder: "fixture", GitHubOIDCIssuer: issuer.URL}
	server, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	now := time.Now().UTC()
	idToken := signOIDCTestToken(t, privateKey, keyID, map[string]any{"iss": issuer.URL, "aud": "layercache:" + cfg.ProjectID, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Minute).Unix(), "sub": "repo:acme/widget:pull_request", "repository": "acme/widget", "repository_id": "123", "actor_id": "42", "ref": "refs/pull/1/merge", "sha": strings.Repeat("a", 40), "run_id": "456", "run_attempt": "2", "check_run_id": "789", "runner_environment": "github-hosted", "event_name": "pull_request"})
	body, _ := json.Marshal(map[string]string{"project": cfg.ProjectID, "integration": "turbo", "compatibility": "linux-node24", "idToken": idToken})
	out := httptest.NewRecorder()
	server.githubOIDCCapabilityExchange(out, httptest.NewRequest("POST", "/v1/auth/github-oidc/exchange", bytes.NewReader(body)))
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var result struct {
		TeamToken string                `json:"teamToken"`
		Supported *execution.Capability `json:"executionObservations"`
	}
	if err = json.Unmarshal(out.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	claims, err := access.ParseCapabilityToken(cfg.LocalToken, result.TeamToken, now)
	if err != nil || claims.Execution == nil || claims.Execution.ExecutionID != "github-actions:123:456:2:789" || claims.Execution.RunnerEnvironment != "github-hosted" || claims.Allows(access.CapabilityWrite) || result.Supported == nil {
		t.Fatalf("signed execution %+v %v", claims, err)
	}
}
