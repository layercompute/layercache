package measurement

import (
	"fmt"
	"testing"
	"time"
)

// Synthetic seven-day reporting probes isolate the additional common-artifact
// projection. They do not measure database replay, complete HTTP latency or
// production traffic. Exact aggregation retains O(unique artifacts + observed
// artifact/run pairs) state; the final selection retains twenty candidates.
func BenchmarkCommonArtifactsSevenDays(b *testing.B) {
	for _, scenario := range []struct {
		name      string
		outcomes  int
		artifacts int
	}{
		{"10k-hits-2k-artifacts", 10000, 2000},
		{"100k-hits-25k-artifacts", 100000, 25000},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			const runs = 168
			reports := make([]RunReport, runs)
			start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
			for index := range reports {
				reports[index].RunID = fmt.Sprint(index)
			}
			for index := range scenario.outcomes {
				run := &reports[index%runs]
				run.Outcomes = append(run.Outcomes, OutcomeReport{
					ArtifactID: fmt.Sprintf("artifact-%d", index%scenario.artifacts), CompatibilityID: "profile",
					Integration: IntegrationTurbo, Result: ResultHit, Source: SourceTeamCache,
					EligibleUnits: 1, HitUnits: 1, Bytes: Bytes{Downloaded: 1024},
					FinishedAt: start.Add(time.Duration(index%runs) * time.Hour),
				})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				rows, truncated := commonArtifacts(reports)
				if len(rows) != 20 || !truncated {
					b.Fatal("synthetic report lost bounded output")
				}
			}
		})
	}
}
