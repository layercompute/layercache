package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/config"
)

func storageTestConfig(t *testing.T) Config {
	t.Helper()
	legacy, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	legacy.MinFreeBytes = 5 << 30
	pool, err := cloneStorageTemplate(legacy)
	if err != nil {
		t.Fatal(err)
	}
	pool.Role, pool.ProjectID, pool.DataDir = "team", "pool-template", filepath.Join(t.TempDir(), "pool")
	pool.CloudPostgresURL = "postgres://fixture:fixture-password@127.0.0.1:5432/cache?sslmode=disable&search_path=public"
	pool.CloudS3Endpoint, pool.CloudS3Bucket, pool.CloudS3Region = "http://127.0.0.1:9000", "fixture-cache", "auto"
	pool.CloudS3AccessKey, pool.CloudS3SecretKey = "fixture-key", "fixture-secret"
	second, err := cloneStorageTemplate(pool)
	if err != nil {
		t.Fatal(err)
	}
	second.CloudS3Bucket = "fixture-cache-second"
	return Config{
		DataDir: t.TempDir(), ProjectTemplate: legacy,
		HostedStoragePools: map[string]config.Config{"r2-primary": pool, "r2-secondary": second},
		StoragePlans: map[string]StoragePlan{
			"beta":   {PoolID: "r2-primary", ProjectMaxBytes: 1024, TeamMaxBytes: 2048, MaxProjects: 3},
			"larger": {PoolID: "r2-secondary", ProjectMaxBytes: 4096, TeamMaxBytes: 8192, MaxProjects: 2},
		},
		DefaultStoragePlan: "beta", TeamCreatorStoragePlans: map[string]string{"3": "larger"},
	}
}

func configureTestStorage(t *testing.T, s *Store, cfg Config) *storagePolicy {
	t.Helper()
	p, err := newStoragePolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.configureStorage(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHostedStoragePolicyValidationAndCredentialSafeIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"no pools", func(c *Config) { c.HostedStoragePools = nil }},
		{"missing default", func(c *Config) { c.DefaultStoragePlan = "" }},
		{"unknown default", func(c *Config) { c.DefaultStoragePlan = "unknown" }},
		{"reserved pool", func(c *Config) { c.HostedStoragePools["legacy"] = c.HostedStoragePools["r2-primary"] }},
		{"unsafe pool", func(c *Config) { c.HostedStoragePools["../pool"] = c.HostedStoragePools["r2-primary"] }},
		{"unknown pool", func(c *Config) { p := c.StoragePlans["beta"]; p.PoolID = "unknown"; c.StoragePlans["beta"] = p }},
		{"zero project capacity", func(c *Config) { p := c.StoragePlans["beta"]; p.ProjectMaxBytes = 0; c.StoragePlans["beta"] = p }},
		{"team smaller than project", func(c *Config) { p := c.StoragePlans["beta"]; p.TeamMaxBytes = 1; c.StoragePlans["beta"] = p }},
		{"unbounded capacity", func(c *Config) {
			p := c.StoragePlans["beta"]
			p.TeamMaxBytes = math.MaxInt64
			c.StoragePlans["beta"] = p
		}},
		{"project count", func(c *Config) { p := c.StoragePlans["beta"]; p.MaxProjects = 11; c.StoragePlans["beta"] = p }},
		{"invalid creator", func(c *Config) { c.TeamCreatorStoragePlans["03"] = "beta" }},
		{"unknown creator plan", func(c *Config) { c.TeamCreatorStoragePlans["4"] = "unknown" }},
		{"public pool", func(c *Config) {
			p := c.HostedStoragePools["r2-primary"]
			p.Role = "public"
			c.HostedStoragePools["r2-primary"] = p
		}},
		{"local pool", func(c *Config) {
			p := c.HostedStoragePools["r2-primary"]
			p.CloudPostgresURL = ""
			c.HostedStoragePools["r2-primary"] = p
		}},
		{"implicit schema", func(c *Config) {
			p := c.HostedStoragePools["r2-primary"]
			p.CloudPostgresURL = "postgres://fixture:fixture-password@127.0.0.1/cache"
			c.HostedStoragePools["r2-primary"] = p
		}},
		{"schema options override", func(c *Config) {
			p := c.HostedStoragePools["r2-primary"]
			p.CloudPostgresURL += "&options=-csearch_path%3Dother"
			c.HostedStoragePools["r2-primary"] = p
		}},
		{"inherited Bazel", func(c *Config) {
			p := c.HostedStoragePools["r2-primary"]
			p.BazelRemote = &config.BazelRemoteConfig{}
			c.HostedStoragePools["r2-primary"] = p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := storageTestConfig(t)
			tc.change(&cfg)
			err := ValidateStorageConfiguration(cfg)
			if err == nil {
				t.Fatal("invalid operator policy accepted")
			}
			for _, secret := range []string{"fixture-password", "fixture-secret", "fixture-key"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("storage validation exposes a credential")
				}
			}
		})
	}
	cfg := storageTestConfig(t)
	policy, err := newStoragePolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rotated := cfg.HostedStoragePools["r2-primary"]
	rotated.CloudPostgresURL = "postgres://new-role:new-password@127.0.0.1:5432/cache?search_path=public&sslmode=disable"
	rotated.CloudS3AccessKey, rotated.CloudS3SecretKey = "rotated-key", "rotated-secret"
	fingerprint, err := storageBackendFingerprint(rotated, "", true)
	if err != nil || fingerprint != policy.pools["r2-primary"].fingerprint {
		t.Fatalf("credential rotation changed backend identity: %v", err)
	}
	// Caller mutation must not replace already-validated templates or policy.
	cfg.HostedStoragePools["r2-primary"] = rotated
	cfg.StoragePlans["beta"] = StoragePlan{}
	cfg.TeamCreatorStoragePlans["3"] = "beta"
	if policy.pools["r2-primary"].template.CloudS3SecretKey != "fixture-secret" || policy.plans["beta"].ProjectMaxBytes != 1024 || policy.creatorPlans["3"] != "larger" {
		t.Fatal("operator policy shares caller-owned mutable configuration")
	}
}

func TestStoreHostedStorageAssignmentCapacityAndPrivateScopes(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	cfg := storageTestConfig(t)
	configureTestStorage(t, s, cfg)
	team := mustTeam(t, s, "1")
	if team.StoragePlanID != "beta" {
		t.Fatalf("new Team plan = %q", team.StoragePlanID)
	}
	first := mustProject(t, s, "1", team.ID, "example/one")
	second := mustProject(t, s, "1", team.ID, "example/two")
	if first.StoragePoolID != "r2-primary" || first.AllocatedCacheBytes != 1024 || first.StorageBackendFingerprint == "" || first.StorageBackendFingerprint != second.StorageBackendFingerprint {
		t.Fatal("new project lacks its immutable allocation")
	}
	_, err := s.CreateProject(ctx, "1", team.ID, "Over capacity", "example/three", "123", "refs/heads/main")
	requireErr(t, err, ErrLimit)
	mustJoin(t, s, "1", team.ID, "2", "writer")
	_, err = s.CreateProject(ctx, "2", team.ID, "No authority", "example/four", "123", "refs/heads/main")
	requireErr(t, err, ErrDenied)
	larger := mustTeam(t, s, "3")
	other := mustProject(t, s, "3", larger.ID, "example/one")
	if larger.StoragePlanID != "larger" || other.StoragePoolID != "r2-secondary" || other.AllocatedCacheBytes != 4096 || other.StorageBackendFingerprint == first.StorageBackendFingerprint {
		t.Fatal("creator plan did not select its isolated backend")
	}
	personal, err := s.CreatePersonalCache(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	private := mustProject(t, s, "1", personal.ID, "example/one")
	if private.StoragePoolID != first.StoragePoolID || private.ID == first.ID || private.Secret == first.Secret {
		t.Fatal("Personal hosting changed project authority")
	}
	_, err = s.MemberRole(ctx, private.ID, "github-id:2")
	requireErr(t, err, ErrDenied)
	projects, err := s.Projects(ctx, "2")
	if err != nil || len(projects) != 2 {
		t.Fatalf("Team reader sees incorrect projects: %d, %v", len(projects), err)
	}
	data, err := json.Marshal(projects)
	if err != nil || strings.Contains(string(data), first.Secret) || strings.Contains(string(data), first.StorageBackendFingerprint) || strings.Contains(string(data), "fixture-secret") {
		t.Fatal("project listing exposes private configuration")
	}
}

func TestStoreHostedCapacityAdmissionIsAtomicAcrossConcurrentCreators(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s := testStore(t)
	cfg := storageTestConfig(t)
	plan := cfg.StoragePlans["beta"]
	plan.TeamMaxBytes, plan.MaxProjects = 1024, 10
	cfg.StoragePlans["beta"] = plan
	configureTestStorage(t, s, cfg)
	team := mustTeam(t, s, "1")
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for attempt := 0; attempt < 20; attempt++ {
				_, err := s.CreateProject(ctx, "1", team.ID, "Contender", fmt.Sprintf("example/contender%d", i), "123", "refs/heads/main")
				if err == nil || errors.Is(err, ErrLimit) || errors.Is(err, ErrDenied) {
					results <- err
					return
				}
				if ctx.Err() != nil {
					results <- err
					return
				}
			}
			results <- errors.New("capacity contender exhausted retries")
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrLimit) {
			t.Errorf("capacity contender: %v", err)
		}
	}
	var count, allocated int64
	if err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(allocated_cache_bytes),0) FROM lc_portal_projects WHERE team_id=$1`, team.ID).Scan(&count, &allocated); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || count != 1 || allocated != 1024 {
		t.Fatalf("admission winners=%d projects=%d allocation=%d", successes, count, allocated)
	}
}

func TestStoreHostedStorageRecoveryPreservesLegacyAndRejectsBackendChanges(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	legacy := mustTeam(t, s, "1")
	legacyProject := mustProject(t, s, "1", legacy.ID, "example/legacy")
	cfg := storageTestConfig(t)
	configureTestStorage(t, s, cfg)
	legacyProjects, err := s.Projects(ctx, "1")
	if err != nil || len(legacyProjects) != 1 || legacyProjects[0].StoragePoolID != "legacy" || legacyProjects[0].AllocatedCacheBytes != 0 || legacyProjects[0].StorageBackendFingerprint == "" {
		t.Fatal("existing project silently migrated or stayed unbound", err)
	}
	if legacyProjects[0].ID != legacyProject.ID || legacyProjects[0].Secret != legacyProject.Secret {
		t.Fatal("legacy authority changed")
	}
	hosted := mustTeam(t, s, "1")
	project := mustProject(t, s, "1", hosted.ID, "example/new")
	// Reload policy, as on restart. A changed default affects future Teams only.
	cfg.DefaultStoragePlan = "larger"
	policy := configureTestStorage(t, s, cfg)
	newTeam := mustTeam(t, s, "2")
	if newTeam.StoragePlanID != "larger" {
		t.Fatal("changed default was not applied to a future Team")
	}
	if _, err := policy.projectTemplate(project, cfg.StoragePlans["beta"]); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"bucket", func(c *Config) {
			p := c.HostedStoragePools["r2-primary"]
			p.CloudS3Bucket = "replacement"
			c.HostedStoragePools["r2-primary"] = p
		}},
		{"database", func(c *Config) {
			p := c.HostedStoragePools["r2-primary"]
			p.CloudPostgresURL = strings.Replace(p.CloudPostgresURL, "/cache?", "/other?", 1)
			c.HostedStoragePools["r2-primary"] = p
		}},
		{"schema", func(c *Config) {
			p := c.HostedStoragePools["r2-primary"]
			p.CloudPostgresURL = strings.Replace(p.CloudPostgresURL, "search_path=public", "search_path=other", 1)
			c.HostedStoragePools["r2-primary"] = p
		}},
		{"plan entitlement", func(c *Config) { p := c.StoragePlans["beta"]; p.ProjectMaxBytes++; c.StoragePlans["beta"] = p }},
		{"missing persisted plan", func(c *Config) { delete(c.StoragePlans, "beta"); c.DefaultStoragePlan = "larger" }},
		{"legacy backend", func(c *Config) { c.DataDir = t.TempDir() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := storageTestConfig(t)
			// Keep the same legacy namespace and pool template paths.
			changed.DataDir = cfg.DataDir
			tc.change(&changed)
			candidate, err := newStoragePolicy(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.configureStorage(ctx, candidate); err == nil {
				t.Fatal("persisted assignment was silently changed")
			}
		})
	}
	rotated := cfg.HostedStoragePools["r2-primary"]
	rotated.CloudPostgresURL = strings.Replace(rotated.CloudPostgresURL, "fixture:fixture-password", "rotated:rotated-password", 1)
	rotated.CloudS3AccessKey, rotated.CloudS3SecretKey = "rotated-key", "rotated-secret"
	cfg.HostedStoragePools["r2-primary"] = rotated
	configureTestStorage(t, s, cfg)
	loaded, err := s.AllProjects(ctx)
	if err != nil || len(loaded) != 2 {
		t.Fatalf("recovery lost project assignments: %d, %v", len(loaded), err)
	}
}
