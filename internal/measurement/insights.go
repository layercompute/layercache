package measurement

import (
	"container/heap"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// ArtifactReport identifies commonly reused bytes without exposing native keys,
// task paths, commands or compatibility strings. It is scoped to the report's
// authorized project. Restores count hit groups; Hits retain native unit counts,
// which can be BuildKit vertices rather than complete build restores.
type ArtifactReport struct {
	Ref                  string      `json:"ref"`
	Integration          Integration `json:"integration"`
	Hits                 int         `json:"hits"`
	Restores             int         `json:"restores"`
	Runs                 int         `json:"runs"`
	DownloadedBytes      int64       `json:"downloadedBytes"`
	LastHitAt            time.Time   `json:"lastHitAt"`
	GrossAvoidedTaskTime Estimate    `json:"grossAvoidedTaskTime"`
}

// ActivityBucket counts finalized outcomes by their run's first start, matching
// PeriodReport selection. Empty buckets mean no reported eligible outcomes,
// rather than proof that unmetered native clients performed no work.
type ActivityBucket struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Runs     int       `json:"runs"`
	Eligible int       `json:"eligible"`
	Hits     int       `json:"hits"`
	Misses   int       `json:"misses"`
}

func integrationAvoidedTime(outcomes []OutcomeReport, integration Integration) Estimate {
	var sum int64
	known, total := 0, 0
	for _, outcome := range outcomes {
		if outcome.Integration != integration || outcome.Result != ResultHit {
			continue
		}
		total++
		if outcome.ProducerDurationMS != nil && outcome.HitUnits == outcome.EligibleUnits {
			sum += *outcome.ProducerDurationMS
			known++
		}
	}
	return completeEstimate(sum, "producerDuration", known, total, ConfidenceHigh)
}

func aggregateIntegrationAvoidedTime(reports []RunReport, integration Integration) Estimate {
	var sum int64
	known, total := 0, 0
	for _, report := range reports {
		for _, entry := range report.Integrations {
			if entry.Integration != integration {
				continue
			}
			estimate := entry.GrossAvoidedTaskTime
			known += estimate.Known
			total += estimate.Total
			if estimate.Milliseconds != nil {
				sum += *estimate.Milliseconds
			}
		}
	}
	return completeEstimate(sum, "producerDuration", known, total, ConfidenceHigh)
}

func commonArtifacts(reports []RunReport) ([]ArtifactReport, bool) {
	type artifact struct {
		ArtifactReport
		runs  map[string]bool
		known int
		ms    int64
	}
	entries := map[string]*artifact{}
	for _, report := range reports {
		for _, outcome := range report.Outcomes {
			if outcome.Result != ResultHit || outcome.ArtifactID == "" || outcome.HitUnits == 0 {
				continue
			}
			// JSON encoding preserves boundaries even if an opaque identity contains
			// control characters. No native identity is included in the response.
			identity, _ := json.Marshal([]string{"dashboard-artifact-v1", string(outcome.Integration), outcome.CompatibilityID, outcome.ArtifactID})
			ref := fmt.Sprintf("%x", sha256.Sum256(identity))
			entry := entries[ref]
			if entry == nil {
				entry = &artifact{ArtifactReport: ArtifactReport{Ref: ref, Integration: outcome.Integration}, runs: map[string]bool{}}
				entries[ref] = entry
			}
			entry.Hits += outcome.HitUnits
			entry.Restores++
			entry.DownloadedBytes += outcome.Bytes.Downloaded
			entry.runs[report.RunID] = true
			if outcome.FinishedAt.After(entry.LastHitAt) {
				entry.LastHitAt = outcome.FinishedAt
			}
			if outcome.ProducerDurationMS != nil && outcome.HitUnits == outcome.EligibleUnits {
				entry.ms += *outcome.ProducerDurationMS
				entry.known++
			}
		}
	}
	const maximumArtifacts = 20
	// Exact frequencies still require the aggregation map above. Keep only the
	// best twenty candidates during selection instead of allocating and sorting
	// a second slice containing every unique artifact.
	leaders := make(artifactLeaders, 0, min(maximumArtifacts, len(entries)))
	for _, entry := range entries {
		entry.Runs = len(entry.runs)
		entry.GrossAvoidedTaskTime = completeEstimate(entry.ms, "producerDuration", entry.known, entry.Restores, ConfidenceHigh)
		if len(leaders) < maximumArtifacts {
			heap.Push(&leaders, entry.ArtifactReport)
		} else if artifactRanksBefore(entry.ArtifactReport, leaders[0]) {
			leaders[0] = entry.ArtifactReport
			heap.Fix(&leaders, 0)
		}
	}
	result := []ArtifactReport(leaders)
	sort.Slice(result, func(i, j int) bool { return artifactRanksBefore(result[i], result[j]) })
	return result, len(entries) > maximumArtifacts
}

func artifactRanksBefore(left, right ArtifactReport) bool {
	if left.Restores != right.Restores {
		return left.Restores > right.Restores
	}
	if left.Hits != right.Hits {
		return left.Hits > right.Hits
	}
	return left.Ref < right.Ref
}

// The root is the worst retained candidate, so replacing it costs log(20).
type artifactLeaders []ArtifactReport

func (items artifactLeaders) Len() int           { return len(items) }
func (items artifactLeaders) Less(i, j int) bool { return artifactRanksBefore(items[j], items[i]) }
func (items artifactLeaders) Swap(i, j int)      { items[i], items[j] = items[j], items[i] }
func (items *artifactLeaders) Push(item any)     { *items = append(*items, item.(ArtifactReport)) }
func (items *artifactLeaders) Pop() any {
	last := len(*items) - 1
	item := (*items)[last]
	*items = (*items)[:last]
	return item
}

func activityBuckets(from, to time.Time, reports []RunReport) []ActivityBucket {
	result := make([]ActivityBucket, 0)
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return result
	}
	from, to = from.UTC(), to.UTC()
	if from.Equal(to) && len(reports) > 0 {
		// Backtest can include a single zero-duration run. Preserve its actual
		// interval rather than inventing elapsed time to create a bucket.
		result = append(result, ActivityBucket{From: from, To: to})
	}
	span := to.Sub(from)
	width := time.Hour
	if span > 24*time.Hour {
		width = 24 * time.Hour
	}
	if span > 31*width {
		width = span/24 + 1
	}
	for start := from; start.Before(to) && len(result) < 32; {
		end := start.Truncate(width).Add(width)
		if !end.After(start) || end.After(to) || len(result) == 31 {
			end = to
		}
		result = append(result, ActivityBucket{From: start, To: end})
		start = end
	}
	for _, report := range reports {
		for index := range result {
			bucket := &result[index]
			// These reports were selected by the caller. Ordinary period queries
			// already exclude starts at their upper bound. A backtest's final
			// zero-duration run may start exactly at its recorded latest finish.
			atSelectedEnd := index == len(result)-1 && report.StartedAt.Equal(to)
			if !report.StartedAt.Before(bucket.From) && (report.StartedAt.Before(bucket.To) || atSelectedEnd) {
				bucket.Runs++
				bucket.Eligible += report.Eligible
				bucket.Hits += report.Hits
				bucket.Misses += report.Misses
				break
			}
		}
	}
	return result
}
