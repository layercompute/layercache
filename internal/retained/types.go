// Package retained defines the immutable declaration and publication receipt for
// one private required artifact. Authentication belongs to the serving adapter.
package retained

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = "retained-artifact-v1"
const MaxRetention = 24 * time.Hour
const ReservationTTL = 15 * time.Minute
const (
	Reserved    = "reserved"
	Staging     = "staging"
	Published   = "published"
	Expired     = "expired"
	Revoked     = "revoked"
	Unavailable = "unavailable"
)

var ErrBusy = errors.New("retained publication is already staging")
var ErrInactive = errors.New("retained publication is not active")

type Producer struct {
	Project      string `json:"project"`
	Repository   string `json:"repository"`
	RepositoryID string `json:"repositoryId"`
	SourceCommit string `json:"sourceCommit"`
	WorkflowRef  string `json:"workflowRef"`
	RunID        string `json:"runId"`
	RunAttempt   string `json:"runAttempt"`
	JobID        string `json:"jobId"`
	Profile      string `json:"profile"`
}
type Declaration struct {
	Producer    Producer  `json:"producer"`
	Name        string    `json:"name"`
	Digest      string    `json:"digest"`
	Size        int64     `json:"size"`
	RetainUntil time.Time `json:"retainUntil"`
}
type Receipt struct {
	SchemaVersion string      `json:"schemaVersion"`
	ID            string      `json:"id"`
	State         string      `json:"state"`
	Declaration   Declaration `json:"declaration"`
	ReservedUntil time.Time   `json:"reservedUntil"`
	CreatedAt     time.Time   `json:"createdAt"`
	PublishedAt   *time.Time  `json:"publishedAt,omitempty"`
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
var numberPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func ValidID(id string) bool { return digestPattern.MatchString(id) }

// ID binds all producer assertions and the logical name. Payload/deadline are
// deliberately excluded so a changed declaration conflicts instead of replacing.
func ID(d Declaration) string {
	data, _ := json.Marshal(struct {
		Producer Producer `json:"producer"`
		Name     string   `json:"name"`
	}{d.Producer, d.Name})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func Validate(d Declaration) error {
	for label, value := range map[string]string{"project": d.Producer.Project, "repository": d.Producer.Repository, "repositoryId": d.Producer.RepositoryID, "workflowRef": d.Producer.WorkflowRef, "runId": d.Producer.RunID, "runAttempt": d.Producer.RunAttempt, "jobId": d.Producer.JobID, "profile": d.Producer.Profile, "name": d.Name} {
		if value == "" || len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("invalid retained %s", label)
		}
	}
	if !numberPattern.MatchString(d.Producer.RepositoryID) || !numberPattern.MatchString(d.Producer.RunID) || !numberPattern.MatchString(d.Producer.RunAttempt) || !numberPattern.MatchString(d.Producer.JobID) {
		return errors.New("invalid retained numeric producer identity")
	}
	if !commitPattern.MatchString(d.Producer.SourceCommit) || !digestPattern.MatchString(d.Digest) || d.Size < 0 || d.RetainUntil.IsZero() {
		return errors.New("invalid retained content declaration")
	}
	if d.RetainUntil.Nanosecond()%1000 != 0 {
		return errors.New("retained deadline requires microsecond precision")
	}
	if len(d.Producer.Project) > 256 || len(d.Producer.Profile) > 256 {
		return errors.New("retained project or profile exceeds limit")
	}
	return nil
}
