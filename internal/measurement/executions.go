package measurement

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/layercache/layercache/internal/execution"
)

// ExecutionRepository reuses this project's measurement connection. Its failure
// is optional inventory loss, never a reason to reject cache authentication.
func (r *SQLiteRepository) ExecutionRepository(ctx context.Context, project string) (execution.Repository, error) {
	return execution.NewSQLRepository(ctx, r.database, "sqlite", project, func(ctx context.Context, a []execution.Authority) (map[string]bool, error) {
		return executionActivity(ctx, r.database, "sqlite", project, a)
	})
}
func (r *PostgresRepository) ExecutionRepository(ctx context.Context, project string) (execution.Repository, error) {
	if project != r.project {
		return nil, execution.ErrInvalid
	}
	return execution.NewSQLRepository(ctx, r.database, "postgres", project, func(ctx context.Context, a []execution.Authority) (map[string]bool, error) {
		return executionActivity(ctx, r.database, "postgres", project, a)
	})
}

func executionActivity(ctx context.Context, db *sql.DB, dialect, project string, authorities []execution.Authority) (map[string]bool, error) {
	result := map[string]bool{}
	for len(authorities) > 0 {
		count := min(len(authorities), 128)
		known, err := executionActivityBatch(ctx, db, dialect, project, authorities[:count])
		if err != nil {
			return nil, err
		}
		for id, value := range known {
			result[id] = value
		}
		authorities = authorities[count:]
	}
	return result, nil
}

func executionActivityBatch(ctx context.Context, db *sql.DB, dialect, project string, authorities []execution.Authority) (map[string]bool, error) {
	result := map[string]bool{}
	if len(authorities) == 0 {
		return result, nil
	}
	// One batched query for finalized outcomes and one for provisional Turbo
	// transport. A workflow-level run ID is insufficient to attribute another job.
	finalTable, turboTable := "measurement_final_outcomes_v1", "measurement_turbo_observations_v1"
	if dialect == "postgres" {
		finalTable, turboTable = "layercache_measurement_final_outcomes_v1", "layercache_measurement_turbo_observations_v1"
	}
	bind := func(query string) string {
		if dialect != "postgres" {
			return query
		}
		var b strings.Builder
		n := 0
		for _, c := range query {
			if c == '?' {
				n++
				fmt.Fprintf(&b, "$%d", n)
			} else {
				b.WriteRune(c)
			}
		}
		return b.String()
	}
	var pairs []string
	var args []any
	byPair := map[string][]string{}
	byRun := map[string][]string{}
	for _, a := range authorities {
		if a.Project != project {
			continue
		}
		if a.WorkspaceID != "" {
			key := a.RunID + "\x00" + a.WorkspaceID
			if _, exists := byPair[key]; !exists {
				pairs = append(pairs, "(run_id=? AND workspace_id=?)")
				args = append(args, a.RunID, a.WorkspaceID)
			}
			byPair[key] = append(byPair[key], a.ExecutionID)
		}
		ciParts := strings.Split(a.ExecutionID, ":")
		checkScoped := a.Kind == "githubActions" && len(ciParts) == 5 && strings.HasSuffix(a.RunID, ":check:"+ciParts[4])
		if a.Kind == "client" || checkScoped {
			byRun[a.RunID] = append(byRun[a.RunID], a.ExecutionID)
		}
	}
	if len(pairs) > 0 {
		query := "SELECT DISTINCT run_id,workspace_id FROM " + finalTable + " WHERE (" + strings.Join(pairs, " OR ") + ")"
		if dialect == "postgres" {
			query += " AND project_id=?"
			args = append(args, project)
		}
		rows, err := db.QueryContext(ctx, bind(query), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var run, workspace string
			if err = rows.Scan(&run, &workspace); err != nil {
				rows.Close()
				return nil, err
			}
			for _, id := range byPair[run+"\x00"+workspace] {
				result[id] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(byRun) > 0 {
		var marks []string
		args = nil
		for run := range byRun {
			marks = append(marks, "?")
			args = append(args, run)
		}
		query := "SELECT DISTINCT run_id FROM " + turboTable + " WHERE run_id IN (" + strings.Join(marks, ",") + ")"
		if dialect == "postgres" {
			query += " AND project_id=?"
			args = append(args, project)
		}
		rows, err := db.QueryContext(ctx, bind(query), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var run string
			if err = rows.Scan(&run); err != nil {
				rows.Close()
				return nil, err
			}
			for _, id := range byRun[run] {
				result[id] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
