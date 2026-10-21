package measurement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type operationTestFactory func(*testing.T) *operationSQLRepository

func operationSQLiteFactory(t *testing.T) *operationSQLRepository {
	t.Helper()
	owner, err := OpenSQLiteRepository(filepath.Join(t.TempDir(), "measurement.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	r, err := owner.OperationRepository(context.Background(), operationTestAuthority().Project)
	if err != nil {
		t.Fatal(err)
	}
	return r.(*operationSQLRepository)
}

func TestOperationSQLiteContract(t *testing.T) { runOperationSQLContract(t, operationSQLiteFactory) }

func runOperationSQLContract(t *testing.T, factory operationTestFactory) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	t.Run("immutable receipt and authority conflict", func(t *testing.T) {
		r := factory(t)
		a := operationTestAuthority()
		report := operationTestReport("immutable-0001", base)
		mutable := a
		mutable.SourceCommit = "main"
		if _, err := r.RecordOperation(ctx, mutable, report, base.Add(time.Minute)); !errors.Is(err, ErrOperationInvalid) {
			t.Fatalf("mutable source admitted: %v", err)
		}
		first, err := r.RecordOperation(ctx, a, report, base.Add(time.Minute))
		if err != nil || !first.Recorded || first.Repeated || first.ReceiptID != operationReceiptID(a, report.OperationID) {
			t.Fatalf("first receipt: %+v %v", first, err)
		}
		retry, err := r.RecordOperation(ctx, a, report, base.Add(2*time.Minute))
		if err != nil || !retry.Repeated || retry.ReceiptID != first.ReceiptID {
			t.Fatalf("exact retry: %+v %v", retry, err)
		}
		changed := report
		changed.Status = "failed"
		if _, err = r.RecordOperation(ctx, a, changed, base.Add(time.Minute)); !errors.Is(err, ErrOperationConflict) {
			t.Fatalf("changed report accepted: %v", err)
		}
		for name, change := range map[string]func(*NativeAuthority){"source": func(a *NativeAuthority) { a.SourceCommit = strings.Repeat("b", 40) }, "subject": func(a *NativeAuthority) { a.Subject = "other-member" }, "profile": func(a *NativeAuthority) { a.CompatibilityID = "other-profile" }} {
			t.Run(name, func(t *testing.T) {
				changed := a
				change(&changed)
				if _, err := r.RecordOperation(ctx, changed, report, base.Add(time.Minute)); !errors.Is(err, ErrOperationConflict) {
					t.Fatalf("authority retry accepted: %v", err)
				}
			})
		}
		period, err := r.OperationPeriod(ctx, base, base.Add(time.Minute))
		if err != nil || period.StoredObservations != 1 || len(period.Comparisons) != 0 {
			t.Fatalf("receipt projection: %+v %v", period, err)
		}
		wire, _ := json.Marshal(period)
		for _, private := range []string{a.Subject, a.RunID, a.WorkspaceID, "authority_json", "report_sha256"} {
			if strings.Contains(string(wire), private) {
				t.Fatalf("private authority projected: %s", private)
			}
		}
	})
	t.Run("retained retry outlives admission without extending expiry", func(t *testing.T) {
		r := factory(t)
		a := operationTestAuthority()
		report := operationTestReport("late-retry-0001", base.Add(-48*time.Hour))
		receivedAt := report.FinishedAt.Add(time.Minute)
		first, err := r.RecordOperation(ctx, a, report, receivedAt)
		if err != nil {
			t.Fatal(err)
		}
		late := receivedAt.Add(25 * time.Hour)
		for _, now := range []time.Time{late, receivedAt.Add(OperationRetentionDays*24*time.Hour - time.Nanosecond)} {
			retry, err := r.RecordOperation(ctx, a, report, now)
			if err != nil || !retry.Repeated || retry.ReceiptID != first.ReceiptID {
				t.Fatalf("retained late retry: %+v %v", retry, err)
			}
			changed := report
			changed.Status = "failed"
			if _, err := r.RecordOperation(ctx, a, changed, now); !errors.Is(err, ErrOperationConflict) {
				t.Fatalf("retained late content conflict hidden by admission age: %v", err)
			}
			changedAuthority := a
			changedAuthority.SourceCommit = strings.Repeat("b", 40)
			if _, err := r.RecordOperation(ctx, changedAuthority, report, now); !errors.Is(err, ErrOperationConflict) {
				t.Fatalf("retained late source conflict hidden by admission age: %v", err)
			}
		}
		var storedAt int64
		if err := r.db.QueryRowContext(ctx, r.query(`SELECT received_at FROM layercache_operation_reports_v1 WHERE project_id=? AND receipt_id=?`), r.project, first.ReceiptID).Scan(&storedAt); err != nil || storedAt != receivedAt.UnixNano() {
			t.Fatalf("retry changed original retention anchor: %d %v", storedAt, err)
		}
		stale := report
		stale.OperationID = "run-operation-stale-new-0001"
		if _, err := r.RecordOperation(ctx, a, stale, late); !errors.Is(err, ErrOperationInvalid) {
			t.Fatalf("new stale report admitted: %v", err)
		}
		expiredAt := receivedAt.Add(OperationRetentionDays*24*time.Hour + time.Nanosecond)
		if _, err := r.RecordOperation(ctx, a, report, expiredAt); !errors.Is(err, ErrOperationInvalid) {
			t.Fatalf("expired report resurrected: %v", err)
		}
		// A current independent admission commits retention cleanup. The old
		// exact body must remain inadmissible after its row is physically gone.
		fresh := operationTestReport("after-expiry-0001", expiredAt.Add(-time.Minute))
		if _, err := r.RecordOperation(ctx, a, fresh, expiredAt); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := r.db.QueryRowContext(ctx, r.query(`SELECT COUNT(*) FROM layercache_operation_reports_v1 WHERE project_id=? AND receipt_id=?`), r.project, first.ReceiptID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expired row was not reclaimed: %d %v", count, err)
		}
		if _, err := r.RecordOperation(ctx, a, report, expiredAt); !errors.Is(err, ErrOperationInvalid) {
			t.Fatalf("deleted expired report resurrected: %v", err)
		}
	})
	t.Run("explicit baseline outside selected period", func(t *testing.T) {
		r := factory(t)
		a := operationTestAuthority()
		baseline := operationTestReport("baseline-0003", base)
		br, err := r.RecordOperation(ctx, a, baseline, base.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		candidate := operationTestReport("candidate-0003", base.Add(2*time.Minute))
		candidate.CachePreparation = "hit"
		candidate.BaselineReceiptID = br.ReceiptID
		if _, err = r.RecordOperation(ctx, a, candidate, base.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		period, err := r.OperationPeriod(ctx, candidate.StartedAt, candidate.FinishedAt.Add(time.Second))
		if err != nil || period.StoredObservations != 1 || len(period.Comparisons) != 1 || !period.Comparisons[0].Matched || *period.Comparisons[0].ObservedDifferenceMS != 0 {
			t.Fatalf("explicit retained baseline: %+v %v", period, err)
		}
		// Neither a later fast observation nor the selected-period bounds are
		// allowed to replace the expressly named baseline.
		other := operationTestReport("other-fast-0003", base.Add(3*time.Minute))
		other.ElapsedMS = 0
		other.FinishedAt = other.StartedAt
		other.PhaseTimingMS = OperationPhaseTiming{}
		if _, err = r.RecordOperation(ctx, a, other, base.Add(4*time.Minute)); err != nil {
			t.Fatal(err)
		}
		period, err = r.OperationPeriod(ctx, base, base.Add(5*time.Minute))
		if err != nil || len(period.Comparisons) != 1 || period.Comparisons[0].BaselineReceiptID != br.ReceiptID {
			t.Fatalf("automatic pairing occurred: %+v %v", period, err)
		}
	})
	t.Run("foreign and expired baselines remain missing", func(t *testing.T) {
		r := factory(t)
		a := operationTestAuthority()
		foreign, err := newOperationSQLRepository(ctx, r.db, r.dialect, "foreign/project")
		if err != nil {
			t.Fatal(err)
		}
		fa := a
		fa.Project = "foreign/project"
		baseline := operationTestReport("foreign-0004", base)
		br, err := foreign.RecordOperation(ctx, fa, baseline, base.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		candidate := operationTestReport("candidate-0004", base.Add(2*time.Minute))
		candidate.CachePreparation = "hit"
		candidate.BaselineReceiptID = br.ReceiptID
		if _, err = r.RecordOperation(ctx, a, candidate, base.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		period, err := r.OperationPeriod(ctx, base, base.Add(4*time.Minute))
		if err != nil || len(period.Comparisons) != 1 || period.Comparisons[0].Matched || period.Comparisons[0].Reasons[0] != "missingBaseline" || period.Comparisons[0].BaselineElapsedMS != nil {
			t.Fatalf("foreign baseline exposed: %+v %v", period, err)
		}
		old := operationTestReport("expired-0004", base.Add(-31*24*time.Hour))
		oldReceipt, err := r.RecordOperation(ctx, a, old, old.FinishedAt)
		if err != nil {
			t.Fatal(err)
		}
		candidate.OperationID = "run-operation-expiry-candidate-0004"
		candidate.BaselineReceiptID = oldReceipt.ReceiptID
		if _, err = r.RecordOperation(ctx, a, candidate, base.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		var count int
		if err = r.db.QueryRowContext(ctx, r.query(`SELECT COUNT(*) FROM layercache_operation_reports_v1 WHERE project_id=? AND receipt_id=?`), r.project, oldReceipt.ReceiptID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expired baseline retained: %d %v", count, err)
		}
	})
	t.Run("failed observations retained without a delta", func(t *testing.T) {
		r := factory(t)
		a := operationTestAuthority()
		b := operationTestReport("failed-base-0005", base)
		br, err := r.RecordOperation(ctx, a, b, base.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		c := operationTestReport("failed-candidate-0005", base.Add(2*time.Minute))
		c.BaselineReceiptID = br.ReceiptID
		c.CachePreparation = "hit"
		c.Status = "failed"
		if _, err = r.RecordOperation(ctx, a, c, base.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		period, err := r.OperationPeriod(ctx, base, base.Add(4*time.Minute))
		if err != nil || period.StoredObservations != 2 || len(period.Comparisons) != 1 || period.Comparisons[0].Matched || period.Comparisons[0].ObservedDifferenceMS != nil || period.Comparisons[0].Reasons[0] != "unsuccessfulOperation" {
			t.Fatalf("failure dropped or compared: %+v %v", period, err)
		}
	})
	t.Run("latest observations bounded with full selected count", func(t *testing.T) {
		r := factory(t)
		a := operationTestAuthority()
		for i := range 35 {
			a.WorkspaceID = fmt.Sprintf("job-%d", i)
			report := operationTestReport(fmt.Sprintf("bounded-%04d", i), base.Add(time.Duration(i)*time.Minute))
			if _, err := r.RecordOperation(ctx, a, report, report.FinishedAt); err != nil {
				t.Fatal(err)
			}
		}
		period, err := r.OperationPeriod(ctx, base, base.Add(time.Hour))
		if err != nil || period.StoredObservations != 35 || len(period.Observations) != 32 || !period.HistoryTruncated || len(period.Comparisons) != 0 || !period.Observations[0].Report.StartedAt.Equal(base.Add(34*time.Minute)) {
			t.Fatalf("unbounded or selected favorable observations: %+v %v", period, err)
		}
	})
	t.Run("job quota admits exact retry and refuses changed retry", func(t *testing.T) {
		r := factory(t)
		a := operationTestAuthority()
		var first OperationReport
		for i := range OperationMaximumPerJob {
			report := operationTestReport(fmt.Sprintf("job-limit-%04d", i), base)
			if i == 0 {
				first = report
			}
			if _, err := r.RecordOperation(ctx, a, report, base.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.RecordOperation(ctx, a, operationTestReport("job-extra-0006", base), base.Add(time.Minute)); !errors.Is(err, ErrOperationQuota) {
			t.Fatalf("job quota escaped: %v", err)
		}
		if receipt, err := r.RecordOperation(ctx, a, first, base.Add(time.Minute)); err != nil || !receipt.Repeated {
			t.Fatalf("exact replay blocked at capacity: %+v %v", receipt, err)
		}
		first.Status = "failed"
		if _, err := r.RecordOperation(ctx, a, first, base.Add(time.Minute)); !errors.Is(err, ErrOperationConflict) {
			t.Fatalf("conflict hidden by quota: %v", err)
		}
	})
	t.Run("project byte admission is atomic", func(t *testing.T) {
		r := factory(t)
		a := operationTestAuthority()
		first := operationTestReport("byte-limit-0007", base)
		if _, err := r.RecordOperation(ctx, a, first, base.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := r.db.ExecContext(ctx, r.query(`UPDATE layercache_operation_reports_v1 SET charged_bytes=? WHERE project_id=?`), operationMaximumDocuments, r.project); err != nil {
			t.Fatal(err)
		}
		a.WorkspaceID = "independent-job"
		if _, err := r.RecordOperation(ctx, a, operationTestReport("byte-extra-0007", base), base.Add(time.Minute)); !errors.Is(err, ErrOperationQuota) {
			t.Fatalf("document byte quota escaped: %v", err)
		}
		var count int
		if err := r.db.QueryRowContext(ctx, r.query(`SELECT COUNT(*) FROM layercache_operation_reports_v1 WHERE project_id=?`), r.project).Scan(&count); err != nil || count != 1 {
			t.Fatalf("partial quota admission: %d %v", count, err)
		}
	})
}

func TestOperationSQLiteRestartAndConcurrentQuota(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.sqlite")
	owner, err := OpenSQLiteRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	a := operationTestAuthority()
	base := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	r, err := owner.OperationRepository(ctx, a.Project)
	if err != nil {
		t.Fatal(err)
	}
	first := operationTestReport("restart-first-0001", base)
	firstReceipt, err := r.RecordOperation(ctx, a, first, base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	owners := make([]*SQLiteRepository, 2)
	repositories := make([]OperationRepository, 2)
	for i := range owners {
		owners[i], err = OpenSQLiteRepository(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = owners[i].Close() })
		repositories[i], err = owners[i].OperationRepository(ctx, a.Project)
		if err != nil {
			t.Fatal(err)
		}
	}
	r = repositories[0]
	if retry, err := r.RecordOperation(ctx, a, first, base.Add(2*time.Minute)); err != nil || !retry.Repeated || retry.ReceiptID != firstReceipt.ReceiptID {
		t.Fatalf("restart changed receipt: %+v %v", retry, err)
	}
	for i := 1; i < 31; i++ {
		if _, err = r.RecordOperation(ctx, a, operationTestReport(fmt.Sprintf("seed-%04d", i), base), base.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repositories[i%2].RecordOperation(ctx, a, operationTestReport(fmt.Sprintf("racing-%04d", i), base), base.Add(time.Minute))
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted, refused := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrOperationQuota) {
			refused++
		} else {
			t.Fatalf("concurrent quota error: %v", err)
		}
	}
	if accepted != 1 || refused != 11 {
		t.Fatalf("quota race admitted %d refused %d", accepted, refused)
	}
	period, err := r.OperationPeriod(ctx, base, base.Add(time.Minute))
	if err != nil || period.StoredObservations != 32 || period.HistoryTruncated {
		t.Fatalf("restart/concurrency result: %+v %v", period, err)
	}
}

func TestOperationSQLiteFailedInsertRollsBackAndDoesNotChangeNative(t *testing.T) {
	ctx := context.Background()
	r := operationSQLiteFactory(t)
	a := operationTestAuthority()
	base := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	native, err := newNativeSQLRepository(ctx, r.db, "sqlite", r.project)
	if err != nil {
		t.Fatal(err)
	}
	nr := NativeReport{Version: 1, InvocationID: "run-independent-native-0001", StartedAt: base, FinishedAt: base.Add(time.Second), Status: "success", Tasks: []NativeTask{{WorkRef: "sha256:" + strings.Repeat("a", 64), Outcome: "fromCache", StartedAt: base, FinishedAt: base.Add(time.Second)}}}
	if _, err = native.RecordReport(ctx, a, nr, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`CREATE TRIGGER operation_failure BEFORE INSERT ON layercache_operation_reports_v1 BEGIN SELECT RAISE(ABORT,'private database diagnostic'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = r.RecordOperation(ctx, a, operationTestReport("sql-failure-0001", base), base.Add(time.Minute)); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe SQL failure: %v", err)
	}
	if _, err = r.db.Exec(`DROP TRIGGER operation_failure`); err != nil {
		t.Fatal(err)
	}
	period, err := r.OperationPeriod(ctx, base, base.Add(time.Minute))
	if err != nil || period.StoredObservations != 0 {
		t.Fatalf("partial receipt retained: %+v %v", period, err)
	}
	nativePeriod, err := native.PeriodReport(ctx, base, base.Add(time.Minute))
	if err != nil || len(nativePeriod) != 1 || nativePeriod[0].ReportedTasks != 1 || nativePeriod[0].Hits != 1 {
		t.Fatalf("native counters changed: %+v %v", nativePeriod, err)
	}
}

func TestOperationSQLiteProjectReportAdmissionBounded(t *testing.T) {
	ctx := context.Background()
	r := operationSQLiteFactory(t)
	a := operationTestAuthority()
	base := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	report := operationTestReport("project-count-0001", base)
	document, _ := json.Marshal(report)
	// Seed a valid retained-history boundary in one transaction. Each job has
	// one operation, so this independently exercises the project count cap.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `INSERT INTO layercache_operation_reports_v1(project_id,receipt_id,run_id,workspace_id,integration,operation_id,authority_json,report_sha256,document_json,started_at,received_at,charged_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := range operationMaximumReports {
		a.WorkspaceID = fmt.Sprintf("project-count-job-%04d", i)
		authority, _ := json.Marshal(a)
		canonical := struct {
			Authority NativeAuthority
			Report    OperationReport
		}{a, report}
		charged, _ := json.Marshal(canonical)
		if _, err = statement.ExecContext(ctx, a.Project, operationReceiptID(a, report.OperationID), a.RunID, a.WorkspaceID, string(a.Integration), report.OperationID, string(authority), nativeDocumentHash(canonical), string(document), base.UnixNano(), base.Add(time.Minute).UnixNano(), len(charged)); err != nil {
			t.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if retry, err := r.RecordOperation(ctx, a, report, base.Add(2*time.Minute)); err != nil || !retry.Repeated {
		t.Fatalf("project capacity blocked immutable retry: %+v %v", retry, err)
	}
	a.WorkspaceID = "additional-project-job"
	if _, err := r.RecordOperation(ctx, a, report, base.Add(2*time.Minute)); !errors.Is(err, ErrOperationQuota) {
		t.Fatalf("project report quota escaped: %v", err)
	}
	var count int
	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM layercache_operation_reports_v1 WHERE project_id=?`, a.Project).Scan(&count); err != nil || count != operationMaximumReports {
		t.Fatalf("partial project admission: %d %v", count, err)
	}
}
