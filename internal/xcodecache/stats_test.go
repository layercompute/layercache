package xcodecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
)

func rejectionCounts(stats Stats) [9]uint64 {
	return [9]uint64{stats.ActionLookupMisses, stats.ActionValueRejects, stats.ActionReferenceRejects, stats.GraphObjectMisses, stats.GraphObjectCorrupt, stats.GraphReferenceRejects, stats.GraphBudgetRejects, stats.GraphAdmissionRejects, stats.GraphDeadlineRejects}
}

func TestActionMissCountersIdentifyValidationStageWithoutPromisingOutputs(t *testing.T) {
	for _, test := range []struct {
		name    string
		index   int
		prepare func(*testing.T, *Service, []byte)
	}{
		{name: "action absent", index: 0, prepare: func(*testing.T, *Service, []byte) {}},
		{name: "action encoding", index: 1, prepare: func(t *testing.T, s *Service, key []byte) {
			id := sha256.Sum256(key)
			if err := s.put(context.Background(), "kv", id[:], []byte{0xff}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "action references", index: 2, prepare: func(t *testing.T, s *Service, key []byte) {
			putDiagnosticAction(t, s, key, &kv.Value{Entries: map[string][]byte{"value": {0xff}}})
		}},
		{name: "object absent", index: 3, prepare: func(t *testing.T, s *Service, key []byte) {
			putDiagnosticAction(t, s, key, &kv.Value{Entries: map[string][]byte{"CASID": bytes.Repeat([]byte{1}, 32)}})
		}},
		{name: "object corrupt", index: 4, prepare: func(t *testing.T, s *Service, key []byte) {
			id := bytes.Repeat([]byte{2}, 32)
			if err := s.put(context.Background(), "cas", id, []byte("not the claimed object")); err != nil {
				t.Fatal(err)
			}
			putDiagnosticAction(t, s, key, &kv.Value{Entries: map[string][]byte{"CASID": id}})
		}},
		{name: "nested native references", index: 5, prepare: func(t *testing.T, s *Service, key []byte) {
			saved, err := s.Save(context.Background(), &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte{0xff})}})
			if err != nil {
				t.Fatal(err)
			}
			putDiagnosticAction(t, s, key, &kv.Value{Entries: map[string][]byte{"value": nativeTestNode([]byte("result"), GraphReference{ID: saved.GetCasId().Id, NativeNode: true})}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := openService(t, t.TempDir(), "project", "toolchain")
			key := []byte("same native action key")
			test.prepare(t, s, key)
			response, err := s.GetValue(context.Background(), &kv.GetValueRequest{Key: key})
			if err != nil || response.GetOutcome() != kv.GetValueResponse_KEY_NOT_FOUND {
				t.Fatal("rejected graph promised a hit", response, err)
			}
			expected := [9]uint64{}
			expected[test.index] = 1
			stats := s.Stats()
			if rejectionCounts(stats) != expected || stats.ActionReads != 1 || stats.ActionHits != 0 || stats.CacheErrors != 0 {
				t.Fatalf("miss stage was ambiguous: %+v", stats)
			}
			if test.index >= 3 && stats.GraphChecks != 1 {
				t.Fatal("graph stage was not actually reached", stats)
			}
		})
	}
}

func putDiagnosticAction(t *testing.T, s *Service, key []byte, value *kv.Value) {
	t.Helper()
	response, err := s.PutValue(context.Background(), &kv.PutValueRequest{Key: key, Value: value})
	if err != nil || response.GetError() != nil {
		t.Fatal("prepare native action", response, err)
	}
}

func TestGraphBudgetDeadlineAndAdmissionHaveDistinctCounters(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		s := openService(t, t.TempDir(), "project", "toolchain")
		roots := make([]GraphReference, maximumGraphObjects+1)
		for i := range roots {
			roots[i].ID = make([]byte, 32)
			binary.BigEndian.PutUint64(roots[i].ID, uint64(i))
		}
		err := s.retainGraph(context.Background(), []byte("budget"), roots)
		if !errors.Is(err, artifact.ErrQuota) || rejectionCounts(s.Stats()) != [9]uint64{0, 0, 0, 0, 0, 0, 1, 0, 0} {
			t.Fatal("graph budget rejection was not isolated", s.Stats(), err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		s := openService(t, t.TempDir(), "project", "toolchain")
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		err := s.retainGraph(ctx, []byte("deadline"), []GraphReference{{ID: make([]byte, 32)}})
		if !errors.Is(err, context.DeadlineExceeded) || rejectionCounts(s.Stats()) != [9]uint64{0, 0, 0, 0, 0, 0, 0, 0, 1} {
			t.Fatal("graph deadline rejection was not isolated", s.Stats(), err)
		}
	})
	t.Run("admission preserves original lease", func(t *testing.T) {
		s := openService(t, t.TempDir(), "project", "toolchain")
		ctx := context.Background()
		first, err := s.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("first immutable output"))}})
		if err != nil {
			t.Fatal(err)
		}
		second, err := s.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("second immutable output"))}})
		if err != nil {
			t.Fatal(err)
		}
		identity := []byte("one immutable graph identity")
		if err := s.retainGraph(ctx, identity, []GraphReference{{ID: first.GetCasId().Id}}); err != nil {
			t.Fatal(err)
		}
		err = s.retainGraph(ctx, identity, []GraphReference{{ID: second.GetCasId().Id}})
		if !errors.Is(err, artifact.ErrConflict) || rejectionCounts(s.Stats()) != [9]uint64{0, 0, 0, 0, 0, 0, 0, 1, 0} {
			t.Fatal("graph admission rejection was not isolated", s.Stats(), err)
		}
		if err := s.store.Delete(ctx, s.key("cas", first.GetCasId().Id)); !errors.Is(err, artifact.ErrPinned) {
			t.Fatal("admission diagnostic weakened the accepted lease", err)
		}
	})
}

func TestDiagnosticStatsContainOnlyFixedNumericCounters(t *testing.T) {
	root := t.TempDir()
	s := openService(t, root, "private-project", "private-toolchain")
	putDiagnosticAction(t, s, []byte("private-native-key"), &kv.Value{Entries: map[string][]byte{"value": []byte("private-payload")}})
	s.GetValue(context.Background(), &kv.GetValueRequest{Key: []byte("private-native-key")})
	encoded, err := json.Marshal(s.Stats())
	if err != nil {
		t.Fatal(err)
	}
	var counters map[string]uint64
	if err := json.Unmarshal(encoded, &counters); err != nil {
		t.Fatal("statistics contain nonnumeric context", err)
	}
	for _, value := range []string{root, "private-project", "private-toolchain", "private-native-key", "private-payload"} {
		if bytes.Contains(encoded, []byte(value)) {
			t.Fatal("diagnostics disclose context")
		}
	}
	if counters["actionReferenceRejects"] != 1 {
		t.Fatal("fixed diagnostic counter is absent")
	}
}
