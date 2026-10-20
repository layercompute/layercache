package measurement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/layercache/layercache/internal/execution"
)

const (
	NativeVersion            = 1
	NativePath               = "/v1/reports/native"
	NativeMaximumBytes       = 4 << 20
	NativeMaximumTasks       = 4096
	NativeMaximumInvocations = 32
	NativeRetentionDays      = 30
)

var (
	ErrNativeInvalid        = errors.New("invalid bounded native report")
	ErrNativeConflict       = errors.New("native invocation already has a different final report")
	ErrNativeQuota          = errors.New("native reporting capacity reached")
	nativeInvocationPattern = regexp.MustCompile(`^run-[A-Za-z0-9_-]{12,124}$`)
	nativeWorkPattern       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	nativeKeyPattern        = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// NativeReport is a bounded projection of completed native observations. It
// contains no project, profile, run authority, paths, commands or resource claims.
type NativeReport struct {
	Version      int                `json:"version"`
	InvocationID string             `json:"invocationId"`
	StartedAt    time.Time          `json:"startedAt"`
	FinishedAt   time.Time          `json:"finishedAt"`
	Status       string             `json:"status"`
	Tasks        []NativeTask       `json:"tasks"`
	Counters     *NativeCounters    `json:"counters,omitempty"`
	TimingMS     *NativePhaseTiming `json:"timingMs,omitempty"`
}

type NativeTask struct {
	WorkRef          string    `json:"workRef"`
	NativeKey        string    `json:"nativeKey,omitempty"`
	CacheEligibility string    `json:"cacheEligibility,omitempty"`
	Outcome          string    `json:"outcome"`
	StartedAt        time.Time `json:"startedAt"`
	FinishedAt       time.Time `json:"finishedAt"`
	ExecutionMS      *int64    `json:"executionMs,omitempty"`
}

// Protocol requests, native compiler results and elapsed phases are separate
// observations. Missing fields remain unknown, including when another is zero.
type NativeCounters struct {
	RPC      *NativeRPCCounters      `json:"rpc,omitempty"`
	Compiler *NativeCompilerCounters `json:"compiler,omitempty"`
}

type NativeRPCCounters struct {
	ActionReads  *int64 `json:"actionReads"`
	ActionHits   *int64 `json:"actionHits"`
	ActionWrites *int64 `json:"actionWrites"`
	CASReads     *int64 `json:"casReads"`
	CASHits      *int64 `json:"casHits"`
	CASWrites    *int64 `json:"casWrites"`
	CacheErrors  *int64 `json:"cacheErrors"`
}

type NativeCompilerCounters struct {
	Scope    string `json:"scope"`
	Coverage string `json:"coverage"`
	Reason   string `json:"reason,omitempty"`
	Hits     *int64 `json:"hits"`
	Misses   *int64 `json:"misses"`
	Skipped  *int64 `json:"skipped"`
	Unknown  *int64 `json:"unknown"`
}

type NativePhaseTiming struct {
	Operation *int64 `json:"operation"`
	Compile   *int64 `json:"compile"`
	Load      *int64 `json:"load"`
	Restore   *int64 `json:"restore"`
	Close     *int64 `json:"close"`
	Snapshot  *int64 `json:"snapshot"`
}

// NativeAuthority is supplied by the credential verifier, never report JSON.
// A member-local source is caller declared; it does not attest an Apple job.
type NativeAuthority struct {
	Project         string
	RunID           string
	WorkspaceID     string
	Integration     Integration
	CompatibilityID string
	Subject         string
	Repository      string
	SourceCommit    string
	Kind            string // githubActions, memberLocal, local
	Execution       *execution.Context
}

type NativeRepository interface {
	RecordTransport(context.Context, NativeTransport) error
	RecordReport(context.Context, NativeAuthority, NativeReport, time.Time) (NativeReceipt, error)
	PeriodReport(context.Context, time.Time, time.Time) ([]NativeIntegrationReport, error)
}

type NativeTransport struct {
	Authority  NativeAuthority
	Kind       string // delivered, missed, published
	ArtifactID string
	Digest     string
	StartedAt  time.Time
	FinishedAt time.Time
	Source     Source
	Bytes      int64
	Resource   *execution.ResourceSnapshot `json:"resource,omitempty"`
}

type NativeReceipt struct {
	Version      int    `json:"version"`
	Recorded     bool   `json:"recorded"`
	Repeated     bool   `json:"repeated"`
	InvocationID string `json:"invocationId"`
	Tasks        int    `json:"tasks"`
}

type NativeOutcomeCounts struct {
	Executed  int `json:"executed"`
	FromCache int `json:"fromCache"`
	UpToDate  int `json:"upToDate"`
	NoSource  int `json:"noSource"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	Unknown   int `json:"unknown"`
}

type NativeCoverage struct {
	Known int `json:"known"`
	Total int `json:"total"`
}

type NativeSourceHits struct {
	Source Source `json:"source"`
	Hits   int    `json:"hits"`
}

type NativeIntegrationReport struct {
	Integration                Integration         `json:"integration"`
	ReportedInvocations        int                 `json:"reportedInvocations"`
	ReportedTasks              int                 `json:"reportedTasks"`
	Outcomes                   NativeOutcomeCounts `json:"outcomes"`
	KnownEligible              int                 `json:"knownEligible"`
	EligibilityUnknown         int                 `json:"eligibilityUnknown"`
	Eligible                   *int                `json:"eligible"`
	Hits                       int                 `json:"hits"`
	Misses                     int                 `json:"misses"`
	HitRate                    *float64            `json:"hitRate"`
	Sources                    []NativeSourceHits  `json:"sources"`
	ProducerTiming             NativeCoverage      `json:"producerTiming"`
	ProducerResources          NativeCoverage      `json:"producerResources"`
	GrossAvoidedTaskTime       Estimate            `json:"grossAvoidedTaskTime"`
	NetEstimatedBuildTimeSaved Estimate            `json:"netEstimatedBuildTimeSaved"`
	NetAvoidedTaskTime         Estimate            `json:"netAvoidedTaskTime"`
	AllocatedCPUSecondsSaved   *float64            `json:"allocatedCpuSecondsSaved"`
	MeasuredCPUSecondsSaved    *float64            `json:"measuredCpuSecondsSaved"`
	CostSaved                  *float64            `json:"costSaved"`
	Counters                   *NativeCounters     `json:"counters,omitempty"`
	TimingMS                   *NativePhaseTiming  `json:"timingMs,omitempty"`
	HistoryTruncated           bool                `json:"historyTruncated"`
}

// DecodeNativeReport bounds input before decoding. Callers also validate its
// clock window before any network request or persistence operation.
func DecodeNativeReport(reader io.Reader) (NativeReport, error) {
	data, err := io.ReadAll(io.LimitReader(reader, NativeMaximumBytes+1))
	if err != nil || len(data) > NativeMaximumBytes {
		return NativeReport{}, ErrNativeInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report NativeReport
	if decoder.Decode(&report) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return NativeReport{}, ErrNativeInvalid
	}
	if validateNativeShape(report) != nil {
		return NativeReport{}, ErrNativeInvalid
	}
	return report, nil
}

func ValidateNativeReport(report NativeReport, now time.Time) error {
	if validateNativeShape(report) != nil || now.IsZero() || report.StartedAt.Before(now.Add(-24*time.Hour-5*time.Second)) || report.FinishedAt.After(now.Add(5*time.Second)) {
		return ErrNativeInvalid
	}
	return nil
}

func validateNativeShape(report NativeReport) error {
	if report.Version != NativeVersion || !nativeInvocationPattern.MatchString(report.InvocationID) || !nativeChoice(report.Status, "success", "failed", "cancelled", "unknown") || report.StartedAt.IsZero() || report.FinishedAt.Before(report.StartedAt) || report.FinishedAt.Sub(report.StartedAt) > 24*time.Hour || report.Tasks == nil || len(report.Tasks) > NativeMaximumTasks || len(report.Tasks) == 0 && report.Counters == nil && report.TimingMS == nil {
		return ErrNativeInvalid
	}
	seen := map[string]bool{}
	for _, task := range report.Tasks {
		if !nativeWorkPattern.MatchString(task.WorkRef) || seen[task.WorkRef] || !nativeChoice(task.CacheEligibility, "", "eligible", "ineligible", "unknown") || task.CacheEligibility == "ineligible" && (task.NativeKey != "" || task.Outcome == "fromCache") || task.NativeKey != "" && !nativeKeyPattern.MatchString(task.NativeKey) || !nativeChoice(task.Outcome, "executed", "fromCache", "upToDate", "noSource", "skipped", "failed", "unknown") || task.StartedAt.IsZero() || task.FinishedAt.Before(task.StartedAt) || task.StartedAt.Before(report.StartedAt.Add(-5*time.Second)) || task.FinishedAt.After(report.FinishedAt.Add(5*time.Second)) || !nativeNumber(task.ExecutionMS, 24*60*60*1000) || task.ExecutionMS != nil && task.Outcome != "executed" {
			return ErrNativeInvalid
		}
		if task.ExecutionMS != nil && time.Duration(*task.ExecutionMS)*time.Millisecond > task.FinishedAt.Sub(task.StartedAt)+time.Millisecond {
			return ErrNativeInvalid
		}
		seen[task.WorkRef] = true
	}
	if report.Counters != nil {
		if rpc := report.Counters.RPC; rpc != nil {
			for _, number := range []*int64{rpc.ActionReads, rpc.ActionHits, rpc.ActionWrites, rpc.CASReads, rpc.CASHits, rpc.CASWrites, rpc.CacheErrors} {
				if !nativeNumber(number, execution.MaximumSafeInteger) {
					return ErrNativeInvalid
				}
			}
			if rpc.ActionHits != nil && rpc.ActionReads != nil && *rpc.ActionHits > *rpc.ActionReads || rpc.CASHits != nil && rpc.CASReads != nil && *rpc.CASHits > *rpc.CASReads {
				return ErrNativeInvalid
			}
		}
		if compiler := report.Counters.Compiler; compiler != nil {
			if !nativeChoice(compiler.Scope, "clang-pch", "swift", "mixed", "unknown") || !nativeChoice(compiler.Coverage, "complete", "partial", "unsupported", "unknown") || !nativeChoice(compiler.Reason, "", "missing", "truncated", "unsupported", "ambiguous", "none") {
				return ErrNativeInvalid
			}
			for _, number := range []*int64{compiler.Hits, compiler.Misses, compiler.Skipped, compiler.Unknown} {
				if !nativeNumber(number, execution.MaximumSafeInteger) {
					return ErrNativeInvalid
				}
			}
			if compiler.Coverage == "complete" && (compiler.Hits == nil || compiler.Misses == nil || compiler.Unknown != nil && *compiler.Unknown != 0 || !nativeChoice(compiler.Reason, "", "none")) {
				return ErrNativeInvalid
			}
		}
	}
	if phases := report.TimingMS; phases != nil {
		for _, number := range []*int64{phases.Operation, phases.Compile, phases.Load, phases.Restore, phases.Close, phases.Snapshot} {
			if !nativeNumber(number, 24*60*60*1000) {
				return ErrNativeInvalid
			}
		}
	}
	return nil
}

func nativeNumber(value *int64, maximum int64) bool {
	return value == nil || *value >= 0 && *value <= maximum
}
func nativeChoice(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

// NativeArtifactIdentity is shared by native-key projection and HTTP evidence.
// The native key remains complete and unchanged at the storage boundary.
func NativeArtifactIdentity(project, profile string, integration Integration, nativeKey string) string {
	digest := sha256.Sum256([]byte(string(integration) + "\x00" + project + "\x00" + profile + "\x00" + nativeKey + "\x00\x00"))
	return "sha256:" + hex.EncodeToString(digest[:])
}
