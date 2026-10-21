package measurement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
)

const (
	OperationVersion             = 1
	OperationPath                = "/v1/reports/operations"
	OperationMaximumBytes        = 16 << 10
	OperationMaximumPerJob       = 32
	OperationRetentionDays       = 30
	OperationMaximumObservations = 32
)

var (
	ErrOperationInvalid     = errors.New("invalid bounded operation report")
	ErrOperationConflict    = errors.New("operation already has a different final report")
	ErrOperationQuota       = errors.New("operation reporting capacity reached")
	operationReceiptPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	operationSourcePattern  = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// OperationReport records one observed prepare/work/update interval. Its opaque
// context is caller-declared; it neither grants authority nor proves causality.
type OperationReport struct {
	Version           int                  `json:"version"`
	OperationID       string               `json:"operationId"`
	SessionID         string               `json:"sessionId,omitempty"`
	WorkloadDigest    string               `json:"workloadDigest"`
	CoverageDigest    string               `json:"coverageDigest"`
	EnvironmentDigest string               `json:"environmentDigest,omitempty"`
	OtherCachesDigest string               `json:"otherCachesDigest,omitempty"`
	OutputState       string               `json:"outputState"`
	CachePolicy       string               `json:"cachePolicy"`
	CachePreparation  string               `json:"cachePreparation"`
	BaselineReceiptID string               `json:"baselineReceiptId,omitempty"`
	StartedAt         time.Time            `json:"startedAt"`
	FinishedAt        time.Time            `json:"finishedAt"`
	ElapsedMS         int64                `json:"elapsedMs"`
	Status            string               `json:"status"`
	PhaseTimingMS     OperationPhaseTiming `json:"phaseTimingMs"`
}

type OperationPhaseTiming struct {
	Setup   *int64 `json:"setup"`
	Restore *int64 `json:"restore"`
	Work    *int64 `json:"work"`
	Close   *int64 `json:"close"`
	Publish *int64 `json:"publish"`
}

type OperationReceipt struct {
	Version     int    `json:"version"`
	Recorded    bool   `json:"recorded"`
	Repeated    bool   `json:"repeated"`
	OperationID string `json:"operationId"`
	ReceiptID   string `json:"receiptId"`
}

type OperationRepository interface {
	RecordOperation(context.Context, NativeAuthority, OperationReport, time.Time) (OperationReceipt, error)
	OperationPeriod(context.Context, time.Time, time.Time) (OperationPeriodReport, error)
}

type OperationObservation struct {
	ReceiptID    string          `json:"receiptId"`
	Integration  Integration     `json:"integration"`
	SourceCommit string          `json:"sourceCommit,omitempty"`
	Profile      string          `json:"profile"`
	Report       OperationReport `json:"report"`
}

type OperationComparison struct {
	BaselineReceiptID            string   `json:"baselineReceiptId"`
	CandidateReceiptID           string   `json:"candidateReceiptId"`
	Method                       string   `json:"method"`
	Matched                      bool     `json:"matched"`
	Reasons                      []string `json:"reasons"`
	BaselineElapsedMS            *int64   `json:"baselineElapsedMs"`
	CandidateElapsedMS           int64    `json:"candidateElapsedMs"`
	ObservedDifferenceMS         *int64   `json:"observedDifferenceMs"`
	ObservedWorkDifferenceMS     *int64   `json:"observedWorkDifferenceMs"`
	ObservedOverheadDifferenceMS *int64   `json:"observedOverheadDifferenceMs"`
	Assumptions                  []string `json:"assumptions"`
}

type OperationPeriodReport struct {
	Observations       []OperationObservation `json:"observations"`
	Comparisons        []OperationComparison  `json:"comparisons"`
	StoredObservations int                    `json:"storedObservations"`
	HistoryTruncated   bool                   `json:"historyTruncated"`
	Unavailable        bool                   `json:"unavailable,omitempty"`
}

// DecodeOperationReport rejects unknown, duplicate and missing required fields
// before a report may be authorized or stored. Null phases remain unknown.
func DecodeOperationReport(reader io.Reader) (OperationReport, error) {
	data, err := io.ReadAll(io.LimitReader(reader, OperationMaximumBytes+1))
	if err != nil || len(data) > OperationMaximumBytes || operationUniqueJSON(data) != nil {
		return OperationReport{}, ErrOperationInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return OperationReport{}, ErrOperationInvalid
	}
	for _, field := range []string{"version", "operationId", "workloadDigest", "coverageDigest", "outputState", "cachePolicy", "cachePreparation", "startedAt", "finishedAt", "elapsedMs", "status", "phaseTimingMs"} {
		if raw, ok := fields[field]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return OperationReport{}, ErrOperationInvalid
		}
	}
	var phases map[string]json.RawMessage
	if json.Unmarshal(fields["phaseTimingMs"], &phases) != nil || len(phases) != 5 {
		return OperationReport{}, ErrOperationInvalid
	}
	for _, phase := range []string{"setup", "restore", "work", "close", "publish"} {
		if _, ok := phases[phase]; !ok {
			return OperationReport{}, ErrOperationInvalid
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report OperationReport
	if decoder.Decode(&report) != nil || decoder.Decode(&struct{}{}) != io.EOF || validateOperationShape(report) != nil {
		return OperationReport{}, ErrOperationInvalid
	}
	return report, nil
}

func operationUniqueJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func(int) error
	value = func(depth int) error {
		if depth > 4 {
			return ErrOperationInvalid
		}
		token, err := d.Token()
		if err != nil {
			return ErrOperationInvalid
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delimiter != '{' {
			return ErrOperationInvalid
		}
		keys := map[string]bool{}
		for d.More() {
			token, err = d.Token()
			key, ok := token.(string)
			if err != nil || !ok || keys[key] {
				return ErrOperationInvalid
			}
			keys[key] = true
			if err = value(depth + 1); err != nil {
				return err
			}
		}
		if token, err = d.Token(); err != nil || token != json.Delim('}') {
			return ErrOperationInvalid
		}
		return nil
	}
	if value(0) != nil {
		return ErrOperationInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrOperationInvalid
	}
	return nil
}

// ValidateOperationReportShape checks a bounded sealed observation without its
// admission age. Retained exact retries must reach persistence to reconcile;
// only persistence can distinguish them from a stale new admission.
func ValidateOperationReportShape(report OperationReport) error {
	return validateOperationShape(report)
}

// ValidateOperationReport applies the age window for a new admission. Existing
// retained identities are reconciled before applying this temporal check.
func ValidateOperationReport(report OperationReport, now time.Time) error {
	if ValidateOperationReportShape(report) != nil || now.IsZero() || report.StartedAt.Before(now.Add(-24*time.Hour-5*time.Second)) || report.FinishedAt.After(now.Add(5*time.Second)) {
		return ErrOperationInvalid
	}
	return nil
}

func validateOperationShape(r OperationReport) error {
	_, startOffset := r.StartedAt.Zone()
	_, finishOffset := r.FinishedAt.Zone()
	if r.Version != OperationVersion || !nativeInvocationPattern.MatchString(r.OperationID) || r.SessionID != "" && !nativeInvocationPattern.MatchString(r.SessionID) || !nativeWorkPattern.MatchString(r.WorkloadDigest) || !nativeWorkPattern.MatchString(r.CoverageDigest) || r.EnvironmentDigest != "" && !nativeWorkPattern.MatchString(r.EnvironmentDigest) || r.OtherCachesDigest != "" && !nativeWorkPattern.MatchString(r.OtherCachesDigest) || r.BaselineReceiptID != "" && !operationReceiptPattern.MatchString(r.BaselineReceiptID) || !nativeChoice(r.OutputState, "fresh", "incremental", "unknown") || !nativeChoice(r.CachePolicy, "enabled", "bypass", "unknown") || !nativeChoice(r.CachePreparation, "hit", "miss", "mixed", "unavailable", "bypassed", "unknown") || !nativeChoice(r.Status, "success", "failed", "cancelled", "unknown") || r.StartedAt.IsZero() || r.FinishedAt.IsZero() || startOffset != 0 || finishOffset != 0 || r.FinishedAt.Before(r.StartedAt) || r.FinishedAt.Sub(r.StartedAt) > 24*time.Hour || r.ElapsedMS < 0 || r.ElapsedMS > 86400000 {
		return ErrOperationInvalid
	}
	difference := r.FinishedAt.Sub(r.StartedAt) - time.Duration(r.ElapsedMS)*time.Millisecond
	if difference < -5*time.Second || difference > 5*time.Second {
		return ErrOperationInvalid
	}
	var total int64
	for _, phase := range operationPhases(r.PhaseTimingMS) {
		if !nativeNumber(phase, 86400000) {
			return ErrOperationInvalid
		}
		if phase != nil {
			total += *phase
		}
	}
	if total > r.ElapsedMS {
		return ErrOperationInvalid
	}
	return nil
}

func operationPhases(p OperationPhaseTiming) []*int64 {
	return []*int64{p.Setup, p.Restore, p.Work, p.Close, p.Publish}
}

// CompareOperations only examines the explicitly selected pair. It does not
// select favorable samples, sum overlapping comparisons or infer CPU or money.
func CompareOperations(ba NativeAuthority, b OperationReport, ca NativeAuthority, c OperationReport) OperationComparison {
	result := operationComparison(ca, c)
	baselineID := operationReceiptID(ba, b.OperationID)
	result.BaselineElapsedMS = operationInt(b.ElapsedMS)
	add := func(condition bool, reason string) {
		if condition {
			result.Reasons = append(result.Reasons, reason)
		}
	}
	add(c.BaselineReceiptID != baselineID, "baselineReceiptMismatch")
	add(ba.Project != ca.Project, "projectMismatch")
	add(ba.Integration != ca.Integration, "integrationMismatch")
	add(ba.CompatibilityID != ca.CompatibilityID, "profileMismatch")
	baselineSource, candidateSource := operationSourcePattern.MatchString(ba.SourceCommit), operationSourcePattern.MatchString(ca.SourceCommit)
	add(!baselineSource || !candidateSource, "sourceMissing")
	add(baselineSource && candidateSource && ba.SourceCommit != ca.SourceCommit, "sourceMismatch")
	add(b.Status != "success" || c.Status != "success", "unsuccessfulOperation")
	add(b.SessionID == "" || c.SessionID == "", "sessionMissing")
	add(b.SessionID != "" && c.SessionID != "" && b.SessionID != c.SessionID, "sessionMismatch")
	add(b.WorkloadDigest != c.WorkloadDigest, "workloadMismatch")
	add(b.CoverageDigest != c.CoverageDigest, "coverageMismatch")
	add(b.EnvironmentDigest == "" || c.EnvironmentDigest == "", "environmentMissing")
	add(b.EnvironmentDigest != "" && c.EnvironmentDigest != "" && b.EnvironmentDigest != c.EnvironmentDigest, "environmentMismatch")
	add(b.OtherCachesDigest == "" || c.OtherCachesDigest == "", "otherCachesMissing")
	add(b.OtherCachesDigest != "" && c.OtherCachesDigest != "" && b.OtherCachesDigest != c.OtherCachesDigest, "otherCachesMismatch")
	add(b.OutputState != "fresh" || c.OutputState != "fresh", "outputNotFresh")
	add(b.FinishedAt.After(c.StartedAt), "operationOrderMismatch")
	add(c.StartedAt.Sub(b.FinishedAt) > 24*time.Hour, "comparisonWindowExceeded")
	method := "unmatched"
	if b.CachePolicy == "bypass" && b.CachePreparation == "bypassed" && c.CachePolicy == "enabled" {
		method = "bypassToEnabled"
	}
	if b.CachePolicy == "enabled" && b.CachePreparation == "miss" && c.CachePolicy == "enabled" && nativeChoice(c.CachePreparation, "hit", "mixed") {
		method = "coldToWarm"
	}
	add(method == "unmatched", "cachePolicyMismatch")
	if len(result.Reasons) != 0 {
		return result
	}
	result.Method, result.Matched = method, true
	result.ObservedDifferenceMS = operationInt(b.ElapsedMS - c.ElapsedMS)
	if b.PhaseTimingMS.Work != nil && c.PhaseTimingMS.Work != nil {
		result.ObservedWorkDifferenceMS = operationInt(*b.PhaseTimingMS.Work - *c.PhaseTimingMS.Work)
	}
	if bo, bok := operationOverhead(b); bok {
		if co, cok := operationOverhead(c); cok {
			result.ObservedOverheadDifferenceMS = operationInt(bo - co)
		}
	}
	return result
}

func operationComparison(authority NativeAuthority, candidate OperationReport) OperationComparison {
	return OperationComparison{BaselineReceiptID: candidate.BaselineReceiptID, CandidateReceiptID: operationReceiptID(authority, candidate.OperationID), Method: "unmatched", Reasons: []string{}, CandidateElapsedMS: candidate.ElapsedMS, Assumptions: []string{"callerDeclaredContext", "sameSessionWarmthMayDiffer", "observedNotCausal", "cpuAndCostUnknown"}}
}

func operationInt(n int64) *int64 { return &n }
func operationOverhead(r OperationReport) (int64, bool) {
	p := r.PhaseTimingMS
	var total int64
	for _, phase := range operationPhases(p) {
		if phase == nil {
			return 0, false
		}
		total += *phase
	}
	if r.ElapsedMS-total > 5 || total > r.ElapsedMS {
		return 0, false
	}
	return *p.Setup + *p.Restore + *p.Close + *p.Publish, true
}

func operationReceiptID(authority NativeAuthority, id string) string {
	return nativeDocumentHash([]string{"operation-v1", authority.Project, authority.RunID, authority.WorkspaceID, string(authority.Integration), id})
}
