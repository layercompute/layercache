package xcodecache

import (
	"crypto/sha256"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/protobuf/encoding/protowire"
)

// GraphReference identifies a remote object. NativeNode means its blob contains
// the native plugin's serialized node, in addition to the CAS envelope's refs.
type GraphReference struct {
	ID         []byte
	NativeNode bool
}

// ActionReferences supports the LLVM relay CASID and the native Xcode plugin's
// inline value node. The latter layout is qualified against retained real Xcode
// messages. Unsupported native fields fail closed rather than hide references.
func ActionReferences(value *kv.Value) ([]GraphReference, error) {
	if root, ok := value.GetEntries()["CASID"]; ok {
		_, native := value.Entries["value"]
		if len(root) != sha256.Size || native {
			return nil, artifact.ErrCorrupt
		}
		return []GraphReference{{ID: root}}, nil
	}
	if node, ok := value.GetEntries()["value"]; ok {
		if len(value.Entries) != 1 {
			return nil, artifact.ErrCorrupt
		}
		return NativeNodeReferences(node)
	}
	// Clang's RemoteCachingOutputs uses output-name -> remote ID entries and
	// restores them with Load. It has no separate manifest object.
	var outputs []GraphReference
	for _, id := range value.GetEntries() {
		if len(id) != sha256.Size {
			return nil, artifact.ErrCorrupt
		}
		outputs = append(outputs, GraphReference{ID: id})
	}
	if len(outputs) == 0 || len(outputs) > maximumGraphObjects {
		return nil, artifact.ErrCorrupt
	}
	return outputs, nil
}

const maximumNativeInlineLeaves = 256

// NativeNodeReferences decodes the evidenced native node's data chunks and
// reference union. Inline references contain their bytes in the action itself.
// External descriptors carry a native digest, remote ID and two boolean flags;
// field 3 identifies a serialized node, while field 4 still refers to a blob.
// Data chunks are opaque to graph retention. Unknown fields or flag values are
// rejected so a new plugin layout becomes a cache miss until it is qualified.
func NativeNodeReferences(data []byte) ([]GraphReference, error) {
	if len(data) > MaxMessageBytes {
		return nil, artifact.ErrCorrupt
	}
	if hasNativeInlineMarker(data) {
		return nil, validateNativeInlineLeaves(data)
	}
	var refs []GraphReference
	chunks := 0
	for len(data) > 0 {
		number, value, rest, err := bytesField(data)
		if err != nil {
			return nil, err
		}
		data = rest
		switch number {
		case 1:
			field, _, remaining, err := bytesField(value)
			if err != nil || len(remaining) != 0 || field != 1 {
				return nil, artifact.ErrCorrupt
			}
			chunks++
		case 2:
			field, reference, remaining, err := bytesField(value)
			if err != nil || len(remaining) != 0 {
				return nil, artifact.ErrCorrupt
			}
			if field == 1 { // Inline leaf, including the empty byte string.
				continue
			}
			if field != 2 || len(refs) >= maximumGraphObjects {
				return nil, artifact.ErrCorrupt
			}
			ref, err := nativeExternalReference(reference)
			if err != nil {
				return nil, err
			}
			refs = append(refs, ref)
		default:
			return nil, artifact.ErrCorrupt
		}
	}
	if chunks == 0 {
		return nil, artifact.ErrCorrupt
	}
	return refs, nil
}

// hasNativeInlineMarker identifies the separate marker branch; it does not
// validate it. No-marker nodes keep their existing explicit reference grammar.
func hasNativeInlineMarker(data []byte) bool {
	number, value, _, err := bytesField(data)
	if err != nil || number != 1 {
		return false
	}
	field, _, remaining, err := bytesField(value)
	return err == nil && field == 2 && len(remaining) == 0
}

// validateNativeInlineLeaves accepts only a leading canonical permutation of
// 1..N followed by exactly N inline leaf payloads. The native API qualification
// compares those leaves against the original objects; this is not permission
// to ignore arbitrary chunks or reference-bearing marker nodes.
func validateNativeInlineLeaves(data []byte) error {
	number, first, data, err := bytesField(data)
	if err != nil || number != 1 {
		return artifact.ErrCorrupt
	}
	field, indices, remaining, err := bytesField(first)
	if err != nil || field != 2 || len(remaining) != 0 || len(indices) == 0 {
		return artifact.ErrCorrupt
	}
	var seen [maximumNativeInlineLeaves + 1]bool
	count := 0
	for len(indices) > 0 {
		value, consumed := protowire.ConsumeVarint(indices)
		if consumed < 0 || consumed != protowire.SizeVarint(value) || value == 0 || value > maximumNativeInlineLeaves || seen[value] || count >= maximumNativeInlineLeaves {
			return artifact.ErrCorrupt
		}
		seen[value] = true
		count++
		indices = indices[consumed:]
	}
	for value := 1; value <= count; value++ {
		if !seen[value] {
			return artifact.ErrCorrupt
		}
	}
	for range count {
		number, value, rest, err := bytesField(data)
		if err != nil || number != 1 {
			return artifact.ErrCorrupt
		}
		field, _, remaining, err := bytesField(value)
		if err != nil || field != 1 || len(remaining) != 0 {
			return artifact.ErrCorrupt
		}
		data = rest
	}
	if len(data) != 0 {
		return artifact.ErrCorrupt
	}
	return nil
}

func bytesField(data []byte) (protowire.Number, []byte, []byte, error) {
	number, kind, consumed := protowire.ConsumeTag(data)
	if consumed < 0 || kind != protowire.BytesType {
		return 0, nil, nil, artifact.ErrCorrupt
	}
	value, size := protowire.ConsumeBytes(data[consumed:])
	if size < 0 {
		return 0, nil, nil, artifact.ErrCorrupt
	}
	return number, value, data[consumed+size:], nil
}

func nativeExternalReference(data []byte) (GraphReference, error) {
	var ref GraphReference
	seen := map[protowire.Number]bool{}
	for len(data) > 0 {
		number, kind, consumed := protowire.ConsumeTag(data)
		if consumed < 0 || seen[number] {
			return ref, artifact.ErrCorrupt
		}
		seen[number] = true
		switch number {
		case 1, 2:
			field, value, rest, err := bytesField(data)
			if err != nil || (field == 1 && len(value) != 65) || (field == 2 && len(value) != sha256.Size) {
				return ref, artifact.ErrCorrupt
			}
			if field == 2 {
				ref.ID = value
			}
			data = rest
		case 3, 4:
			if kind != protowire.VarintType {
				return ref, artifact.ErrCorrupt
			}
			flag, size := protowire.ConsumeVarint(data[consumed:])
			if size < 0 || flag > 1 {
				return ref, artifact.ErrCorrupt
			}
			if number == 3 {
				ref.NativeNode = flag == 1
			}
			data = data[consumed+size:]
		default:
			return ref, artifact.ErrCorrupt
		}
	}
	if !seen[1] || !seen[2] {
		return ref, artifact.ErrCorrupt
	}
	return ref, nil
}

// ObjectReferences combines CAS envelope edges with native plugin edges. A raw
// output blob is never guessed to contain a node based on its byte contents.
func ObjectReferences(ref GraphReference, object *cas.CASObject) ([]GraphReference, error) {
	var refs []GraphReference
	for _, child := range object.GetReferences() {
		if len(child.GetId()) != sha256.Size {
			return nil, artifact.ErrCorrupt
		}
		refs = append(refs, GraphReference{ID: child.Id})
	}
	if ref.NativeNode {
		if _, inline := object.GetBlob().GetContents().(*cas.CASBytes_Data); !inline {
			return nil, artifact.ErrCorrupt
		}
		data := object.GetBlob().GetData()
		native, err := NativeNodeReferences(data)
		if err != nil {
			return nil, err
		}
		if hasNativeInlineMarker(data) && len(object.GetReferences()) != 0 {
			return nil, artifact.ErrCorrupt
		}
		refs = append(refs, native...)
	}
	return refs, nil
}
