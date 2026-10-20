package xcodecache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/protobuf/encoding/protowire"
)

func indexedInlineLeafNode(marker []byte, leaves ...[]byte) []byte {
	node := nativeBytesField(1, nativeBytesField(2, marker))
	for _, leaf := range leaves {
		node = append(node, nativeBytesField(1, nativeBytesField(1, leaf))...)
	}
	return node
}

func TestNativeIndexedInlineLeavesActionRetainsRoot(t *testing.T) {
	assertIndexedInlineLeafActionHit(t, indexedInlineLeafNode([]byte{2, 3, 1}, []byte("leaf C"), []byte("leaf A"), []byte("leaf B")))
}

func assertIndexedInlineLeafActionHit(t *testing.T, node []byte) {
	t.Helper()
	ctx := context.Background()
	service := openService(t, t.TempDir(), "project", "toolchain")
	root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
	if err != nil {
		t.Fatal(err)
	}
	value := nativeTestNode([]byte("compile result"), GraphReference{ID: root.GetCasId().Id, NativeNode: true})
	key := []byte("indexed inline leaves")
	if saved, err := service.PutValue(ctx, &kv.PutValueRequest{Key: key, Value: &kv.Value{Entries: map[string][]byte{"value": value}}}); err != nil || saved.GetError() != nil {
		t.Fatalf("save indexed action: %v %v", saved, err)
	}
	hit, err := service.GetValue(ctx, &kv.GetValueRequest{Key: key})
	if err != nil || hit.GetOutcome() != kv.GetValueResponse_SUCCESS {
		t.Fatalf("qualified indexed inline leaves must yield an action hit: %v %v", hit, err)
	}
	if !bytes.Equal(hit.GetValue().Entries["value"], value) {
		t.Fatal("action bytes changed during indexed-leaf graph validation")
	}
	if err := service.store.Delete(ctx, service.key("cas", root.GetCasId().Id)); !errors.Is(err, artifact.ErrPinned) {
		t.Fatalf("indexed inline-leaf root was not retained: %v", err)
	}
	loaded, err := service.Load(ctx, &cas.CASLoadRequest{CasId: root.GetCasId()})
	if err != nil || loaded.GetOutcome() != cas.CASLoadResponse_SUCCESS || !bytes.Equal(loaded.GetData().GetBlob().GetData(), node) {
		t.Fatalf("retained indexed node bytes changed: %v %v", loaded, err)
	}
}

func TestNativeIndexedInlineLeavesCanonicalVarintBoundaries(t *testing.T) {
	for _, count := range []int{1, 127, 128, 256} {
		t.Run(fmt.Sprintf("%d leaves", count), func(t *testing.T) {
			var marker []byte
			for index := count; index > 0; index-- {
				marker = protowire.AppendVarint(marker, uint64(index))
			}
			assertIndexedInlineLeafActionHit(t, indexedInlineLeafNode(marker, make([][]byte, count)...))
		})
	}
}

func TestNativeIndexedInlineLeavesRejectUnqualifiedShapes(t *testing.T) {
	join := func(parts ...[]byte) []byte {
		var result []byte
		for _, part := range parts {
			result = append(result, part...)
		}
		return result
	}
	literal := nativeBytesField(1, nativeBytesField(1, []byte("leaf")))
	marker := nativeBytesField(1, nativeBytesField(2, []byte{2, 3, 1}))
	valid := indexedInlineLeafNode([]byte{2, 3, 1}, []byte("C"), []byte("A"), []byte("B"))
	externalDescriptor := join(nativeBytesField(1, make([]byte, 65)), nativeBytesField(2, bytes.Repeat([]byte{1}, 32)))
	external := nativeBytesField(2, nativeBytesField(2, externalDescriptor))
	inline := nativeBytesField(2, nativeBytesField(1, []byte("inline reference")))
	var oversizedMarker []byte
	for index := 1; index <= 257; index++ {
		oversizedMarker = protowire.AppendVarint(oversizedMarker, uint64(index))
	}
	cases := map[string][]byte{
		"empty marker":                   indexedInlineLeafNode(nil, nil),
		"duplicate indices":              indexedInlineLeafNode([]byte{1, 1, 3}, nil, nil, nil),
		"missing index":                  indexedInlineLeafNode([]byte{1, 3, 4}, nil, nil, nil),
		"zero based indices":             indexedInlineLeafNode([]byte{0, 1, 2}, nil, nil, nil),
		"out of range index":             indexedInlineLeafNode([]byte{1, 2, 4}, nil, nil, nil),
		"noncanonical index":             indexedInlineLeafNode([]byte{0x82, 0x00, 3, 1}, nil, nil, nil),
		"truncated index":                indexedInlineLeafNode([]byte{1, 2, 0x80}, nil, nil, nil),
		"overflow index":                 indexedInlineLeafNode(bytes.Repeat([]byte{0xff}, 11), nil),
		"missing literal":                indexedInlineLeafNode([]byte{2, 3, 1}, nil, nil),
		"extra literal":                  indexedInlineLeafNode([]byte{2, 3, 1}, nil, nil, nil, nil),
		"single marker without literal":  indexedInlineLeafNode([]byte{1}),
		"single marker extra literal":    indexedInlineLeafNode([]byte{1}, nil, nil),
		"literal before marker":          join(literal, valid),
		"duplicate leading marker":       join(marker, valid),
		"marker after literals":          join(valid, marker),
		"inline reference before marker": join(inline, valid),
		"inline reference after marker":  join(valid, inline),
		"external before marker":         join(external, valid),
		"external after marker":          join(valid, external),
		"unknown top field":              join(valid, nativeBytesField(3, nil)),
		"unknown marker field":           join(nativeBytesField(1, nativeBytesField(3, []byte{1})), literal),
		"marker union has extra field":   join(nativeBytesField(1, join(nativeBytesField(2, []byte{1}), nativeBytesField(1, nil))), literal),
		"literal union has extra field":  join(marker, literal, literal, nativeBytesField(1, join(nativeBytesField(1, nil), nativeBytesField(2, []byte{1})))),
		"wrong marker wire type":         join(nativeBytesField(1, protowire.AppendVarint(protowire.AppendTag(nil, 2, protowire.VarintType), 1)), literal),
		"unknown literal field":          join(marker, literal, literal, nativeBytesField(1, nativeBytesField(3, nil))),
		"truncated outer field":          join(valid, []byte{0x0a, 0x02, 0x0a}),
		"truncated nested literal":       join(marker, literal, literal, nativeBytesField(1, []byte{0x0a, 0x02, 0x00})),
		"more than 256 leaves":           indexedInlineLeafNode(oversizedMarker, make([][]byte, 257)...),
	}
	service := openService(t, t.TempDir(), "project", "toolchain")
	for name, node := range cases {
		t.Run(name, func(t *testing.T) {
			if refs, err := NativeNodeReferences(node); !errors.Is(err, artifact.ErrCorrupt) {
				t.Fatalf("unqualified indexed node parsed: %+v %v", refs, err)
			}
			ctx := context.Background()
			root, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob(node)}})
			if err != nil {
				t.Fatal(err)
			}
			value := nativeTestNode([]byte("compile result"), GraphReference{ID: root.GetCasId().Id, NativeNode: true})
			key := []byte(name)
			if response, err := service.PutValue(ctx, &kv.PutValueRequest{Key: key, Value: &kv.Value{Entries: map[string][]byte{"value": value}}}); err != nil || response.GetError() != nil {
				t.Fatalf("save action before graph qualification: %v %v", response, err)
			}
			miss, err := service.GetValue(ctx, &kv.GetValueRequest{Key: key})
			if err != nil || miss.GetOutcome() != kv.GetValueResponse_KEY_NOT_FOUND {
				t.Fatalf("unqualified indexed node promised: %v %v", miss, err)
			}
		})
	}
}

func TestNativeIndexedInlineLeavesRejectCASEnvelopeReferences(t *testing.T) {
	for _, count := range []int{1, 3} {
		t.Run(fmt.Sprintf("%d leaves", count), func(t *testing.T) {
			var marker []byte
			for index := 1; index <= count; index++ {
				marker = protowire.AppendVarint(marker, uint64(index))
			}
			node := indexedInlineLeafNode(marker, make([][]byte, count)...)
			ctx := context.Background()
			service := openService(t, t.TempDir(), "project", "toolchain")
			child, err := service.Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("existing envelope child"))}})
			if err != nil {
				t.Fatal(err)
			}
			object := &cas.CASObject{Blob: blob(node), References: []*cas.CASDataID{child.GetCasId()}}
			if refs, err := ObjectReferences(GraphReference{NativeNode: true}, object); !errors.Is(err, artifact.ErrCorrupt) {
				t.Fatalf("indexed node with separate envelope edges parsed: %+v %v", refs, err)
			}
			root, err := service.Put(ctx, &cas.CASPutRequest{Data: object})
			if err != nil {
				t.Fatal(err)
			}
			value := nativeTestNode([]byte("compile result"), GraphReference{ID: root.GetCasId().Id, NativeNode: true})
			key := []byte("indexed node with envelope reference")
			if response, err := service.PutValue(ctx, &kv.PutValueRequest{Key: key, Value: &kv.Value{Entries: map[string][]byte{"value": value}}}); err != nil || response.GetError() != nil {
				t.Fatalf("save envelope-bearing action: %v %v", response, err)
			}
			miss, err := service.GetValue(ctx, &kv.GetValueRequest{Key: key})
			if err != nil || miss.GetOutcome() != kv.GetValueResponse_KEY_NOT_FOUND {
				t.Fatalf("indexed node with unqualified envelope references promised: %v %v", miss, err)
			}
		})
	}
}
