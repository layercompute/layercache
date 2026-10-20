//go:build linux || darwin

package portal

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
)

func TestSnapshotAuthPortalRoutesAndManagedTemplatePropagation(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	grantPath := filepath.Join(directory, "snapshot-grants.json")
	q := newPortalQA(t, func(cfg *Config) { cfg.ProjectTemplate.SnapshotCredentialsFile = grantPath })
	alice := q.login("alice")
	team := q.team(alice, "Scoped snapshots")
	project := q.project(alice, team, "acme/alpha")
	foreign := q.project(alice, team, "acme/beta")
	bootstrap := "lcs1.apple-reader." + strings.Repeat("a", 64)
	grant := access.SnapshotCredential{ID: "apple-reader", SecretSHA256: access.SnapshotSecretDigest(bootstrap), Project: project.ID, Compatibility: "darwin-arm64-xcode26", KeyPrefix: "xcode-cloud-v1/", Role: "reader", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	encoded, _ := json.Marshal(map[string]any{"version": 1, "credentials": []access.SnapshotCredential{grant}})
	if err := os.WriteFile(grantPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(map[string]string{"project": project.ID, "compatibility": grant.Compatibility, "key": "xcode-cloud-v1/build-42", "mode": "read"})
	headers := map[string]string{"Authorization": "Bearer " + bootstrap}
	out := q.request("POST", "/v1/auth/snapshot/exchange", string(request), nil, headers)
	requireStatus(t, out, http.StatusOK)
	var result struct {
		Token     string `json:"token"`
		NativeKey string `json:"nativeKey"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil || result.Token == "" {
		t.Fatal("missing scoped token", err)
	}
	for _, path := range []string{"/v1/status", "/v1/reports", "/api/session", "/v1/executions/observations"} {
		out = q.request("GET", path, "", nil, map[string]string{"Authorization": "Bearer " + result.Token})
		if out.Code < 400 {
			t.Fatalf("snapshot acquired unrelated portal authority at %s", path)
		}
	}
	out = q.request("GET", "/v8/artifacts/"+result.NativeKey, "", nil, map[string]string{"Authorization": "Bearer " + result.Token, "X-LayerCache-Compatibility": grant.Compatibility})
	requireStatus(t, out, 404) // Authorized miss, not a private membership impersonation.
	wrong, _ := json.Marshal(map[string]string{"project": foreign.ID, "compatibility": grant.Compatibility, "key": "xcode-cloud-v1/build-42", "mode": "read"})
	requireStatus(t, q.request("POST", "/v1/auth/snapshot/exchange", string(wrong), nil, headers), 401)
	requireStatus(t, q.request("POST", "/v1/auth/snapshot/exchange", string(request), nil, map[string]string{"Authorization": "Bearer " + bootstrap, "X-LayerCache-Project": foreign.ID}), 404)
	// Operational grant paths and hashed secrets must not enter human projections.
	projectView := q.request("GET", "/api/projects/"+project.ID+"/snapshot", "", alice, nil)
	requireStatus(t, projectView, 200)
	for _, private := range []string{bootstrap, grantPath, grant.SecretSHA256, result.Token} {
		if strings.Contains(projectView.Body.String(), private) {
			t.Fatal("private grant material leaked into dashboard")
		}
	}
	if err := os.WriteFile(grantPath, []byte(`{"version":1,"credentials":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("GET", "/v8/artifacts/"+result.NativeKey, "", nil, map[string]string{"Authorization": "Bearer " + result.Token, "X-LayerCache-Compatibility": grant.Compatibility}), 404)
	requireStatus(t, q.request("POST", "/v1/auth/snapshot/exchange", string(request), nil, headers), 401)
}

func TestSnapshotGrantPathRotationDoesNotMigrateOperatorProjectIdentity(t *testing.T) {
	project := operatorTestProject(t)
	before, backend, err := operatorFingerprint(project)
	if err != nil {
		t.Fatal(err)
	}
	project.Config.SnapshotCredentialsFile = filepath.Join(t.TempDir(), "grants.json")
	after, currentBackend, err := operatorFingerprint(project)
	if err != nil || before != after || backend != currentBackend {
		t.Fatal("operational grant path changed adopted project identity", err)
	}
}
