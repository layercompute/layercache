package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
)

// The literal digest is a cross-client protocol vector for github.com/acme/widget.
const projectAudienceVector = "https://layercache.dev/oidc/projects/8ac5bc08a41714b439de011cd921e0dddce459b45787709cfa4e295aa228f879"

func TestGitHubOIDCProjectAudiences(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "project-audience-test"
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jwks" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
			"kid": keyID, "kty": "RSA", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}})
	}))
	t.Cleanup(issuer.Close)
	cfg := config.Config{
		Version: 1, Role: "team", DataDir: t.TempDir(), Listen: "127.0.0.1:7437",
		MaxBytes: 1 << 20, ProjectID: "github.com/acme/widget", LocalToken: "team-token",
		CompatibilityID: "linux-amd64-schema1", ActionsRepository: "acme/widget",
		ActionsRef: "refs/heads/main", ActionsDefaultRef: "refs/heads/main",
		GitHubOIDCIssuer: issuer.URL,
	}
	instance, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, audience string
		foreignRepo    bool
		expired        bool
		tampered       bool
		want           int
	}{
		{name: "canonical HTTPS project", audience: projectAudienceVector, want: http.StatusOK},
		{name: "exact legacy project", audience: "layercache:github.com/acme/widget", want: http.StatusOK},
		{name: "foreign canonical project", audience: "https://layercache.dev/oidc/projects/563682034a4c8ea7ad46688a92777b1656725fb08835888a5b10b308c174f8ef", want: http.StatusUnauthorized},
		{name: "foreign legacy project", audience: "layercache:github.com/acme/other", want: http.StatusUnauthorized},
		{name: "GitHub default audience", audience: "https://github.com/acme", want: http.StatusUnauthorized},
		{name: "origin without project", audience: "https://layercache.dev", want: http.StatusUnauthorized},
		{name: "unhashed project", audience: "https://layercache.dev/oidc/projects/github.com/acme/widget", want: http.StatusUnauthorized},
		{name: "empty audience", want: http.StatusUnauthorized},
		{name: "canonical foreign repository", audience: projectAudienceVector, foreignRepo: true, want: http.StatusUnauthorized},
		{name: "canonical expired token", audience: projectAudienceVector, expired: true, want: http.StatusUnauthorized},
		{name: "canonical invalid signature", audience: projectAudienceVector, tampered: true, want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{
				"iss": issuer.URL, "aud": tc.audience,
				"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(5 * time.Minute).Unix(),
				"sub": "repo:acme/widget:ref:refs/heads/main", "repository": "acme/widget",
				"ref": "refs/heads/main", "sha": strings.Repeat("a", 40), "event_name": "push",
				"run_id": "123", "run_attempt": "1", "check_run_id": "456",
			}
			if tc.foreignRepo {
				claims["repository"] = "acme/other"
			}
			if tc.expired {
				claims["exp"] = now.Add(-time.Minute).Unix()
			}
			token := signOIDCTestToken(t, key, keyID, claims)
			if tc.tampered {
				parts := strings.Split(token, ".")
				signature, err := base64.RawURLEncoding.DecodeString(parts[2])
				if err != nil {
					t.Fatal(err)
				}
				signature[0] ^= 1
				parts[2] = base64.RawURLEncoding.EncodeToString(signature)
				token = strings.Join(parts, ".")
			}
			body, err := json.Marshal(map[string]string{"project": cfg.ProjectID, "compatibility": "linux-amd64-schema1", "integration": "turbo", "idToken": token})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			instance.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/auth/github-oidc/exchange", bytes.NewReader(body)))
			if response.Code != tc.want {
				t.Fatalf("OIDC exchange status = %d, want %d", response.Code, tc.want)
			}
			if tc.want != http.StatusOK {
				return
			}
			var result struct{ TeamToken string }
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			capability, err := access.ParseCapabilityToken(cfg.LocalToken, result.TeamToken, now)
			if err != nil || capability.Project != cfg.ProjectID || capability.Integration != "turbo" {
				t.Fatalf("project-scoped Turbo capability = %+v, error = %v", capability, err)
			}
		})
	}
}
