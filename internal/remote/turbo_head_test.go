package remote_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/layercache/layercache/internal/remote"
)

func TestTurboHeadRequiresCompleteMetadataAndScopedAuthorization(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		status     int
		digest     string
		length     string
		wantError  error
		additional bool
	}{
		{name: "valid", status: 200, digest: "sha256:" + strings.Repeat("a", 64), length: "17"},
		{name: "valid empty archive", status: 200, digest: strings.Repeat("b", 64), length: "0"},
		{name: "missing digest", status: 200, length: "17", wantError: remote.ErrMetadata},
		{name: "short digest", status: 200, digest: "sha256:abc", length: "17", wantError: remote.ErrMetadata},
		{name: "uppercase digest", status: 200, digest: strings.Repeat("A", 64), length: "17", wantError: remote.ErrMetadata},
		{name: "nonhex digest", status: 200, digest: strings.Repeat("g", 64), length: "17", wantError: remote.ErrMetadata},
		{name: "duplicate digest", status: 200, digest: strings.Repeat("a", 64), length: "17", wantError: remote.ErrMetadata, additional: true},
		{name: "missing length", status: 200, digest: strings.Repeat("a", 64), wantError: remote.ErrMetadata},
		{name: "missing", status: 404, wantError: remote.ErrMiss},
		{name: "unauthorized", status: 401, wantError: remote.ErrUnauthorized},
		{name: "forbidden", status: 403, wantError: remote.ErrUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodHead || request.URL.Path != "/v8/artifacts/task-hash" || request.Header.Get("Authorization") != "Bearer team-secret" || request.Header.Get("X-LayerCache-Compatibility") != "linux-amd64-node@24-schema1" {
					t.Errorf("unexpected HEAD request method, path or scoped headers")
				}
				if test.digest != "" {
					writer.Header().Set("x-layercache-digest", test.digest)
				}
				if test.additional {
					writer.Header().Add("x-layercache-digest", test.digest)
				}
				if test.length != "" {
					writer.Header().Set("Content-Length", test.length)
				}
				writer.WriteHeader(test.status)
			}))
			defer endpoint.Close()
			client, err := remote.NewTurboClientForCompatibility(endpoint.URL, "team-secret", "linux-amd64-node@24-schema1")
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := client.Head(context.Background(), "task-hash")
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Head error = %v, want %v", err, test.wantError)
			}
			if err == nil && (len(metadata.Digest) != 64 || metadata.Size < 0) {
				t.Fatalf("invalid accepted metadata: %+v", metadata)
			}
		})
	}
}

func TestTurboHeadRefusesCrossOriginRedirectWithoutLeakingToken(t *testing.T) {
	t.Parallel()
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/artifact", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client, err := remote.NewTurboClient(source.URL, "private-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Head(context.Background(), "task"); err == nil || !strings.Contains(err.Error(), "changed origin") {
		t.Fatalf("Head redirect error = %v", err)
	}
	if received.Load() != 0 {
		t.Fatal("redirect destination received a credentialed request")
	}
}
