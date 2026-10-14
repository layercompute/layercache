package server_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/publictrust"
	"github.com/layercache/layercache/internal/server"
)

func nxCapability(t *testing.T, cfg config.Config, profile string, capability access.Capability) string {
	t.Helper()
	now := time.Now().UTC()
	token, err := access.MintCapabilityToken(cfg.LocalToken, access.Claims{
		Subject: "nx-client", Project: cfg.ProjectID, Integration: "nx", Compatibility: profile,
		Capabilities: []access.Capability{capability}, ExpiresAt: now.Add(time.Minute),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestNxNativeAuthorityIsScopedAndReadOnlyWritesAreDeclined(t *testing.T) {
	t.Parallel()
	endpoint, cfg := newTeamServer(t)
	const profile = "linux-amd64-node24-nx23-schema1"
	writer := nxCapability(t, cfg, profile, access.CapabilityWrite)
	reader := nxCapability(t, cfg, profile, access.CapabilityRead)
	otherProfile := nxCapability(t, cfg, "darwin-arm64-node24-nx23-schema1", access.CapabilityRead)
	for _, test := range []struct {
		name, method, token, body, profile string
		want                               int
	}{
		{name: "missing", method: http.MethodGet, token: reader, want: 404},
		{name: "unauthorized", method: http.MethodPut, token: "invalid", body: "private", want: 401},
		{name: "unscoped team admin", method: http.MethodPut, token: cfg.LocalToken, body: "private", want: 401},
		{name: "read-only", method: http.MethodPut, token: reader, body: "private", want: 403},
		{name: "profile override", method: http.MethodPut, token: writer, body: "private", profile: "darwin-arm64-node24-nx23-schema1", want: 401},
		{name: "publication", method: http.MethodPut, token: writer, body: "private", want: 200},
		{name: "other profile miss", method: http.MethodGet, token: otherProfile, want: 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := authenticatedRequest(t, test.token, test.method, endpoint.URL+"/v1/cache/opaque-native-hash", strings.NewReader(test.body))
			if test.profile != "" {
				r.Header.Set("X-LayerCache-Compatibility", test.profile)
			}
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("HTTP %d, want %d", response.StatusCode, test.want)
			}
			if test.want == 401 && !strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain") {
				t.Fatalf("native auth error must be plain text: %v", response.Header)
			}
		})
	}
}

func TestNxNativeConcurrentFirstWriterAndQuotaRemainBounded(t *testing.T) {
	t.Parallel()
	endpoint, cfg := newTeamServer(t)
	writer := nxCapability(t, cfg, "linux-amd64-node24-nx23-schema1", access.CapabilityWrite)
	var clients sync.WaitGroup
	statuses := make(chan int, 8)
	for client := 0; client < 8; client++ {
		clients.Add(1)
		go func(client int) {
			defer clients.Done()
			r := authenticatedRequest(t, writer, http.MethodPut, endpoint.URL+"/v1/cache/first-writer", strings.NewReader(strings.Repeat(string(rune('a'+client)), 4096)))
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Error(err)
				return
			}
			response.Body.Close()
			statuses <- response.StatusCode
		}(client)
	}
	clients.Wait()
	close(statuses)
	created, conflicts := 0, 0
	for code := range statuses {
		if code == http.StatusOK {
			created++
		} else if code == http.StatusConflict {
			conflicts++
		} else {
			t.Fatalf("concurrent writer HTTP %d", code)
		}
	}
	if created != 1 || conflicts != 7 {
		t.Fatalf("first-writer outcomes: created=%d conflicts=%d", created, conflicts)
	}
	oversize := authenticatedRequest(t, writer, http.MethodPut, endpoint.URL+"/v1/cache/oversize", strings.NewReader(strings.Repeat("x", int(cfg.MaxBytes+1))))
	response, err := http.DefaultClient.Do(oversize)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("oversize publication = HTTP %d", response.StatusCode)
	}
	status := authenticatedRequest(t, cfg.LocalToken, http.MethodGet, endpoint.URL+"/v1/status", nil)
	response, err = http.DefaultClient.Do(status)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var stats struct {
		UsageBytes int64 `json:"usageBytes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil || stats.UsageBytes != 4096 || stats.UsageBytes > cfg.MaxBytes {
		t.Fatalf("committed usage = %+v, %v", stats, err)
	}
}

func TestNxNativePublicScopeIsDisabled(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Version: 1, Role: "public", DataDir: t.TempDir(), MaxBytes: 1 << 20, ProjectID: "project", LocalToken: "owner", CompatibilityID: "linux-amd64-schema1"}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg.PublicPrivateKey = publictrust.EncodePrivateKey(privateKey)
	cfg.PublicTrustKey = publictrust.EncodePublicKey(publicKey)
	instance, err := server.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	endpoint := httptest.NewServer(instance.Handler())
	t.Cleanup(endpoint.Close)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		r := authenticatedRequest(t, cfg.LocalToken, method, endpoint.URL+"/v1/cache/native-hash", strings.NewReader("private"))
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("public Nx %s = HTTP %d", method, response.StatusCode)
		}
	}
}

func TestNxNativeCorruptArchiveBecomesMissAndCanBeRebuilt(t *testing.T) {
	t.Parallel()
	endpoint, cfg := newTeamServer(t)
	writer := nxCapability(t, cfg, "linux-amd64-node24-nx23-schema1", access.CapabilityWrite)
	request := func(method, contents string) *http.Response {
		t.Helper()
		r := authenticatedRequest(t, writer, method, endpoint.URL+"/v1/cache/rebuild", strings.NewReader(contents))
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := request(http.MethodPut, "valid archive")
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("publish = HTTP %d", response.StatusCode)
	}
	response = request(http.MethodGet, "")
	digest := strings.TrimPrefix(response.Header.Get("x-layercache-digest"), "sha256:")
	response.Body.Close()
	if len(digest) != 64 {
		t.Fatalf("missing archive digest: %q", digest)
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "blobs", "sha256", digest[:2], digest[2:]), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	response = request(http.MethodGet, "")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("corrupt restore = HTTP %d", response.StatusCode)
	}
	response = request(http.MethodPut, "rebuilt archive")
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("rebuild after integrity rejection = HTTP %d", response.StatusCode)
	}
}

func TestNxNativePrivateCachePreservesOpaquePayloadAndImmutability(t *testing.T) {
	t.Parallel()
	endpoint, cfg := newTeamServer(t)
	writer := nxCapability(t, cfg, "linux-amd64-node24-nx23-schema1", access.CapabilityWrite)
	const nativeHash = "12593870829035092274"
	const archive = "opaque\x00gzip archive with output and terminal logs"
	request := func(method, token, body string) *http.Response {
		t.Helper()
		r := authenticatedRequest(t, token, method, endpoint.URL+"/v1/cache/"+nativeHash, strings.NewReader(body))
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := request(http.MethodPut, writer, archive)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("native publication = HTTP %d", response.StatusCode)
	}
	for _, body := range []string{archive, "different logs"} {
		response = request(http.MethodPut, writer, body)
		response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("existing native key = HTTP %d, want 409", response.StatusCode)
		}
	}
	reader := nxCapability(t, cfg, "linux-amd64-node24-nx23-schema1", access.CapabilityRead)
	response = request(http.MethodGet, reader, "")
	defer response.Body.Close()
	restored, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(restored) != archive {
		t.Fatalf("native restore = HTTP %d, %q, %v", response.StatusCode, restored, err)
	}
	if response.Header.Get("Content-Type") != "application/octet-stream" || response.Header.Get("Content-Encoding") != "" || response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("private opaque transport headers = %v", response.Header)
	}
}
