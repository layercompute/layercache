package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResourceSnapshotSQLite(t *testing.T) {
	db, repo := openSQLiteExecution(t, filepath.Join(t.TempDir(), "resources.db"), nil)
	defer db.Close()
	exerciseResourceSnapshot(t, repo)
}

func TestResourceSnapshotPostgres(t *testing.T) {
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
	schema := fmt.Sprintf("execution_resources_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
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
	repo, err := NewSQLRepository(context.Background(), db, "postgres", "widgets", nil)
	if err != nil {
		t.Fatal(err)
	}
	exerciseResourceSnapshot(t, repo)
}

func exerciseResourceSnapshot(t *testing.T, repo *SQLRepository) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	write := func(a Authority, report ObservationRequest, received time.Time) {
		t.Helper()
		if err := repo.Record(ctx, a, report, received); err != nil {
			t.Fatal(err)
		}
	}
	lookup := func(a Authority, when time.Time) *ResourceSnapshot {
		t.Helper()
		got, err := repo.ResourceSnapshot(ctx, a, when)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	missing := authorityFixture("resources-missing")
	if got := lookup(missing, at); got != nil {
		t.Fatalf("missing execution acquired resources: %+v", got)
	}

	a := authorityFixture("resources-main")
	start := reportFixture()
	start.Provider = "ubicloud"
	write(a, start, at.Add(-2*time.Hour))
	finish := reportFixture()
	finish.Phase = "finish"
	finish.Sample.CPU.EffectiveMilliCPU = number(2000)
	write(a, finish, at.Add(-time.Hour))
	got := lookup(a, at.Add(-90*time.Minute))
	if got == nil || got.Phase != "start" || !got.ObservedAt.Equal(at.Add(-2*time.Hour)) || got.Basis != "client-reported" || got.Provider != "ubicloud" || got.ProviderEvidence != "declared" || !reflect.DeepEqual(got.Sample, start.Sample) {
		t.Fatalf("publication acquired a future phase or changed sample: %+v", got)
	}
	got = lookup(a, at)
	if got == nil || got.Phase != "finish" || !got.ObservedAt.Equal(at.Add(-time.Hour)) || got.Provider != "unknown" || got.ProviderEvidence != "unknown" || *got.Sample.CPU.EffectiveMilliCPU != 2000 {
		t.Fatalf("latest prior phase was not selected: %+v", got)
	}
	for name, mutate := range map[string]func(*Authority){
		"project":   func(value *Authority) { value.Project = "foreign" },
		"owner":     func(value *Authority) { value.OwnerID = "github-id:43" },
		"execution": func(value *Authority) { value.ExecutionID = missing.ExecutionID },
		"run":       func(value *Authority) { value.RunID = "another-run" },
		"workspace": func(value *Authority) { value.WorkspaceID = "another-workspace" },
	} {
		other := a
		mutate(&other)
		if got := lookup(other, at); got != nil {
			t.Fatalf("%s mismatch acquired another context's resources: %+v", name, got)
		}
	}
	if _, err := repo.ResourceSnapshot(ctx, a, time.Time{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero publication time accepted: %v", err)
	}

	for _, age := range []time.Duration{-time.Nanosecond, 0, 24 * time.Hour, 24*time.Hour + time.Nanosecond} {
		a := authorityFixture(fmt.Sprintf("resources-age-%d", age))
		write(a, reportFixture(), at.Add(-age))
		got := lookup(a, at)
		want := age >= 0 && age <= 24*time.Hour
		if (got != nil) != want {
			t.Fatalf("age %s: sample=%+v, want qualified=%v", age, got, want)
		}
	}

	// An alias carried by a newer primary observation has no independent receive
	// time. It cannot qualify the older context even though inventory can use it
	// for cache-activity linkage.
	alias := authorityFixture("resources-alias")
	write(alias, reportFixture(), at.Add(-time.Hour))
	newPrimary := alias
	newPrimary.RunID, newPrimary.WorkspaceID = "new-primary-run", "new-primary-workspace"
	write(newPrimary, reportFixture(), at.Add(-time.Minute))
	if got := lookup(alias, at); got != nil {
		t.Fatalf("context alias acquired newer primary resources: %+v", got)
	}
	if got := lookup(newPrimary, at); got == nil || !got.ObservedAt.Equal(at.Add(-time.Minute)) {
		t.Fatalf("exact newer primary missing: %+v", got)
	}

	// A decoded result owns its pointers and slices, so later phase overwrites
	// and a caller's mutations cannot alter a frozen publication or another read.
	frozen := lookup(a, at)
	if frozen == nil || frozen.Phase != "finish" || *frozen.Sample.CPU.EffectiveMilliCPU != 2000 {
		t.Fatalf("another execution on this installation supplied resources: %+v", frozen)
	}
	before, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	finish.Sample.CPU.EffectiveMilliCPU = number(1000)
	finish.Sample.Filesystems[0].Roles = []string{"staging"}
	write(a, finish, at.Add(-time.Second))
	after, err := json.Marshal(frozen)
	if err != nil || string(before) != string(after) {
		t.Fatalf("overwriting the source observation mutated a frozen result: %v", err)
	}
	*frozen.Sample.CPU.EffectiveMilliCPU = 1
	frozen.Sample.Filesystems[0].Roles[0] = "localCache"
	current := lookup(a, at)
	if current == nil || *current.Sample.CPU.EffectiveMilliCPU != 1000 || !reflect.DeepEqual(current.Sample.Filesystems[0].Roles, []string{"staging"}) {
		t.Fatalf("caller mutation escaped its snapshot: %+v", current)
	}
	for _, private := range []string{a.ExecutionID, a.RunID, a.WorkspaceID, a.OwnerID} {
		if strings.Contains(string(before), private) {
			t.Fatalf("resource snapshot exported execution context %q", private)
		}
	}

	job := Authority{Context: Context{Kind: "githubActions", OwnerID: "github-id:42", ExecutionID: "github-actions:123:456:1:789", RunnerEnvironment: "github-hosted"}, Project: repo.project, RunID: "job-run", WorkspaceID: "job-workspace"}
	report := reportFixture()
	report.InstallationID, report.Provider = "", "ubicloud"
	write(job, report, at.Add(-time.Second))
	if got := lookup(job, at); got == nil || got.Provider != "github" || got.ProviderEvidence != "signed" || got.Basis != "client-reported" {
		t.Fatalf("provider authority changed resource evidence: %+v", got)
	}
	otherJob := job
	otherJob.RunnerEnvironment = "self-hosted"
	if got := lookup(otherJob, at); got != nil {
		t.Fatalf("runner context mismatch acquired job resources: %+v", got)
	}

	unknown := authorityFixture("resources-unknown")
	unknownReport := ObservationRequest{Version: Version, Phase: "start", InstallationID: strings.Repeat("c", 64), Sample: Sample{OS: "other", Arch: "other", CPU: CPU{LimitKnowledge: "unknown"}, Memory: Memory{LimitKnowledge: "unknown"}}}
	write(unknown, unknownReport, at)
	if got := lookup(unknown, at); got == nil || got.Sample.CPU.EffectiveMilliCPU != nil || got.Sample.Memory.EffectiveLimitBytes != nil {
		t.Fatalf("missing resources were invented: %+v", got)
	}
}

func TestResourceSnapshotRejectsCorruptPrimaryDocument(t *testing.T) {
	db, repo := openSQLiteExecution(t, filepath.Join(t.TempDir(), "resources.db"), nil)
	defer db.Close()
	a := authorityFixture("resources-corrupt")
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if err := repo.Record(context.Background(), a, reportFixture(), at); err != nil {
		t.Fatal(err)
	}
	for _, document := range []string{`{"unknown":true}`, `null`, `{"authority":`} {
		if _, err := db.Exec(`UPDATE layercache_execution_observations_v1 SET document_json=?`, document); err != nil {
			t.Fatal(err)
		}
		if got, err := repo.ResourceSnapshot(context.Background(), a, at); got != nil || err == nil {
			t.Fatalf("corrupt primary qualified: %+v, %v", got, err)
		}
	}
}
