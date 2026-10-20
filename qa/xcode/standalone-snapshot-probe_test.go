package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func wireBytes(number protowire.Number, value []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, number, protowire.BytesType), value)
}

func TestShapeUsesProductionParserAndReportsOnlyBoundedStructure(t *testing.T) {
	private := []byte("private SDK payload /private/source/file.h bootstrap-credential")
	description := append(wireBytes(1, bytes.Repeat([]byte{9}, 65)), wireBytes(2, bytes.Repeat([]byte{8}, 32))...)
	description = protowire.AppendVarint(protowire.AppendTag(description, 3, protowire.VarintType), 1)
	description = protowire.AppendVarint(protowire.AppendTag(description, 4, protowire.VarintType), 0)
	node := append(wireBytes(1, wireBytes(1, private)), wireBytes(2, wireBytes(2, description))...)
	shape := describeNode(node)
	if !shape.StrictAccepted || shape.References != 1 || len(shape.Descriptors) != 1 || shape.Descriptors[0] != (descriptorFlags{"one", "zero"}) {
		t.Fatalf("unexpected accepted shape: %+v", shape)
	}
	unknown := append(append([]byte{}, node...), wireBytes(6, private)...)
	if describeNode(unknown).StrictAccepted {
		t.Fatal("unknown native structure was incorrectly accepted")
	}
	report := report{Version: 1, Available: true, Objects: []objectShape{{Kind: "cas", Node: describeNode(unknown)}}}
	output, err := encodeReport(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{private, []byte("/private/"), []byte("bootstrap-credential"), bytes.Repeat([]byte("09"), 32), bytes.Repeat([]byte("08"), 32)} {
		if bytes.Contains(output, secret) {
			t.Fatal("private payload/path/digest escaped shape report")
		}
	}
	long := bytes.Repeat(wireBytes(2, wireBytes(2, description)), maxInspectedFields+1)
	long = append(wireBytes(1, wireBytes(1, private)), long...)
	bounded := describeNode(long)
	if len(bounded.Fields) != maxFields || len(bounded.Descriptors) != maxFields || !bounded.FieldsTruncated || !bounded.DescriptorsTruncated {
		t.Fatalf("trace bounds not enforced: fields=%d descriptors=%d", len(bounded.Fields), len(bounded.Descriptors))
	}
}

func TestShapeRejectsConflictingBooleanFlagWithoutExposingItsValue(t *testing.T) {
	value := protowire.AppendVarint(protowire.AppendTag(nil, 3, protowire.VarintType), 91234567)
	if flags := descriptor(value); flags != (descriptorFlags{"other", "absent"}) {
		t.Fatalf("unexpected flag class: %+v", flags)
	}
	value = protowire.AppendVarint(protowire.AppendTag(value, 3, protowire.VarintType), 1)
	if flags := descriptor(value); flags.Flag3 != "malformed" {
		t.Fatal("duplicate flag was treated as a valid boolean")
	}
}

func TestClosedStoreInspectionIsReadOnlyAndScoped(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := artifact.Open(ctx, root, 16<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	project, profile := "github.com/layercache/xcode-standalone-qa", "xcode-standalone-v1-test"
	data, _ := proto.Marshal(&cas.CASObject{Blob: &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: []byte("SDK data do not publish /private/path")}}})
	sum := sha256.Sum256(data)
	key := artifact.Key{Integration: "xcode", Project: project, Compatibility: profile, Native: "cas-" + hex.EncodeToString(sum[:]), Version: "1"}
	if _, _, err := store.Put(ctx, key, artifact.Metadata{}, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	key.Project = "foreign-project-with-private-name"
	if _, _, err := store.Put(ctx, key, artifact.Metadata{}, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := fileHashes(t, root)
	result, err := inspect(ctx, root, project, profile)
	if err != nil || result.Rows != 1 || len(result.Objects) != 1 || result.Objects[0].BlobBytes == 0 {
		t.Fatalf("scope/read inspection failed: %+v %v", result, err)
	}
	if after := fileHashes(t, root); !equalHashes(before, after) {
		t.Fatal("read-only inspection changed closed store")
	}
	output, err := encodeReport(result)
	if err != nil || bytes.Contains(output, []byte(root)) || bytes.Contains(output, []byte("SDK data")) || bytes.Contains(output, []byte("foreign-project")) || bytes.Contains(output, []byte(hex.EncodeToString(sum[:]))) {
		t.Fatal("private store content escaped report")
	}
	if _, err := inspect(ctx, root, "foreign-project-with-private-name", profile); err == nil {
		t.Fatal("helper accepted another project")
	}
}

func TestClosedStoreURICharactersReadOnlyOriginalDatabase(t *testing.T) {
	for _, directory := range []string{"fixture#closed", "fixture?closed", "fixture%25closed", "fixture#?%-closed"} {
		t.Run(directory, func(t *testing.T) {
			ctx := context.Background()
			parent := t.TempDir()
			root := filepath.Join(parent, directory)
			store, err := artifact.Open(ctx, root, 16<<20, 0)
			if err != nil {
				t.Fatal(err)
			}
			project, profile := "github.com/layercache/xcode-standalone-qa", "xcode-standalone-v1-uri"
			data, _ := proto.Marshal(&cas.CASObject{Blob: &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: []byte("original database fixture")}}})
			sum := sha256.Sum256(data)
			key := artifact.Key{Integration: "xcode", Project: project, Compatibility: profile, Native: "cas-" + hex.EncodeToString(sum[:]), Version: "1"}
			if _, _, err := store.Put(ctx, key, artifact.Metadata{}, bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			before := fileHashes(t, parent)
			result, readErr := inspect(ctx, root, project, profile)
			// Compare the whole parent, including an accidentally created prefix file.
			if after := fileHashes(t, parent); !equalHashes(before, after) {
				t.Fatal("URI path parsing created a file or changed original database bytes")
			}
			if readErr != nil || result.Rows != 1 || len(result.Objects) != 1 {
				t.Fatalf("URI escaped path did not read original database: %+v %v", result, readErr)
			}
			if _, err := os.Lstat(filepath.Join(parent, "fixture")); !os.IsNotExist(err) {
				t.Fatal("unescaped URI prefix was created or inspected")
			}
		})
	}
}

func TestDeclaredNativeReferenceLinksRejectedTargetByOrdinalOnly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := artifact.Open(ctx, root, 16<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	project, profile := "github.com/layercache/xcode-standalone-qa", "xcode-standalone-v1-linked"
	// The action explicitly declares this as a native node. Its alternate chunk
	// is unsupported, which must stay a rejection while its shape is observable.
	node := wireBytes(1, wireBytes(2, []byte{2}))
	data, _ := proto.Marshal(&cas.CASObject{Blob: &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: node}}})
	sum := sha256.Sum256(data)
	put := func(kind string, id [32]byte, payload []byte) {
		t.Helper()
		key := artifact.Key{Integration: "xcode", Project: project, Compatibility: profile, Native: kind + "-" + hex.EncodeToString(id[:]), Version: "1"}
		if _, _, err := store.Put(ctx, key, artifact.Metadata{}, bytes.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
	}
	put("cas", sum, data)
	desc := append(wireBytes(1, bytes.Repeat([]byte{9}, 65)), wireBytes(2, sum[:])...)
	desc = protowire.AppendVarint(protowire.AppendTag(desc, 3, protowire.VarintType), 1)
	rootNode := append(wireBytes(1, wireBytes(1, []byte("private opaque result"))), wireBytes(2, wireBytes(2, desc))...)
	action, _ := proto.Marshal(&kv.Value{Entries: map[string][]byte{"value": rootNode}})
	put("kv", sha256.Sum256([]byte("private action key")), action)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	value, err := inspect(ctx, root, project, profile)
	if err != nil || len(value.Objects) != 2 {
		t.Fatalf("linked fixture inspection failed: %v", err)
	}
	target, source := value.Objects[0], value.Objects[1]
	if target.Ordinal != 1 || !target.ExpectedNativeNode || target.Node.StrictAccepted || len(target.Node.Chunks) != 1 {
		t.Fatalf("declared native target lost its rejection/context: %+v", target)
	}
	chunk := target.Node.Chunks[0]
	if chunk.Field.Number != 2 || chunk.Field.Wire != int(protowire.BytesType) || chunk.Field.Length != 2 || chunk.IsSingleByteOne {
		t.Fatalf("unexpected bounded nested chunk: %+v", chunk)
	}
	if !source.ActionAccepted || len(source.Links) != 1 || source.Links[0] != (linkedReference{TargetOrdinal: 1, Present: true, NativeNode: true}) {
		t.Fatalf("action did not link declared native node ordinal: %+v", source)
	}
	output, err := encodeReport(value)
	if err != nil || bytes.Contains(output, []byte(hex.EncodeToString(sum[:]))) || bytes.Contains(output, []byte("private opaque result")) || bytes.Contains(output, []byte(root)) {
		t.Fatal("linked shape leaked private identifiers or payloads")
	}
}

func nativeShapeNode(payload []byte, id []byte, native bool) []byte {
	value := wireBytes(1, wireBytes(1, payload))
	if id == nil {
		return value
	}
	desc := append(wireBytes(1, bytes.Repeat([]byte{0}, 65)), wireBytes(2, id)...)
	if native {
		desc = protowire.AppendVarint(protowire.AppendTag(desc, 3, protowire.VarintType), 1)
	}
	return append(value, wireBytes(2, wireBytes(2, desc))...)
}

func fixtureShapeCAS(payload []byte) (privateRecord, [32]byte) {
	data, _ := proto.Marshal(&cas.CASObject{Blob: &cas.CASBytes{Contents: &cas.CASBytes_Data{Data: payload}}})
	return privateRecord{"cas", data}, sha256.Sum256(data)
}

func fixtureShapeReport(t *testing.T, records []privateRecord) report {
	t.Helper()
	result := report{Objects: []objectShape{}}
	ordinals := map[string]int{}
	for index, record := range records {
		shape, err := describe(record.kind, record.data)
		if err != nil {
			t.Fatal(err)
		}
		shape.Ordinal, shape.Links = index+1, []linkedReference{}
		result.Objects = append(result.Objects, shape)
		if record.kind == "cas" {
			id := sha256.Sum256(record.data)
			ordinals[hex.EncodeToString(id[:])] = index + 1
		}
	}
	if err := deriveReferenceContext(context.Background(), records, ordinals, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRawBlobShapedLikeNativeNodeCannotInventReferences(t *testing.T) {
	leaf, leafID := fixtureShapeCAS([]byte("unreferenced leaf"))
	raw, rawID := fixtureShapeCAS(nativeShapeNode([]byte("ordinary raw output shaped like a node"), leafID[:], true))
	data, _ := proto.Marshal(&kv.Value{Entries: map[string][]byte{"CASID": rawID[:]}})
	value := fixtureShapeReport(t, []privateRecord{raw, leaf, {kind: "kv", data: data}})
	if !value.Objects[0].Node.StrictAccepted || len(value.Objects[0].Links) != 0 || value.Objects[0].ExpectedNativeNode || value.Objects[1].ExpectedNativeNode {
		t.Fatal("ordinary raw output invented native graph edges")
	}
	if len(value.Objects[2].Links) != 1 || value.Objects[2].Links[0].NativeNode {
		t.Fatal("accepted raw action context was lost")
	}
}

func TestNativeTargetAfterDisplayedLinkLimitIsStillClassified(t *testing.T) {
	target, targetID := fixtureShapeCAS(nativeShapeNode([]byte("declared native target"), nil, false))
	root := nativeShapeNode([]byte("action"), nil, false)
	for index := 1; index <= maxFields+1; index++ {
		node := nativeShapeNode(nil, bytes.Repeat([]byte{byte(index)}, 32), false)
		// Strip only the known empty data chunk from this test's reference.
		root = append(root, node[len(wireBytes(1, wireBytes(1, nil))):]...)
	}
	node := nativeShapeNode(nil, targetID[:], true)
	root = append(root, node[len(wireBytes(1, wireBytes(1, nil))):]...)
	data, _ := proto.Marshal(&kv.Value{Entries: map[string][]byte{"value": root}})
	value := fixtureShapeReport(t, []privateRecord{target, {kind: "kv", data: data}})
	if !value.Objects[0].ExpectedNativeNode || len(value.Objects[1].Links) != maxFields || !value.Objects[1].LinksTruncated || value.Objects[1].ActionReferences != maxFields+2 {
		t.Fatal("display truncation hid a later declared native target from classification")
	}
}

func TestAcceptedIncomingReferencePromotesRawNodeContext(t *testing.T) {
	leaf, leafID := fixtureShapeCAS(nativeShapeNode([]byte("leaf"), nil, false))
	parent, parentID := fixtureShapeCAS(nativeShapeNode([]byte("parent"), leafID[:], true))
	bridge, bridgeID := fixtureShapeCAS(nativeShapeNode([]byte("bridge"), parentID[:], true))
	// Stack order visits raw parent first, then the native bridge introduces a
	// declared native parent reference and must discover the native leaf.
	root := nativeShapeNode([]byte("action"), bridgeID[:], true)
	rawRef := nativeShapeNode(nil, parentID[:], false)
	root = append(root, rawRef[len(wireBytes(1, wireBytes(1, nil))):]...)
	data, _ := proto.Marshal(&kv.Value{Entries: map[string][]byte{"value": root}})
	value := fixtureShapeReport(t, []privateRecord{parent, leaf, bridge, {kind: "kv", data: data}})
	if !value.Objects[0].ExpectedNativeNode || !value.Objects[1].ExpectedNativeNode || len(value.Objects[0].Links) != 1 || !value.Objects[0].Links[0].NativeNode {
		t.Fatal("accepted raw-to-native promotion lost declared child context")
	}
}

func TestShapeOutputBoundFailsWithoutReturningPartialReport(t *testing.T) {
	value := report{Objects: make([]objectShape, maxRows)}
	for index := range value.Objects {
		value.Objects[index].Kind = strings.Repeat("x", maxReportBytes/maxRows+1)
	}
	if output, err := encodeReport(value); err == nil || output != nil {
		t.Fatal("oversized diagnostics escaped output limit")
	}
}

func TestShapeUsesProductionActionReferenceParser(t *testing.T) {
	data, _ := proto.Marshal(&kv.Value{Entries: map[string][]byte{"unknown": []byte("opaque private bytes")}})
	shape, err := describe("kv", data)
	if err != nil || shape.ActionAccepted || shape.Entries != 1 || shape.Node != nil {
		t.Fatalf("unknown action shape incorrectly accepted: %+v %v", shape, err)
	}
}

func fileHashes(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil {
			result[path] = sha256.Sum256(data)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func equalHashes(a, b map[string][32]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}
