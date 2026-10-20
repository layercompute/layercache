package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecutionRecurrenceSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recurrence.db")
	db, repo := openSQLiteExecution(t, path, nil)
	exerciseExecutionRecurrence(t, repo)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, repo = openSQLiteExecution(t, path, nil)
	defer db.Close()
	now := time.Now().UTC()
	assertExecutionRecurrence(t, repo, now.Add(-24*time.Hour), now.Add(time.Minute), "github-id:42", strings.Repeat("a", 64), 2)
}

func TestExecutionRecurrencePostgres(t *testing.T) {
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
	schema := fmt.Sprintf("execution_recurrence_%d", time.Now().UnixNano())
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
	repo, err := NewSQLRepository(context.Background(), db, "postgres", "widgets", nil)
	if err != nil {
		t.Fatal(err)
	}
	exerciseExecutionRecurrence(t, repo)
	// Reconstructing the projection uses the retained samples, without a new
	// collector, recurrence table or process-local counter.
	repo, err = NewSQLRepository(context.Background(), db, "postgres", "widgets", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	assertExecutionRecurrence(t, repo, now.Add(-24*time.Hour), now.Add(time.Minute), "github-id:42", strings.Repeat("a", 64), 2)
}

func exerciseExecutionRecurrence(t *testing.T, repo *SQLRepository) {
	t.Helper()
	now := time.Now().UTC()
	start, end := now.Add(-24*time.Hour), now.Add(time.Minute)
	write := func(authority Authority, report ObservationRequest, at time.Time) {
		t.Helper()
		if err := repo.Record(context.Background(), authority, report, at); err != nil {
			t.Fatal(err)
		}
	}
	report := reportFixture()
	first := authorityFixture("recurrence-first")
	write(authorityFixture("outside-period"), report, now.Add(-48*time.Hour))
	write(first, report, now.Add(-5*time.Minute))
	write(first, report, now.Add(-4*time.Minute))
	report.Phase = "finish"
	write(first, report, now.Add(-3*time.Minute))
	assertExecutionRecurrence(t, repo, start, end, first.OwnerID, report.InstallationID, 1)

	second := authorityFixture("recurrence-second")
	// Run and workspace values alone cannot collapse independently
	// authenticated executions. The complete tuple includes ExecutionID.
	second.RunID, second.WorkspaceID = first.RunID, first.WorkspaceID
	report.Phase = "start"
	write(second, report, now.Add(-2*time.Minute))
	report.Phase = "finish"
	write(second, report, now.Add(-time.Minute))
	write(authorityFixture("after-period"), report, now.Add(2*time.Hour))
	assertExecutionRecurrence(t, repo, start, end, first.OwnerID, report.InstallationID, 2)
	assertExecutionRecurrence(t, repo, now.Add(-90*time.Second), end, first.OwnerID, report.InstallationID, 1)

	otherOwner := authorityFixture("other-owner")
	otherOwner.Context = *ClientContext("github-id:43", "other-owner")
	report.Phase = "start"
	write(otherOwner, report, now.Add(-time.Minute))
	assertExecutionRecurrence(t, repo, start, end, otherOwner.OwnerID, report.InstallationID, 1)

	// Aliases carried into a newer stored document have no independent receive
	// time. A context from outside the period cannot manufacture recurrence.
	alias := authorityFixture("alias")
	report.InstallationID = strings.Repeat("b", 64)
	write(alias, report, now.Add(-48*time.Hour))
	alias.RunID, alias.WorkspaceID = "newer-run", "newer-workspace"
	write(alias, report, now.Add(-2*time.Minute))
	report.Phase = "finish"
	write(alias, report, now.Add(-time.Minute))
	assertExecutionRecurrence(t, repo, start, end, alias.OwnerID, report.InstallationID, 1)
	alias.WorkspaceID = "independent-workspace"
	write(alias, report, now.Add(-30*time.Second))
	assertExecutionRecurrence(t, repo, start, end, alias.OwnerID, report.InstallationID, 2)
	assertExecutionRecurrence(t, repo, start, end, first.OwnerID, strings.Repeat("a", 64), 2)

	inv, err := repo.Inventory(context.Background(), repo.project, start, end)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{first.RunID, first.WorkspaceID, "newer-run", "independent-workspace"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private execution context exported: %q", private)
		}
	}
}

func assertExecutionRecurrence(t *testing.T, repo *SQLRepository, from, to time.Time, owner, installation string, want int) {
	t.Helper()
	inv, err := repo.Inventory(context.Background(), repo.project, from, to)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range inv.Installations {
		if entry.OwnerID == owner && entry.InstallationID == installation {
			if entry.ObservedExecutions != want {
				t.Fatalf("reported executions=%d, want %d", entry.ObservedExecutions, want)
			}
			return
		}
	}
	t.Fatal("installation missing from recurrence inventory")
}
