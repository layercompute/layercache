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
