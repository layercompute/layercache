package actionscache

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLookupMissReasonIsBoundedAndPreservesMissStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		reason string
	}{
		{"generic", ErrNotFound, "not-found"},
		{"no-match", ErrLookupNoMatch, "no-matching-entry"},
		{"removed", ErrLookupEntryRemoved, "entry-removed"},
		{"integrity", ErrLookupIntegrity, "integrity-rejected"},
		{"outage", errors.Join(ErrNotFound, errRemoteDegraded), "remote-unavailable"},
		{"private-cause", fmt.Errorf("private backend detail: %w", ErrNotFound), "not-found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, err := NewHandler(Config{
				Repository: "acme/phone", Ref: "refs/heads/main", DefaultRef: "refs/heads/main", Compatibility: "linux-x64",
			}, lookupMissStorage{StorageIndex: NewMemoryStorage(), err: test.err})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/_apis/artifactcache/cache?keys=handoff&version=v1", nil))
			if response.Code != http.StatusNoContent || response.Header().Get("X-LayerCache-Miss-Reason") != test.reason || response.Body.Len() != 0 {
				t.Fatalf("miss HTTP %d, reason=%q, body=%q", response.Code, response.Header().Get("X-LayerCache-Miss-Reason"), response.Body.String())
			}
		})
	}
}

type lookupMissStorage struct {
	StorageIndex
	err error
}

func (storage lookupMissStorage) Lookup(context.Context, LookupRequest) (LookupResult, error) {
	return LookupResult{}, storage.err
}
