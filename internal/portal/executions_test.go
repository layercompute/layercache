package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/execution"
)

func TestExecutionPortalProjectsOnlyAuthorizedInventoryAndRechecksMembership(t *testing.T) {
	q := newPortalQA(t)
	alice, bob := q.login("alice"), q.login("bob")
	team := q.team(alice, "Shared")
	project := q.project(alice, team, "acme/alpha")
	other := q.project(alice, team, "acme/beta")
	now := time.Now().UTC()
	installation := strings.Repeat("a", 64)
	claims := access.Claims{Subject: "github-id:1", Project: project.ID, Integration: "turbo", RunID: "github-local:github-id:1:run-local-nonce-12345", WorkspaceID: "github-local:github-id:1:sha256:" + strings.Repeat("b", 64), Execution: execution.ClientContext("github-id:1", "fixture-command"), Capabilities: []access.Capability{access.CapabilityRead}, ExpiresAt: now.Add(time.Minute)}
	token, err := access.MintCapabilityToken(dashboardStoredSecret(t, q, project.ID), claims, now)
	if err != nil {
		t.Fatal(err)
	}
	report := execution.ObservationRequest{Version: 1, Phase: "start", InstallationID: installation, Sample: execution.Sample{OS: "linux", Arch: "amd64", CPU: execution.CPU{LimitKnowledge: "unknown"}, Memory: execution.Memory{LimitKnowledge: "unknown"}, Filesystems: []execution.Filesystem{}}}
	body, _ := json.Marshal(report)
	requireStatus(t, q.request("POST", execution.Path, string(body), nil, map[string]string{"Authorization": "Bearer " + token}), http.StatusCreated)
	path := "/api/projects/" + project.ID + "/snapshot"
	response := q.request("GET", path, "", alice, nil)
	requireStatus(t, response, 200)
	got := decodeQA[struct {
		Executions *execution.Inventory `json:"executions"`
	}](t, response)
	if got.Executions == nil || len(got.Executions.Installations) != 1 || got.Executions.Installations[0].InstallationID != installation || got.Executions.Installations[0].CacheActivity != "unconfirmed" {
		t.Fatalf("project projection %+v", got)
	}
	for _, protected := range []string{token, claims.RunID, claims.WorkspaceID, dashboardStoredSecret(t, q, project.ID)} {
		if strings.Contains(response.Body.String(), protected) {
			t.Fatalf("portal exposed authority %q", protected)
		}
	}
	response = q.request("GET", "/api/projects/"+other.ID+"/snapshot", "", alice, nil)
	requireStatus(t, response, 200)
	different := decodeQA[struct {
		Executions *execution.Inventory `json:"executions"`
	}](t, response)
	if different.Executions == nil || len(different.Executions.Installations) != 0 {
		t.Fatal("another project inherited inventory")
	}
	requireStatus(t, q.request("GET", path, "", bob, nil), 403)
	mustJoin(t, q.p.store, "1", team.ID, "2", "reader")
	requireStatus(t, q.request("GET", path, "", bob, nil), 200)
	if err = q.p.store.ChangeMember(context.Background(), "1", team.ID, "2", ""); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("GET", path, "", bob, nil), 403)
}
