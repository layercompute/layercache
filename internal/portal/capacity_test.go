package portal

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/server"
)

func TestDashboardCapacityAuthorizationAndStrictInput(t *testing.T) {
	q := newPortalQA(t)
	alice, bob := q.login("alice"), q.login("bob")
	team := q.team(alice, "Shared")
	project := q.project(alice, team, "acme/alpha")
	path := "/api/projects/" + project.ID + "/capacity"
	body := `{"maxBytes":1024,"expectedMaxBytes":2048,"confirm":true}`
	requireStatus(t, q.request("GET", path, "", nil, nil), 401)
	requireStatus(t, q.request("GET", path, "", bob, nil), 403)
	requireStatus(t, q.request("PUT", path, body, bob, nil), 403)
	mustJoin(t, q.p.store, "1", team.ID, "2", "reader")
	for _, role := range []string{"reader", "writer"} {
		if err := q.p.store.ChangeMember(context.Background(), "1", team.ID, "2", role); err != nil {
			t.Fatal(err)
		}
		response := q.request("GET", path, "", bob, nil)
		requireStatus(t, response, 200)
		result := decodeQA[server.ProjectCapacity](t, response)
		if result.State != "unsupported" || result.CanChange || result.MaxBytes != nil || result.CeilingBytes != nil || result.Reason == "" {
			t.Fatalf("standalone unsupported capacity = %+v", result)
		}
		requireStatus(t, q.request("PUT", path, body, bob, nil), 403)
	}
	for _, malformed := range []string{
		`{}`, `{"maxBytes":1,"expectedMaxBytes":2}`, `{"maxBytes":1,"expectedMaxBytes":2,"confirm":false}`,
		`{"maxBytes":null,"expectedMaxBytes":2,"confirm":true}`, `{"maxBytes":0,"expectedMaxBytes":2,"confirm":true}`,
		`{"maxBytes":1.5,"expectedMaxBytes":2,"confirm":true}`, `{"maxBytes":"1","expectedMaxBytes":2,"confirm":true}`,
		`{"maxBytes":1,"expectedMaxBytes":0,"confirm":true}`, `{"maxBytes":1,"expectedMaxBytes":9007199254740992,"confirm":true}`,
		`{"maxBytes":9007199254740992,"expectedMaxBytes":2,"confirm":true}`, body + `{}`,
		`{"maxBytes":1,"expectedMaxBytes":2,"confirm":true,"prune":true}`, strings.Repeat(" ", 4096) + body,
	} {
		requireStatus(t, q.request("PUT", path, malformed, alice, map[string]string{"Content-Type": "application/json"}), 400)
	}
	for _, headers := range []map[string]string{
		{"Content-Type": "application/json", "Origin": "https://foreign.example"},
		{"Content-Type": "application/json", "Origin": ""},
		{"Content-Type": "application/json", "X-CSRF-Token": "wrong"},
	} {
		requireStatus(t, q.request("PUT", path, body, alice, headers), 403)
	}
	requireStatus(t, q.request("PUT", path, body, alice, map[string]string{"Content-Type": "text/plain"}), 400)
	if err := q.p.store.ChangeMember(context.Background(), "1", team.ID, "2", ""); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("GET", path, "", bob, nil), 403)
}

func TestStoreOperatorCapacityPersistsWithoutEvictionAndRespectsBothAuthorities(t *testing.T) {
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
	adopted := operatorTestProject(t)
	adopted.Config.CloudPostgresURL = parsed.String()
	adopted.Config.CloudS3Endpoint, adopted.Config.CloudS3UsePathStyle = s3.server.URL, true
	adopted.Config.GitHubAPIURL = q.github.URL
	adopted.Config.MaxBytes, adopted.Config.CacheSoftBytes = 4096, 128
	original, err := server.New(context.Background(), adopted.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	if err := q.p.Close(); err != nil {
		t.Fatal(err)
	}
	q.cfg.OperatorProjects = []OperatorProject{adopted}
	q.p, err = New(context.Background(), q.cfg)
	if err != nil {
		t.Fatal(err)
	}
	alice := q.login("alice")
	path := "/api/projects/" + url.PathEscape(adopted.Config.ProjectID) + "/capacity"
	get := func() server.ProjectCapacity {
		t.Helper()
		response := q.request("GET", path, "", alice, nil)
		requireStatus(t, response, 200)
		return decodeQA[server.ProjectCapacity](t, response)
	}
	put := func(maximum, expected int64, status int) {
		t.Helper()
		body := fmt.Sprintf(`{"maxBytes":%d,"expectedMaxBytes":%d,"confirm":true}`, maximum, expected)
		requireStatus(t, q.request("PUT", path, body, alice, map[string]string{"Content-Type": "application/json"}), status)
	}
	got := get()
	if got.State != "connected" || !got.CanChange || got.MaxBytes == nil || *got.MaxBytes != 4096 || got.CeilingBytes == nil || *got.CeilingBytes != 4096 || got.MinimumBytes == nil || *got.MinimumBytes != 128 || got.LimitSource != "operator" {
		t.Fatalf("initial capacity = %+v", got)
	}
	put(1024, 4096, 200)
	put(2048, 4096, 409)
	put(4097, 1024, 403)
	put(127, 1024, 400)
	projects, err := q.p.store.Projects(context.Background(), "1")
	if err != nil || len(projects) != 1 {
		t.Fatal("missing adopted project", err)
	}
	token := q.capability(projects[0], "alice")
	headers := map[string]string{"Authorization": "Bearer " + token}
	requireStatus(t, q.request("PUT", "/v8/artifacts/preserved", strings.Repeat("x", 512), nil, headers), 200)
	put(256, 1024, 409)
	requireStatus(t, q.request("GET", "/v8/artifacts/preserved", "", nil, headers), 200)
	if got = get(); got.MaxBytes == nil || *got.MaxBytes != 1024 || got.UsedBytes == nil || *got.UsedBytes != 512 {
		t.Fatalf("rejected shrink changed capacity or bytes = %+v", got)
	}
	if err := q.p.Close(); err != nil {
		t.Fatal(err)
	}
	q.p, err = New(context.Background(), q.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got = get(); got.MaxBytes == nil || *got.MaxBytes != 1024 || got.CeilingBytes == nil || *got.CeilingBytes != 4096 {
		t.Fatal("restart lost dynamic limit or changed assigned ceiling")
	}
	put(4096, 1024, 200)
	// The operator-adoption fingerprint, reserved allocation and configuration
	// remain unchanged. Native SQL authority still bounds the portal owner.
	store, err := cloud.OpenExisting(context.Background(), cloud.Config{PostgresURL: parsed.String(), S3Endpoint: s3.server.URL, S3Bucket: adopted.Config.CloudS3Bucket, S3Region: "auto", S3AccessKey: adopted.Config.CloudS3AccessKey, S3SecretKey: adopted.Config.CloudS3SecretKey, S3PathStyle: true, Project: adopted.Config.ProjectID, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ReconcileMembers(context.Background(), "fixture-operator", []cloud.Member{{Project: adopted.Config.ProjectID, Subject: "github:alice", Role: "reader"}}); err != nil {
		t.Fatal(err)
	}
	if got = get(); got.CanChange {
		t.Fatal("native reader retained portal capacity authority")
	}
	put(2048, 4096, 403)
	if err := store.RemoveMember(context.Background(), "fixture-operator", "github:alice"); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("GET", path, "", alice, nil), 403)
	put(2048, 4096, 403)
}

func TestStorePlanCapacityRetainsAssignedAllocationAndNativeQuotaProtection(t *testing.T) {
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
	storage := storageTestConfig(t)
	for id, pool := range storage.HostedStoragePools {
		pool.CloudPostgresURL, pool.CloudS3Endpoint, pool.CloudS3UsePathStyle = parsed.String(), s3.server.URL, true
		storage.HostedStoragePools[id] = pool
	}
	q := newPortalQA(t, func(c *Config) {
		c.HostedStoragePools, c.StoragePlans, c.DefaultStoragePlan = storage.HostedStoragePools, storage.StoragePlans, storage.DefaultStoragePlan
	})
	alice, bob := q.login("alice"), q.login("bob")
	team := q.team(alice, "Assigned capacity")
	project := q.project(alice, team, "acme/alpha")
	q.project(alice, team, "acme/beta")
	path := "/api/projects/" + project.ID + "/capacity"
	body := `{"maxBytes":512,"expectedMaxBytes":1024,"confirm":true}`
	requireStatus(t, q.request("PUT", path, body, alice, map[string]string{"Content-Type": "application/json"}), 200)
	response := q.request("GET", path, "", alice, nil)
	requireStatus(t, response, 200)
	capacity := decodeQA[server.ProjectCapacity](t, response)
	if capacity.MaxBytes == nil || *capacity.MaxBytes != 512 || capacity.CeilingBytes == nil || *capacity.CeilingBytes != 1024 || capacity.LimitSource != "plan" || !capacity.CanChange {
		t.Fatalf("operating quota and allocation were conflated: %+v", capacity)
	}
	projects, err := q.p.store.Projects(context.Background(), "1")
	if err != nil || len(projects) != 2 {
		t.Fatal("missing plan projects", err)
	}
	for _, project := range projects {
		if project.AllocatedCacheBytes != 1024 {
			t.Fatal("changing operating capacity changed reserved plan allocation")
		}
	}
	// Reducing an operating cap does not sell or reassign reserved entitlement.
	requireStatus(t, q.request("POST", "/api/teams/"+team.ID+"/projects", `{"name":"Extra","repository":"acme/extra"}`, alice, nil), 409)
	token := q.capability(project, "alice")
	requireStatus(t, q.request("PUT", "/v1/cache/quota", `{"maxBytes":9999999}`, nil, map[string]string{"Authorization": "Bearer " + token}), 403)
	requireStatus(t, q.request("PUT", path, `{"maxBytes":1025,"expectedMaxBytes":512,"confirm":true}`, alice, map[string]string{"Content-Type": "application/json"}), 403)
	mustJoin(t, q.p.store, "1", team.ID, "2", "reader")
	response = q.request("GET", path, "", bob, nil)
	requireStatus(t, response, 200)
	if decodeQA[server.ProjectCapacity](t, response).CanChange {
		t.Fatal("plan reader was offered capacity administration")
	}
	requireStatus(t, q.request("PUT", path, `{"maxBytes":1024,"expectedMaxBytes":512,"confirm":true}`, bob, map[string]string{"Content-Type": "application/json"}), 403)
	if err := q.p.Close(); err != nil {
		t.Fatal(err)
	}
	q.p, err = New(context.Background(), q.cfg)
	if err != nil {
		t.Fatal("restart rejected preserved plan assignment", err)
	}
	response = q.request("GET", path, "", alice, nil)
	requireStatus(t, response, 200)
	if got := decodeQA[server.ProjectCapacity](t, response); got.MaxBytes == nil || *got.MaxBytes != 512 || got.CeilingBytes == nil || *got.CeilingBytes != 1024 {
		t.Fatalf("restart changed operating quota or entitlement: %+v", got)
	}
	requireStatus(t, q.request("PUT", path, `{"maxBytes":1024,"expectedMaxBytes":512,"confirm":true}`, alice, map[string]string{"Content-Type": "application/json"}), 200)
	if err := q.p.store.ChangeMember(context.Background(), "1", team.ID, "2", ""); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("GET", path, "", bob, nil), 403)
}
