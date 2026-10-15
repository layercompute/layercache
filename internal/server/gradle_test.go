package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/publictrust"
)

func gradleBasic(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("layercache:"+token))
}

func gradleToken(t *testing.T, cfg config.Config, profile, integration string, capability access.Capability) string {
	t.Helper()
	now := time.Now().UTC()
	token, err := access.MintCapabilityToken(cfg.LocalToken, access.Claims{
		Subject: "github:alice", Project: cfg.ProjectID, Integration: integration, Compatibility: profile,
		Capabilities: []access.Capability{capability}, ExpiresAt: now.Add(time.Minute),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func gradleServer(t *testing.T, role string) (*Server, config.Config) {
	t.Helper()
	cfg := gatewayTestConfig(t, "gradle")
	cfg.Role, cfg.MaxBytes = role, 1<<20
	if role == "public" {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		cfg.PublicPrivateKey, cfg.PublicTrustKey = publictrust.EncodePrivateKey(private), publictrust.EncodePublicKey(public)
	}
	instance, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance, cfg
}

func gradleRequest(instance *Server, method, target, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Authorization", gradleBasic(token))
	w := httptest.NewRecorder()
	instance.Handler().ServeHTTP(w, r)
	return w
}

func TestGradleNativeScopedAuthorityAndOpaqueArchives(t *testing.T) {
	t.Parallel()
	instance, cfg := gradleServer(t, "team")
	const profile = "linux-amd64-jdk21-gradle9-schema1"
	writer := gradleToken(t, cfg, profile, "gradle", access.CapabilityWrite)
	reader := gradleToken(t, cfg, profile, "gradle", access.CapabilityRead)
	const archive = "opaque\x00Gradle archive including origin metadata"
	for _, test := range []struct {
		name, method, token, body, username, selector string
		want                                          int
	}{
		{name: "missing", method: "GET", token: reader, want: 404},
		{name: "invalid", method: "PUT", token: "invalid", body: archive, want: 401},
		{name: "wrong Basic username", method: "PUT", token: writer, body: archive, username: "another-project", want: 401},
		{name: "unscoped private root", method: "PUT", token: cfg.LocalToken, body: archive, want: 401},
		{name: "missing profile", method: "PUT", token: gradleToken(t, cfg, "", "gradle", access.CapabilityWrite), body: archive, want: 401},
		{name: "other integration", method: "PUT", token: gradleToken(t, cfg, profile, "nx", access.CapabilityWrite), body: archive, want: 401},
		{name: "read only", method: "PUT", token: reader, body: archive, want: 403},
		{name: "profile override", method: "PUT", token: writer, body: archive, selector: "darwin-arm64-schema1", want: 401},
		{name: "publication", method: "PUT", token: writer, body: archive, want: 200},
		{name: "different profile", method: "GET", token: gradleToken(t, cfg, "darwin-arm64-schema1", "gradle", access.CapabilityRead), want: 404},
		{name: "restore", method: "GET", token: reader, want: 200},
		{name: "unsupported method", method: "POST", token: reader, want: 405},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, gradleCachePrefix+"native-hash", strings.NewReader(test.body))
			r.Header.Set("Authorization", gradleBasic(test.token))
			if test.username != "" {
				r.SetBasicAuth(test.username, test.token)
			}
			if test.selector != "" {
				r.Header.Set("X-LayerCache-Compatibility", test.selector)
			}
			w := httptest.NewRecorder()
			instance.Handler().ServeHTTP(w, r)
			if w.Code != test.want || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("HTTP %d, want %d; headers=%v", w.Code, test.want, w.Header())
			}
			if test.method == "GET" && test.want == 200 && (w.Body.String() != archive || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("Content-Encoding") != "") {
				t.Fatalf("opaque archive changed: %q, %v", w.Body.String(), w.Header())
			}
			if test.want == 401 && w.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("native Basic challenge missing")
			}
		})
	}
	for _, auth := range []string{"Bearer " + writer, "Basic invalid"} {
		r := httptest.NewRequest("GET", gradleCachePrefix+"native-hash", nil)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		instance.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("unsupported authentication accepted: HTTP %d", w.Code)
		}
	}
	r := httptest.NewRequest("GET", gradleCachePrefix+"native-hash", nil)
	r.Header["Authorization"] = []string{gradleBasic(writer), gradleBasic(writer)}
	w := httptest.NewRecorder()
	instance.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("multiple auth headers accepted: HTTP %d", w.Code)
	}
	// Basic translation must not grant access on another protocol route.
	if w := gradleRequest(instance, "PUT", "/v8/artifacts/same-key", writer, archive); w.Code != 401 {
		t.Fatalf("Gradle Basic authority escaped its route: HTTP %d", w.Code)
	}
}

func TestGradleNativeDuplicatesKeepFirstWinnerIncludingConcurrentOrigins(t *testing.T) {
	t.Parallel()
	instance, cfg := gradleServer(t, "local")
	token := gradleToken(t, cfg, cfg.CompatibilityID, "gradle", access.CapabilityWrite)
	for _, body := range []string{"first origin", "first origin", "different origin metadata"} {
		if w := gradleRequest(instance, "PUT", gradleCachePrefix+"retained", token, body); w.Code != 200 {
			t.Fatalf("native duplicate HTTP %d: %s", w.Code, w.Body.String())
		}
	}
	if w := gradleRequest(instance, "GET", gradleCachePrefix+"retained", token, ""); w.Code != 200 || w.Body.String() != "first origin" {
		t.Fatalf("duplicate replaced winner: HTTP %d %q", w.Code, w.Body.String())
	}
	var clients sync.WaitGroup
	for client := 0; client < 8; client++ {
		clients.Add(1)
		go func(client int) {
			defer clients.Done()
			if w := gradleRequest(instance, "PUT", gradleCachePrefix+"concurrent", token, fmt.Sprintf("origin-%d", client)); w.Code != 200 {
				t.Errorf("concurrent native publication HTTP %d: %s", w.Code, w.Body.String())
			}
		}(client)
	}
	clients.Wait()
	w := gradleRequest(instance, "GET", gradleCachePrefix+"concurrent", token, "")
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "origin-") || w.Body.Len() != len("origin-0") {
		t.Fatalf("incomplete winner: HTTP %d %q", w.Code, w.Body.String())
	}
	stats, err := instance.store.Stats(context.Background())
	if err != nil || stats.Entries != 2 || stats.UsageBytes != int64(len("first origin")+len("origin-0")) {
		t.Fatalf("duplicate bytes retained: %+v, %v", stats, err)
	}
}

func TestGradleNativeInterruptedAndOversizeUploadsNeverCommit(t *testing.T) {
	t.Parallel()
	instance, cfg := gradleServer(t, "local")
	token := gradleToken(t, cfg, cfg.CompatibilityID, "gradle", access.CapabilityWrite)
	for _, test := range []struct {
		name   string
		size   int64
		body   string
		cancel bool
		want   int
	}{
		{name: "short declared stream", size: 20, body: "partial", want: 400},
		{name: "long declared stream", size: 1, body: "extra", want: 400},
		{name: "cancelled upload", size: -1, body: "cancelled", cancel: true, want: 400},
		{name: "known oversize", size: cfg.MaxBytes + 1, body: "unused", want: 413},
		{name: "unknown oversize", size: -1, body: strings.Repeat("x", int(cfg.MaxBytes+1)), want: 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &nxDrainingBody{reader: strings.NewReader(test.body)}
			if test.cancel {
				body.afterFirst = cancel
			}
			r := httptest.NewRequest("PUT", gradleCachePrefix+"interrupted", body).WithContext(ctx)
			r.ContentLength = test.size
			r.Header.Set("Authorization", gradleBasic(token))
			w := httptest.NewRecorder()
			instance.Handler().ServeHTTP(w, r)
			if w.Code != test.want || !body.closed {
				t.Fatalf("HTTP %d, want %d; body closed=%v", w.Code, test.want, body.closed)
			}
			if got := gradleRequest(instance, "GET", gradleCachePrefix+"interrupted", token, ""); got.Code != 404 {
				t.Fatalf("rejected upload committed: HTTP %d", got.Code)
			}
			assertGradleStagingEmpty(t, cfg.DataDir)
		})
	}
	r := httptest.NewRequest("PUT", gradleCachePrefix+"chunked", strings.NewReader("valid chunked archive"))
	r.ContentLength = -1
	r.Header.Set("Authorization", gradleBasic(token))
	w := httptest.NewRecorder()
	instance.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("bounded chunked upload HTTP %d: %s", w.Code, w.Body.String())
	}
}

func assertGradleStagingEmpty(t *testing.T, root string) {
	t.Helper()
	files, err := os.ReadDir(filepath.Join(root, "staging"))
	if err != nil || len(files) != 0 {
		t.Fatalf("upload staging retained: %v, %v", files, err)
	}
}

func TestGradleNativeCorruptionBecomesMissAndCanBeRebuilt(t *testing.T) {
	t.Parallel()
	instance, cfg := gradleServer(t, "local")
	token := gradleToken(t, cfg, cfg.CompatibilityID, "gradle", access.CapabilityWrite)
	if w := gradleRequest(instance, "PUT", gradleCachePrefix+"repair", token, "complete"); w.Code != 200 {
		t.Fatalf("publication HTTP %d", w.Code)
	}
	w := gradleRequest(instance, "GET", gradleCachePrefix+"repair", token, "")
	digest := strings.TrimPrefix(w.Header().Get("x-layercache-digest"), "sha256:")
	if len(digest) != 64 {
		t.Fatal("missing verified digest")
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "blobs", "sha256", digest[:2], digest[2:]), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := gradleRequest(instance, "GET", gradleCachePrefix+"repair", token, ""); w.Code != 404 {
		t.Fatalf("corrupt archive served: HTTP %d", w.Code)
	}
	if w := gradleRequest(instance, "PUT", gradleCachePrefix+"repair", token, "rebuilt"); w.Code != 200 {
		t.Fatalf("rebuild HTTP %d", w.Code)
	}
}

func TestGradleNativeQuotaPressureKeepsOnlyCompleteArchives(t *testing.T) {
	t.Parallel()
	instance, cfg := gradleServer(t, "local")
	token := gradleToken(t, cfg, cfg.CompatibilityID, "gradle", access.CapabilityWrite)
	size := int(cfg.MaxBytes/2 + 1)
	for index := 0; index < 3; index++ {
		body := strings.Repeat(string(rune('a'+index)), size)
		if w := gradleRequest(instance, "PUT", fmt.Sprintf("%squota-%d", gradleCachePrefix, index), token, body); w.Code != 200 {
			t.Fatalf("bounded publication HTTP %d: %s", w.Code, w.Body.String())
		}
	}
	stats, err := instance.store.Stats(context.Background())
	if err != nil || stats.UsageBytes != int64(size) || stats.Entries != 1 || stats.UsageBytes > cfg.MaxBytes {
		t.Fatalf("quota exceeded or incomplete entry retained: %+v, %v", stats, err)
	}
	if w := gradleRequest(instance, "GET", gradleCachePrefix+"quota-0", token, ""); w.Code != 404 {
		t.Fatalf("evicted archive did not miss: HTTP %d", w.Code)
	}
	if w := gradleRequest(instance, "GET", gradleCachePrefix+"quota-2", token, ""); w.Code != 200 || w.Body.String() != strings.Repeat("c", size) {
		t.Fatalf("retained archive damaged under pressure: HTTP %d, bytes=%d", w.Code, w.Body.Len())
	}
	assertGradleStagingEmpty(t, cfg.DataDir)
}

func TestGradleNativePublicDisabledAndLocalReadSurvivesMaintenance(t *testing.T) {
	t.Parallel()
	public, publicConfig := gradleServer(t, "public")
	for _, method := range []string{"GET", "PUT"} {
		if w := gradleRequest(public, method, gradleCachePrefix+"private", publicConfig.LocalToken, "private"); w.Code != 404 {
			t.Fatalf("Public Gradle %s HTTP %d", method, w.Code)
		}
	}
	instance, cfg := gradleServer(t, "local")
	token := gradleToken(t, cfg, cfg.CompatibilityID, "gradle", access.CapabilityWrite)
	archive := strings.Repeat("opaque archive", 8192)
	if w := gradleRequest(instance, "PUT", gradleCachePrefix+"active-read", token, archive); w.Code != 200 {
		t.Fatalf("publication HTTP %d", w.Code)
	}
	writer := &gradleMaintenanceWriter{ResponseRecorder: httptest.NewRecorder(), maintenance: func() {
		if err := instance.store.GC(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
	}}
	r := httptest.NewRequest("GET", gradleCachePrefix+"active-read", nil)
	r.Header.Set("Authorization", gradleBasic(token))
	instance.Handler().ServeHTTP(writer, r)
	if writer.Code != 200 || writer.Body.String() != archive {
		t.Fatalf("active read damaged by maintenance: HTTP %d, bytes=%d", writer.Code, writer.Body.Len())
	}
	key := artifact.Key{Integration: "gradle", Project: cfg.ProjectID, Compatibility: cfg.CompatibilityID, Native: "active-read"}
	if _, err := instance.store.Head(context.Background(), key); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("maintenance did not evict logical entry: %v", err)
	}
}

type gradleMaintenanceWriter struct {
	*httptest.ResponseRecorder
	maintenance func()
}

func (writer *gradleMaintenanceWriter) WriteHeader(code int) {
	writer.ResponseRecorder.WriteHeader(code)
	if writer.maintenance != nil {
		maintenance := writer.maintenance
		writer.maintenance = nil
		maintenance()
	}
}
