package xcodecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// assertGraphRejectAction wraps root in a minimal native action, saves it and
// confirms both that the action is a miss and that it was rejected for
// exactly wantReason.
func assertGraphRejectAction(t *testing.T, service *Service, name string, root []byte, wantReason graphRejectReason) {
	t.Helper()
	ctx := context.Background()
	value := nativeTestNode([]byte("compile result"), GraphReference{ID: root, NativeNode: true})
	key := []byte(name)
	if response, err := service.PutValue(ctx, &kv.PutValueRequest{Key: key, Value: &kv.Value{Entries: map[string][]byte{"value": value}}}); err != nil || response.GetError() != nil {
		t.Fatalf("save action before graph rejection: %v %v", response, err)
	}
	miss, err := service.GetValue(ctx, &kv.GetValueRequest{Key: key})
	if err != nil || miss.GetOutcome() != kv.GetValueResponse_KEY_NOT_FOUND {
		t.Fatalf("expected a miss for reason %q: %v %v", wantReason, miss, err)
	}
	stats := service.Stats()
	if stats.GraphReferenceRejects == 0 {
		t.Fatalf("graphReferenceRejects did not increment for reason %q: %+v", wantReason, stats)
	}
	if stats.GraphRejectReasons[string(wantReason)] == 0 {
		t.Fatalf("expected reason %q recorded, got %+v", wantReason, stats.GraphRejectReasons)
	}
}

func TestGraphRejectReasonsIdentifyEachRule(t *testing.T) {
	ctx := context.Background()

	t.Run("inline leaf count exceeded", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		var marker []byte
		for index := 1; index <= 1025; index++ {
			marker = protowire.AppendVarint(marker, uint64(index))
		}
		node := indexedInlineLeafNode(marker, make([][]byte, 1025)...)
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "leaf count", root.GetCasId().Id, reasonInlineLeafCountExceeded)
	})

	t.Run("exactly 1,024 leaves still passes", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		var marker []byte
		for index := 1; index <= 1024; index++ {
			marker = protowire.AppendVarint(marker, uint64(index))
		}
		node := indexedInlineLeafNode(marker, make([][]byte, 1024)...)
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		value := nativeTestNode([]byte("compile result"), GraphReference{ID: root.GetCasId().Id, NativeNode: true})
		key := []byte("boundary")
		if _, err := service.PutValue(ctx, &kv.PutValueRequest{Key: key, Value: &kv.Value{Entries: map[string][]byte{"value": value}}}); err != nil {
			t.Fatal(err)
		}
		hit, err := service.GetValue(ctx, &kv.GetValueRequest{Key: key})
		if err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS {
			t.Fatalf("1,024 leaves must still be accepted: %v %v", hit, err)
		}
	})

	t.Run("inline marker with envelope references", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		child, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("existing envelope child"))}})
		if err != nil {
			t.Fatal(err)
		}
		node := indexedInlineLeafNode([]byte{1}, []byte("leaf"))
		root, err := service.Put(ctx, &cas.CASPutRequest{Data: &cas.CASObject{Blob: blob(node), References: []*cas.CASDataID{child.GetCasId()}}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "marker with refs", root.GetCasId().Id, reasonInlineMarkerWithEnvelopeReferences)
	})

	t.Run("unknown field number", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		node := nativeBytesField(5, []byte("x"))
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "unknown field", root.GetCasId().Id, reasonUnknownFieldNumber)
	})

	t.Run("wrong wire type", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		// Field 1 wraps a varint instead of the required bytes-typed marker.
		node := nativeBytesField(1, protowire.AppendVarint(protowire.AppendTag(nil, 2, protowire.VarintType), 1))
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "wrong wire type", root.GetCasId().Id, reasonWrongWireType)
	})

	t.Run("trailing bytes", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		// The marker union has an extra field after its ordinal list.
		node := nativeBytesField(1, append(nativeBytesField(2, []byte{1}), nativeBytesField(1, nil)...))
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "trailing bytes", root.GetCasId().Id, reasonTrailingBytes)
	})

	t.Run("duplicate field", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		descriptor := append(append([]byte(nil), nativeBytesField(1, make([]byte, 65))...), nativeBytesField(1, make([]byte, 65))...)
		node := nativeBytesField(2, nativeBytesField(2, descriptor))
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "duplicate field", root.GetCasId().Id, reasonDuplicateField)
	})

	t.Run("missing required field", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		// A reference union with only the native digest, never the remote ID.
		descriptor := nativeBytesField(1, make([]byte, 65))
		node := nativeBytesField(2, nativeBytesField(2, descriptor))
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "missing required field", root.GetCasId().Id, reasonMissingRequiredField)
	})

	t.Run("invalid flag value", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		descriptor := append(nativeBytesField(1, make([]byte, 65)), nativeBytesField(2, bytes.Repeat([]byte{1}, 32))...)
		descriptor = protowire.AppendVarint(protowire.AppendTag(descriptor, 3, protowire.VarintType), 2)
		node := nativeBytesField(2, nativeBytesField(2, descriptor))
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "invalid flag value", root.GetCasId().Id, reasonInvalidFlagValue)
	})

	t.Run("malformed varint", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		node := []byte{0x0a, 0x05}
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "malformed varint", root.GetCasId().Id, reasonMalformedVarint)
	})

	t.Run("invalid ordinal index", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		node := indexedInlineLeafNode([]byte{0, 1, 2}, nil, nil, nil)
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "zero based index", root.GetCasId().Id, reasonInvalidOrdinalIndex)
	})

	t.Run("literal count mismatch", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		node := indexedInlineLeafNode([]byte{1})
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "single marker without literal", root.GetCasId().Id, reasonLiteralCountMismatch)
	})

	t.Run("native blob not inline", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		object := &cas.CASObject{Blob: &cas.CASBytes{Contents: &cas.CASBytes_FilePath{FilePath: "/tmp/does-not-matter"}}}
		encoded, err := proto.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(encoded)
		if err := service.put(ctx, "cas", digest[:], encoded); err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "not inline", digest[:], reasonNativeBlobNotInline)
	})

	t.Run("invalid reference id", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		roots := []GraphReference{{ID: bytes.Repeat([]byte{1}, 31)}}
		err := service.retainGraph(ctx, []byte("short id"), roots)
		var reject *graphRejectError
		if !errors.As(err, &reject) || reject.reason != reasonInvalidReferenceId {
			t.Fatalf("expected invalidReferenceId reason, got %v", err)
		}
		if !errors.Is(err, artifact.ErrCorrupt) {
			t.Fatalf("typed reject error lost artifact.ErrCorrupt: %v", err)
		}
		stats := service.Stats()
		if stats.GraphReferenceRejects != 1 || stats.GraphRejectReasons[string(reasonInvalidReferenceId)] != 1 {
			t.Fatalf("counters not recorded: %+v", stats)
		}
	})

	t.Run("graph object limit exceeded", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		target := bytes.Repeat([]byte{9}, 32)
		refs := make([]GraphReference, maximumGraphObjects+1)
		for i := range refs {
			refs[i] = GraphReference{ID: target}
		}
		node := nativeTestNode([]byte("result"), refs...)
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
		if err != nil {
			t.Fatal(err)
		}
		assertGraphRejectAction(t, service, "too many references", root.GetCasId().Id, reasonGraphObjectLimitExceeded)
	})
}

// assertActionRejectReason puts a raw kv.Value under key and confirms the
// action is a miss rejected for exactly wantReason, tallied separately from
// any graph-level reject.
func assertActionRejectReason(t *testing.T, service *Service, name string, entries map[string][]byte, wantReason graphRejectReason) {
	t.Helper()
	ctx := context.Background()
	key := []byte(name)
	if response, err := service.PutValue(ctx, &kv.PutValueRequest{Key: key, Value: &kv.Value{Entries: entries}}); err != nil || response.GetError() != nil {
		t.Fatalf("save action before action-level rejection: %v %v", response, err)
	}
	miss, err := service.GetValue(ctx, &kv.GetValueRequest{Key: key})
	if err != nil || miss.GetOutcome() != kv.GetValueResponse_KEY_NOT_FOUND {
		t.Fatalf("expected a miss for reason %q: %v %v", wantReason, miss, err)
	}
	stats := service.Stats()
	if stats.ActionReferenceRejects == 0 {
		t.Fatalf("actionReferenceRejects did not increment for reason %q: %+v", wantReason, stats)
	}
	if stats.ActionRejectReasons[string(wantReason)] == 0 {
		t.Fatalf("expected action reason %q recorded, got %+v", wantReason, stats.ActionRejectReasons)
	}
	if stats.GraphReferenceRejects != 0 || len(stats.GraphRejectReasons) != 0 {
		t.Fatalf("action-level reject leaked into graph counters: %+v", stats)
	}
}

func TestActionRejectReasonsAreRecordedSeparatelyFromGraphReasons(t *testing.T) {
	t.Run("casid with native value", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		entries := map[string][]byte{"CASID": bytes.Repeat([]byte{1}, 32), "value": []byte("x")}
		assertActionRejectReason(t, service, "casid with native value", entries, reasonCasidWithNativeValue)
	})

	t.Run("native value with other entries", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		entries := map[string][]byte{"value": []byte("x"), "extra": []byte("y")}
		assertActionRejectReason(t, service, "native value with other entries", entries, reasonNativeValueWithOtherEntries)
	})

	t.Run("empty action outputs", func(t *testing.T) {
		service := openService(t, t.TempDir(), "project", "toolchain")
		assertActionRejectReason(t, service, "empty action outputs", map[string][]byte{}, reasonEmptyActionOutputs)
	})
}

func TestGraphRejectSamplesAreCappedAndNeverLeakLiterals(t *testing.T) {
	ctx := context.Background()
	service := openService(t, t.TempDir(), "project", "toolchain")

	secretLiteral := []byte("super-secret-literal-content-marker")

	var oversizedMarker []byte
	for index := 1; index <= 1025; index++ {
		oversizedMarker = protowire.AppendVarint(oversizedMarker, uint64(index))
	}

	nodes := []struct {
		name string
		node []byte
	}{
		{"leaf count", indexedInlineLeafNode(oversizedMarker, make([][]byte, 1025)...)},
		{"duplicate ordinal", indexedInlineLeafNode([]byte{1, 1, 3}, secretLiteral, nil, nil)},
		{"missing literal", indexedInlineLeafNode([]byte{2, 3, 1}, secretLiteral, nil)},
		{"unknown top field", append(append([]byte(nil), indexedInlineLeafNode([]byte{2, 3, 1}, secretLiteral, nil, nil)...), nativeBytesField(5, []byte("new references"))...)},
		{"malformed", []byte{0x0a, 0x05}},
	}

	for i, trial := range nodes {
		root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(trial.node)}})
		if err != nil {
			t.Fatal(err)
		}
		roots := []GraphReference{{ID: root.GetCasId().Id, NativeNode: true}}
		if err := service.retainGraph(ctx, []byte{byte(i)}, roots); err == nil {
			t.Fatalf("trial %q unexpectedly admitted", trial.name)
		}
	}

	stats := service.Stats()
	if len(stats.GraphRejectSamples) > maxGraphRejectSamples {
		t.Fatalf("too many samples retained: %d", len(stats.GraphRejectSamples))
	}
	seen := map[string]bool{}
	for _, sample := range stats.GraphRejectSamples {
		if seen[sample.Reason] {
			t.Fatalf("more than one sample retained for reason %q", sample.Reason)
		}
		seen[sample.Reason] = true
	}

	encoded, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, secretLiteral) {
		t.Fatal("encoded statistics leaked a literal byte sequence")
	}
}
