package measurement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	operationMaximumReports         = 4096
	operationMaximumDocuments       = 8 << 20
	operationSchemaLock       int64 = 0x4c436f7065726174
)

type operationSQLRepository struct {
	db      *sql.DB
	dialect string
	project string
}

type operationStoredRow struct {
	id        string
	authority NativeAuthority
	report    OperationReport
}

// OperationRepository shares the connection but has independent bounded tables,
// admission and retention. It cannot alter native cache or task measurements.
func (r *SQLiteRepository) OperationRepository(ctx context.Context, project string) (OperationRepository, error) {
	return newOperationSQLRepository(ctx, r.database, "sqlite", project)
}

func (r *PostgresRepository) OperationRepository(ctx context.Context, project string) (OperationRepository, error) {
	if project != r.project {
		return nil, ErrOperationInvalid
	}
	return newOperationSQLRepository(ctx, r.database, "postgres", project)
}

func newOperationSQLRepository(ctx context.Context, db *sql.DB, dialect, project string) (OperationRepository, error) {
	if db == nil || validateBoundedText("operation project", project, 256, false) != nil {
		return nil, ErrOperationInvalid
	}
	r := &operationSQLRepository{db: db, dialect: dialect, project: project}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, operationSQLError()
	}
	defer tx.Rollback()
	if dialect == "postgres" {
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", operationSchemaLock); err != nil {
			return nil, operationSQLError()
		}
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS layercache_operation_locks_v1 (project_id TEXT PRIMARY KEY, revision BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS layercache_operation_reports_v1 (
		project_id TEXT NOT NULL, receipt_id TEXT NOT NULL,
		run_id TEXT NOT NULL, workspace_id TEXT NOT NULL, integration TEXT NOT NULL CHECK(integration IN ('gradle','xcode')),
		operation_id TEXT NOT NULL, authority_json TEXT NOT NULL, report_sha256 TEXT NOT NULL, document_json TEXT NOT NULL,
		started_at BIGINT NOT NULL, received_at BIGINT NOT NULL, charged_bytes BIGINT NOT NULL CHECK(charged_bytes>0),
		PRIMARY KEY(project_id,receipt_id), UNIQUE(project_id,run_id,workspace_id,integration,operation_id))`,
		`CREATE INDEX IF NOT EXISTS layercache_operation_reports_period_v1 ON layercache_operation_reports_v1(project_id,started_at,receipt_id)`,
		`CREATE INDEX IF NOT EXISTS layercache_operation_reports_expiry_v1 ON layercache_operation_reports_v1(project_id,received_at,receipt_id)`,
		`CREATE INDEX IF NOT EXISTS layercache_operation_reports_job_v1 ON layercache_operation_reports_v1(project_id,run_id,workspace_id,integration)`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return nil, operationSQLError()
		}
	}
	if tx.Commit() != nil {
		return nil, operationSQLError()
	}
	return r, nil
}

func operationSQLError() error { return errors.New("operation measurement persistence unavailable") }

func validateOperationAuthority(authority NativeAuthority) error {
	if validateNativeAuthority(authority) != nil || authority.SourceCommit != "" && !operationSourcePattern.MatchString(authority.SourceCommit) {
		return ErrOperationInvalid
	}
	return nil
}

func (r *operationSQLRepository) query(statement string) string {
	if r.dialect != "postgres" {
		return statement
	}
	var result strings.Builder
	count := 0
	for _, character := range statement {
		if character == '?' {
			count++
			fmt.Fprintf(&result, "$%d", count)
		} else {
			result.WriteRune(character)
		}
	}
	return result.String()
}

func (r *operationSQLRepository) begin(ctx context.Context) (*sql.Tx, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, operationSQLError()
	}
	if r.dialect == "postgres" {
		_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,$2))`, r.project, operationSchemaLock)
	} else {
		// Reserve the SQLite writer before reads so concurrent admissions cannot
		// upgrade stale snapshots or bypass a project/job limit.
		_, err = tx.ExecContext(ctx, `INSERT INTO layercache_operation_locks_v1(project_id,revision) VALUES(?,0) ON CONFLICT(project_id) DO UPDATE SET revision=layercache_operation_locks_v1.revision+1`, r.project)
	}
	if err != nil {
		tx.Rollback()
		return nil, operationSQLError()
	}
	return tx, nil
}

func (r *operationSQLRepository) RecordOperation(ctx context.Context, authority NativeAuthority, report OperationReport, now time.Time) (OperationReceipt, error) {
	if authority.Project != r.project || validateOperationAuthority(authority) != nil || ValidateOperationReportShape(report) != nil || now.IsZero() {
		return OperationReceipt{}, ErrOperationInvalid
	}
	document, err := json.Marshal(report)
	if err != nil || len(document) > OperationMaximumBytes {
		return OperationReceipt{}, ErrOperationInvalid
	}
	authorityDocument, err := json.Marshal(authority)
	if err != nil || len(authorityDocument) > 16<<10 {
		return OperationReceipt{}, ErrOperationInvalid
	}
	canonical := struct {
		Authority NativeAuthority
		Report    OperationReport
	}{authority, report}
	charged, err := json.Marshal(canonical)
	if err != nil {
		return OperationReceipt{}, ErrOperationInvalid
	}
	id, sha := operationReceiptID(authority, report.OperationID), nativeDocumentHash(canonical)
	receipt := OperationReceipt{Version: OperationVersion, Recorded: true, OperationID: report.OperationID, ReceiptID: id}
	tx, err := r.begin(ctx)
	if err != nil {
		return OperationReceipt{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, r.query(`DELETE FROM layercache_operation_reports_v1 WHERE project_id=? AND received_at<?`), r.project, now.Add(-OperationRetentionDays*24*time.Hour).UnixNano()); err != nil {
		return OperationReceipt{}, operationSQLError()
	}
	var previous string
	err = tx.QueryRowContext(ctx, r.query(`SELECT report_sha256 FROM layercache_operation_reports_v1 WHERE project_id=? AND receipt_id=?`), r.project, id).Scan(&previous)
	if err == nil {
		if previous != sha {
			return OperationReceipt{}, ErrOperationConflict
		}
		receipt.Repeated = true
		// Commit expiry too; retries never extend the retained report's lifetime.
		if tx.Commit() != nil {
			return OperationReceipt{}, operationSQLError()
		}
		return receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return OperationReceipt{}, operationSQLError()
	}
	// An accepted receipt may outlive the 24-hour new-admission window. Exact
	// retained retries reconcile above without renewing received_at; changed
	// authority or content conflicts while retained. Missing/expired identities
	// must still satisfy the original admission window and cannot be resurrected
	// by replaying an old sealed report.
	if ValidateOperationReport(report, now) != nil {
		return OperationReceipt{}, ErrOperationInvalid
	}
	var count, bytes, job int64
	if err = tx.QueryRowContext(ctx, r.query(`SELECT COUNT(*),COALESCE(SUM(charged_bytes),0) FROM layercache_operation_reports_v1 WHERE project_id=?`), r.project).Scan(&count, &bytes); err != nil {
		return OperationReceipt{}, operationSQLError()
	}
	if err = tx.QueryRowContext(ctx, r.query(`SELECT COUNT(*) FROM layercache_operation_reports_v1 WHERE project_id=? AND run_id=? AND workspace_id=? AND integration=?`), r.project, authority.RunID, authority.WorkspaceID, string(authority.Integration)).Scan(&job); err != nil {
		return OperationReceipt{}, operationSQLError()
	}
	if count >= operationMaximumReports || bytes < 0 || int64(len(charged)) > operationMaximumDocuments-bytes || job >= OperationMaximumPerJob {
		return OperationReceipt{}, ErrOperationQuota
	}
	if _, err = tx.ExecContext(ctx, r.query(`INSERT INTO layercache_operation_reports_v1(project_id,receipt_id,run_id,workspace_id,integration,operation_id,authority_json,report_sha256,document_json,started_at,received_at,charged_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`), r.project, id, authority.RunID, authority.WorkspaceID, string(authority.Integration), report.OperationID, string(authorityDocument), sha, string(document), report.StartedAt.UnixNano(), now.UnixNano(), len(charged)); err != nil {
		return OperationReceipt{}, operationSQLError()
	}
	if tx.Commit() != nil {
		return OperationReceipt{}, operationSQLError()
	}
	return receipt, nil
}

func (row operationStoredRow) observation() OperationObservation {
	return OperationObservation{ReceiptID: row.id, Integration: row.authority.Integration, SourceCommit: row.authority.SourceCommit, Profile: row.authority.CompatibilityID, Report: row.report}
}

func (r *operationSQLRepository) OperationPeriod(ctx context.Context, from, to time.Time) (OperationPeriodReport, error) {
	result := OperationPeriodReport{Observations: []OperationObservation{}, Comparisons: []OperationComparison{}}
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return result, ErrOperationInvalid
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelSerializable})
	if err != nil {
		return result, operationSQLError()
	}
	defer tx.Rollback()
	cutoff := time.Now().UTC().Add(-OperationRetentionDays * 24 * time.Hour).UnixNano()
	if err = tx.QueryRowContext(ctx, r.query(`SELECT COUNT(*) FROM layercache_operation_reports_v1 WHERE project_id=? AND received_at>=? AND started_at>=? AND started_at<?`), r.project, cutoff, from.UnixNano(), to.UnixNano()).Scan(&result.StoredObservations); err != nil || result.StoredObservations < 0 || result.StoredObservations > operationMaximumReports {
		return OperationPeriodReport{}, operationSQLError()
	}
	rows, err := tx.QueryContext(ctx, r.query(`SELECT receipt_id,authority_json,document_json FROM layercache_operation_reports_v1 WHERE project_id=? AND received_at>=? AND started_at>=? AND started_at<? ORDER BY started_at DESC,receipt_id DESC LIMIT ?`), r.project, cutoff, from.UnixNano(), to.UnixNano(), OperationMaximumObservations)
	if err != nil {
		return OperationPeriodReport{}, operationSQLError()
	}
	selected := []operationStoredRow{}
	for rows.Next() {
		row, err := scanOperationRow(rows)
		if err != nil || len(selected) >= OperationMaximumObservations || row.authority.Project != r.project {
			rows.Close()
			return OperationPeriodReport{}, operationSQLError()
		}
		selected = append(selected, row)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return OperationPeriodReport{}, operationSQLError()
	}
	if rows.Close() != nil {
		return OperationPeriodReport{}, operationSQLError()
	}
	result.HistoryTruncated = result.StoredObservations > len(selected)
	for _, row := range selected {
		result.Observations = append(result.Observations, row.observation())
		if row.report.BaselineReceiptID == "" {
			continue
		}
		baseline, err := scanOperationRow(tx.QueryRowContext(ctx, r.query(`SELECT receipt_id,authority_json,document_json FROM layercache_operation_reports_v1 WHERE project_id=? AND receipt_id=? AND received_at>=?`), r.project, row.report.BaselineReceiptID, cutoff))
		if errors.Is(err, sql.ErrNoRows) {
			comparison := operationComparison(row.authority, row.report)
			comparison.Reasons = []string{"missingBaseline"}
			result.Comparisons = append(result.Comparisons, comparison)
			continue
		}
		if err != nil || baseline.authority.Project != r.project {
			return OperationPeriodReport{}, operationSQLError()
		}
		result.Comparisons = append(result.Comparisons, CompareOperations(baseline.authority, baseline.report, row.authority, row.report))
	}
	if tx.Commit() != nil {
		return OperationPeriodReport{}, operationSQLError()
	}
	return result, nil
}

type operationScanner interface{ Scan(...any) error }

func scanOperationRow(scanner operationScanner) (operationStoredRow, error) {
	var row operationStoredRow
	var authority, document string
	if err := scanner.Scan(&row.id, &authority, &document); err != nil {
		return row, err
	}
	if !operationReceiptPattern.MatchString(row.id) || len(authority) > 16<<10 || len(document) > OperationMaximumBytes || json.Unmarshal([]byte(authority), &row.authority) != nil || validateOperationAuthority(row.authority) != nil {
		return row, operationSQLError()
	}
	var err error
	row.report, err = DecodeOperationReport(strings.NewReader(document))
	if err != nil || operationReceiptID(row.authority, row.report.OperationID) != row.id {
		return row, operationSQLError()
	}
	return row, nil
}
