package access

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/compatibility"
)

const SnapshotCapabilityLifetime = 15 * time.Minute
const maximumSnapshotCredentials = 64
const maximumSnapshotFileBytes = 64 << 10

var ErrSnapshotCredentials = errors.New("protected snapshot credentials unavailable")
var snapshotIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var snapshotDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var snapshotNativePattern = regexp.MustCompile(`^native-v1-[a-f0-9]{64}$`)

// SnapshotScope is a shared operator grant, not workload identity or attestation.
// The current protected grant must still authorize each exact artifact request.
type SnapshotScope struct {
	CredentialID string `json:"credentialId"`
	GrantDigest  string `json:"grantDigest"`
	NativeKey    string `json:"nativeKey"`
}

type SnapshotCredential struct {
	ID            string    `json:"id"`
	SecretSHA256  string    `json:"secretSha256"`
	Project       string    `json:"project"`
	Compatibility string    `json:"compatibility"`
	KeyPrefix     string    `json:"keyPrefix"`
	Role          string    `json:"role"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type snapshotCredentialFile struct {
	Version     int                  `json:"version"`
	Credentials []SnapshotCredential `json:"credentials"`
}

// ReadSnapshotCredentials reloads on every use. An unreadable or invalid file
// never falls back to an older copy. An empty list intentionally revokes all grants.
func ReadSnapshotCredentials(path string) ([]SnapshotCredential, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrSnapshotCredentials
	}
	data, err := readSnapshotCredentialFile(path)
	if err != nil {
		return nil, ErrSnapshotCredentials
	}
	var file snapshotCredentialFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&file) != nil || decoder.Decode(&struct{}{}) != io.EOF || file.Version != 1 || file.Credentials == nil || len(file.Credentials) > maximumSnapshotCredentials {
		return nil, ErrSnapshotCredentials
	}
	ids, secrets := map[string]bool{}, map[string]bool{}
	for index := range file.Credentials {
		grant := &file.Credentials[index]
		if !snapshotIDPattern.MatchString(grant.ID) || !snapshotDigestPattern.MatchString(grant.SecretSHA256) || !snapshotProject(grant.Project) ||
			compatibility.Validate(grant.Compatibility) != nil || !snapshotKey(grant.KeyPrefix) || len(grant.KeyPrefix) > 256 || !strings.HasSuffix(grant.KeyPrefix, "/") ||
			(grant.Role != "reader" && grant.Role != "writer") || grant.ExpiresAt.IsZero() || ids[grant.ID] || secrets[grant.SecretSHA256] {
			return nil, ErrSnapshotCredentials
		}
		grant.ExpiresAt = grant.ExpiresAt.UTC()
		ids[grant.ID], secrets[grant.SecretSHA256] = true, true
	}
	return file.Credentials, nil
}

func SnapshotBootstrapID(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "lcs1" || !snapshotIDPattern.MatchString(parts[1]) || !snapshotDigestPattern.MatchString(parts[2]) {
		return "", false
	}
	return parts[1], true
}

func SnapshotSecretDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// Digest changes on rotation, role/scope edits and expiry changes, invalidating
// capabilities already exchanged against a previous grant.
func (grant SnapshotCredential) Digest() string {
	encoded, _ := json.Marshal(grant)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (grant SnapshotCredential) Allows(project, profile, key, mode string, now time.Time) bool {
	return grant.Project == project && grant.Compatibility == profile && grant.ExpiresAt.After(now) &&
		snapshotKey(key) && len(key) > len(grant.KeyPrefix) && strings.HasPrefix(key, grant.KeyPrefix) &&
		(mode == "read" || mode == "write" && grant.Role == "writer")
}

// SnapshotNativeKey preserves native/client.ts artifactKey's existing identity.
// Restrict this exchange to printable ASCII so Go and JS encode the same JSON.
func SnapshotNativeKey(project, profile, key string) (string, error) {
	if !snapshotProject(project) || compatibility.Validate(profile) != nil || !snapshotKey(key) {
		return "", ErrInvalidToken
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if encoder.Encode([]string{project, profile, key}) != nil {
		return "", ErrInvalidToken
	}
	digest := sha256.Sum256(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	return "native-v1-" + hex.EncodeToString(digest[:]), nil
}

func snapshotProject(value string) bool {
	return len(value) <= 256 && snapshotKey(value)
}

func snapshotKey(value string) bool {
	if len(value) == 0 || len(value) > 8192 {
		return false
	}
	for _, char := range []byte(value) {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

func validSnapshotClaims(claims Claims, now time.Time) bool {
	scope := claims.SnapshotScope
	return scope != nil && snapshotIDPattern.MatchString(scope.CredentialID) && snapshotDigestPattern.MatchString(scope.GrantDigest) &&
		snapshotNativePattern.MatchString(scope.NativeKey) && claims.Subject == "snapshot:"+scope.CredentialID && claims.Integration == "turbo" &&
		snapshotProject(claims.Project) && compatibility.Validate(claims.Compatibility) == nil && claims.ExpiresAt.Sub(now) <= SnapshotCapabilityLifetime &&
		len(claims.Capabilities) == 1 && (claims.Capabilities[0] == CapabilityRead || claims.Capabilities[0] == CapabilityWrite) &&
		claims.Execution == nil && claims.RunID == "" && claims.WorkspaceID == "" && claims.Repository == "" && claims.Ref == "" && claims.DefaultRef == "" &&
		claims.SourceCommit == "" && claims.ActionsActorID == "" && claims.ActionsActor == "" && claims.RepositoryID == "" && claims.WorkflowRef == "" &&
		claims.ActionsRunID == "" && claims.RunAttempt == "" && claims.CheckRunID == "" && claims.RecipeDigest == "" && claims.Target == "" &&
		claims.Platform == "" && claims.Toolchain == "" && claims.Builder == ""
}
