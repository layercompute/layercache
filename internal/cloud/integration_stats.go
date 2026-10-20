package cloud

import (
	"context"
	"database/sql"

	"github.com/layercache/layercache/internal/artifact"
)

func (store *Store) integrationStats(ctx context.Context, tx *sql.Tx) ([]artifact.IntegrationStats, error) {
	// Charged cloud payloads are scoped by project and ref_count > 0. Unreferenced
	// R2 objects pending physical garbage collection are not charged cache usage.
	rows, err := tx.QueryContext(ctx, `WITH per_blob AS (
		SELECT a.digest, a.size_bytes AS usage_bytes, COUNT(e.digest) AS entries,
			CASE WHEN COUNT(DISTINCT e.integration) > 1 THEN 'shared'
				WHEN MIN(e.integration) IN ('turbo','actions','gradle','nx','retained','buildkit','bazel','xcode','expo') THEN MIN(e.integration)
				ELSE 'other' END AS integration
		FROM layercache_artifacts_v1 AS a
		LEFT JOIN layercache_entries_v1 AS e ON e.project_id = a.project_id AND e.digest = a.digest
		WHERE a.project_id = $1 AND a.ref_count > 0
		GROUP BY a.digest, a.size_bytes
	)
	SELECT integration, SUM(usage_bytes), COUNT(*), SUM(entries)
	FROM per_blob GROUP BY integration ORDER BY integration`, store.config.Project)
	if err != nil {
		return nil, store.safeError("read cloud integration statistics", err)
	}
	defer rows.Close()
	result := make([]artifact.IntegrationStats, 0)
	for rows.Next() {
		var item artifact.IntegrationStats
		if err := rows.Scan(&item.Integration, &item.UsageBytes, &item.Artifacts, &item.Entries); err != nil {
			return nil, store.safeError("decode cloud integration statistics", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, store.safeError("read cloud integration statistics", err)
	}
	return result, nil
}
