package execution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func number(value int64) *int64 { return &value }
func reportFixture() ObservationRequest {
	return ObservationRequest{Version: Version, Phase: "start", InstallationID: strings.Repeat("a", 64), Sample: Sample{OS: "linux", Arch: "amd64", CPU: CPU{VisibleLogical: number(8), AffinityLogical: number(4), QuotaMilliCPU: number(2500), EffectiveMilliCPU: number(2500), LimitKnowledge: "complete"}, Memory: Memory{VisibleTotalBytes: number(16 << 30), CgroupLimitBytes: number(8 << 30), EffectiveLimitBytes: number(8 << 30), LimitKnowledge: "complete"}, Filesystems: []Filesystem{{ID: strings.Repeat("b", 64), Roles: []string{"workspace", "localCache"}, CapacityBytes: number(100), FreeBytes: number(10), AvailableBytes: number(5)}}}}
}
func authorityFixture(nonce string) Authority {
	return Authority{Context: *ClientContext("github-id:42", nonce), Project: "widgets", RunID: "run-" + nonce, WorkspaceID: "workspace-" + nonce}
}

func TestExecutionObservationValidation(t *testing.T) {
	a := authorityFixture("one")
	base := reportFixture()
	if err := base.Validate(a); err != nil {
		t.Fatal(err)
	}
	full := reportFixture()
	full.Sample.Filesystems[0].FreeBytes = number(0)
	full.Sample.Filesystems[0].AvailableBytes = number(0)
	if err := full.Validate(a); err != nil {
		t.Fatalf("full filesystem rejected: %v", err)
	}
	unknown := ObservationRequest{Version: 1, Phase: "start", InstallationID: strings.Repeat("a", 64), Sample: Sample{OS: "other", Arch: "other", CPU: CPU{LimitKnowledge: "unknown"}, Memory: Memory{LimitKnowledge: "unknown"}, Filesystems: []Filesystem{}}}
	if err := unknown.Validate(a); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*ObservationRequest){"installation missing": func(r *ObservationRequest) { r.InstallationID = "" }, "hostname identity": func(r *ObservationRequest) { r.InstallationID = "developer-laptop" }, "unknown provider": func(r *ObservationRequest) { r.Provider = "https://host.example" }, "negative available": func(r *ObservationRequest) { r.Sample.Filesystems[0].AvailableBytes = number(-1) }, "free above capacity": func(r *ObservationRequest) { r.Sample.Filesystems[0].FreeBytes = number(101) }, "available above free": func(r *ObservationRequest) { r.Sample.Filesystems[0].AvailableBytes = number(11) }, "duplicate filesystem": func(r *ObservationRequest) {
		r.Sample.Filesystems = append(r.Sample.Filesystems, r.Sample.Filesystems[0])
	}, "unsafe integer": func(r *ObservationRequest) { r.Sample.Memory.VisibleTotalBytes = number(MaximumSafeInteger + 1) }, "guessed effective cpu": func(r *ObservationRequest) { r.Sample.CPU.LimitKnowledge = "partial" }, "affinity exceeds visible": func(r *ObservationRequest) { r.Sample.CPU.AffinityLogical = number(9) }, "effective exceeds quota": func(r *ObservationRequest) { r.Sample.CPU.EffectiveMilliCPU = number(3000) }, "effective memory exceeds bound": func(r *ObservationRequest) { r.Sample.Memory.EffectiveLimitBytes = number(9 << 30) }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := reportFixture()
			mutate(&r)
			if r.Validate(a) == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
}

func openSQLiteExecution(t *testing.T, path string, activity ActivityLookup) (*sql.DB, *SQLRepository) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		t.Fatal(err)
	}
	repo, err := NewSQLRepository(context.Background(), db, "sqlite", "widgets", activity)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, repo
}

func TestExecutionSQLitePersistenceBoundsAndConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "measurements.db")
	db, repo := openSQLiteExecution(t, path, nil)
	exerciseRepository(t, repo, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, repo = openSQLiteExecution(t, path, nil)
	defer db.Close()
	now := time.Now().UTC()
	inventory, err := repo.Inventory(context.Background(), "widgets", now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil || len(inventory.Installations) == 0 {
		t.Fatalf("restart inventory %+v %v", inventory, err)
	}
}

func exerciseRepository(t *testing.T, repo *SQLRepository, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Minute)
	a := authorityFixture("one")
	r := reportFixture()
	if err := repo.Record(ctx, a, r, now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- repo.Record(ctx, a, r, now.Add(time.Duration(i)*time.Millisecond))
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatalf("concurrent upsert: %v", err)
		}
	}
	r.Phase = "finish"
	if err := repo.Record(ctx, a, r, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(repo.query(`SELECT COUNT(*) FROM layercache_execution_observations_v1 WHERE project_id=?`), repo.project).Scan(&count); err != nil || count != 2 {
		t.Fatalf("phase count %d %v", count, err)
	}
	other := a
	other.OwnerID = "github-id:43"
	if !errors.Is(repo.Record(ctx, other, r, now.Add(2*time.Second)), ErrConflict) {
		t.Fatal("foreign owner overwrote execution")
	}
	other = a
	other.Project = "foreign"
	if !errors.Is(repo.Record(ctx, other, r, now), ErrInvalid) {
		t.Fatal("foreign project accepted")
	}
	second := authorityFixture("two")
	r.Phase = "start"
	if err := repo.Record(ctx, second, r, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	job := Authority{Context: Context{Kind: "githubActions", OwnerID: "github-id:42", ExecutionID: "github-actions:123:456:1:789", RunnerEnvironment: "github-hosted"}, Project: "widgets", RunID: "github-actions:acme/widgets:456:1:check:789", WorkspaceID: "github-actions-check:acme/widgets:789"}
	r.InstallationID = ""
	r.Provider = "ubicloud"
	if err := repo.Record(ctx, job, r, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	r.Phase = "finish"
	if err := repo.Record(ctx, job, r, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	inv, err := repo.Inventory(ctx, "widgets", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Installations) != 1 || len(inv.Jobs) != 1 || inv.Jobs[0].Provider != "github" || inv.Jobs[0].ProviderEvidence != "signed" || inv.Installations[0].CacheActivity != "unconfirmed" {
		t.Fatalf("inventory %+v", inv)
	}
	if inv.Installations[0].ExecutionID != second.ExecutionID || inv.Jobs[0].Phase != "finish" {
		t.Fatalf("latest sample selection %+v", inv)
	}
	// Retain at most two executions, even when both have two phase snapshots.
	repo.maximumExecutions = 2
	third := authorityFixture("three")
	r = reportFixture()
	if err = repo.Record(ctx, third, r, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	inv, err = repo.Inventory(ctx, "widgets", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || !inv.HistoryTruncated {
		t.Fatalf("coverage %+v %v", inv, err)
	}
	if err = db.QueryRow(repo.query(`SELECT COUNT(DISTINCT execution_id) FROM layercache_execution_observations_v1 WHERE project_id=?`), repo.project).Scan(&count); err != nil || count > 2 {
		t.Fatalf("execution bound %d %v", count, err)
	}
	// A new alias displaces the older alias without changing artifact tables.
	repo.maximumInstallations = 1
	r.InstallationID = strings.Repeat("c", 64)
	fourth := authorityFixture("four")
	if err = repo.Record(ctx, fourth, r, now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	inv, err = repo.Inventory(ctx, "widgets", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || len(inv.Installations) != 1 || inv.Installations[0].InstallationID != r.InstallationID {
		t.Fatalf("installation bound %+v %v", inv, err)
	}
	// Expiry is based on trusted receive time. Old rows disappear on next write.
	if err = repo.Record(ctx, authorityFixture("expired"), r, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = repo.Record(ctx, fourth, r, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(repo.query(`SELECT COUNT(*) FROM layercache_execution_observations_v1 WHERE project_id=? AND received_at<?`), repo.project, now.Add(-30*24*time.Hour).UnixNano()).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired rows %d %v", count, err)
	}
}

func TestExecutionInventoryAggregatesActivityWithinInstallation(t *testing.T) {
	first := authorityFixture("first")
	second := authorityFixture("second")
	db, repo := openSQLiteExecution(t, filepath.Join(t.TempDir(), "measurements.db"), func(_ context.Context, values []Authority) (map[string]bool, error) {
		known := map[string]bool{}
		for _, value := range values {
			if value.ExecutionID == first.ExecutionID {
				known[value.ExecutionID] = true
			}
		}
		return known, nil
	})
	defer db.Close()
	now := time.Now().UTC().Add(-time.Minute)
	for i, a := range []Authority{first, second} {
		if err := repo.Record(context.Background(), a, reportFixture(), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := repo.Inventory(context.Background(), "widgets", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil || len(inv.Installations) != 1 || inv.Installations[0].ExecutionID != second.ExecutionID || inv.Installations[0].CacheActivity != "observed" {
		t.Fatalf("aggregated %+v %v", inv, err)
	}
}

func TestExecutionPostgresPersistenceAgainstPostgres(t *testing.T) {
	source := os.Getenv("LAYERCACHE_MEASUREMENT_POSTGRES_URL")
	if source == "" {
		t.Skip("set LAYERCACHE_MEASUREMENT_POSTGRES_URL for isolated PostgreSQL fixture")
	}
	parsed, err := url.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", source)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("execution_qa_%d", time.Now().UnixNano())
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// First-start DDL for different projects shares one PostgreSQL schema.
	start := make(chan struct{})
	results := make(chan error, 8)
	var initialized sync.WaitGroup
	for i := range 8 {
		initialized.Add(1)
		go func() {
			defer initialized.Done()
			<-start
			_, e := NewSQLRepository(context.Background(), db, "postgres", fmt.Sprintf("parallel-%d", i), nil)
			results <- e
		}()
	}
	close(start)
	initialized.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatalf("concurrent schema initialization: %v", e)
		}
	}
	repo, err := NewSQLRepository(context.Background(), db, "postgres", "widgets", nil)
	if err != nil {
		t.Fatal(err)
	}
	exerciseRepository(t, repo, db)
}
