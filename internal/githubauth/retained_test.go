package githubauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRetainedRunVerificationUsesFixedAPIWithoutRedirect(t *testing.T) {
	identity := ActionsIdentity{Repository: "acme/widget", RepositoryID: "123", RunID: "234", RunAttempt: "2", EventName: "pull_request"}
	var leaked atomic.Bool
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer foreign.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, foreign.URL, http.StatusFound) }))
	defer redirect.Close()
	if VerifySameRepositoryRun(context.Background(), redirect.URL, "ephemeral", identity) == nil {
		t.Fatal("redirect followed")
	}
	if leaked.Load() {
		t.Fatal("job credential sent to redirected origin")
	}
	for _, endpoint := range []string{"http://example.com", "https://user:secret@api.github.com", "https://api.github.com?override=1", "https://api.github.com#fragment"} {
		if VerifySameRepositoryRun(context.Background(), endpoint, "ephemeral", identity) == nil {
			t.Fatalf("invalid API origin accepted: %s", endpoint)
		}
	}
}

func TestRetainedRunVerificationRejectsForeignOrIncompleteMetadata(t *testing.T) {
	identity := ActionsIdentity{Repository: "acme/widget", RepositoryID: "123", RunID: "234", RunAttempt: "2", EventName: "pull_request"}
	for _, test := range []struct {
		name                    string
		id, attempt, repo, head int
		event                   string
		want                    bool
	}{
		{"same repository", 234, 2, 123, 123, "pull_request", true},
		{"foreign run", 999, 2, 123, 123, "pull_request", false},
		{"foreign attempt", 234, 3, 123, 123, "pull_request", false},
		{"foreign repository", 234, 2, 999, 999, "pull_request", false},
		{"fork head", 234, 2, 123, 999, "pull_request", false},
		{"missing head", 234, 2, 123, 0, "pull_request", false},
		{"foreign event", 234, 2, 123, 123, "push", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": test.id, "run_attempt": test.attempt, "event": test.event, "repository": map[string]int{"id": test.repo}, "head_repository": map[string]int{"id": test.head}})
			}))
			defer endpoint.Close()
			err := VerifySameRepositoryRun(context.Background(), endpoint.URL, "ephemeral", identity)
			if (err == nil) != test.want {
				t.Fatalf("verification result=%v", err)
			}
		})
	}
}
