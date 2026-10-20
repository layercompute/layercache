package execution

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"
)

const MaximumInventoryJobs = 64

// ActivityLookup associates only exact authenticated executions with existing
// cache observations. A failed lookup leaves activity unconfirmed.
type ActivityLookup func(context.Context, []Authority) (map[string]bool, error)
type SQLRepository struct {
	db                   *sql.DB
	dialect              string
	project              string
	activity             ActivityLookup
	maximumExecutions    int
	maximumInstallations int
}
type storedObservation struct {
	Authority Authority          `json:"authority"`
	Report    ObservationRequest `json:"report"`
	Contexts  []Authority        `json:"contexts,omitempty"`
}

// NewSQLRepository shares the existing project measurement connection. It owns
// only its tables and never closes the database or changes artifact retention.
func NewSQLRepository(ctx context.Context, db *sql.DB, dialect, project string, activity ActivityLookup) (*SQLRepository, error) {
	if db == nil || !oneOf(dialect, "sqlite", "postgres") || project == "" || len(project) > 256 {
		return nil, ErrInvalid
	}
	r := &SQLRepository{db: db, dialect: dialect, project: project, activity: activity, maximumExecutions: MaximumExecutions, maximumInstallations: MaximumInstallations}
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.New("execution observation persistence unavailable")
	}
	defer transaction.Rollback()
	if dialect == "postgres" {
		if _, err = transaction.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x4c43657865637574)); err != nil {
			return nil, errors.New("execution observation persistence unavailable")
		}
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS layercache_execution_observations_v1 (project_id TEXT NOT NULL CHECK(length(project_id) BETWEEN 1 AND 256),execution_id TEXT NOT NULL CHECK(length(execution_id) BETWEEN 1 AND 256),phase TEXT NOT NULL CHECK(phase IN ('start','finish')),owner_id TEXT NOT NULL CHECK(length(owner_id) BETWEEN 1 AND 80),kind TEXT NOT NULL CHECK(kind IN ('client','githubActions')),installation_id TEXT NOT NULL CHECK(length(installation_id) IN (0,64)),received_at BIGINT NOT NULL,document_json TEXT NOT NULL CHECK(length(document_json)<=16384),PRIMARY KEY(project_id,execution_id,phase))`,
		`CREATE INDEX IF NOT EXISTS layercache_execution_observations_recent_v1 ON layercache_execution_observations_v1(project_id,received_at)`,
		`CREATE TABLE IF NOT EXISTS layercache_execution_coverage_v1 (project_id TEXT PRIMARY KEY,truncated_until BIGINT NOT NULL)`,
	} {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return nil, errors.New("execution observation persistence unavailable")
		}
	}
	if err = transaction.Commit(); err != nil {
		return nil, errors.New("execution observation persistence unavailable")
	}
	return r, nil
}
func (r *SQLRepository) query(value string) string {
	if r.dialect != "postgres" {
		return value
	}
	n := 0
	return valueWithPlaceholders(value, &n)
}
func valueWithPlaceholders(value string, n *int) string {
	var b strings.Builder
	for _, c := range value {
		if c == '?' {
			*n++
			fmt.Fprintf(&b, "$%d", *n)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (r *SQLRepository) Record(ctx context.Context, authority Authority, report ObservationRequest, receivedAt time.Time) error {
	if authority.Project != r.project || receivedAt.IsZero() || report.Validate(authority) != nil {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("execution observation persistence unavailable")
	}
	defer tx.Rollback()
	if r.dialect == "postgres" {
		hash := fnv.New64a()
		_, _ = hash.Write([]byte("layercache-execution-v1\x00" + r.project))
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(hash.Sum64())); err != nil {
			return errors.New("execution observation persistence unavailable")
		}
	}
	// Any phase of a job belongs to the same authenticated owner and installation.
	var owner, kind, installation string
	err = tx.QueryRowContext(ctx, r.query(`SELECT owner_id,kind,installation_id FROM layercache_execution_observations_v1 WHERE project_id=? AND execution_id=? LIMIT 1`), r.project, authority.ExecutionID).Scan(&owner, &kind, &installation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.New("execution observation persistence unavailable")
	}
	if err == nil && (owner != authority.OwnerID || kind != authority.Kind || installation != report.InstallationID) {
		return ErrConflict
	}
	contexts := []Authority{authority}
	seenContexts := map[string]bool{authority.RunID + "\x00" + authority.WorkspaceID: true}
	previous, err := tx.QueryContext(ctx, r.query(`SELECT document_json FROM layercache_execution_observations_v1 WHERE project_id=? AND execution_id=?`), r.project, authority.ExecutionID)
	if err != nil {
		return errors.New("execution observation persistence unavailable")
	}
	for previous.Next() {
		var document string
		if err = previous.Scan(&document); err != nil {
			break
		}
		var old storedObservation
		if len(document) > MaximumRequestBytes || json.Unmarshal([]byte(document), &old) != nil {
			err = ErrInvalid
			break
		}
		prior := append([]Authority{old.Authority}, old.Contexts...)
		for _, a := range prior {
			if a.Project != authority.Project || a.Context != authority.Context {
				err = ErrConflict
				break
			}
			key := a.RunID + "\x00" + a.WorkspaceID
			if !seenContexts[key] {
				seenContexts[key] = true
				contexts = append(contexts, a)
			}
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		err = previous.Err()
	}
	previous.Close()
	if err != nil {
		return errors.New("execution observation persistence unavailable")
	}
	if len(contexts) > 8 {
		return ErrInvalid
	}
	encoded, err := json.Marshal(storedObservation{Authority: authority, Report: report, Contexts: contexts})
	if err != nil || len(encoded) > MaximumRequestBytes {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, r.query(`INSERT INTO layercache_execution_observations_v1 (project_id,execution_id,phase,owner_id,kind,installation_id,received_at,document_json) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(project_id,execution_id,phase) DO UPDATE SET received_at=excluded.received_at,document_json=excluded.document_json WHERE layercache_execution_observations_v1.received_at<=excluded.received_at`), r.project, authority.ExecutionID, report.Phase, authority.OwnerID, authority.Kind, report.InstallationID, receivedAt.UnixNano(), string(encoded))
	if err != nil {
		return errors.New("execution observation persistence unavailable")
	}
	if _, err = tx.ExecContext(ctx, r.query(`DELETE FROM layercache_execution_observations_v1 WHERE project_id=? AND received_at<?`), r.project, receivedAt.Add(-RetentionDays*24*time.Hour).UnixNano()); err != nil {
		return errors.New("execution observation persistence unavailable")
	}
	// Bounds count whole executions, so pruning never leaves a fabricated extra
	// job or an orphan phase. Aliases are independently bounded for clients.
	filters := []struct {
		query string
		limit int
	}{
		{`SELECT execution_id,MAX(received_at) AS latest FROM layercache_execution_observations_v1 WHERE project_id=? GROUP BY execution_id ORDER BY latest DESC,execution_id DESC`, r.maximumExecutions},
		{`SELECT owner_id || ':' || installation_id,MAX(received_at) AS latest FROM layercache_execution_observations_v1 WHERE project_id=? AND kind='client' GROUP BY owner_id,installation_id ORDER BY latest DESC,owner_id DESC,installation_id DESC`, r.maximumInstallations},
	}
	var truncatedUntil int64
	for i, filter := range filters {
		rows, e := tx.QueryContext(ctx, r.query(filter.query), r.project)
		if e != nil {
			return errors.New("execution observation persistence unavailable")
		}
		var remove []string
		position := 0
		for rows.Next() {
			var id string
			var latest int64
			if e = rows.Scan(&id, &latest); e != nil {
				rows.Close()
				return errors.New("execution observation persistence unavailable")
			}
			if position >= filter.limit {
				remove = append(remove, id)
				if latest > truncatedUntil {
					truncatedUntil = latest
				}
			}
			position++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return errors.New("execution observation persistence unavailable")
		}
		for _, id := range remove {
			predicate := "execution_id=?"
			if i == 1 {
				predicate = "owner_id || ':' || installation_id=? AND kind='client'"
			}
			if _, e = tx.ExecContext(ctx, r.query(`DELETE FROM layercache_execution_observations_v1 WHERE project_id=? AND `+predicate), r.project, id); e != nil {
				return errors.New("execution observation persistence unavailable")
			}
		}
	}
	if truncatedUntil > 0 {
		if _, err = tx.ExecContext(ctx, r.query(`INSERT INTO layercache_execution_coverage_v1(project_id,truncated_until) VALUES (?,?) ON CONFLICT(project_id) DO UPDATE SET truncated_until=CASE WHEN excluded.truncated_until>layercache_execution_coverage_v1.truncated_until THEN excluded.truncated_until ELSE layercache_execution_coverage_v1.truncated_until END`), r.project, truncatedUntil); err != nil {
			return errors.New("execution observation persistence unavailable")
		}
	}
	if err = tx.Commit(); err != nil {
		return errors.New("execution observation persistence unavailable")
	}
	return nil
}

func (r *SQLRepository) Inventory(ctx context.Context, project string, from, to time.Time) (Inventory, error) {
	if project != r.project || from.IsZero() || to.Before(from) || to.Sub(from) > RetentionDays*24*time.Hour {
		return Inventory{}, ErrInvalid
	}
	result := Inventory{Version: Version, From: from, To: to, UpdatedAt: to, RetentionDays: RetentionDays, StaleAfterSeconds: StaleAfterSeconds, Installations: []Entry{}, Jobs: []Entry{}}
	rows, err := r.db.QueryContext(ctx, r.query(`SELECT received_at,document_json FROM layercache_execution_observations_v1 WHERE project_id=? AND received_at>=? AND received_at<=? ORDER BY received_at DESC,execution_id DESC,phase`), project, from.UnixNano(), to.UnixNano())
	if err != nil {
		return Inventory{}, errors.New("execution inventory unavailable")
	}
	type observed struct {
		entry       Entry
		authorities []Authority
	}
	executions := map[string]observed{}
	for rows.Next() {
		var received int64
		var encoded string
		if err = rows.Scan(&received, &encoded); err != nil {
			break
		}
		if len(encoded) > MaximumRequestBytes {
			err = ErrInvalid
			break
		}
		var stored storedObservation
		if json.Unmarshal([]byte(encoded), &stored) != nil || stored.Authority.Project != project || stored.Report.Validate(stored.Authority) != nil {
			err = ErrInvalid
			break
		}
		a, p := stored.Authority, stored.Report
		provider, evidence := p.Provider, "declared"
		if provider == "" || provider == "unknown" {
			provider, evidence = "unknown", "unknown"
		}
		if a.RunnerEnvironment == "github-hosted" {
			provider, evidence = "github", "signed"
		}
		id := a.ExecutionID
		if a.Kind == "client" {
			hash := sha256.Sum256([]byte("layercache-installation-v1\x00" + a.OwnerID + "\x00" + p.InstallationID))
			id = "installation:" + hex.EncodeToString(hash[:])
		}
		entry := Entry{Context: a.Context, ID: id, InstallationID: p.InstallationID, Provider: provider, ProviderEvidence: evidence, LastObservedAt: time.Unix(0, received).UTC(), Phase: p.Phase, Sample: p.Sample, CacheActivity: "unconfirmed"}
		contexts := stored.Contexts
		if len(contexts) == 0 {
			contexts = []Authority{a}
		}
		for _, candidate := range contexts {
			if candidate.Project != project || candidate.Context != a.Context || candidate.RunID == "" || len(candidate.RunID) > 256 || len(candidate.WorkspaceID) > 512 {
				err = ErrInvalid
				break
			}
		}
		if err != nil {
			break
		}
		old, exists := executions[a.ExecutionID]
		if !exists {
			executions[a.ExecutionID] = observed{entry, contexts}
		} else {
			old.authorities = append(old.authorities, contexts...)
			if old.entry.LastObservedAt.Before(entry.LastObservedAt) {
				old.entry = entry
			}
			executions[a.ExecutionID] = old
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return Inventory{}, errors.New("execution inventory unavailable")
	}
	installations := map[string]observed{}
	var jobs []observed
	for _, value := range executions {
		if value.entry.Kind == "client" {
			old, exists := installations[value.entry.ID]
			if !exists {
				installations[value.entry.ID] = value
			} else {
				old.authorities = append(old.authorities, value.authorities...)
				if old.entry.LastObservedAt.Before(value.entry.LastObservedAt) {
					old.entry = value.entry
				}
				installations[value.entry.ID] = old
			}
		} else {
			jobs = append(jobs, value)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].entry.LastObservedAt.Equal(jobs[j].entry.LastObservedAt) {
			return jobs[i].entry.ID < jobs[j].entry.ID
		}
		return jobs[i].entry.LastObservedAt.After(jobs[j].entry.LastObservedAt)
	})
	result.JobCount = len(jobs)
	if len(jobs) > MaximumInventoryJobs {
		jobs = jobs[:MaximumInventoryJobs]
		result.ListTruncated = true
	}
	var selected []observed
	for _, value := range installations {
		selected = append(selected, value)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].entry.ID < selected[j].entry.ID })
	selected = append(selected, jobs...)
	var authorities []Authority
	for _, value := range selected {
		authorities = append(authorities, value.authorities...)
	}
	activity := map[string]bool{}
	if r.activity != nil {
		if known, e := r.activity(ctx, authorities); e == nil {
			activity = known
		}
	}
	for _, value := range selected {
		for _, a := range value.authorities {
			if activity[a.ExecutionID] {
				value.entry.CacheActivity = "observed"
				break
			}
		}
		if value.entry.Kind == "client" {
			result.Installations = append(result.Installations, value.entry)
		} else {
			result.Jobs = append(result.Jobs, value.entry)
		}
	}
	var truncated int64
	err = r.db.QueryRowContext(ctx, r.query(`SELECT truncated_until FROM layercache_execution_coverage_v1 WHERE project_id=?`), r.project).Scan(&truncated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Inventory{}, errors.New("execution inventory unavailable")
	}
	result.HistoryTruncated = truncated >= from.UnixNano() && truncated > 0
	return result, nil
}
