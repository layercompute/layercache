// Package execution records bounded client installations and authenticated CI
// jobs. Resource samples describe the reporting process's environment; they do
// not establish physical machine identity, enrollment, or remote control.
package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
)

const (
	Version                    = 1
	Path                       = "/v1/executions/observations"
	MaximumRequestBytes        = 16 << 10
	MaximumFilesystems         = 4
	MaximumSafeInteger   int64 = 1<<53 - 1
	RetentionDays              = 30
	MaximumExecutions          = 4096
	MaximumInstallations       = 128
	StaleAfterSeconds          = 24 * 60 * 60
)

var ErrInvalid = errors.New("invalid execution observation")
var ErrConflict = errors.New("execution observation authority conflict")
var opaqueID = regexp.MustCompile(`^[0-9a-f]{64}$`)
var numericOwner = regexp.MustCompile(`^github-id:[1-9][0-9]{0,19}$`)
var ciExecution = regexp.MustCompile(`^github-actions:[1-9][0-9]{0,19}:[1-9][0-9]{0,19}:[1-9][0-9]{0,19}:[1-9][0-9]{0,19}$`)

// Context is signed by the capability issuer after verifying the human or job
// identity. It is optional so existing native credentials remain compatible.
type Context struct {
	Kind              string `json:"kind"`
	OwnerID           string `json:"ownerId"`
	ExecutionID       string `json:"executionId"`
	RunnerEnvironment string `json:"runnerEnvironment"`
}

func ClientContext(ownerID, nonce string) *Context {
	digest := sha256.Sum256([]byte("layercache-client-execution-v1\x00" + ownerID + "\x00" + nonce))
	return &Context{Kind: "client", OwnerID: ownerID, ExecutionID: "client:" + hex.EncodeToString(digest[:]), RunnerEnvironment: "unknown"}
}

func (value Context) Validate() error {
	localOwner := strings.HasPrefix(value.OwnerID, "local:") && opaqueID.MatchString(strings.TrimPrefix(value.OwnerID, "local:"))
	if !numericOwner.MatchString(value.OwnerID) && !(localOwner && value.Kind == "client") {
		return ErrInvalid
	}
	switch value.Kind {
	case "client":
		if !strings.HasPrefix(value.ExecutionID, "client:") || !opaqueID.MatchString(strings.TrimPrefix(value.ExecutionID, "client:")) || value.RunnerEnvironment != "unknown" {
			return ErrInvalid
		}
	case "githubActions":
		if !ciExecution.MatchString(value.ExecutionID) || !oneOf(value.RunnerEnvironment, "github-hosted", "self-hosted", "unknown") {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

type Capability struct {
	Version int    `json:"version"`
	Path    string `json:"path"`
}

func Supported() *Capability { return &Capability{Version: Version, Path: Path} }

type CPU struct {
	VisibleLogical    *int64 `json:"visibleLogical"`
	AffinityLogical   *int64 `json:"affinityLogical"`
	QuotaMilliCPU     *int64 `json:"quotaMilliCPU"`
	EffectiveMilliCPU *int64 `json:"effectiveMilliCPU"`
	LimitKnowledge    string `json:"limitKnowledge"`
}
type Memory struct {
	VisibleTotalBytes   *int64 `json:"visibleTotalBytes"`
	CgroupLimitBytes    *int64 `json:"cgroupLimitBytes"`
	EffectiveLimitBytes *int64 `json:"effectiveLimitBytes"`
	LimitKnowledge      string `json:"limitKnowledge"`
}
type Filesystem struct {
	ID             string   `json:"id"`
	Roles          []string `json:"roles"`
	CapacityBytes  *int64   `json:"capacityBytes"`
	FreeBytes      *int64   `json:"freeBytes"`
	AvailableBytes *int64   `json:"availableBytes"`
}
type Sample struct {
	OS          string       `json:"os"`
	Arch        string       `json:"arch"`
	CPU         CPU          `json:"cpu"`
	Memory      Memory       `json:"memory"`
	Filesystems []Filesystem `json:"filesystems"`
}
type ObservationRequest struct {
	Version        int    `json:"version"`
	Phase          string `json:"phase"`
	InstallationID string `json:"installationId,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Sample         Sample `json:"sample"`
}

// Authority is assembled from authenticated claims, never decoded from a report.
type Authority struct {
	Context
	Project     string
	RunID       string
	WorkspaceID string
}
type Entry struct {
	Context
	ID               string    `json:"id"`
	InstallationID   string    `json:"installationId,omitempty"`
	Provider         string    `json:"provider"`
	ProviderEvidence string    `json:"providerEvidence"`
	LastObservedAt   time.Time `json:"lastObservedAt"`
	Phase            string    `json:"phase"`
	Sample           Sample    `json:"sample"`
	CacheActivity    string    `json:"cacheActivity"`
}
type Inventory struct {
	Version           int       `json:"version"`
	From              time.Time `json:"from"`
	To                time.Time `json:"to"`
	UpdatedAt         time.Time `json:"updatedAt"`
	RetentionDays     int       `json:"retentionDays"`
	StaleAfterSeconds int       `json:"staleAfterSeconds"`
	HistoryTruncated  bool      `json:"historyTruncated"`
	ListTruncated     bool      `json:"listTruncated"`
	JobCount          int       `json:"jobCount"`
	Installations     []Entry   `json:"installations"`
	Jobs              []Entry   `json:"jobs"`
}
type Repository interface {
	Record(context.Context, Authority, ObservationRequest, time.Time) error
	Inventory(context.Context, string, time.Time, time.Time) (Inventory, error)
}

func (value ObservationRequest) Validate(authority Authority) error {
	if authority.Context.Validate() != nil || authority.Project == "" || len(authority.Project) > 256 || authority.RunID == "" || len(authority.RunID) > 256 || len(authority.WorkspaceID) > 512 {
		return ErrInvalid
	}
	if value.Version != Version || !oneOf(value.Phase, "start", "finish") || !oneOf(value.Provider, "", "github", "ubicloud", "selfHosted", "unknown") {
		return ErrInvalid
	}
	if authority.Kind == "client" && !opaqueID.MatchString(value.InstallationID) || authority.Kind == "githubActions" && value.InstallationID != "" {
		return ErrInvalid
	}
	s := value.Sample
	if !oneOf(s.OS, "linux", "darwin", "windows", "other") || !oneOf(s.Arch, "amd64", "arm64", "other") || !knowledge(s.CPU.LimitKnowledge) || !knowledge(s.Memory.LimitKnowledge) || len(s.Filesystems) > MaximumFilesystems {
		return ErrInvalid
	}
	for _, n := range []*int64{s.CPU.VisibleLogical, s.CPU.AffinityLogical, s.CPU.QuotaMilliCPU, s.CPU.EffectiveMilliCPU, s.Memory.VisibleTotalBytes, s.Memory.CgroupLimitBytes, s.Memory.EffectiveLimitBytes} {
		if !boundedNumber(n, true) {
			return ErrInvalid
		}
	}
	if s.CPU.EffectiveMilliCPU != nil && s.CPU.LimitKnowledge != "complete" || s.Memory.EffectiveLimitBytes != nil && s.Memory.LimitKnowledge != "complete" {
		return ErrInvalid
	}
	if s.CPU.AffinityLogical != nil && s.CPU.VisibleLogical != nil && *s.CPU.AffinityLogical > *s.CPU.VisibleLogical {
		return ErrInvalid
	}
	if s.CPU.EffectiveMilliCPU != nil {
		for _, pair := range []struct {
			value  *int64
			factor int64
		}{{s.CPU.VisibleLogical, 1000}, {s.CPU.AffinityLogical, 1000}, {s.CPU.QuotaMilliCPU, 1}} {
			if pair.value != nil && *pair.value <= MaximumSafeInteger/pair.factor && *s.CPU.EffectiveMilliCPU > *pair.value*pair.factor {
				return ErrInvalid
			}
		}
	}
	if s.Memory.EffectiveLimitBytes != nil {
		for _, n := range []*int64{s.Memory.VisibleTotalBytes, s.Memory.CgroupLimitBytes} {
			if n != nil && *s.Memory.EffectiveLimitBytes > *n {
				return ErrInvalid
			}
		}
	}
	seen := map[string]bool{}
	for _, fs := range s.Filesystems {
		if !opaqueID.MatchString(fs.ID) || seen[fs.ID] || len(fs.Roles) == 0 || len(fs.Roles) > 3 || !boundedNumber(fs.CapacityBytes, true) || !boundedNumber(fs.FreeBytes, false) || !boundedNumber(fs.AvailableBytes, false) {
			return ErrInvalid
		}
		seen[fs.ID] = true
		roles := map[string]bool{}
		for _, role := range fs.Roles {
			if !oneOf(role, "workspace", "localCache", "staging") || roles[role] {
				return ErrInvalid
			}
			roles[role] = true
		}
		if fs.CapacityBytes != nil && fs.FreeBytes != nil && *fs.FreeBytes > *fs.CapacityBytes || fs.FreeBytes != nil && fs.AvailableBytes != nil && *fs.AvailableBytes > *fs.FreeBytes || fs.CapacityBytes != nil && fs.AvailableBytes != nil && *fs.AvailableBytes > *fs.CapacityBytes {
			return ErrInvalid
		}
	}
	return nil
}
func boundedNumber(n *int64, positive bool) bool {
	return n == nil || *n >= 0 && *n <= MaximumSafeInteger && (!positive || *n > 0)
}
func knowledge(s string) bool { return oneOf(s, "unknown", "partial", "complete") }
func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
