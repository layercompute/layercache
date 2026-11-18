package artifact_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/xcodecache"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This adapter integration lives beside the test-only filesystem probe so it
// exercises the real store without depending on the host's available space.
func TestXcodeWritesWithExplicitAndAutomaticReserves(t *testing.T) {
	const gib = int64(1 << 30)
	for _, test := range []struct {
		name       string
		configured int64
		reserve    int64
		refuse     bool
	}{
		{"automatic", 0, 23 * gib, true},
		{"explicit", 6 * gib, 6 * gib, false},
		{"explicit below floor", 1, 5 * gib, false},
		{"explicit above available", 30 * gib, 30 * gib, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := artifact.Open(ctx, t.TempDir(), 1<<20, test.configured)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			artifact.SetFilesystemSpaceForTest(store, 20*gib, 460*gib, 4096)
			service, err := xcodecache.New(store, "reserve-project", "reserve-toolchain")
			if err != nil {
				t.Fatal(err)
			}
			blob := &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: []byte("compiled output")}}
			_, err = service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob}})
			if test.refuse {
				if status.Code(err) != codes.ResourceExhausted {
					t.Fatalf("Save error = %v, want reserve refusal", err)
				}
			} else if err != nil {
				t.Fatalf("Save: %v", err)
			}
			assertXcodeReserveStats(t, service, test.reserve, test.refuse, 1, 0)

			_, err = service.Put(ctx, &cas.CASPutRequest{Data: &cas.CASObject{Blob: blob}})
			if test.refuse {
				if status.Code(err) != codes.ResourceExhausted {
					t.Fatalf("Put error = %v, want reserve refusal", err)
				}
			} else if err != nil {
				t.Fatalf("Put: %v", err)
			}
			assertXcodeReserveStats(t, service, test.reserve, test.refuse, 2, 0)

			response, err := service.PutValue(ctx, &kv.PutValueRequest{
				Key: []byte("compile action"), Value: &kv.Value{Entries: map[string][]byte{"metadata": []byte("compiled")}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.refuse {
				if !strings.Contains(response.GetError().GetDescription(), artifact.ErrMinFreeSpace.Error()) {
					t.Fatalf("PutValue = %v, want reserve refusal", response)
				}
			} else if response.GetError() != nil {
				t.Fatalf("PutValue: %v", response.GetError())
			}
			assertXcodeReserveStats(t, service, test.reserve, test.refuse, 2, 1)

			// Statistics retain the startup reserve even if the probe changes.
			artifact.SetFilesystemSpaceForTest(store, 20*gib, 1000*gib, 4096)
			encoded, err := json.Marshal(service.Stats())
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]int64
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if fields["minFreeBytes"] != test.reserve {
				t.Fatalf("JSON reserve = %s, want %d bytes", encoded, test.reserve)
			}
			if _, ok := fields["minFreeSpaceRefusals"]; !ok {
				t.Fatalf("JSON missing minFreeSpaceRefusals: %s", encoded)
			}
		})
	}
}

func assertXcodeReserveStats(t *testing.T, service *xcodecache.Service, reserve int64, refused bool, casWrites, actionWrites uint64) {
	t.Helper()
	stats := service.Stats()
	var refusals uint64
	if refused {
		refusals = casWrites + actionWrites
		casWrites, actionWrites = 0, 0
	}
	if stats.MinFreeBytes != reserve || stats.MinFreeSpaceRefusals != refusals || stats.CacheErrors != refusals ||
		stats.CASWrites != casWrites || stats.ActionWrites != actionWrites {
		t.Fatalf("write statistics = %+v, want reserve %d, refusals/errors %d, CAS/action writes %d/%d", stats, reserve, refusals, casWrites, actionWrites)
	}
}
