//go:build linux || darwin

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
)

type snapshotExchangeResult struct {
	Version        int                 `json:"version"`
	Token          string              `json:"token"`
	ExpiresAt      time.Time           `json:"expiresAt"`
	Project        string              `json:"project"`
	Compatibility  string              `json:"compatibility"`
	NativeKey      string              `json:"nativeKey"`
	Mode           string              `json:"mode"`
	Capabilities   []access.Capability `json:"capabilities"`
	CredentialKind string              `json:"credentialKind"`
}

func snapshotGrants(t *testing.T, path string, grants []access.SnapshotCredential) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": 1, "credentials": grants})
	if err != nil {
		t.Fatal(err)
	}
	temporary := path + ".next"
	if err := os.WriteFile(temporary, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
}

func snapshotAuthFixture(t *testing.T) (*Server, string, access.SnapshotCredential, string) {
	t.Helper()
	cfg := gatewayTestConfig(t, "snapshot")
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.SnapshotCredentialsFile = filepath.Join(directory, "grants.json")
	bootstrap := "lcs1.trusted-writer." + strings.Repeat("a", 64)
	grant := access.SnapshotCredential{ID: "trusted-writer", SecretSHA256: access.SnapshotSecretDigest(bootstrap), Project: cfg.ProjectID, Compatibility: "darwin-arm64-xcode26", KeyPrefix: "xcode-cloud-v1/", Role: "writer", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	snapshotGrants(t, cfg.SnapshotCredentialsFile, []access.SnapshotCredential{grant})
	instance, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance, bootstrap, grant, "xcode-cloud-v1/build-42"
}

func snapshotExchange(t *testing.T, handler http.Handler, bootstrap string, project, profile, key, mode string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"project": project, "compatibility": profile, "key": key, "mode": mode})
	request := httptest.NewRequest(http.MethodPost, snapshotExchangePath, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+bootstrap)
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, request)
	return out
}
func snapshotResult(t *testing.T, response *httptest.ResponseRecorder) snapshotExchangeResult {
	t.Helper()
	var result snapshotExchangeResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("snapshot exchange failed: status=%d", response.Code)
	}
	return result
}
func snapshotArtifactCall(handler http.Handler, result snapshotExchangeResult, method, path, body, profile string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+result.Token)
	request.Header.Set("X-LayerCache-Compatibility", profile)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestSnapshotExchangeExactArchiveAuthorityAndNoWorkloadAttestation(t *testing.T) {
	instance, bootstrap, grant, key := snapshotAuthFixture(t)
	// An unrelated managed membership policy is not impersonated by this grant.
	instance.projectAuthority = &executionMembership{role: ""}
	result := snapshotResult(t, snapshotExchange(t, instance.Handler(), bootstrap, grant.Project, grant.Compatibility, key, "write"))
	nativeKey, err := access.SnapshotNativeKey(grant.Project, grant.Compatibility, key)
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || result.Project != grant.Project || result.Compatibility != grant.Compatibility || result.NativeKey != nativeKey || result.Mode != "write" || result.CredentialKind != "shared-snapshot-bootstrap" ||
		len(result.Capabilities) != 1 || result.Capabilities[0] != access.CapabilityWrite || !result.ExpiresAt.After(time.Now()) || result.ExpiresAt.After(time.Now().Add(access.SnapshotCapabilityLifetime)) {
		t.Fatal("exchange response changed its exact identity envelope")
	}
	claims, err := access.ParseCapabilityToken(instance.config.LocalToken, result.Token, time.Now())
	if err != nil || claims.Execution != nil || claims.Subject != "snapshot:trusted-writer" || claims.RunID != "" || claims.Repository != "" || claims.Ref != "" || claims.SnapshotScope == nil {
		t.Fatal("snapshot falsely gained workload identity", err)
	}
	path := "/v8/artifacts/" + result.NativeKey
	if out := snapshotArtifactCall(instance.Handler(), result, "PUT", path, "immutable-archive", grant.Compatibility); out.Code != 200 {
		t.Fatalf("writer PUT %d", out.Code)
	}
	for _, method := range []string{"GET", "HEAD"} {
		out := snapshotArtifactCall(instance.Handler(), result, method, path, "", grant.Compatibility)
		if out.Code != 200 || method == "GET" && out.Body.String() != "immutable-archive" {
			t.Fatalf("archive %s %d", method, out.Code)
		}
	}
	for _, test := range []struct{ method, path, profile string }{
		{"DELETE", path, grant.Compatibility}, {"POST", path, grant.Compatibility}, {"GET", path, "other-profile"},
		{"GET", "/v8/artifacts/native-v1-" + strings.Repeat("b", 64), grant.Compatibility},
		{"GET", "/v8/artifacts/status", grant.Compatibility}, {"POST", "/v8/artifacts/events", grant.Compatibility},
		{"POST", "/v1/sync/turbo", grant.Compatibility}, {"GET", "/v1/status", grant.Compatibility}, {"POST", "/v1/gc", grant.Compatibility},
		{"GET", "/v1/reports", grant.Compatibility}, {"POST", "/v1/reports/turbo", grant.Compatibility},
		{"GET", "/v1/executions/observations", grant.Compatibility}, {"POST", "/v1/executions/observations", grant.Compatibility},
		{"GET", "/v1/retained-artifacts", grant.Compatibility}, {"GET", "/v1/gradle-cache/other", grant.Compatibility}, {"GET", "/v1/bazel-cache/ac/other", grant.Compatibility},
		{"GET", path + "?teamId=foreign", grant.Compatibility},
		{"GET", "/v8/artifacts/%6e" + strings.TrimPrefix(result.NativeKey, "n"), grant.Compatibility},
	} {
		out := snapshotArtifactCall(instance.Handler(), result, test.method, test.path, "", test.profile)
		if out.Code < 400 {
			t.Fatalf("snapshot escaped to %s %s: %d", test.method, test.path, out.Code)
		}
	}
	readerBootstrap := "lcs1.reader." + strings.Repeat("b", 64)
	readerGrant := grant
	readerGrant.ID = "reader"
	readerGrant.Role = "reader"
	readerGrant.SecretSHA256 = access.SnapshotSecretDigest(readerBootstrap)
	snapshotGrants(t, instance.config.SnapshotCredentialsFile, []access.SnapshotCredential{grant, readerGrant})
	reader := snapshotResult(t, snapshotExchange(t, instance.Handler(), readerBootstrap, grant.Project, grant.Compatibility, key, "read"))
	if out := snapshotArtifactCall(instance.Handler(), reader, "GET", path, "", grant.Compatibility); out.Code != 200 {
		t.Fatal("reader lost archive", out.Code)
	}
	if out := snapshotArtifactCall(instance.Handler(), reader, "PUT", path, "poison", grant.Compatibility); out.Code < 400 {
		t.Fatal("reader acquired writer authority")
	}
	if out := snapshotExchange(t, instance.Handler(), readerBootstrap, grant.Project, grant.Compatibility, key, "write"); out.Code != 401 {
		t.Fatal("reader requested write", out.Code)
	}
	if out := snapshotArtifactCall(instance.Handler(), result, "PUT", path, "different-archive", grant.Compatibility); out.Code != 409 {
		t.Fatal("scoped writer overwrote immutable recording", out.Code)
	}
}

func TestSnapshotGrantEditsRemovalAndFailuresRevokeAlreadyExchangedTokens(t *testing.T) {
	instance, bootstrap, grant, key := snapshotAuthFixture(t)
	result := snapshotResult(t, snapshotExchange(t, instance.Handler(), bootstrap, grant.Project, grant.Compatibility, key, "write"))
	path := "/v8/artifacts/" + result.NativeKey
	const payload = "immutable-restart-fixture"
	if out := snapshotArtifactCall(instance.Handler(), result, "PUT", path, payload, grant.Compatibility); out.Code != 200 {
		t.Fatal("could not seed revocation fixture", out.Code)
	}
	assertReadable := func(handler http.Handler) {
		t.Helper()
		out := snapshotArtifactCall(handler, result, "GET", path, "", grant.Compatibility)
		if out.Code != 200 || out.Body.String() != payload {
			t.Fatal("valid grant could not read existing exact bytes", out.Code)
		}
	}
	assertDenied := func(handler http.Handler) {
		t.Helper()
		for _, method := range []string{"GET", "HEAD"} {
			out := snapshotArtifactCall(handler, result, method, path, "", grant.Compatibility)
			if out.Code != 404 || out.Body.Len() != 0 {
				t.Fatal("revoked grant disclosed existing artifact", method, out.Code)
			}
		}
		out := snapshotArtifactCall(handler, result, "PUT", path, "must-not-publish", grant.Compatibility)
		if out.Code != 401 {
			t.Fatal("revoked writer reached archive handler", out.Code)
		}
	}
	assertReadable(instance.Handler())
	for name, mutate := range map[string]func(*access.SnapshotCredential){
		"secret":  func(g *access.SnapshotCredential) { g.SecretSHA256 = strings.Repeat("c", 64) },
		"role":    func(g *access.SnapshotCredential) { g.Role = "reader" },
		"project": func(g *access.SnapshotCredential) { g.Project = "foreign" },
		"profile": func(g *access.SnapshotCredential) { g.Compatibility = "other" },
		"prefix":  func(g *access.SnapshotCredential) { g.KeyPrefix = "other/" },
		"expired": func(g *access.SnapshotCredential) { g.ExpiresAt = time.Now().Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := grant
			mutate(&changed)
			snapshotGrants(t, instance.config.SnapshotCredentialsFile, []access.SnapshotCredential{changed})
			assertDenied(instance.Handler())
		})
	}
	for _, body := range []string{`{"version":1,"credentials":[]}`, `{broken`} {
		if err := os.WriteFile(instance.config.SnapshotCredentialsFile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		assertDenied(instance.Handler())
	}
	if err := os.Remove(instance.config.SnapshotCredentialsFile); err != nil {
		t.Fatal(err)
	}
	assertDenied(instance.Handler())
	snapshotGrants(t, instance.config.SnapshotCredentialsFile, []access.SnapshotCredential{grant})
	assertReadable(instance.Handler())
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(context.Background(), instance.config)
	if err != nil {
		t.Fatal(err)
	}
	assertReadable(restarted.Handler())
	snapshotGrants(t, instance.config.SnapshotCredentialsFile, []access.SnapshotCredential{})
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	revokedRestart, err := New(context.Background(), instance.config)
	if err != nil {
		t.Fatal(err)
	}
	defer revokedRestart.Close()
	assertDenied(revokedRestart.Handler())
}

func TestSnapshotExchangeRejectsForgedContextAndCapsExpiry(t *testing.T) {
	instance, bootstrap, grant, key := snapshotAuthFixture(t)
	grant.ExpiresAt = time.Now().Add(30 * time.Second).UTC()
	snapshotGrants(t, instance.config.SnapshotCredentialsFile, []access.SnapshotCredential{grant})
	result := snapshotResult(t, snapshotExchange(t, instance.Handler(), bootstrap, grant.Project, grant.Compatibility, key, "read"))
	if !result.ExpiresAt.Equal(grant.ExpiresAt) {
		t.Fatal("token outlived grant")
	}
	for _, test := range []struct{ token, project, profile, key, mode string }{
		{"lcs1.trusted-writer." + strings.Repeat("b", 64), grant.Project, grant.Compatibility, key, "read"},
		{bootstrap, "foreign", grant.Compatibility, key, "read"}, {bootstrap, grant.Project, "other", key, "read"}, {bootstrap, grant.Project, grant.Compatibility, "xcode-cloud-v1-other/42", "read"},
		{bootstrap, grant.Project, grant.Compatibility, key, "admin"}, {bootstrap, grant.Project, grant.Compatibility, "xcode-cloud-v1/", "read"},
	} {
		if out := snapshotExchange(t, instance.Handler(), test.token, test.project, test.profile, test.key, test.mode); out.Code != 401 {
			t.Fatal("foreign exchange accepted", out.Code)
		}
	}
	for _, extra := range []string{`"branch":"main"`, `"role":"writer"`, `"capabilities":["admin"]`, `"execution":{"kind":"githubActions"}`} {
		body := `{"project":"` + grant.Project + `","compatibility":"` + grant.Compatibility + `","key":"` + key + `","mode":"read",` + extra + `}`
		request := httptest.NewRequest("POST", snapshotExchangePath, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+bootstrap)
		out := httptest.NewRecorder()
		instance.Handler().ServeHTTP(out, request)
		if out.Code != 400 {
			t.Fatal("self-reported authority accepted", out.Code)
		}
	}
	for _, role := range []string{"local", "public"} {
		instance.config.Role = role
		if out := snapshotExchange(t, instance.Handler(), bootstrap, grant.Project, grant.Compatibility, key, "read"); out.Code != 404 {
			t.Fatal("non-Team snapshot auth enabled", out.Code)
		}
	}
}

func TestSnapshotExchangeGatewayRoutesExactBodyProject(t *testing.T) {
	instance, bootstrap, grant, key := snapshotAuthFixture(t)
	foreignCfg := gatewayTestConfig(t, "foreign")
	foreign, err := New(context.Background(), foreignCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	gateway := &ProjectGateway{projects: map[string]*Server{grant.Project: instance, foreignCfg.ProjectID: foreign}}
	result := snapshotResult(t, snapshotExchange(t, gateway, bootstrap, grant.Project, grant.Compatibility, key, "read"))
	if result.Project != grant.Project {
		t.Fatal("wrong project routed")
	}
	body, _ := json.Marshal(map[string]string{"project": grant.Project, "compatibility": grant.Compatibility, "key": key, "mode": "read"})
	for _, selector := range []string{"?teamId=" + foreignCfg.ProjectID, ""} {
		request := httptest.NewRequest("POST", snapshotExchangePath+selector, bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+bootstrap)
		if selector == "" {
			request.Header.Set("X-LayerCache-Project", foreignCfg.ProjectID)
		}
		out := httptest.NewRecorder()
		gateway.ServeHTTP(out, request)
		if out.Code != 404 {
			t.Fatal("conflicting routing selector accepted", out.Code)
		}
	}
}
