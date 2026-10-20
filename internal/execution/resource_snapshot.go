package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// ResourceSnapshot preserves the resource observation available when work was
// published. The sample is client-reported, even when execution authority or
// the compute provider has signed evidence. It establishes neither CPU usage
// nor the allocation that remained available for an entire task.
type ResourceSnapshot struct {
	ObservedAt       time.Time `json:"observedAt"`
	Phase            string    `json:"phase"`
	Provider         string    `json:"provider"`
	ProviderEvidence string    `json:"providerEvidence"`
	Basis            string    `json:"basis"`
	Sample           Sample    `json:"sample"`
}

// ResourceSnapshot returns an independent copy of the most recent qualifying
// primary sample for this exact execution context. At most the two phase rows
// are inspected. Aliases have no independent observation time and never match;
// missing, future or older-than-24-hour samples remain unknown. The caller must
// persist a returned snapshot with the original publication rather than look
// up a newer sample when another execution restores that artifact.
func (r *SQLRepository) ResourceSnapshot(ctx context.Context, authority Authority, at time.Time) (*ResourceSnapshot, error) {
	if authority.Project != r.project {
		return nil, nil
	}
	if authority.Context.Validate() != nil || authority.RunID == "" || len(authority.RunID) > 256 || len(authority.WorkspaceID) > 512 || at.IsZero() {
		return nil, ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, r.query(`SELECT received_at,phase,document_json
		FROM layercache_execution_observations_v1
		WHERE project_id=? AND execution_id=? AND owner_id=? AND kind=?
		AND received_at>=? AND received_at<=?
		ORDER BY received_at DESC,phase LIMIT 2`), r.project, authority.ExecutionID, authority.OwnerID, authority.Kind, at.Add(-24*time.Hour).UnixNano(), at.UnixNano())
	if err != nil {
		return nil, errors.New("execution resource snapshot unavailable")
	}
	defer rows.Close()
	for rows.Next() {
		var received int64
		var phase, encoded string
		if err := rows.Scan(&received, &phase, &encoded); err != nil || len(encoded) > MaximumRequestBytes {
			return nil, errors.New("execution resource snapshot unavailable")
		}
		var stored storedObservation
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&stored) != nil || decoder.Decode(&struct{}{}) != io.EOF || stored.Report.Validate(stored.Authority) != nil || stored.Report.Phase != phase {
			return nil, errors.New("execution resource snapshot unavailable")
		}
		if stored.Authority != authority {
			continue
		}
		provider, evidence := stored.Report.Provider, "declared"
		if provider == "" || provider == "unknown" {
			provider, evidence = "unknown", "unknown"
		}
		if authority.RunnerEnvironment == "github-hosted" {
			provider, evidence = "github", "signed"
		}
		return &ResourceSnapshot{
			ObservedAt: time.Unix(0, received).UTC(), Phase: phase,
			Provider: provider, ProviderEvidence: evidence, Basis: "client-reported", Sample: stored.Report.Sample,
		}, nil
	}
	if rows.Err() != nil {
		return nil, errors.New("execution resource snapshot unavailable")
	}
	return nil, nil
}
