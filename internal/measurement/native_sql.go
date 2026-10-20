package measurement

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/execution"
)

const (
	nativeMaximumReports     = 4096
	nativeMaximumStoredTasks = 65536
	nativeMaximumDocuments   = 32 << 20
	nativeMaximumTransport   = 65536
	nativeMatchingBudget     = 1 << 20
	// This lock is separate from artifact admission and generic measurements.
	nativeSchemaLock int64 = 0x4c436e6174697665
)

type nativeSQLRepository struct {
	db      *sql.DB
	dialect string
	project string
}

type nativeStoredTask struct {
	NativeTask
	ArtifactID string `json:"artifactId,omitempty"`
}

type nativeStoredReport struct {
	Report NativeReport       `json:"report"`
	Tasks  []nativeStoredTask `json:"tasks"`
}

type nativeReportRow struct {
	id        string
	authority NativeAuthority
	stored    nativeStoredReport
}

type nativeTaskRow struct {
	id        string
	reportID  string
	authority NativeAuthority
	task      nativeStoredTask
}

type nativeTransportRow struct {
	id string
	NativeTransport
}

type nativeLossRange struct {
	integration Integration
	kind        string
	first, last time.Time
}

// NativeRepository shares this repository's connection. It never closes it or
// changes the generic task graph, artifact identity or cache retention policy.
func (r *SQLiteRepository) NativeRepository(ctx context.Context, project string) (NativeRepository, error) {
	return newNativeSQLRepository(ctx, r.database, "sqlite", project)
}

func (r *PostgresRepository) NativeRepository(ctx context.Context, project string) (NativeRepository, error) {
	if project != r.project {
		return nil, ErrNativeInvalid
	}
	return newNativeSQLRepository(ctx, r.database, "postgres", project)
}

func newNativeSQLRepository(ctx context.Context, db *sql.DB, dialect, project string) (NativeRepository, error) {
	if db == nil || validateBoundedText("native project", project, 256, false) != nil {
		return nil, ErrNativeInvalid
	}
	r := &nativeSQLRepository{db: db, dialect: dialect, project: project}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nativeSQLError()
	}
	defer tx.Rollback()
	if dialect == "postgres" {
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", nativeSchemaLock); err != nil {
			return nil, nativeSQLError()
		}
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS layercache_native_locks_v1 (project_id TEXT PRIMARY KEY, revision BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS layercache_native_loss_days_v1 (
			project_id TEXT NOT NULL,
			integration TEXT NOT NULL CHECK(integration IN ('gradle','xcode')),
			loss_kind TEXT NOT NULL CHECK(loss_kind IN ('report','transport')),
			day_start BIGINT NOT NULL, first_lost_at BIGINT NOT NULL, last_lost_at BIGINT NOT NULL,
			PRIMARY KEY(project_id,integration,loss_kind,day_start), CHECK(first_lost_at<=last_lost_at))`,
		`CREATE TABLE IF NOT EXISTS layercache_native_reports_v1 (
			project_id TEXT NOT NULL, receipt_id TEXT NOT NULL,
			run_id TEXT NOT NULL, workspace_id TEXT NOT NULL, integration TEXT NOT NULL,
			invocation_id TEXT NOT NULL, authority_json TEXT NOT NULL,
			report_sha256 TEXT NOT NULL, document_json TEXT NOT NULL,
			started_at BIGINT NOT NULL, received_at BIGINT NOT NULL,
			charged_bytes BIGINT NOT NULL, task_count BIGINT NOT NULL,
			PRIMARY KEY(project_id,receipt_id),
			UNIQUE(project_id,run_id,workspace_id,integration,invocation_id))`,
		`CREATE INDEX IF NOT EXISTS layercache_native_reports_period_v1 ON layercache_native_reports_v1(project_id,started_at)`,
		`CREATE INDEX IF NOT EXISTS layercache_native_reports_expiry_v1 ON layercache_native_reports_v1(project_id,received_at,receipt_id)`,
		`CREATE INDEX IF NOT EXISTS layercache_native_reports_job_v1 ON layercache_native_reports_v1(project_id,run_id,workspace_id,integration)`,
		`CREATE TABLE IF NOT EXISTS layercache_native_tasks_v1 (
			project_id TEXT NOT NULL, receipt_id TEXT NOT NULL, work_ref TEXT NOT NULL,
			artifact_id TEXT NOT NULL, outcome TEXT NOT NULL,
			started_at BIGINT NOT NULL, finished_at BIGINT NOT NULL, execution_ms BIGINT,
			PRIMARY KEY(project_id,receipt_id,work_ref),
			FOREIGN KEY(project_id,receipt_id) REFERENCES layercache_native_reports_v1(project_id,receipt_id) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS layercache_native_tasks_artifact_v1 ON layercache_native_tasks_v1(project_id,artifact_id)`,
		`CREATE TABLE IF NOT EXISTS layercache_native_transport_v1 (
			project_id TEXT NOT NULL, transport_id TEXT NOT NULL,
			received_at BIGINT NOT NULL, document_json TEXT NOT NULL,
			PRIMARY KEY(project_id,transport_id))`,
		`CREATE INDEX IF NOT EXISTS layercache_native_transport_age_v1 ON layercache_native_transport_v1(project_id,received_at)`,
		`CREATE TABLE IF NOT EXISTS layercache_native_transport_usage_v1 (
			project_id TEXT PRIMARY KEY,
			row_count BIGINT NOT NULL CHECK(row_count>=0),
			document_bytes BIGINT NOT NULL CHECK(document_bytes>=0))`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return nil, nativeSQLError()
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, nativeSQLError()
	}
	if err = r.initializeTransportUsage(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *nativeSQLRepository) documentByteLength() string {
	if r.dialect == "postgres" {
		return "octet_length(document_json)"
	}
	return "length(CAST(document_json AS BLOB))"
}

// Existing ledgers are counted only when this project has no usage row. Every
// later admission reads that row instead of rescanning retained JSON payloads.
func (r *nativeSQLRepository) initializeTransportUsage(ctx context.Context) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count, bytes int64
	err = tx.QueryRowContext(ctx, r.query(`SELECT row_count,document_bytes FROM layercache_native_transport_usage_v1 WHERE project_id=?`), r.project).Scan(&count, &bytes)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.QueryRowContext(ctx, r.query(`SELECT COUNT(*),COALESCE(SUM(`+r.documentByteLength()+`),0) FROM layercache_native_transport_v1 WHERE project_id=?`), r.project).Scan(&count, &bytes); err != nil {
			return nativeSQLError()
		}
		if _, err = tx.ExecContext(ctx, r.query(`INSERT INTO layercache_native_transport_usage_v1(project_id,row_count,document_bytes) VALUES(?,?,?)`), r.project, count, bytes); err != nil {
			return nativeSQLError()
		}
	} else if err != nil || count < 0 || bytes < 0 {
		return nativeSQLError()
	}
	if err = tx.Commit(); err != nil {
		return nativeSQLError()
	}
	return nil
}

func nativeSQLError() error { return errors.New("native measurement persistence unavailable") }

func (r *nativeSQLRepository) query(statement string) string {
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

func (r *nativeSQLRepository) begin(ctx context.Context) (*sql.Tx, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nativeSQLError()
	}
	if r.dialect == "postgres" {
		_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`, r.project, nativeSchemaLock)
	} else {
		// Acquire SQLite's write reservation before any read. A deferred reader
		// upgraded after another writer commits can fail with SQLITE_BUSY_SNAPSHOT.
		_, err = tx.ExecContext(ctx, `INSERT INTO layercache_native_locks_v1(project_id,revision) VALUES(?,0) ON CONFLICT(project_id) DO UPDATE SET revision=layercache_native_locks_v1.revision+1`, r.project)
	}
	if err != nil {
		tx.Rollback()
		return nil, nativeSQLError()
	}
	return tx, nil
}

func validateNativeAuthority(a NativeAuthority) error {
	if !nativeChoice(a.Kind, "githubActions", "memberLocal", "local") || a.Integration != IntegrationGradle && a.Integration != IntegrationXcode {
		return ErrNativeInvalid
	}
	for _, item := range []struct {
		value string
		limit int
		empty bool
	}{
		{a.Project, 256, false}, {a.RunID, 512, false}, {a.WorkspaceID, 512, false},
		{a.CompatibilityID, 512, false}, {a.Subject, 512, false},
		{a.Repository, 256, true}, {a.SourceCommit, 64, true},
	} {
		if validateBoundedText("native authority", item.value, item.limit, item.empty) != nil {
			return ErrNativeInvalid
		}
	}
	if a.Execution != nil && a.Execution.Validate() != nil {
		return ErrNativeInvalid
	}
	return nil
}

func nativeDocumentHash(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func nativeAuthorityHash(a NativeAuthority) string { return nativeDocumentHash(a) }

func nativeReceiptID(a NativeAuthority, invocation string) string {
	return nativeDocumentHash([]string{a.Project, a.RunID, a.WorkspaceID, string(a.Integration), invocation})
}

func (r *nativeSQLRepository) expire(ctx context.Context, tx *sql.Tx, now time.Time) error {
	cutoff := now.Add(-NativeRetentionDays * 24 * time.Hour).UnixNano()
	// Explicitly delete child rows too: sharing a connection must not depend on
	// whichever SQLite foreign_keys pragma another module configured.
	for _, item := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM layercache_native_tasks_v1 WHERE project_id=? AND receipt_id IN (SELECT receipt_id FROM layercache_native_reports_v1 WHERE project_id=? AND received_at<?)`, []any{r.project, r.project, cutoff}},
		{`DELETE FROM layercache_native_reports_v1 WHERE project_id=? AND received_at<?`, []any{r.project, cutoff}},
		{`DELETE FROM layercache_native_loss_days_v1 WHERE project_id=? AND day_start<?`, []any{r.project, nativeCoverageDay(now).AddDate(0, 0, -NativeRetentionDays).UnixNano()}},
	} {
		if _, err := tx.ExecContext(ctx, r.query(item.query), item.args...); err != nil {
			return nativeSQLError()
		}
	}
	// The age index limits this aggregation to expired rows. A normal cache
	// request with nothing expired never reads current transport documents.
	var expired, bytes int64
	if err := tx.QueryRowContext(ctx, r.query(`SELECT COUNT(*),COALESCE(SUM(`+r.documentByteLength()+`),0) FROM layercache_native_transport_v1 WHERE project_id=? AND received_at<?`), r.project, cutoff).Scan(&expired, &bytes); err != nil {
		return nativeSQLError()
	}
	if expired > 0 {
		result, err := tx.ExecContext(ctx, r.query(`UPDATE layercache_native_transport_usage_v1 SET row_count=row_count-?,document_bytes=document_bytes-? WHERE project_id=? AND row_count>=? AND document_bytes>=?`), expired, bytes, r.project, expired, bytes)
		if err != nil {
			return nativeSQLError()
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return nativeSQLError()
		}
		result, err = tx.ExecContext(ctx, r.query(`DELETE FROM layercache_native_transport_v1 WHERE project_id=? AND received_at<?`), r.project, cutoff)
		if err != nil {
			return nativeSQLError()
		}
		deleted, err := result.RowsAffected()
		if err != nil || deleted != expired {
			return nativeSQLError()
		}
	}
	return nil
}

func nativeCoverageDay(at time.Time) time.Time { return at.UTC().Truncate(24 * time.Hour) }

// Loss is scoped to the workload time and integration, not the report's upload
// time. Each UTC day retains a min/max range; gaps inside that range remain
// conservatively incomplete. At most 31 days per fixed integration and loss
// kind are kept: 124 rows per project. The kinds have different cohort rules.
// Exact replays, conflicts and invalid payloads never create loss evidence.
func (r *nativeSQLRepository) refuseQuota(ctx context.Context, tx *sql.Tx, integration Integration, kind string, workloadAt time.Time) error {
	at := workloadAt.UnixNano()
	if _, err := tx.ExecContext(ctx, r.query(`INSERT INTO layercache_native_loss_days_v1(project_id,integration,loss_kind,day_start,first_lost_at,last_lost_at) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,integration,loss_kind,day_start) DO UPDATE SET first_lost_at=CASE WHEN excluded.first_lost_at<layercache_native_loss_days_v1.first_lost_at THEN excluded.first_lost_at ELSE layercache_native_loss_days_v1.first_lost_at END,last_lost_at=CASE WHEN excluded.last_lost_at>layercache_native_loss_days_v1.last_lost_at THEN excluded.last_lost_at ELSE layercache_native_loss_days_v1.last_lost_at END`), r.project, string(integration), kind, nativeCoverageDay(workloadAt).UnixNano(), at, at); err != nil {
		return nativeSQLError()
	}
	if _, err := tx.ExecContext(ctx, r.query(`DELETE FROM layercache_native_loss_days_v1 WHERE project_id=? AND integration=? AND loss_kind=? AND day_start NOT IN (SELECT day_start FROM layercache_native_loss_days_v1 WHERE project_id=? AND integration=? AND loss_kind=? ORDER BY day_start DESC LIMIT ?)`), r.project, string(integration), kind, r.project, string(integration), kind, NativeRetentionDays+1); err != nil {
		return nativeSQLError()
	}
	if err := tx.Commit(); err != nil {
		return nativeSQLError()
	}
	return ErrNativeQuota
}

func (r *nativeSQLRepository) RecordTransport(ctx context.Context, evidence NativeTransport) error {
	if validateNativeAuthority(evidence.Authority) != nil || evidence.Authority.Project != r.project || !nativeChoice(evidence.Kind, "delivered", "missed", "published") || !nativeWorkPattern.MatchString(evidence.ArtifactID) || evidence.StartedAt.IsZero() || evidence.FinishedAt.Before(evidence.StartedAt) || evidence.FinishedAt.Sub(evidence.StartedAt) > 24*time.Hour || evidence.Bytes < 0 || evidence.Bytes > 1<<53-1 {
		return ErrNativeInvalid
	}
	if evidence.Kind == "missed" {
		if evidence.Source != SourceNone || evidence.Digest != "" || evidence.Bytes != 0 {
			return ErrNativeInvalid
		}
	} else if len(evidence.Digest) != 64 || !nativeWorkPattern.MatchString("sha256:"+evidence.Digest) || evidence.Kind == "published" && evidence.Source != SourceNone || evidence.Kind == "delivered" && !nativeChoice(string(evidence.Source), string(SourceLocalCache), string(SourceTeamCache), string(SourcePublicCache)) {
		return ErrNativeInvalid
	}
	// Resource capture is optional. Invalid or future samples cannot replace
	// original producer provenance or enter persistence as arbitrary strings.
	if !nativePublicationResourceValid(evidence) {
		evidence.Resource = nil
	}
	document, err := json.Marshal(evidence)
	if err != nil || len(document) > 16<<10 {
		return ErrNativeInvalid
	}
	id := nativeDocumentHash(evidence)
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if err = r.expire(ctx, tx, now); err != nil {
		return err
	}
	var exists int
	err = tx.QueryRowContext(ctx, r.query(`SELECT 1 FROM layercache_native_transport_v1 WHERE project_id=? AND transport_id=?`), r.project, id).Scan(&exists)
	if err == nil {
		return nil // Exact delivery retries never multiply observations.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nativeSQLError()
	}
	var count, documentBytes int64
	if err = tx.QueryRowContext(ctx, r.query(`SELECT row_count,document_bytes FROM layercache_native_transport_usage_v1 WHERE project_id=?`), r.project).Scan(&count, &documentBytes); err != nil || count < 0 || documentBytes < 0 {
		return nativeSQLError()
	}
	if count >= nativeMaximumTransport || int64(len(document)) > nativeMaximumDocuments-documentBytes {
		return r.refuseQuota(ctx, tx, evidence.Authority.Integration, "transport", evidence.FinishedAt)
	}
	if _, err = tx.ExecContext(ctx, r.query(`INSERT INTO layercache_native_transport_v1(project_id,transport_id,received_at,document_json) VALUES(?,?,?,?)`), r.project, id, now.UnixNano(), string(document)); err != nil {
		return nativeSQLError()
	}
	result, err := tx.ExecContext(ctx, r.query(`UPDATE layercache_native_transport_usage_v1 SET row_count=row_count+1,document_bytes=document_bytes+? WHERE project_id=?`), len(document), r.project)
	if err != nil {
		return nativeSQLError()
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return nativeSQLError()
	}
	if err = tx.Commit(); err != nil {
		return nativeSQLError()
	}
	return nil
}

func nativePublicationResourceValid(evidence NativeTransport) bool {
	resource := evidence.Resource
	authority := evidence.Authority
	if resource == nil || evidence.Kind != "published" || authority.Execution == nil || resource.Basis != "client-reported" || resource.ObservedAt.IsZero() || resource.ObservedAt.After(evidence.FinishedAt) || resource.ObservedAt.Before(evidence.StartedAt.Add(-24*time.Hour)) || !nativeChoice(resource.ProviderEvidence, "unknown", "declared", "signed") {
		return false
	}
	context := execution.Authority{Context: *authority.Execution, Project: authority.Project, RunID: authority.RunID, WorkspaceID: authority.WorkspaceID}
	observation := execution.ObservationRequest{Version: execution.Version, Phase: resource.Phase, Provider: resource.Provider, Sample: resource.Sample}
	if context.Kind == "client" {
		// Only sample validation needs an installation-shaped value; identities
		// remain the exact signed context and no invented installation is stored.
		observation.InstallationID = strings.Repeat("0", 64)
	}
	return observation.Validate(context) == nil
}

func (r *nativeSQLRepository) RecordReport(ctx context.Context, authority NativeAuthority, report NativeReport, now time.Time) (NativeReceipt, error) {
	if authority.Project != r.project || validateNativeAuthority(authority) != nil || ValidateNativeReport(report, now) != nil || authority.Integration == IntegrationXcode && len(report.Tasks) != 0 {
		return NativeReceipt{}, ErrNativeInvalid
	}
	canonical, err := json.Marshal(struct {
		Authority NativeAuthority
		Report    NativeReport
	}{authority, report})
	if err != nil || len(canonical) > NativeMaximumBytes+16<<10 {
		return NativeReceipt{}, ErrNativeInvalid
	}
	// Native keys are used only to derive opaque coordinates. Neither the
	// receipt document nor the individual rows retain a raw native cache key.
	stored := nativeStoredReport{Report: report, Tasks: make([]nativeStoredTask, 0, len(report.Tasks))}
	stored.Report.Tasks = nil
	for _, task := range report.Tasks {
		item := nativeStoredTask{NativeTask: task}
		if task.NativeKey != "" {
			item.ArtifactID = NativeArtifactIdentity(authority.Project, authority.CompatibilityID, authority.Integration, task.NativeKey)
		}
		item.NativeKey = ""
		stored.Tasks = append(stored.Tasks, item)
	}
	document, err := json.Marshal(stored)
	if err != nil {
		return NativeReceipt{}, ErrNativeInvalid
	}
	authorityDocument, _ := json.Marshal(authority)
	id := nativeReceiptID(authority, report.InvocationID)
	sha := nativeDocumentHash(struct {
		Authority NativeAuthority
		Report    NativeReport
	}{authority, report})
	receipt := NativeReceipt{Version: NativeVersion, Recorded: true, InvocationID: report.InvocationID, Tasks: len(report.Tasks)}
	tx, err := r.begin(ctx)
	if err != nil {
		return NativeReceipt{}, err
	}
	defer tx.Rollback()
	if err = r.expire(ctx, tx, now); err != nil {
		return NativeReceipt{}, err
	}
	var previous string
	err = tx.QueryRowContext(ctx, r.query(`SELECT report_sha256 FROM layercache_native_reports_v1 WHERE project_id=? AND receipt_id=?`), r.project, id).Scan(&previous)
	if err == nil {
		if previous != sha {
			return NativeReceipt{}, ErrNativeConflict
		}
		receipt.Repeated = true
		return receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return NativeReceipt{}, nativeSQLError()
	}
	var reports, tasks, documents, invocations int64
	if err = tx.QueryRowContext(ctx, r.query(`SELECT COUNT(*),COALESCE(SUM(task_count),0),COALESCE(SUM(charged_bytes),0) FROM layercache_native_reports_v1 WHERE project_id=?`), r.project).Scan(&reports, &tasks, &documents); err != nil {
		return NativeReceipt{}, nativeSQLError()
	}
	if err = tx.QueryRowContext(ctx, r.query(`SELECT COUNT(*) FROM layercache_native_reports_v1 WHERE project_id=? AND run_id=? AND workspace_id=? AND integration=?`), r.project, authority.RunID, authority.WorkspaceID, string(authority.Integration)).Scan(&invocations); err != nil {
		return NativeReceipt{}, nativeSQLError()
	}
	if reports >= nativeMaximumReports || tasks+int64(len(report.Tasks)) > nativeMaximumStoredTasks || int64(len(canonical)) > nativeMaximumDocuments-documents || invocations >= NativeMaximumInvocations {
		return NativeReceipt{}, r.refuseQuota(ctx, tx, authority.Integration, "report", report.StartedAt)
	}
	if _, err = tx.ExecContext(ctx, r.query(`INSERT INTO layercache_native_reports_v1(project_id,receipt_id,run_id,workspace_id,integration,invocation_id,authority_json,report_sha256,document_json,started_at,received_at,charged_bytes,task_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`), r.project, id, authority.RunID, authority.WorkspaceID, string(authority.Integration), report.InvocationID, string(authorityDocument), sha, string(document), report.StartedAt.UnixNano(), now.UnixNano(), len(canonical), len(stored.Tasks)); err != nil {
		return NativeReceipt{}, nativeSQLError()
	}
	for _, task := range stored.Tasks {
		if _, err = tx.ExecContext(ctx, r.query(`INSERT INTO layercache_native_tasks_v1(project_id,receipt_id,work_ref,artifact_id,outcome,started_at,finished_at,execution_ms) VALUES(?,?,?,?,?,?,?,?)`), r.project, id, task.WorkRef, task.ArtifactID, task.Outcome, task.StartedAt.UnixNano(), task.FinishedAt.UnixNano(), task.ExecutionMS); err != nil {
			return NativeReceipt{}, nativeSQLError()
		}
	}
	if err = tx.Commit(); err != nil {
		return NativeReceipt{}, nativeSQLError()
	}
	return receipt, nil
}

func (r *nativeSQLRepository) PeriodReport(ctx context.Context, from, to time.Time) ([]NativeIntegrationReport, error) {
	if from.IsZero() || !to.After(from) {
		return nil, ErrNativeInvalid
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, nativeSQLError()
	}
	defer tx.Rollback()
	reports, err := r.readReports(ctx, tx)
	if err != nil {
		return nil, err
	}
	transports, err := r.readTransports(ctx, tx)
	if err != nil {
		return nil, err
	}
	const maximumLossRows = 4 * (NativeRetentionDays + 1)
	losses := make([]nativeLossRange, 0)
	rows, err := tx.QueryContext(ctx, r.query(`SELECT integration,loss_kind,first_lost_at,last_lost_at FROM layercache_native_loss_days_v1 WHERE project_id=? AND day_start>=? LIMIT ?`), r.project, nativeCoverageDay(time.Now().UTC()).AddDate(0, 0, -NativeRetentionDays).UnixNano(), maximumLossRows+1)
	if err != nil {
		return nil, nativeSQLError()
	}
	for rows.Next() {
		var item nativeLossRange
		var first, last int64
		if err = rows.Scan(&item.integration, &item.kind, &first, &last); err != nil || len(losses) >= maximumLossRows {
			rows.Close()
			return nil, nativeSQLError()
		}
		item.first, item.last = time.Unix(0, first).UTC(), time.Unix(0, last).UTC()
		losses = append(losses, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, nativeSQLError()
	}
	if err = rows.Close(); err != nil {
		return nil, nativeSQLError()
	}
	if err = tx.Commit(); err != nil {
		return nil, nativeSQLError()
	}
	result := projectNativeReports(reports, transports, from, to)
	incomplete := nativePeriodLosses(reports, losses, from, to)
	for index := range result {
		if incomplete[result[index].Integration] {
			result[index].HistoryTruncated = true
		}
	}
	return result, nil
}

func nativePeriodLosses(reports []nativeReportRow, losses []nativeLossRange, from, to time.Time) map[Integration]bool {
	result := map[Integration]bool{}
	for _, loss := range losses {
		if loss.kind == "report" && loss.first.Before(to) && !loss.last.Before(from) {
			result[loss.integration] = true
		}
	}
	for _, report := range reports {
		start, finish := report.stored.Report.StartedAt, report.stored.Report.FinishedAt
		if start.Before(from) || !start.Before(to) {
			continue
		}
		for _, task := range report.stored.Tasks {
			if task.StartedAt.Before(start) {
				start = task.StartedAt
			}
			if task.FinishedAt.After(finish) {
				finish = task.FinishedAt
			}
		}
		start, finish = start.Add(-5*time.Second), finish.Add(5*time.Second)
		for _, loss := range losses {
			// Selection uses invocation start, but attribution uses the complete
			// selected invocation's native window. Do not clip it at query.to:
			// later transport loss can invalidate an already reported invocation.
			if loss.integration == report.authority.Integration && loss.kind == "transport" && !loss.first.After(finish) && !loss.last.Before(start) {
				result[loss.integration] = true
			}
		}
	}
	return result
}

func (r *nativeSQLRepository) readReports(ctx context.Context, tx *sql.Tx) ([]nativeReportRow, error) {
	cutoff := time.Now().UTC().Add(-NativeRetentionDays * 24 * time.Hour).UnixNano()
	rows, err := tx.QueryContext(ctx, r.query(`SELECT receipt_id,authority_json,document_json FROM layercache_native_reports_v1 WHERE project_id=? AND received_at>=? ORDER BY started_at,receipt_id LIMIT ?`), r.project, cutoff, nativeMaximumReports+1)
	if err != nil {
		return nil, nativeSQLError()
	}
	defer rows.Close()
	result := make([]nativeReportRow, 0)
	bytes := 0
	for rows.Next() {
		var item nativeReportRow
		var authority, document string
		if err = rows.Scan(&item.id, &authority, &document); err != nil {
			return nil, nativeSQLError()
		}
		bytes += len(authority) + len(document)
		if len(result) >= nativeMaximumReports || bytes > nativeMaximumDocuments+nativeMaximumReports*(16<<10) || json.Unmarshal([]byte(authority), &item.authority) != nil || json.Unmarshal([]byte(document), &item.stored) != nil {
			return nil, nativeSQLError()
		}
		result = append(result, item)
	}
	if rows.Err() != nil {
		return nil, nativeSQLError()
	}
	return result, nil
}

func (r *nativeSQLRepository) readTransports(ctx context.Context, tx *sql.Tx) ([]nativeTransportRow, error) {
	cutoff := time.Now().UTC().Add(-NativeRetentionDays * 24 * time.Hour).UnixNano()
	rows, err := tx.QueryContext(ctx, r.query(`SELECT transport_id,document_json FROM layercache_native_transport_v1 WHERE project_id=? AND received_at>=? ORDER BY received_at,transport_id LIMIT ?`), r.project, cutoff, nativeMaximumTransport+1)
	if err != nil {
		return nil, nativeSQLError()
	}
	defer rows.Close()
	result := make([]nativeTransportRow, 0)
	documentBytes := 0
	for rows.Next() {
		var item nativeTransportRow
		var document string
		if err = rows.Scan(&item.id, &document); err != nil || len(result) >= nativeMaximumTransport || len(document) > 16<<10 || json.Unmarshal([]byte(document), &item.NativeTransport) != nil {
			return nil, nativeSQLError()
		}
		documentBytes += len(document)
		if documentBytes > nativeMaximumDocuments {
			return nil, nativeSQLError()
		}
		result = append(result, item)
	}
	if rows.Err() != nil {
		return nil, nativeSQLError()
	}
	return result, nil
}

func nativeTaskCoordinate(receipt, work string) string { return receipt + ":" + work }

// Match exact authenticated authority and opaque key coordinates. A delivery
// must fit exactly one task window and that task must fit exactly one delivery
// of that kind. Dense/ambiguous evidence is conservatively unattributed.
func matchNativeTransports(tasks []nativeTaskRow, transports []nativeTransportRow) map[string]map[string]nativeTransportRow {
	taskBuckets := map[string][]nativeTaskRow{}
	transportBuckets := map[string][]nativeTransportRow{}
	for _, task := range tasks {
		if task.task.ArtifactID != "" {
			key := nativeAuthorityHash(task.authority) + ":" + task.task.ArtifactID
			taskBuckets[key] = append(taskBuckets[key], task)
		}
	}
	for _, transport := range transports {
		if transport.Kind != "missed" {
			key := nativeAuthorityHash(transport.Authority) + ":" + transport.ArtifactID
			transportBuckets[key] = append(transportBuckets[key], transport)
		}
	}
	keys := make([]string, 0, len(taskBuckets))
	for key := range taskBuckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := map[string]map[string]nativeTransportRow{}
	remaining := nativeMatchingBudget
	for _, key := range keys {
		group, evidence := taskBuckets[key], transportBuckets[key]
		if len(evidence) == 0 || len(group) > remaining/len(evidence) {
			continue
		}
		remaining -= len(group) * len(evidence)
		transportCandidates := map[string][]string{}
		taskCandidates := map[string]map[string][]nativeTransportRow{}
		for _, task := range group {
			id := task.id
			for _, transport := range evidence {
				if transport.StartedAt.Before(task.task.StartedAt.Add(-5*time.Second)) || transport.FinishedAt.After(task.task.FinishedAt.Add(5*time.Second)) {
					continue
				}
				transportCandidates[transport.id] = append(transportCandidates[transport.id], id)
				if taskCandidates[id] == nil {
					taskCandidates[id] = map[string][]nativeTransportRow{}
				}
				taskCandidates[id][transport.Kind] = append(taskCandidates[id][transport.Kind], transport)
			}
		}
		for id, kinds := range taskCandidates {
			for kind, candidates := range kinds {
				if len(candidates) == 1 && len(transportCandidates[candidates[0].id]) == 1 {
					if result[id] == nil {
						result[id] = map[string]nativeTransportRow{}
					}
					result[id][kind] = candidates[0]
				}
			}
		}
	}
	return result
}

func projectNativeReports(reports []nativeReportRow, transports []nativeTransportRow, from, to time.Time) []NativeIntegrationReport {
	allTasks := make([]nativeTaskRow, 0)
	for _, report := range reports {
		for _, task := range report.stored.Tasks {
			allTasks = append(allTasks, nativeTaskRow{id: nativeTaskCoordinate(report.id, task.WorkRef), reportID: report.id, authority: report.authority, task: task})
		}
	}
	matches := matchNativeTransports(allTasks, transports)
	firstPublication := map[string]nativeTransportRow{}
	for _, transport := range transports {
		if transport.Kind != "published" {
			continue
		}
		key := transport.ArtifactID + ":" + transport.Digest
		previous, exists := firstPublication[key]
		if !exists || transport.FinishedAt.Before(previous.FinishedAt) || transport.FinishedAt.Equal(previous.FinishedAt) && transport.id < previous.id {
			firstPublication[key] = transport
		}
	}
	producerTiming := map[string]int64{}
	producerResources := map[string]bool{}
	for _, task := range allTasks {
		if task.task.Outcome != "executed" {
			continue
		}
		publication, matched := matches[task.id]["published"]
		if !matched {
			continue
		}
		key := publication.ArtifactID + ":" + publication.Digest
		if firstPublication[key].id == publication.id {
			if task.task.ExecutionMS != nil {
				producerTiming[publication.id] = *task.task.ExecutionMS
			}
			producerResources[publication.id] = nativePublicationResourceValid(publication.NativeTransport)
		}
	}
	byIntegration := map[Integration]*NativeIntegrationReport{}
	selected := map[Integration][]NativeReport{}
	milliseconds := map[Integration]int64{}
	netMilliseconds := map[Integration]int64{}
	sourceHits := map[Integration]map[Source]int{}
	counterOnly := map[Integration]bool{}
	for _, row := range reports {
		started := row.stored.Report.StartedAt
		if started.Before(from) || !started.Before(to) {
			continue
		}
		integration := row.authority.Integration
		projection := byIntegration[integration]
		if projection == nil {
			projection = &NativeIntegrationReport{Integration: integration, Sources: make([]NativeSourceHits, 0),
				NetEstimatedBuildTimeSaved: Estimate{Method: "nativeGraphUnavailable", Confidence: ConfidenceUnknown},
				GrossAvoidedTaskTime:       Estimate{Method: "matchedNativeProducerDuration", Confidence: ConfidenceUnknown},
				NetAvoidedTaskTime:         Estimate{Method: "matchedNativeProducerMinusCachedTaskElapsed", Confidence: ConfidenceUnknown},
				HistoryTruncated:           from.Before(time.Now().UTC().Add(-NativeRetentionDays * 24 * time.Hour)),
			}
			byIntegration[integration] = projection
			sourceHits[integration] = map[Source]int{}
		}
		projection.ReportedInvocations++
		projection.ReportedTasks += len(row.stored.Tasks)
		selected[integration] = append(selected[integration], row.stored.Report)
		if len(row.stored.Tasks) == 0 {
			counterOnly[integration] = true
		}
		for _, task := range row.stored.Tasks {
			switch task.Outcome {
			case "executed":
				projection.Outcomes.Executed++
			case "fromCache":
				projection.Outcomes.FromCache++
			case "upToDate":
				projection.Outcomes.UpToDate++
			case "noSource":
				projection.Outcomes.NoSource++
			case "skipped":
				projection.Outcomes.Skipped++
			case "failed":
				projection.Outcomes.Failed++
			default:
				projection.Outcomes.Unknown++
			}
			if task.Outcome != "executed" && task.Outcome != "fromCache" {
				continue
			}
			if task.Outcome == "executed" {
				if task.CacheEligibility == "ineligible" {
					continue
				}
				if task.ArtifactID == "" {
					projection.EligibilityUnknown++
					continue
				}
				projection.KnownEligible++
				projection.Misses++
				continue
			}
			// A native FROM-CACHE verdict establishes cache eligibility even if
			// the exact key was unavailable. Attribution still requires the key.
			projection.KnownEligible++
			projection.Hits++
			projection.ProducerTiming.Total++
			projection.ProducerResources.Total++
			source := SourceUnattributed
			if delivery, found := matches[nativeTaskCoordinate(row.id, task.WorkRef)]["delivered"]; found {
				source = delivery.Source
				publication := firstPublication[delivery.ArtifactID+":"+delivery.Digest]
				if !publication.FinishedAt.After(delivery.FinishedAt) {
					if duration, exists := producerTiming[publication.id]; exists {
						projection.ProducerTiming.Known++
						milliseconds[integration] += duration
						netMilliseconds[integration] += duration - task.FinishedAt.Sub(task.StartedAt).Milliseconds()
					}
					if producerResources[publication.id] {
						projection.ProducerResources.Known++
					}
				}
			}
			sourceHits[integration][source]++
		}
	}
	result := make([]NativeIntegrationReport, 0, len(byIntegration))
	for integration, projection := range byIntegration {
		if projection.EligibilityUnknown == 0 && projection.KnownEligible > 0 && !counterOnly[integration] {
			eligible := projection.KnownEligible
			projection.Eligible = &eligible
			rate := float64(projection.Hits) / float64(eligible)
			projection.HitRate = &rate
		}
		for _, source := range []Source{SourceLocalCache, SourceTeamCache, SourcePublicCache, SourceUnattributed} {
			if hits := sourceHits[integration][source]; hits > 0 {
				projection.Sources = append(projection.Sources, NativeSourceHits{Source: source, Hits: hits})
			}
		}
		projection.GrossAvoidedTaskTime.Known = projection.ProducerTiming.Known
		projection.GrossAvoidedTaskTime.Total = projection.ProducerTiming.Total
		projection.NetAvoidedTaskTime.Known = projection.ProducerTiming.Known
		projection.NetAvoidedTaskTime.Total = projection.ProducerTiming.Total
		if projection.ProducerTiming.Known > 0 {
			value := milliseconds[integration]
			net := netMilliseconds[integration]
			projection.GrossAvoidedTaskTime.Milliseconds = &value
			projection.NetAvoidedTaskTime.Milliseconds = &net
			projection.GrossAvoidedTaskTime.Confidence = ConfidenceLow
			projection.NetAvoidedTaskTime.Confidence = ConfidenceLow
			if projection.ProducerTiming.Known == projection.ProducerTiming.Total {
				projection.GrossAvoidedTaskTime.Confidence = ConfidenceMedium
				projection.NetAvoidedTaskTime.Confidence = ConfidenceMedium
			}
		}
		projection.NetEstimatedBuildTimeSaved.Total = projection.ReportedInvocations
		projection.Counters, projection.TimingMS = aggregateNativePhases(selected[integration])
		result = append(result, *projection)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Integration < result[j].Integration })
	return result
}

func aggregateNativePhases(reports []NativeReport) (*NativeCounters, *NativePhaseTiming) {
	sum := func(field func(NativeReport) *int64) *int64 {
		var result int64
		for _, report := range reports {
			value := field(report)
			if value == nil || *value > (1<<53-1)-result {
				return nil
			}
			result += *value
		}
		return &result
	}
	hasRPC, hasCompiler, hasTiming := false, false, false
	for _, report := range reports {
		hasRPC = hasRPC || report.Counters != nil && report.Counters.RPC != nil
		hasCompiler = hasCompiler || report.Counters != nil && report.Counters.Compiler != nil
		hasTiming = hasTiming || report.TimingMS != nil
	}
	var counters *NativeCounters
	if hasRPC || hasCompiler {
		counters = &NativeCounters{}
	}
	if hasRPC {
		fields := []func(*NativeRPCCounters) *int64{
			func(v *NativeRPCCounters) *int64 { return v.ActionReads }, func(v *NativeRPCCounters) *int64 { return v.ActionHits },
			func(v *NativeRPCCounters) *int64 { return v.ActionWrites }, func(v *NativeRPCCounters) *int64 { return v.CASReads },
			func(v *NativeRPCCounters) *int64 { return v.CASHits }, func(v *NativeRPCCounters) *int64 { return v.CASWrites },
			func(v *NativeRPCCounters) *int64 { return v.CacheErrors },
		}
		values := make([]*int64, len(fields))
		for index, field := range fields {
			values[index] = sum(func(report NativeReport) *int64 {
				if report.Counters == nil || report.Counters.RPC == nil {
					return nil
				}
				return field(report.Counters.RPC)
			})
		}
		counters.RPC = &NativeRPCCounters{values[0], values[1], values[2], values[3], values[4], values[5], values[6]}
	}
	if hasCompiler {
		compiler := &NativeCompilerCounters{Scope: "unknown", Coverage: "unknown"}
		scopes, coverages, limitations := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, report := range reports {
			if report.Counters == nil || report.Counters.Compiler == nil {
				limitations["missing"] = true
				continue
			}
			value := report.Counters.Compiler
			scopes[value.Scope], coverages[value.Coverage] = true, true
			if value.Reason != "" && value.Reason != "none" {
				limitations[value.Reason] = true
			}
		}
		for scope := range scopes {
			compiler.Scope = scope
		}
		for coverage := range coverages {
			compiler.Coverage = coverage
		}
		for limitation := range limitations {
			compiler.Reason = limitation
		}
		if len(coverages) > 1 || limitations["missing"] {
			compiler.Coverage = "partial"
		}
		if len(limitations) > 1 {
			compiler.Coverage, compiler.Reason = "partial", "ambiguous"
		}
		if len(scopes) > 1 {
			compiler.Scope, compiler.Coverage, compiler.Reason = "mixed", "partial", "ambiguous"
		} else {
			fields := []func(*NativeCompilerCounters) *int64{
				func(v *NativeCompilerCounters) *int64 { return v.Hits }, func(v *NativeCompilerCounters) *int64 { return v.Misses },
				func(v *NativeCompilerCounters) *int64 { return v.Skipped }, func(v *NativeCompilerCounters) *int64 { return v.Unknown },
			}
			values := make([]*int64, len(fields))
			for index, field := range fields {
				values[index] = sum(func(report NativeReport) *int64 {
					if report.Counters == nil || report.Counters.Compiler == nil {
						return nil
					}
					return field(report.Counters.Compiler)
				})
			}
			compiler.Hits, compiler.Misses, compiler.Skipped, compiler.Unknown = values[0], values[1], values[2], values[3]
		}
		if compiler.Coverage == "complete" && (compiler.Hits == nil || compiler.Misses == nil || compiler.Unknown != nil && *compiler.Unknown != 0) {
			compiler.Coverage, compiler.Reason = "partial", "ambiguous"
		}
		counters.Compiler = compiler
	}
	var phases *NativePhaseTiming
	if hasTiming {
		fields := []func(*NativePhaseTiming) *int64{
			func(v *NativePhaseTiming) *int64 { return v.Operation }, func(v *NativePhaseTiming) *int64 { return v.Compile },
			func(v *NativePhaseTiming) *int64 { return v.Load }, func(v *NativePhaseTiming) *int64 { return v.Restore },
			func(v *NativePhaseTiming) *int64 { return v.Close }, func(v *NativePhaseTiming) *int64 { return v.Snapshot },
		}
		values := make([]*int64, len(fields))
		for index, field := range fields {
			values[index] = sum(func(report NativeReport) *int64 {
				if report.TimingMS == nil {
					return nil
				}
				return field(report.TimingMS)
			})
		}
		phases = &NativePhaseTiming{values[0], values[1], values[2], values[3], values[4], values[5]}
	}
	return counters, phases
}
