package remote_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/remote"
)

func TestTurboPutClassifiesImmutableConflictWithoutAdditionalRequests(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status int
		want   error
	}{
		{"conflict", http.StatusConflict, remote.ErrConflict},
		{"unauthorized", http.StatusUnauthorized, remote.ErrUnauthorized},
		{"forbidden", http.StatusForbidden, remote.ErrUnauthorized},
		{"server error", http.StatusInternalServerError, nil},
		{"unavailable", http.StatusServiceUnavailable, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			const compatibility = "linux-amd64-node@24-tests-v1"
			const contents = "successful-result"
			var requests atomic.Int64
			endpoint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.Method != http.MethodPut || request.URL.Path != "/v8/artifacts/task-hash" || request.Header.Get("Authorization") != "Bearer team-writer" || request.Header.Get("X-LayerCache-Compatibility") != compatibility {
					t.Errorf("unexpected PUT request method, path or scoped headers")
				}
				body, err := io.ReadAll(request.Body)
				if err != nil || string(body) != contents || request.ContentLength != int64(len(contents)) {
					t.Errorf("PUT body = %q, length=%d, error=%v", body, request.ContentLength, err)
				}
				writer.WriteHeader(test.status)
			}))
			defer endpoint.Close()
			client, err := remote.NewTurboClientForCompatibility(endpoint.URL, "team-writer", compatibility)
			if err != nil {
				t.Fatal(err)
			}
			err = client.Put(context.Background(), "task-hash", artifact.Entry{Size: int64(len(contents))}, strings.NewReader(contents))
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("PUT error = %v, want %v", err, test.want)
				}
			} else if err == nil || errors.Is(err, remote.ErrConflict) || errors.Is(err, remote.ErrUnauthorized) {
				t.Fatalf("server failure incorrectly classified: %v", err)
			}
			if requests.Load() != 1 {
				t.Fatalf("PUT made %d requests, want no automatic confirmation", requests.Load())
			}
		})
	}
}
