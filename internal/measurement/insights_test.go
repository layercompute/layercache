package measurement_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/measurement"
)

func TestPeriodInsightsAggregateRepeatedArtifactsWithoutExposingKeys(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 20, 10, 15, 0, 0, time.UTC)
	r := measurement.NewRecorder()
	for index, profile := range []string{"private-profile-A", "private-profile-A", "private-profile-B"} {
		at := start.Add(time.Duration(index) * time.Hour)
		outcome := measurement.FinalOutcome{
			RunID: fmt.Sprintf("private-run-%d", index), WorkID: "private-task-path/compile", Integration: measurement.IntegrationTurbo,
			ArtifactID: "private-actual-cache-key", CompatibilityID: profile,
			Result: measurement.ResultHit, Source: measurement.SourceTeamCache,
			StartedAt: at, FinishedAt: at.Add(time.Second),
			ProducerDuration: duration(time.Minute), Bytes: measurement.Bytes{Downloaded: 100},
		}
		if err := r.Record(outcome); err != nil {
			t.Fatal(err)
		}
	}
	report, err := r.PeriodReport(start, start.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.TopArtifacts) != 2 || report.TopArtifactsTruncated {
		t.Fatalf("artifact groups = %#v", report.TopArtifacts)
	}
	first := report.TopArtifacts[0]
	if first.Restores != 2 || first.Hits != 2 || first.Runs != 2 || first.DownloadedBytes != 200 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first.Ref) {
		t.Fatalf("repeated artifact = %#v", first)
	}
	assertEstimate(t, first.GrossAvoidedTaskTime, 120000, "producerDuration", measurement.ConfidenceHigh, 2, 2)
	assertEstimate(t, report.Integrations[0].GrossAvoidedTaskTime, 180000, "producerDuration", measurement.ConfidenceHigh, 3, 3)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-profile", "private-run", "private-task-path", "private-actual-cache-key"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("period insight exposed native identity %q", secret)
		}
	}
}

func TestPeriodInsightsBoundCommonArtifactsAndKeepUnknownTimings(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	r := measurement.NewRecorder()
	for index := range 25 {
		if err := r.Record(measurement.FinalOutcome{
			RunID: "run", WorkID: fmt.Sprint(index), ArtifactID: fmt.Sprintf("artifact-%d", index),
			Integration: measurement.IntegrationActions, CompatibilityID: "profile", Result: measurement.ResultHit,
			Source: measurement.SourceTeamCache, StartedAt: start, FinishedAt: start.Add(time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := r.PeriodReport(start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.TopArtifacts) != 20 || !report.TopArtifactsTruncated {
		t.Fatalf("bounded artifacts = %d, truncated = %v", len(report.TopArtifacts), report.TopArtifactsTruncated)
	}
	for index, artifact := range report.TopArtifacts {
		if artifact.GrossAvoidedTaskTime.Milliseconds != nil || artifact.GrossAvoidedTaskTime.Known != 0 || artifact.GrossAvoidedTaskTime.Total != 1 {
			t.Fatalf("unknown timing = %#v", artifact)
		}
		if index > 0 && report.TopArtifacts[index-1].Ref >= artifact.Ref {
			t.Fatal("tied artifact order is not deterministic")
		}
	}
}

func TestPartialNativeGroupKeepsRatioWithoutCreditingWholeProducerWork(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	r := measurement.NewRecorder()
	if err := r.Record(measurement.FinalOutcome{
		RunID: "docker", WorkID: "solve", Integration: measurement.IntegrationBuildkit, ArtifactID: "graph", CompatibilityID: "linux-arm64",
		Result: measurement.ResultHit, Source: measurement.SourceLocalCache, StartedAt: start, FinishedAt: start.Add(40 * time.Second),
		ProducerDuration: duration(time.Minute), Timing: measurement.Timing{Restore: 40 * time.Second}, EligibleUnits: 10, HitUnits: 3,
	}); err != nil {
		t.Fatal(err)
	}
	report, err := r.PeriodReport(start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.Hits != 3 || report.Eligible != 10 || report.TopArtifacts[0].Restores != 1 || report.TopArtifacts[0].Hits != 3 {
		t.Fatalf("native groups and units = %#v", report)
	}
	for _, estimate := range []measurement.Estimate{report.GrossAvoidedTaskTime, report.Integrations[0].GrossAvoidedTaskTime, report.TopArtifacts[0].GrossAvoidedTaskTime} {
		if estimate.Milliseconds != nil || estimate.Known != 0 || estimate.Total != 1 {
			t.Fatalf("partial native hit credited complete producer work: %#v", estimate)
		}
	}
	assertEstimate(t, report.NetEstimatedBuildTimeSaved, 20000, "criticalPath", measurement.ConfidenceMedium, 1, 1)
}

func TestActivityBucketsMatchHalfOpenRunSelectionAndUTCBoundaries(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 20, 10, 15, 0, 0, time.FixedZone("offset", 2*60*60))
	r := measurement.NewRecorder()
	for index, at := range []time.Time{start.Add(-time.Second), start, start.Add(45 * time.Minute), start.Add(24 * time.Hour)} {
		if err := r.Record(measurement.FinalOutcome{
			RunID: fmt.Sprint(index), WorkID: "task", Integration: measurement.IntegrationTurbo,
			Result: measurement.ResultHit, Source: measurement.SourceTeamCache, StartedAt: at, FinishedAt: at.Add(time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := r.PeriodReport(start, start.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Activity) != 25 || report.Activity[0].Runs != 1 || report.Activity[1].Runs != 1 || !report.Activity[0].From.Equal(start) || !report.Activity[len(report.Activity)-1].To.Equal(start.Add(24*time.Hour)) {
		t.Fatalf("activity boundaries = %#v", report.Activity)
	}
	runs, hits, eligible := 0, 0, 0
	for index, bucket := range report.Activity {
		runs += bucket.Runs
		hits += bucket.Hits
		eligible += bucket.Eligible
		if index > 0 && !report.Activity[index-1].To.Equal(bucket.From) {
			t.Fatal("activity has a gap or overlap")
		}
		if bucket.From.Location() != time.UTC || bucket.To.Location() != time.UTC {
			t.Fatal("bucket endpoints are not UTC")
		}
	}
	if runs != report.Runs || hits != report.Hits || eligible != report.Eligible {
		t.Fatalf("activity totals = %d/%d/%d, report = %d/%d/%d", runs, hits, eligible, report.Runs, report.Hits, report.Eligible)
	}
}
