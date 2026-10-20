package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
)

func TestGitHubLocalRunIssuesOnlyScopedNativeAuthority(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer fixture-secret" {
			http.Error(w, "denied", 401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "developer", "id": 12345})
	}))
	defer github.Close()
	for _, role := range []string{"reader", "writer", "admin"} {
		t.Run(role, func(t *testing.T) {
			instance := &Server{config: config.Config{Role: "team", ProjectID: "github.com/acme/widgets", ActionsRepository: "acme/widgets", ActionsDefaultRef: "refs/heads/main", LocalToken: "server-signing-key", GitHubAPIURL: github.URL, TeamMembers: map[string]string{"developer": role}}}
			input := map[string]string{"project": instance.config.ProjectID, "repository": "acme/widgets", "ref": "refs/heads/feature/actual", "defaultRef": "refs/heads/main", "compatibility": "linux-amd64-node24", "githubToken": "fixture-secret", "runId": "run-local-nonce-12345", "workspaceId": "sha256:" + strings.Repeat("a", 64), "sourceCommit": strings.Repeat("b", 40)}
			body, _ := json.Marshal(input)
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/github/exchange", bytes.NewReader(body))
			response := httptest.NewRecorder()
			instance.githubCapabilityExchange(response, request)
			if response.Code != 200 {
				t.Fatalf("exchange=%d", response.Code)
			}
			var result struct {
				RunID, WorkspaceID, TeamToken string
				NativeTokens                  map[string]string
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			for integration, token := range result.NativeTokens {
				claims, err := access.ParseCapabilityToken(instance.config.LocalToken, token, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if claims.Integration != integration || claims.Project != input["project"] || claims.SourceCommit != input["sourceCommit"] || claims.Repository != input["repository"] || claims.Ref != input["ref"] || claims.DefaultRef != input["defaultRef"] || claims.Compatibility != input["compatibility"] || claims.RunID != "github-local:github:developer:"+input["runId"] || claims.WorkspaceID != "github-local:github:developer:"+input["workspaceId"] || claims.Allows(access.CapabilityAdmin) || claims.Allows(access.CapabilityWrite) != (role != "reader") {
					t.Fatalf("incorrect scoped %s authority %+v", integration, claims)
				}
			}
			if len(result.NativeTokens) != 6 {
				t.Fatal("native credential set incomplete")
			}
			for name, mutate := range map[string]func(map[string]string){
				"run traversal": func(m map[string]string) { m["runId"] = "github-actions:acme/widgets:run" },
				"foreign workspace": func(m map[string]string) {
					m["workspaceId"] = "github-local:github:other:sha256:" + strings.Repeat("a", 64)
				},
				"partial context":        func(m map[string]string) { delete(m, "workspaceId") },
				"mutable commit":         func(m map[string]string) { m["sourceCommit"] = "main" },
				"foreign repo":           func(m map[string]string) { m["repository"] = "other/repo" },
				"integration escalation": func(m map[string]string) { m["integration"] = "retained" },
				"capability escalation":  func(m map[string]string) { m["capabilities"] = "admin" },
			} {
				t.Run(name, func(t *testing.T) {
					changed := map[string]string{}
					for k, v := range input {
						changed[k] = v
					}
					mutate(changed)
					body, _ := json.Marshal(changed)
					response := httptest.NewRecorder()
					instance.githubCapabilityExchange(response, httptest.NewRequest(http.MethodPost, "/v1/auth/github/exchange", bytes.NewReader(body)))
					if response.Code < 400 {
						t.Fatalf("unsafe exchange accepted %d", response.Code)
					}
				})
			}
		})
	}
}

func TestTurboLocalReportRequiresSignedMemberRunAndIntegration(t *testing.T) {
	instance, repository, _ := turboReportFixture(t)
	instance.config.TeamMembers = map[string]string{"developer": "reader"}
	now := time.Now().Add(-time.Second).UTC()
	claims := access.Claims{Subject: "github:developer", Project: instance.config.ProjectID, Integration: "turbo", Compatibility: "linux-amd64-node24", Repository: "acme/widget", RunID: "github-local:github:developer:run-local-nonce-12345", WorkspaceID: "github-local:github:developer:sha256:" + strings.Repeat("a", 64), Capabilities: []access.Capability{access.CapabilityRead}, ExpiresAt: time.Now().Add(time.Hour)}
	for range 2 {
		response := postTurboReport(t, instance, claims, reportSummary(t, "local-summary", "HIT", now))
		if response.Code != 200 {
			t.Fatalf("local summary=%d %s", response.Code, response.Body.String())
		}
	}
	report, err := repository.RunReport(claims.RunID)
	if err != nil || report.Hits != 1 || len(report.Outcomes) != 1 {
		t.Fatalf("local report did not reconcile idempotently: %v", err)
	}
	for name, mutate := range map[string]func(*access.Claims){
		"other member":      func(c *access.Claims) { c.Subject = "github:other" },
		"other workspace":   func(c *access.Claims) { c.WorkspaceID = "github-local:github:other:sha256:" + strings.Repeat("a", 64) },
		"generic login":     func(c *access.Claims) { c.Integration = "" },
		"other integration": func(c *access.Claims) { c.Integration = "actions" },
		"fake OIDC":         func(c *access.Claims) { c.RunID = "github-actions:acme/widget:123:1:check:456" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := claims
			mutate(&invalid)
			response := postTurboReport(t, instance, invalid, reportSummary(t, "invalid", "MISS", now))
			if response.Code != 401 {
				t.Fatalf("foreign local authority=%d", response.Code)
			}
		})
	}
}
