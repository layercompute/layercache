package measurement

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func operationTestAuthority() NativeAuthority {
	return NativeAuthority{Project: "example/project", RunID: "run-operation-qa", WorkspaceID: "job-operation-qa", Integration: IntegrationGradle, CompatibilityID: "example-gradle-profile-v1", Subject: "member-example", Repository: "example/project", SourceCommit: strings.Repeat("a", 40), Kind: "memberLocal"}
}

func operationTestReport(id string, start time.Time) OperationReport {
	return OperationReport{Version: 1, OperationID: "run-operation-" + id, SessionID: "run-session-example-0001", WorkloadDigest: "sha256:" + strings.Repeat("a", 64), CoverageDigest: "sha256:" + strings.Repeat("b", 64), EnvironmentDigest: "sha256:" + strings.Repeat("c", 64), OtherCachesDigest: "sha256:" + strings.Repeat("d", 64), OutputState: "fresh", CachePolicy: "enabled", CachePreparation: "miss", StartedAt: start, FinishedAt: start.Add(time.Second), ElapsedMS: 1000, Status: "success", PhaseTimingMS: OperationPhaseTiming{Setup: operationInt(100), Restore: operationInt(0), Work: operationInt(800), Close: operationInt(100), Publish: operationInt(0)}}
}

func operationTestClone(report OperationReport) OperationReport {
	encoded, _ := json.Marshal(report)
	var result OperationReport
	_ = json.Unmarshal(encoded, &result)
	return result
}

func TestOperationDecoderStrictBoundsAndTiming(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	r := operationTestReport("decode-0001", base)
	encoded, _ := json.Marshal(r)
	got, err := DecodeOperationReport(bytes.NewReader(encoded))
	if err != nil || ValidateOperationReport(got, base.Add(time.Second)) != nil {
		t.Fatalf("valid observation: %v", err)
	}
	for name, body := range map[string][]byte{
		"oversized":         bytes.Repeat([]byte(" "), OperationMaximumBytes+1),
		"unknown authority": bytes.Replace(encoded, []byte(`"version":1`), []byte(`"version":1,"project":"foreign"`), 1),
		"duplicate value":   bytes.Replace(encoded, []byte(`"elapsedMs":1000`), []byte(`"elapsedMs":1,"elapsedMs":1000`), 1),
		"case alias":        bytes.Replace(encoded, []byte(`"version":1`), []byte(`"version":1,"VERSION":1`), 1),
		"duplicate phase":   bytes.Replace(encoded, []byte(`"setup":100`), []byte(`"setup":0,"setup":100`), 1),
		"missing elapsed":   bytes.Replace(encoded, []byte(`"elapsedMs":1000,`), nil, 1),
		"missing phase":     bytes.Replace(encoded, []byte(`"restore":0,`), nil, 1),
		"null phases":       bytes.Replace(encoded, []byte(`"phaseTimingMs":{"setup":100,"restore":0,"work":800,"close":100,"publish":0}`), []byte(`"phaseTimingMs":null`), 1),
		"extra phase":       bytes.Replace(encoded, []byte(`"setup":100`), []byte(`"setup":100,"command":"private"`), 1),
		"trailing":          append(append([]byte{}, encoded...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeOperationReport(bytes.NewReader(body)); !errors.Is(err, ErrOperationInvalid) {
				t.Fatalf("unsafe JSON accepted: %v", err)
			}
		})
	}
	for name, change := range map[string]func(*OperationReport){
		"unsafe operation":           func(r *OperationReport) { r.OperationID = "/private/path" },
		"unsafe session":             func(r *OperationReport) { r.SessionID = "host-name" },
		"raw workload":               func(r *OperationReport) { r.WorkloadDigest = "./compile.sh" },
		"unknown state":              func(r *OperationReport) { r.OutputState = "clean-ish" },
		"unknown status":             func(r *OperationReport) { r.Status = "passed" },
		"negative elapsed":           func(r *OperationReport) { r.ElapsedMS = -1 },
		"negative phase":             func(r *OperationReport) { r.PhaseTimingMS.Work = operationInt(-1) },
		"phase sum exceeds interval": func(r *OperationReport) { r.PhaseTimingMS.Work = operationInt(801) },
		"wall monotonic mismatch":    func(r *OperationReport) { r.FinishedAt = r.FinishedAt.Add(6 * time.Second) },
		"nonUTC timestamp":           func(r *OperationReport) { r.StartedAt = r.StartedAt.In(time.FixedZone("offset", 3600)) },
		"old window":                 func(r *OperationReport) { r.StartedAt = r.StartedAt.Add(-25 * time.Hour) },
		"future": func(r *OperationReport) {
			r.StartedAt = r.StartedAt.Add(time.Minute)
			r.FinishedAt = r.FinishedAt.Add(time.Minute)
		},
		"invalid baseline": func(r *OperationReport) { r.BaselineReceiptID = "latest" },
	} {
		t.Run(name, func(t *testing.T) {
			report := operationTestClone(r)
			change(&report)
			if !errors.Is(ValidateOperationReport(report, r.FinishedAt), ErrOperationInvalid) {
				t.Fatal("invalid observation accepted")
			}
		})
	}
	// Measured zero and missing are distinct; an unsuccessful operation still has
	// useful phase observations and is admitted rather than silently discarded.
	r.PhaseTimingMS.Restore = nil
	r.Status = "failed"
	encoded, _ = json.Marshal(r)
	got, err = DecodeOperationReport(bytes.NewReader(encoded))
	if err != nil || got.PhaseTimingMS.Restore != nil || got.PhaseTimingMS.Publish == nil || *got.PhaseTimingMS.Publish != 0 {
		t.Fatal("unknown and zero collapsed")
	}
}

func TestOperationComparisonExplicitMatchingAndNegativeDelta(t *testing.T) {
	a := operationTestAuthority()
	base := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	b := operationTestReport("baseline-0001", base)
	b.CachePolicy, b.CachePreparation = "bypass", "bypassed"
	c := operationTestReport("candidate-0001", base.Add(2*time.Second))
	c.CachePreparation = "hit"
	c.BaselineReceiptID = operationReceiptID(a, b.OperationID)
	c.ElapsedMS = 1300
	c.FinishedAt = c.StartedAt.Add(1300 * time.Millisecond)
	c.PhaseTimingMS = OperationPhaseTiming{Setup: operationInt(100), Restore: operationInt(200), Work: operationInt(900), Close: operationInt(100), Publish: operationInt(0)}
	comparison := CompareOperations(a, b, a, c)
	if !comparison.Matched || comparison.Method != "bypassToEnabled" || *comparison.ObservedDifferenceMS != -300 || *comparison.ObservedWorkDifferenceMS != -100 || *comparison.ObservedOverheadDifferenceMS != -200 || len(comparison.Reasons) != 0 {
		t.Fatalf("negative observation lost: %+v", comparison)
	}
	b.CachePolicy, b.CachePreparation = "enabled", "miss"
	c.CachePreparation = "mixed"
	comparison = CompareOperations(a, b, a, c)
	if !comparison.Matched || comparison.Method != "coldToWarm" {
		t.Fatalf("cold/warm pair refused: %+v", comparison)
	}
	// Bypass/enabled must retain unhelpful or failed preparation, not cherry-pick
	// only successful cache hits into an inflated comparison cohort.
	b.CachePolicy, b.CachePreparation = "bypass", "bypassed"
	c.CachePreparation = "unavailable"
	if !CompareOperations(a, b, a, c).Matched {
		t.Fatal("bypass comparison cherry-picked cache hits")
	}
	c.PhaseTimingMS.Setup = nil
	comparison = CompareOperations(a, b, a, c)
	if comparison.ObservedDifferenceMS == nil || comparison.ObservedWorkDifferenceMS == nil || comparison.ObservedOverheadDifferenceMS != nil {
		t.Fatal("partial overhead became complete")
	}
	c.PhaseTimingMS.Setup = operationInt(90)
	if CompareOperations(a, b, a, c).ObservedOverheadDifferenceMS != nil {
		t.Fatal("unmeasured residual became overhead")
	}
	c.PhaseTimingMS.Setup = operationInt(95)
	if CompareOperations(a, b, a, c).ObservedOverheadDifferenceMS == nil {
		t.Fatal("bounded floor-rounding residual refused")
	}
}

func TestOperationComparisonMismatchControls(t *testing.T) {
	a := operationTestAuthority()
	b := operationTestReport("baseline-0002", time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC))
	c := operationTestReport("candidate-0002", b.FinishedAt.Add(time.Second))
	c.CachePreparation = "hit"
	c.BaselineReceiptID = operationReceiptID(a, b.OperationID)
	for _, test := range []struct {
		name   string
		change func(*NativeAuthority, *OperationReport)
		reason string
	}{
		{"explicit selection", func(_ *NativeAuthority, r *OperationReport) { r.BaselineReceiptID = strings.Repeat("0", 64) }, "baselineReceiptMismatch"},
		{"project", func(a *NativeAuthority, _ *OperationReport) { a.Project = "foreign/project" }, "projectMismatch"},
		{"integration", func(a *NativeAuthority, _ *OperationReport) { a.Integration = IntegrationXcode }, "integrationMismatch"},
		{"profile", func(a *NativeAuthority, _ *OperationReport) { a.CompatibilityID = "other-profile" }, "profileMismatch"},
		{"missing source", func(a *NativeAuthority, _ *OperationReport) { a.SourceCommit = "" }, "sourceMissing"},
		{"mutable source", func(a *NativeAuthority, _ *OperationReport) { a.SourceCommit = "main" }, "sourceMissing"},
		{"changed source", func(a *NativeAuthority, _ *OperationReport) { a.SourceCommit = strings.Repeat("b", 40) }, "sourceMismatch"},
		{"failed operation", func(_ *NativeAuthority, r *OperationReport) { r.Status = "failed" }, "unsuccessfulOperation"},
		{"missing session", func(_ *NativeAuthority, r *OperationReport) { r.SessionID = "" }, "sessionMissing"},
		{"session", func(_ *NativeAuthority, r *OperationReport) { r.SessionID = "run-different-session-0001" }, "sessionMismatch"},
		{"workload", func(_ *NativeAuthority, r *OperationReport) { r.WorkloadDigest = "sha256:" + strings.Repeat("0", 64) }, "workloadMismatch"},
		{"coverage", func(_ *NativeAuthority, r *OperationReport) { r.CoverageDigest = "sha256:" + strings.Repeat("0", 64) }, "coverageMismatch"},
		{"missing environment", func(_ *NativeAuthority, r *OperationReport) { r.EnvironmentDigest = "" }, "environmentMissing"},
		{"environment", func(_ *NativeAuthority, r *OperationReport) {
			r.EnvironmentDigest = "sha256:" + strings.Repeat("0", 64)
		}, "environmentMismatch"},
		{"missing other caches", func(_ *NativeAuthority, r *OperationReport) { r.OtherCachesDigest = "" }, "otherCachesMissing"},
		{"other caches", func(_ *NativeAuthority, r *OperationReport) {
			r.OtherCachesDigest = "sha256:" + strings.Repeat("0", 64)
		}, "otherCachesMismatch"},
		{"incremental", func(_ *NativeAuthority, r *OperationReport) { r.OutputState = "incremental" }, "outputNotFresh"},
		{"overlap", func(_ *NativeAuthority, r *OperationReport) { r.StartedAt = b.StartedAt }, "operationOrderMismatch"},
		{"old comparison", func(_ *NativeAuthority, r *OperationReport) {
			r.StartedAt = b.FinishedAt.Add(24*time.Hour + time.Second)
		}, "comparisonWindowExceeded"},
		{"cold candidate", func(_ *NativeAuthority, r *OperationReport) { r.CachePreparation = "miss" }, "cachePolicyMismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ca, report := a, operationTestClone(c)
			test.change(&ca, &report)
			got := CompareOperations(a, b, ca, report)
			if got.Matched || got.Method != "unmatched" || !reflect.DeepEqual(got.Reasons, []string{test.reason}) || got.ObservedDifferenceMS != nil || got.ObservedWorkDifferenceMS != nil || got.ObservedOverheadDifferenceMS != nil {
				t.Fatalf("unsafe comparison: %+v", got)
			}
		})
	}
}
