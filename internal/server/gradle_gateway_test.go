package server

import (
	"encoding/base64"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/access"
)

func TestProjectGatewayGradleBasicIsolationAndCurrentMembership(t *testing.T) {
	gateway, projects := gatewayFixture(t, nil)
	for _, auth := range []string{"", "Basic invalid", gradleBasic("invalid"), "Basic " + base64.StdEncoding.EncodeToString([]byte("another-project:invalid"))} {
		response := gatewayRequest(gateway, "GET", gradleCachePrefix+"same-hash", auth, "", nil)
		if response.Code != 401 || response.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("invalid native auth must challenge, HTTP %d", response.Code)
		}
	}
	auth := map[string]string{}
	for alias, cfg := range projects {
		auth[alias] = gradleBasic(gatewayToken(t, cfg, "github:alice", "gradle", access.CapabilityWrite))
		if response := gatewayRequest(gateway, "PUT", gradleCachePrefix+"same-hash", auth[alias], alias+"-private", nil); response.Code != 200 {
			t.Fatalf("%s publication HTTP %d: %s", alias, response.Code, response.Body.String())
		}
	}
	for alias := range projects {
		response := gatewayRequest(gateway, "GET", gradleCachePrefix+"same-hash", auth[alias], "", nil)
		if response.Code != 200 || response.Body.String() != alias+"-private" {
			t.Fatalf("project %s exposed another project's bytes: HTTP %d %s", alias, response.Code, response.Body.String())
		}
	}
	other := gradleBasic(gatewayToken(t, projects["alpha"], "github:alice", "nx", access.CapabilityWrite))
	if response := gatewayRequest(gateway, "GET", gradleCachePrefix+"same-hash", other, "", nil); response.Code != 401 {
		t.Fatalf("Nx capability accessed Gradle bytes: HTTP %d", response.Code)
	}
	for name, headers := range map[string]map[string]string{
		"project mismatch": {"X-LayerCache-Project": projects["beta"].ProjectID},
		"profile mismatch": {"X-LayerCache-Compatibility": "darwin-arm64-schema1"},
	} {
		response := gatewayRequest(gateway, "GET", gradleCachePrefix+"same-hash", auth["alpha"], "", headers)
		if response.Code != 404 && response.Code != 401 {
			t.Fatalf("%s accepted: HTTP %d", name, response.Code)
		}
	}
	// Changing the password's routing hint cannot supply a signature for beta.
	token := gatewayToken(t, projects["alpha"], "github:alice", "gradle", access.CapabilityWrite)
	parts := strings.Split(token, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(strings.ReplaceAll(string(payload), "acme/alpha", "acme/beta")))
	if response := gatewayRequest(gateway, "GET", gradleCachePrefix+"same-hash", gradleBasic(strings.Join(parts, ".")), "", nil); response.Code != 401 {
		t.Fatalf("forged project routing hint accessed data: HTTP %d", response.Code)
	}
	delete(gateway.projects[projects["alpha"].ProjectID].config.TeamMembers, "alice")
	for _, method := range []string{"GET", "PUT"} {
		if response := gatewayRequest(gateway, method, gradleCachePrefix+"same-hash", auth["alpha"], "private", nil); response.Code != 401 {
			t.Fatalf("revoked member retained Gradle %s access: HTTP %d", method, response.Code)
		}
	}
}

func TestProjectGatewayGradleCapacityDeclinesAndCleansInterruptedPublication(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "upfront reserve exhausted"
		if streaming {
			name = "capacity lost during stream"
		}
		t.Run(name, func(t *testing.T) {
			gateway, projects := gatewayFixture(t, nil)
			probe := filepath.Join(t.TempDir(), "capacity-probe")
			if err := os.Mkdir(probe, 0700); err != nil {
				t.Fatal(err)
			}
			gateway.pool = StoragePoolConfig{Path: probe, MinFreeBytes: math.MaxInt64}
			if streaming {
				gateway.pool.MinFreeBytes = 0
			}
			body := &nxDrainingBody{reader: strings.NewReader(strings.Repeat("x", 4096))}
			if streaming {
				body.afterFirst = func() {
					if err := os.Remove(probe); err != nil {
						t.Fatal(err)
					}
				}
			}
			auth := gradleBasic(gatewayToken(t, projects["alpha"], "github:alice", "gradle", access.CapabilityWrite))
			r := httptest.NewRequest("PUT", gradleCachePrefix+"capacity-test", body)
			r.ContentLength = 4096
			r.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, r)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("capacity must decline native publication, HTTP %d: %s", w.Code, w.Body.String())
			}
			if streaming && !body.closed {
				t.Fatal("streaming body remained open")
			}
			if got := gatewayRequest(gateway, "GET", gradleCachePrefix+"capacity-test", auth, "", nil); got.Code != 404 {
				t.Fatalf("rejected upload was committed: HTTP %d", got.Code)
			}
			assertGradleStagingEmpty(t, projects["alpha"].DataDir)
		})
	}
}

func TestGradleRejectsUnsafeNativePaths(t *testing.T) {
	instance, cfg := gradleServer(t, "local")
	token := gradleToken(t, cfg, cfg.CompatibilityID, "gradle", access.CapabilityWrite)
	for _, key := range []string{"", ".", "..", "nested/key", "with space", "with\\backslash", "percent%key", "\x00", strings.Repeat("a", 1025)} {
		if validGradleHash(key) || gradleArtifactPath(gradleCachePrefix+key) {
			t.Fatalf("unsafe key accepted: %q", key)
		}
	}
	for _, target := range []string{gradleCachePrefix + "encoded%2fkey", gradleCachePrefix + "%61rchive"} {
		if w := gradleRequest(instance, "PUT", target, token, "bytes"); w.Code < 400 {
			t.Fatalf("encoded path accepted: %s HTTP %d", target, w.Code)
		}
	}
}
