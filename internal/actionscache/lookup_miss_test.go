package actionscache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
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

func TestArchiveDownloadReportsFullVerificationStaging(t *testing.T) {
	handler, err := NewHandler(Config{
		Repository: "acme/phone", Ref: "refs/heads/main", DefaultRef: "refs/heads/main", Compatibility: "linux-x64",
	}, openFailureStorage{StorageIndex: NewMemoryStorage(), err: fmt.Errorf("stage archive: %w", artifact.ErrQuota)})
	if err != nil {
		t.Fatal(err)
	}
	lookup := httptest.NewRecorder()
	handler.ServeHTTP(lookup, httptest.NewRequest(http.MethodGet, "/_apis/artifactcache/cache?keys=handoff&version=v1", nil))
	var found struct {
		ArchiveLocation string `json:"archiveLocation"`
	}
	if lookup.Code != http.StatusOK || json.Unmarshal(lookup.Body.Bytes(), &found) != nil {
		t.Fatalf("lookup HTTP %d: %s", lookup.Code, lookup.Body.String())
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, found.ArchiveLocation, nil))
	if response.Code != http.StatusInsufficientStorage || response.Header().Get("x-layercache-degraded") != "verification-staging-full" {
		t.Fatalf("full staging HTTP %d, degraded=%q, body=%q", response.Code, response.Header().Get("x-layercache-degraded"), response.Body.String())
	}
}

type openFailureStorage struct {
	StorageIndex
	err error
}

func (storage openFailureStorage) Lookup(_ context.Context, request LookupRequest) (LookupResult, error) {
	return LookupResult{
		Entry: Entry{ID: 1, Key: "handoff", Version: "v1", Ref: request.Scope.Ref, Size: 4},
		Match: MatchExact, RequestedKey: "handoff", RefScope: RefScopeCurrent,
	}, nil
}

func (storage openFailureStorage) Open(context.Context, OpenRequest) (Archive, error) {
	return Archive{}, storage.err
}
