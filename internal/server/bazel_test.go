package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
)

const bazelTestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func bazelServer(t *testing.T, backend http.Handler) (*Server, config.Config) {
	t.Helper()
	upstream := httptest.NewServer(backend)
	t.Cleanup(upstream.Close)
	cfg := gatewayTestConfig(t, "bazel")
	cfg.BazelRemote = &config.BazelRemoteConfig{
		ProjectID: cfg.ProjectID, CompatibilityID: cfg.CompatibilityID, URL: upstream.URL,
		MaxBlobBytes: 1 << 20, RequestTimeout: 30 * time.Second,
	}
	instance, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance, cfg
}

func bazelRequest(instance *Server, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	instance.Handler().ServeHTTP(response, request)
	return response
}

func TestBazelProxyScopesCurrentPrivateAuthorityAndHeaders(t *testing.T) {
	var calls atomic.Int64
	instance, cfg := bazelServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/cas/"+bazelTestHash || r.URL.RawQuery != "" {
			t.Errorf("unexpected native engine target: %s", r.URL)
		}
		for _, header := range []string{"Authorization", "Cookie", "X-LayerCache-Project", "X-LayerCache-Compatibility", "Forwarded", "X-Forwarded-For"} {
			if r.Header.Get(header) != "" {
				t.Errorf("client %s escaped authority boundary", header)
			}
		}
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("native engine compression was not disabled")
		}
		if r.Method == http.MethodPut {
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != "native bytes" || r.ContentLength != 12 {
				t.Errorf("native upload changed: %q, length %d, error %v", body, r.ContentLength, err)
			}
			return
		}
		w.Header().Set("Content-Length", "12")
		w.Header().Set("Set-Cookie", "backend-secret=not-for-client")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "native bytes")
		}
	}))
	writer := gradleToken(t, cfg, cfg.CompatibilityID, "bazel", access.CapabilityWrite)
	reader := gradleToken(t, cfg, cfg.CompatibilityID, "bazel", access.CapabilityRead)
	foreign := cfg
	foreign.ProjectID = "foreign-project"
	for _, test := range []struct {
		name, method, token string
		want                int
	}{
		{"missing credential", "GET", "", 401},
		{"wrong integration", "GET", gradleToken(t, cfg, cfg.CompatibilityID, "gradle", access.CapabilityRead), 401},
		{"foreign project signed by this engine", "GET", gradleToken(t, foreign, cfg.CompatibilityID, "bazel", access.CapabilityRead), 401},
		{"wrong profile", "GET", gradleToken(t, cfg, "another-profile", "bazel", access.CapabilityRead), 401},
		{"unscoped Team administrator", "GET", cfg.LocalToken, 401},
		{"read-only publication", "PUT", reader, 403},
		{"unsupported deletion", "DELETE", writer, 405},
		{"publication", "PUT", writer, 200},
		{"read", "GET", reader, 200},
		{"head", "HEAD", reader, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, bazelCachePrefix+"cas/"+bazelTestHash, strings.NewReader("native bytes"))
			request.Header.Set("Authorization", "Bearer "+test.token)
			request.Header.Set("Cookie", "private=secret")
			request.Header.Set("Forwarded", "host=foreign")
			request.Header.Set("X-LayerCache-Project", cfg.ProjectID)
			response := httptest.NewRecorder()
			instance.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("HTTP %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
			if response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("native response leaked backend cookies or cache authority")
			}
			if test.method == "GET" && response.Code == 200 && response.Body.String() != "native bytes" {
				t.Fatal("native bytes changed")
			}
		})
	}
	if calls.Load() != 3 {
		t.Fatalf("unauthorized request reached native engine: %d calls", calls.Load())
	}
	delete(instance.config.TeamMembers, "alice")
	if response := bazelRequest(instance, "GET", bazelCachePrefix+"cas/"+bazelTestHash, reader, nil); response.Code != 401 {
		t.Fatalf("revoked reader retained access: %d", response.Code)
	}
	if calls.Load() != 3 {
		t.Fatal("revoked request reached native engine")
	}
}

func TestBazelProxyPrivateProjectsCannotReadEachOthersNativeBytes(t *testing.T) {
	projects := map[string]config.Config{}
	for _, alias := range []string{"alpha", "beta"} {
		payload := "native-" + alias
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = io.WriteString(w, payload)
		}))
		t.Cleanup(backend.Close)
		cfg := gatewayTestConfig(t, alias)
		cfg.CompatibilityID = "linux-amd64-schema1"
		cfg.BazelRemote = &config.BazelRemoteConfig{
			ProjectID: cfg.ProjectID, CompatibilityID: cfg.CompatibilityID, URL: backend.URL,
			MaxBlobBytes: 1 << 20, RequestTimeout: time.Second,
		}
		projects[alias] = cfg
	}
	gateway, err := NewProjectGateway(context.Background(), ProjectGatewayConfig{Origin: "https://cache.example", Projects: projects})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	for alias, cfg := range projects {
		token := gradleToken(t, cfg, cfg.CompatibilityID, "bazel", access.CapabilityRead)
		response := gatewayRequest(gateway, "GET", bazelCachePrefix+"cas/"+bazelTestHash, "Bearer "+token, "", nil)
		if response.Code != 200 || response.Body.String() != "native-"+alias {
			t.Fatalf("project %s read foreign native bytes: HTTP %d %q", alias, response.Code, response.Body.String())
		}
		other := "alpha"
		if alias == other {
			other = "beta"
		}
		response = gatewayRequest(gateway, "GET", bazelCachePrefix+"cas/"+bazelTestHash, "Bearer "+token, "", map[string]string{"X-LayerCache-Project": projects[other].ProjectID})
		if response.Code < 400 {
			t.Fatal("project routing selector overrode signed native authority")
		}
	}
}

func TestBazelProxyDisabledByDefaultAndOnPublicRuntime(t *testing.T) {
	for _, role := range []string{"local", "team", "public"} {
		instance, cfg := gradleServer(t, role)
		if response := bazelRequest(instance, "GET", bazelCachePrefix+"cas/"+bazelTestHash, cfg.LocalToken, nil); response.Code != 404 {
			t.Fatalf("unconfigured %s native engine exposed: HTTP %d", role, response.Code)
		}
	}
}

func TestBazelProxyRejectsMalformedAndIncompleteBodiesBeforeBackend(t *testing.T) {
	var calls atomic.Int64
	instance, cfg := bazelServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	writer := gradleToken(t, cfg, cfg.CompatibilityID, "bazel", access.CapabilityWrite)
	for _, test := range []struct {
		name string
		body io.Reader
		size int64
		want int
	}{
		{"short", strings.NewReader("short"), 100, 400},
		{"dishonest zero length", strings.NewReader("extra"), 0, 413},
		{"terminal failure", &bazelTerminalFailure{body: "complete", failure: io.ErrUnexpectedEOF}, 8, 400},
		{"full buffer terminal failure", &bazelTerminalFailure{body: strings.Repeat("a", 32768), failure: io.ErrUnexpectedEOF}, 32768, 400},
		{"declared over bound", strings.NewReader("small"), cfg.BazelRemote.MaxBlobBytes + 1, 413},
		{"unknown over bound", strings.NewReader(strings.Repeat("a", int(cfg.BazelRemote.MaxBlobBytes)+1)), -1, 413},
		{"unknown complete", strings.NewReader("complete"), -1, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := calls.Load()
			request := httptest.NewRequest("PUT", bazelCachePrefix+"cas/"+bazelTestHash, test.body)
			request.ContentLength = test.size
			request.Header.Set("Authorization", "Bearer "+writer)
			response := httptest.NewRecorder()
			instance.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("HTTP %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
			if test.want != 200 && calls.Load() != before {
				t.Fatal("incomplete upload reached native engine")
			}
			staging, err := filepath.Glob(filepath.Join(cfg.DataDir, "staging", "bazel-upload-*"))
			if err != nil || len(staging) != 0 {
				t.Fatalf("upload staging leaked: %v %v", staging, err)
			}
		})
	}
	for _, path := range []string{
		bazelCachePrefix + "cas/" + strings.ToUpper(bazelTestHash), bazelCachePrefix + "cas/short",
		bazelCachePrefix + "status/" + bazelTestHash, bazelCachePrefix + "other/cas/" + bazelTestHash,
		bazelCachePrefix + "cas/" + bazelTestHash + "?project=another", bazelCachePrefix + "cas/%61" + bazelTestHash[1:],
	} {
		before := calls.Load()
		response := bazelRequest(instance, "PUT", path, writer, strings.NewReader("invalid"))
		if response.Code != 400 || calls.Load() != before {
			t.Fatalf("invalid path reached native engine: %q HTTP %d", path, response.Code)
		}
	}
	request := httptest.NewRequest("PUT", bazelCachePrefix+"cas/"+bazelTestHash, strings.NewReader("encoded"))
	request.Header.Set("Authorization", "Bearer "+writer)
	request.Header.Set("Content-Encoding", "zstd")
	response := httptest.NewRecorder()
	instance.Handler().ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatalf("unsupported request encoding: %d", response.Code)
	}
}

type bazelTerminalFailure struct {
	body    string
	failure error
}

func (reader *bazelTerminalFailure) Read(buffer []byte) (int, error) {
	written := copy(buffer, reader.body)
	reader.body = reader.body[written:]
	if len(reader.body) == 0 {
		return written, reader.failure
	}
	return written, nil
}

func TestBazelProxyBoundsBackendFailuresAndNeverFollowsRedirects(t *testing.T) {
	var destinationCalls atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
		want    int
	}{
		{"missing CAS", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }, 404},
		{"quota", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(507) }, 507},
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }, 502},
		{"oversized response", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Length", strconv.Itoa(2<<20)) }, 502},
		{"compressed response", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Encoding", "gzip") }, 502},
		{"timeout", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, 504},
	} {
		t.Run(test.name, func(t *testing.T) {
			instance, cfg := bazelServer(t, test.handler)
			if test.name == "timeout" {
				instance.config.BazelRemote.RequestTimeout = 20 * time.Millisecond
			}
			token := gradleToken(t, cfg, cfg.CompatibilityID, "bazel", access.CapabilityRead)
			response := bazelRequest(instance, "GET", bazelCachePrefix+"ac/"+bazelTestHash, token, nil)
			if response.Code != test.want {
				t.Fatalf("HTTP %d, want %d", response.Code, test.want)
			}
			if response.Header().Get("Location") != "" {
				t.Fatal("backend redirect escaped to native client")
			}
		})
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("native engine redirect was followed")
	}
	instance, cfg := bazelServer(t, http.NotFoundHandler())
	instance.bazelTransport.CloseIdleConnections()
	instance.config.BazelRemote.URL = "http://127.0.0.1:1"
	token := gradleToken(t, cfg, cfg.CompatibilityID, "bazel", access.CapabilityRead)
	if response := bazelRequest(instance, "GET", bazelCachePrefix+"ac/"+bazelTestHash, token, nil); response.Code != 502 {
		t.Fatalf("unavailable native engine: %d", response.Code)
	}
}

func TestBazelProxySlowUploadCannotExtendCompleteRequestDeadline(t *testing.T) {
	var calls atomic.Int64
	instance, cfg := bazelServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	instance.config.BazelRemote.RequestTimeout = 100 * time.Millisecond
	endpoint := httptest.NewServer(instance.Handler())
	defer endpoint.Close()
	connection, err := net.DialTimeout("tcp", strings.TrimPrefix(endpoint.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	token := gradleToken(t, cfg, cfg.CompatibilityID, "bazel", access.CapabilityWrite)
	started := time.Now()
	_, err = io.WriteString(connection, "PUT "+bazelCachePrefix+"cas/"+bazelTestHash+" HTTP/1.1\r\nHost: cache.example\r\nAuthorization: Bearer "+token+"\r\nContent-Length: 100\r\nConnection: close\r\n\r\nx")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(connection)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("one upload chunk extended the request deadline: %s", elapsed)
	}
	if calls.Load() != 0 {
		t.Fatal("incomplete slow upload reached native engine")
	}
}

func TestBazelRuntimeRejectsCopiedBindingAndGatewaySharedEngine(t *testing.T) {
	instance, cfg := bazelServer(t, http.NotFoundHandler())
	_ = instance.Close()
	copied := cfg
	copied.ProjectID = "copied-project"
	copied.DataDir = t.TempDir()
	if runtime, err := New(context.Background(), copied); err == nil {
		_ = runtime.Close()
		t.Fatal("runtime accepted a copied foreign native engine binding")
	}
	other := gatewayTestConfig(t, "other-bazel")
	other.BazelRemote = &config.BazelRemoteConfig{
		ProjectID: other.ProjectID, CompatibilityID: other.CompatibilityID, URL: cfg.BazelRemote.URL,
		MaxBlobBytes: 1 << 20, RequestTimeout: time.Second,
	}
	gateway, err := NewProjectGateway(context.Background(), ProjectGatewayConfig{
		Origin: "https://cache.example", Projects: map[string]config.Config{"bazel": cfg, "other-bazel": other},
	})
	if err == nil {
		_ = gateway.Close()
		t.Fatal("gateway accepted one native engine for two private projects")
	}
	if _, err := os.Stat(filepath.Join(copied.DataDir, "metadata.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("copied binding created runtime storage before rejection")
	}
}
