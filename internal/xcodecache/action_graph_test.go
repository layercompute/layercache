package xcodecache

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/protobuf/encoding/protowire"
)

func nativeBytesField(number protowire.Number, data []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, number, protowire.BytesType), data)
}

func nativeTestNode(data []byte, refs ...GraphReference) []byte {
	node := nativeBytesField(1, nativeBytesField(1, data))
	for _, ref := range refs {
		descriptor := append(nativeBytesField(1, make([]byte, 65)), nativeBytesField(2, ref.ID)...)
		if ref.NativeNode {
			descriptor = protowire.AppendVarint(protowire.AppendTag(descriptor, 3, protowire.VarintType), 1)
		}
		node = append(node, nativeBytesField(2, nativeBytesField(2, descriptor))...)
	}
	return node
}

func TestNativeActionReferencesMatchRecordedXcodeWireValue(t *testing.T) {
	// Native Xcode 26.6 action value from the public mixed-language QA fixture.
	// Its inline empty object/schema are not external CAS reads. The external
	// descriptor contains both the native hash and the adapter's remote CAS ID.
	data, err := hex.DecodeString("0a040a02020012020a00126712650a41001069f2c127af486099b1a76b33c83f9894eccced63bd62d17f205c11f084879798c41f9eea23e073e601d47f49de90c604d4cfc17610ebbe8e5bb36b026ec718122052c50e45ac15d306df121ae43cfe469d14b3ff5927c21744e567d2ba8afa4400122b0a296c6c766d3a3a6361733a3a736368656d613a3a636f6d70696c655f6a6f625f726573756c743a3a7631")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := ActionReferences(&kv.Value{Entries: map[string][]byte{"value": data}})
	if err != nil || len(refs) != 1 || refs[0].NativeNode || hex.EncodeToString(refs[0].ID) != "52c50e45ac15d306df121ae43cfe469d14b3ff5927c21744e567d2ba8afa4400" {
		t.Fatalf("native wire references: %+v %v", refs, err)
	}
	// The retained compiler also emits this multi-chunk encoded-node shape.
	nested := append(nativeBytesField(1, nativeBytesField(2, []byte{1})), nativeBytesField(1, nativeBytesField(1, []byte("/fixture/main.m")))...)
	if refs, err := NativeNodeReferences(nested); err != nil || len(refs) != 0 {
		t.Fatalf("native encoded leaf: %+v %v", refs, err)
	}
}

func TestNativeActionGraphRetainsTransitiveOutputsAndUpgradesSharedInterpretation(t *testing.T) {
	ctx := context.Background()
	service := openService(t, t.TempDir(), "project", "toolchain")
	leaf, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("compiled output"))}})
	if err != nil {
		t.Fatal(err)
	}
	node, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(nativeTestNode([]byte("manifest"), GraphReference{ID: leaf.GetCasId().Id}))}})
	if err != nil {
		t.Fatal(err)
	}
	// The stack visits the raw interpretation first. A later native-node
	// interpretation of the same immutable object must still discover the leaf.
	value := nativeTestNode([]byte("result"), GraphReference{ID: node.GetCasId().Id, NativeNode: true}, GraphReference{ID: node.GetCasId().Id})
	if _, err := service.PutValue(ctx, &kv.PutValueRequest{Key: []byte("action"), Value: &kv.Value{Entries: map[string][]byte{"value": value}}}); err != nil {
		t.Fatal(err)
	}
	hit, err := service.GetValue(ctx, &kv.GetValueRequest{Key: []byte("action")})
	if err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS || !bytes.Equal(hit.GetValue().Entries["value"], value) {
		t.Fatalf("native graph hit: %v %v", hit, err)
	}
	if err := service.store.Delete(ctx, service.key("cas", leaf.GetCasId().Id)); !errors.Is(err, artifact.ErrPinned) {
		t.Fatalf("native descendant not retained: %v", err)
	}
	if stats := service.Stats(); stats.GraphChecks != 1 || stats.GraphObjectsChecked != 2 || stats.CASReads != 0 {
		t.Fatalf("native graph verification was skipped or inflated reads: %+v", stats)
	}
}

func TestNativeActionGraphRejectsMissingOutputsAndUnsupportedWireShapes(t *testing.T) {
	service := openService(t, t.TempDir(), "project", "toolchain")
	valid := nativeTestNode([]byte("result"), GraphReference{ID: bytes.Repeat([]byte{1}, 32)})
	for name, value := range map[string][]byte{
		"missing output": valid,
		"unknown field":  append(append([]byte(nil), valid...), nativeBytesField(5, []byte("new references"))...),
		"truncated":      valid[:len(valid)-1],
		"empty node":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			key := []byte(name)
			if _, err := service.PutValue(ctx, &kv.PutValueRequest{Key: key, Value: &kv.Value{Entries: map[string][]byte{"value": value}}}); err != nil {
				t.Fatal(err)
			}
			response, err := service.GetValue(ctx, &kv.GetValueRequest{Key: key})
			if err != nil || response.GetOutcome() != kv.GetValueResponse_KEY_NOT_FOUND {
				t.Fatalf("unrestorable native action promised: %v %v", response, err)
			}
		})
	}
}

func TestUnknownActionSchemaAndUnqualifiedNativeChunksAreMisses(t *testing.T) {
	service := openService(t, t.TempDir(), "project", "toolchain")
	for name, entries := range map[string]map[string][]byte{
		"unknown map":       {"unknown-output-schema": []byte("opaque bytes could hide references")},
		"empty map":         {},
		"unqualified chunk": {"value": nativeBytesField(1, nativeBytesField(2, bytes.Repeat([]byte{1}, 32)))},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := service.PutValue(ctx, &kv.PutValueRequest{Key: []byte(name), Value: &kv.Value{Entries: entries}}); err != nil {
				t.Fatal(err)
			}
			response, err := service.GetValue(ctx, &kv.GetValueRequest{Key: []byte(name)})
			if err != nil || response.GetOutcome() != kv.GetValueResponse_KEY_NOT_FOUND {
				t.Fatalf("unqualified action graph promised: %v %v", response, err)
			}
		})
	}
}

func TestClangFlatOutputMapRetainsEveryOutput(t *testing.T) {
	ctx := context.Background()
	service := openService(t, t.TempDir(), "project", "toolchain")
	values := make(map[string][]byte)
	for _, name := range []string{"<output>", "<stderr>"} {
		output, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte(name))}})
		if err != nil {
			t.Fatal(err)
		}
		values[name] = output.GetCasId().Id
	}
	if _, err := service.PutValue(ctx, &kv.PutValueRequest{Key: []byte("flat-action"), Value: &kv.Value{Entries: values}}); err != nil {
		t.Fatal(err)
	}
	response, err := service.GetValue(ctx, &kv.GetValueRequest{Key: []byte("flat-action")})
	if err != nil || response.GetOutcome() != kv.GetValueResponse_SUCCESS {
		t.Fatalf("flat output hit: %v %v", response, err)
	}
	for _, id := range values {
		if err := service.store.Delete(ctx, service.key("cas", id)); !errors.Is(err, artifact.ErrPinned) {
			t.Fatalf("flat output not retained: %v", err)
		}
	}
}
