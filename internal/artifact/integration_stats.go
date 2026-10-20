package artifact

import (
	"context"
	"database/sql"
	"fmt"
)

// IntegrationStats partitions charged artifact bytes by the explicit storage
// integration. Multiple integration references to one digest share one row;
// entries count every reference to those blobs. The fixed labels never infer
// artifact purpose from a native key, path or compatibility string.
type IntegrationStats struct {
	Integration string `json:"integration"`
	UsageBytes  int64  `json:"usageBytes"`
	Artifacts   int64  `json:"artifacts"`
	Entries     int64  `json:"entries"`
}

// Derive totals and bounded categories from one statement. An unknown raw
// integration remains "other"; distinct raw integrations share a blob even if
// both would otherwise be coarsened to "other". Local stores can deduplicate
// payloads across project references, so their totals describe the whole store.
func readArtifactStats(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (Stats, error) {
	rows, err := query.QueryContext(ctx, `WITH per_blob AS (
		SELECT a.digest, a.size AS usage_bytes, COUNT(e.digest) AS entries,
			CASE WHEN COUNT(DISTINCT e.integration) > 1 THEN 'shared'
				WHEN MIN(e.integration) IN ('turbo','actions','gradle','nx','retained','buildkit','bazel','xcode','expo') THEN MIN(e.integration)
				ELSE 'other' END AS integration
		FROM artifacts AS a LEFT JOIN cache_entries AS e ON e.digest = a.digest
		GROUP BY a.digest, a.size
	)
	SELECT integration, SUM(usage_bytes), COUNT(*), SUM(entries)
	FROM per_blob GROUP BY integration ORDER BY integration`)
	if err != nil {
		return Stats{}, fmt.Errorf("read artifact integration statistics: %w", err)
	}
	defer rows.Close()
	stats := Stats{StorageByIntegration: make([]IntegrationStats, 0)}
	for rows.Next() {
		var item IntegrationStats
		if err := rows.Scan(&item.Integration, &item.UsageBytes, &item.Artifacts, &item.Entries); err != nil {
			return Stats{}, fmt.Errorf("decode artifact integration statistics: %w", err)
		}
		stats.StorageByIntegration = append(stats.StorageByIntegration, item)
		stats.UsageBytes += item.UsageBytes
		stats.Artifacts += item.Artifacts
		stats.Entries += item.Entries
	}
	if err := rows.Err(); err != nil {
		return Stats{}, fmt.Errorf("read artifact integration statistics: %w", err)
	}
	return stats, nil
}
