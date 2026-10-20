package portal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/dashboard"
)

func dashboardStoredSecret(t *testing.T, q *portalQA, id string) string {
	t.Helper()
	projects, err := q.p.store.Projects(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range projects {
		if project.ID == id && project.Secret != "" {
			return project.Secret
		}
	}
	t.Fatal("missing fixture project authority")
	return ""
}

func TestDashboardScopeCostsAndTeamUsageStayAuthorizedAndSeparate(t *testing.T) {
	q := newPortalQA(t)
	alice, bob := q.login("alice"), q.login("bob")
	team := q.team(alice, "Shared")
	first := q.project(alice, team, "acme/alpha")
	q.project(alice, team, "acme/beta")
	personalResponse := q.request("POST", "/api/teams", `{"kind":"personal"}`, alice, nil)
	requireStatus(t, personalResponse, http.StatusCreated)
	personal := decodeQA[Team](t, personalResponse)
	private := q.project(alice, personal, "acme/alpha")
	ledger := filepath.Join(t.TempDir(), "costs.json")
	data := fmt.Sprintf(`{"version":1,"entries":[
	{"scope":"project","id":%q,"currency":"USD","periodStart":"2026-10-01T00:00:00Z","periodEnd":"2026-11-01T00:00:00Z","basis":"recorded","complete":true,"items":[{"name":"Hosted storage","amountMinor":125}]},
	{"scope":"team","id":%q,"currency":"USD","periodStart":"2026-10-01T00:00:00Z","periodEnd":"2026-11-01T00:00:00Z","basis":"estimate","complete":true,"items":[{"name":"Recorded operating estimate","amountMinor":900}]}
	]}`, first.ID, team.ID)
	if err := os.WriteFile(ledger, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	q.p.cfg.DashboardCostsFile = ledger
	for _, tc := range []struct {
		project Project
		scope   string
		state   string
		total   int64
	}{{first, "team", "recorded", 125}, {private, "personal", "unknown", 0}} {
		response := q.request("GET", "/api/projects/"+tc.project.ID+"/snapshot", "", alice, nil)
		requireStatus(t, response, http.StatusOK)
		got := decodeQA[struct {
			Scope string               `json:"scope"`
			Cache dashboard.Cache      `json:"cache"`
			Cost  dashboard.CostReport `json:"cost"`
		}](t, response)
		if got.Scope != tc.scope || got.Cost.State != tc.state || got.Cache.State != "connected" || got.Cache.Status == nil || got.Cache.Status.Visibility != tc.scope || !got.Cache.Status.CanPrune {
			t.Fatalf("scope/cost/cache projection = %+v", got)
		}
		if tc.state == "recorded" && (got.Cost.TotalMinor == nil || *got.Cost.TotalMinor != tc.total) {
			t.Fatalf("project cost was mixed with team estimate: %+v", got.Cost)
		}
		for _, protected := range []string{dashboardStoredSecret(t, q, tc.project.ID), ledger, q.cfg.GitHubClientSecret, "storageBackendFingerprint"} {
			if strings.Contains(response.Body.String(), protected) {
				t.Fatalf("snapshot exposed protected field %q", protected)
			}
		}
	}
	usagePath := "/api/teams/" + team.ID + "/usage"
	requireStatus(t, q.request("GET", usagePath, "", nil, nil), http.StatusUnauthorized)
	requireStatus(t, q.request("GET", usagePath, "", bob, nil), http.StatusForbidden)
	requireStatus(t, q.request("GET", "/api/projects/"+first.ID+"/snapshot", "", bob, nil), http.StatusForbidden)
	mustJoin(t, q.p.store, "1", team.ID, "2", "reader")
	response := q.request("GET", usagePath, "", bob, nil)
	requireStatus(t, response, http.StatusOK)
	usage := decodeQA[struct {
		ProjectCount        int                  `json:"projectCount"`
		AllocatedCacheBytes *int64               `json:"allocatedCacheBytes"`
		AllocationComplete  bool                 `json:"allocationComplete"`
		Cost                dashboard.CostReport `json:"cost"`
	}](t, response)
	if usage.ProjectCount != 2 || usage.AllocatedCacheBytes != nil || usage.AllocationComplete || usage.Cost.State != "estimate" || usage.Cost.TotalMinor == nil || *usage.Cost.TotalMinor != 900 {
		t.Fatalf("team allocation/cost projection = %+v", usage)
	}
	// Legacy projects have no reserved plan allocation. Their runtime capacities
	// and private project observations must not be substituted into this total.
	for _, forbidden := range []string{"usageBytes", "hits", "netEstimatedBuildTimeSaved", dashboardStoredSecret(t, q, first.ID), private.ID} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("team usage exposed unrelated/overlapping field %q", forbidden)
		}
	}
	readerSnapshot := q.request("GET", "/api/projects/"+first.ID+"/snapshot", "", bob, nil)
	requireStatus(t, readerSnapshot, http.StatusOK)
	reader := decodeQA[struct {
		Cache dashboard.Cache `json:"cache"`
	}](t, readerSnapshot)
	if reader.Cache.Status == nil || reader.Cache.Status.CanPrune {
		t.Fatal("reader was offered administration")
	}
	if err := q.p.store.ChangeMember(context.Background(), "1", team.ID, "2", ""); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("GET", usagePath, "", bob, nil), http.StatusForbidden)
	requireStatus(t, q.request("GET", "/api/projects/"+first.ID+"/snapshot", "", bob, nil), http.StatusForbidden)
	if err := os.WriteFile(ledger, []byte(`{"version":1,"entries":`), 0600); err != nil {
		t.Fatal(err)
	}
	response = q.request("GET", "/api/projects/"+first.ID+"/snapshot", "", alice, nil)
	requireStatus(t, response, http.StatusOK)
	if got := decodeQA[struct {
		Cost dashboard.CostReport `json:"cost"`
	}](t, response); got.Cost.State != "unavailable" || strings.Contains(response.Body.String(), ledger) {
		t.Fatal("unreadable cost evidence leaked details or became a zero cost")
	}
}

func TestDashboardPruneRequiresCurrentAdminAndConfirmedSameOriginJSON(t *testing.T) {
	q := newPortalQA(t)
	alice, bob := q.login("alice"), q.login("bob")
	team := q.team(alice, "Shared")
	project := q.project(alice, team, "acme/alpha")
	path := "/api/projects/" + project.ID + "/prune"
	headers := map[string]string{"Content-Type": "application/json"}
	confirmed := `{"targetBytes":0,"confirm":true}`
	requireStatus(t, q.request("POST", path, confirmed, nil, headers), http.StatusUnauthorized)
	requireStatus(t, q.request("POST", path, confirmed, bob, headers), http.StatusForbidden)
	mustJoin(t, q.p.store, "1", team.ID, "2", "reader")
	for _, role := range []string{"reader", "writer"} {
		if err := q.p.store.ChangeMember(context.Background(), "1", team.ID, "2", role); err != nil {
			t.Fatal(err)
		}
		requireStatus(t, q.request("POST", path, confirmed, bob, headers), http.StatusForbidden)
	}
	requireStatus(t, q.request("POST", "/api/projects/project-foreign/prune", confirmed, alice, headers), http.StatusForbidden)
	for _, body := range []string{
		`{}`, `{"targetBytes":0}`, `{"targetBytes":0,"confirm":false}`, `{"targetBytes":null,"confirm":true}`,
		`{"targetBytes":-1,"confirm":true}`, `{"targetBytes":0.5,"confirm":true}`, `{"targetBytes":"0","confirm":true}`,
		`{"targetBytes":0,"confirm":true,"force":true}`, confirmed + `{}`, strings.Repeat(" ", 4096) + confirmed,
		`{"targetBytes":9223372036854775808,"confirm":true}`,
	} {
		requireStatus(t, q.request("POST", path, body, alice, headers), http.StatusBadRequest)
	}
	for _, fields := range []map[string]string{
		{"Content-Type": "text/plain"},
		{"Content-Type": "application/json", "Origin": "https://foreign.example"},
		{"Content-Type": "application/json", "Origin": ""},
		{"Content-Type": "application/json", "X-CSRF-Token": "incorrect"},
	} {
		want := http.StatusForbidden
		if fields["Content-Type"] == "text/plain" {
			want = http.StatusBadRequest
		}
		requireStatus(t, q.request("POST", path, confirmed, alice, fields), want)
	}
	token := q.capability(project, "alice")
	cacheHeaders := map[string]string{"Authorization": "Bearer " + token}
	requireStatus(t, q.request("PUT", "/v8/artifacts/prunable", "output", nil, cacheHeaders), http.StatusOK)
	response := q.request("POST", path, confirmed, alice, headers)
	requireStatus(t, response, http.StatusOK)
	got := decodeQA[dashboard.PruneResult](t, response)
	if got.BeforeBytes != 6 || got.AfterBytes != 0 || got.FreedBytes != 6 || got.BeforeEntries != 1 || got.AfterEntries != 0 || got.TargetBytes != 0 {
		t.Fatalf("actual prune result = %+v", got)
	}
	requireStatus(t, q.request("GET", "/v8/artifacts/prunable", "", nil, cacheHeaders), http.StatusNotFound)
	if strings.Contains(response.Body.String(), dashboardStoredSecret(t, q, project.ID)) {
		t.Fatal("prune result exposed signing authority")
	}
	// Even an existing browser session loses administration immediately.
	if err := q.p.store.ChangeMember(context.Background(), "1", team.ID, "2", "admin"); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("POST", path, confirmed, bob, headers), http.StatusOK)
	if err := q.p.store.ChangeMember(context.Background(), "1", team.ID, "2", "reader"); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("POST", path, confirmed, bob, headers), http.StatusForbidden)
	if err := q.p.store.ChangeMember(context.Background(), "1", team.ID, "2", ""); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("POST", path, confirmed, bob, headers), http.StatusForbidden)
}

func TestDashboardPrunePreservesPinsAndActiveReaders(t *testing.T) {
	q := newPortalQA(t)
	alice := q.login("alice")
	team := q.team(alice, "Shared")
	project := q.project(alice, team, "acme/alpha")
	token := q.capability(project, "alice")
	headers := map[string]string{"Authorization": "Bearer " + token}
	for _, name := range []string{"pinned", "reading"} {
		requireStatus(t, q.request("PUT", "/v8/artifacts/"+name, name, nil, headers), http.StatusOK)
	}
	storage, err := artifact.Open(context.Background(), filepath.Join(q.cfg.DataDir, "projects", project.ID), q.cfg.ProjectTemplate.MaxBytes, q.cfg.ProjectTemplate.MinFreeBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	key := artifact.Key{Integration: "turbo", Project: project.ID, Compatibility: "linux-amd64-schema1", Native: "pinned"}
	entry, err := storage.Head(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.Pin(context.Background(), "dashboard-test", artifact.Pin{Owner: "retained-owner", Key: key, Digest: entry.Digest, Size: entry.Size}); err != nil {
		t.Fatal(err)
	}
	key.Native = "reading"
	readingEntry, reader, err := storage.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := storage.RetainForRead(context.Background(), "dashboard-test", "current-reader", []artifact.Entry{readingEntry}, time.Minute); err != nil {
		t.Fatal(err)
	}
	response := q.request("POST", "/api/projects/"+project.ID+"/prune", `{"targetBytes":0,"confirm":true}`, alice, map[string]string{"Content-Type": "application/json"})
	requireStatus(t, response, http.StatusServiceUnavailable)
	if !strings.Contains(response.Body.String(), "refresh cache status") {
		t.Fatal("uncertain/blocked prune did not request status reconciliation")
	}
	for _, name := range []string{"pinned", "reading"} {
		requireStatus(t, q.request("GET", "/v8/artifacts/"+name, "", nil, headers), http.StatusOK)
	}
	if body, err := io.ReadAll(reader); err != nil || string(body) != "reading" {
		t.Fatalf("active read after prune = %q, %v", body, err)
	}
}

func TestDashboardTeamUsageAllocationCompleteness(t *testing.T) {
	q := newPortalQA(t)
	alice := q.login("alice")
	team := q.team(alice, "Shared")
	path := "/api/teams/" + team.ID + "/usage"
	check := func(count int, total *int64) {
		t.Helper()
		response := q.request("GET", path, "", alice, nil)
		requireStatus(t, response, http.StatusOK)
		got := decodeQA[struct {
			ProjectCount        int    `json:"projectCount"`
			AllocatedCacheBytes *int64 `json:"allocatedCacheBytes"`
			AllocationComplete  bool   `json:"allocationComplete"`
		}](t, response)
		if got.ProjectCount != count || got.AllocationComplete != (total != nil) || (got.AllocatedCacheBytes == nil) != (total == nil) {
			t.Fatalf("allocation completeness = %+v, want count %d and total %v", got, count, total)
		}
		if total != nil && *got.AllocatedCacheBytes != *total {
			t.Fatalf("allocation = %d, want %d", *got.AllocatedCacheBytes, *total)
		}
	}
	zero := int64(0)
	check(0, &zero)
	first := q.project(alice, team, "acme/alpha")
	second := q.project(alice, team, "acme/beta")
	check(2, nil)
	// Seed authoritative allocation records directly; runtime byte usage and
	// quota are deliberately left unchanged to distinguish the two measures.
	setAllocation := func(project Project, pool string, size int64) {
		t.Helper()
		if _, err := q.p.store.db.Exec(`UPDATE lc_portal_projects SET storage_pool_id=$2,allocated_cache_bytes=$3 WHERE id=$1`, project.ID, pool, size); err != nil {
			t.Fatal(err)
		}
	}
	setAllocation(first, "managed-pool", 7)
	check(2, nil)
	setAllocation(second, "managed-pool", 11)
	total := int64(18)
	check(2, &total)
	setAllocation(second, "", 11)
	check(2, nil)
	setAllocation(second, legacyStorageID, 11)
	check(2, nil)
	setAllocation(second, "not a pool", 11)
	check(2, nil)
	setAllocation(second, "managed-pool", 0)
	check(2, nil)
	setAllocation(first, "managed-pool", 1<<53-1)
	setAllocation(second, "managed-pool", 1)
	check(2, nil)
	setAllocation(first, "managed-pool", 1<<53-2)
	safe := int64(1<<53 - 1)
	check(2, &safe)
}
