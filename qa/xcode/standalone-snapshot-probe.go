// This QA-only helper inspects one already closed synthetic compiler fixture.
// It prints wire shapes, never payloads, identifiers, hashes or filesystem paths.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/xcodecache"
	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

const maxRows = 256
const maxReadBytes = 64 << 20
const maxReportBytes = 256 << 10
const maxFields = 16
const maxInspectedFields = 256

var hexDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type fieldShape struct {
	Number int `json:"number"`
	Wire   int `json:"wire"`
	Length int `json:"encodedValueBytes"`
}
type descriptorFlags struct {
	Flag3 string `json:"flag3"`
	Flag4 string `json:"flag4"`
}
type chunkShape struct {
	Field                    fieldShape `json:"field"`
	IsSingleByteOne          bool       `json:"isSingleByteOne"`
	PayloadBytes             int        `json:"payloadBytes"`
	AllBytesOne              bool       `json:"allBytesOne"`
	FollowingLiteralChunks   int        `json:"followingLiteralChunks"`
	LeadingUniqueMarker      bool       `json:"leadingUniqueMarker"`
	VectorCardinalityMatches bool       `json:"vectorCardinalityMatches"`
}
type linkedReference struct {
	TargetOrdinal int  `json:"targetOrdinal"`
	Present       bool `json:"present"`
	NativeNode    bool `json:"nativeNode"`
}
type nodeShape struct {
	Bytes                int               `json:"bytes"`
	StrictAccepted       bool              `json:"strictAccepted"`
	References           int               `json:"references"`
	Fields               []fieldShape      `json:"fields"`
	FieldsTruncated      bool              `json:"fieldsTruncated"`
	Descriptors          []descriptorFlags `json:"descriptors"`
	DescriptorsTruncated bool              `json:"descriptorsTruncated"`
	Chunks               []chunkShape      `json:"chunks"`
	ChunksTruncated      bool              `json:"chunksTruncated"`
	MarkerCount          int               `json:"markerCount"`
	LiteralChunkCount    int               `json:"literalChunkCount"`
	SequenceComplete     bool              `json:"sequenceComplete"`
	OnlyTopLevelChunks   bool              `json:"onlyTopLevelChunks"`
}
type objectShape struct {
	Ordinal            int               `json:"ordinal"`
	ExpectedNativeNode bool              `json:"expectedNativeNode"`
	Links              []linkedReference `json:"declaredReferences"`
	LinksTruncated     bool              `json:"declaredReferencesTruncated"`
	Kind               string            `json:"kind"`
	EncodedBytes       int               `json:"encodedBytes"`
	EnvelopeRefs       int               `json:"envelopeReferences"`
	BlobBytes          int               `json:"blobBytes"`
	Entries            int               `json:"entries"`
	ActionAccepted     bool              `json:"actionAccepted"`
	ActionReferences   int               `json:"actionReferences"`
	Node               *nodeShape        `json:"node,omitempty"`
}
type report struct {
	Version        int           `json:"version"`
	Available      bool          `json:"available"`
	Rows           int           `json:"rows"`
	InspectedBytes int           `json:"inspectedBytes"`
	Objects        []objectShape `json:"objects"`
}

// parseField returns slices only for in-memory structure inspection. They never
// enter any report. Its caller enforces a fixed number of fields and bytes.
func parseField(data []byte) (fieldShape, []byte, []byte, bool) {
	number, wire, n := protowire.ConsumeTag(data)
	if n < 0 || number <= 0 {
		return fieldShape{}, nil, nil, false
	}
	length := protowire.ConsumeFieldValue(number, wire, data[n:])
	if length < 0 {
		return fieldShape{}, nil, nil, false
	}
	shape := fieldShape{int(number), int(wire), length}
	var payload []byte
	if wire == protowire.BytesType {
		value, count := protowire.ConsumeBytes(data[n:])
		if count < 0 {
			return fieldShape{}, nil, nil, false
		}
		payload = value
	}
	return shape, payload, data[n+length:], true
}

func descriptor(data []byte) descriptorFlags {
	flags := descriptorFlags{"absent", "absent"}
	seen := map[int]bool{}
	for count := 0; len(data) != 0 && count < maxFields; count++ {
		shape, _, rest, ok := parseField(data)
		if !ok {
			break
		}
		if shape.Number == 3 || shape.Number == 4 {
			result := "malformed"
			_, _, n := protowire.ConsumeTag(data)
			if shape.Wire == int(protowire.VarintType) && !seen[shape.Number] {
				value, consumed := protowire.ConsumeVarint(data[n:])
				if consumed >= 0 {
					result = "other"
					if value == 0 {
						result = "zero"
					} else if value == 1 {
						result = "one"
					}
				}
			}
			seen[shape.Number] = true
			if shape.Number == 3 {
				flags.Flag3 = result
			} else {
				flags.Flag4 = result
			}
		}
		data = rest
	}
	return flags
}

func describeNode(data []byte) *nodeShape {
	refs, err := xcodecache.NativeNodeReferences(data)
	result := &nodeShape{Bytes: len(data), StrictAccepted: err == nil, Fields: []fieldShape{}, Descriptors: []descriptorFlags{}, Chunks: []chunkShape{}, OnlyTopLevelChunks: true}
	if err == nil {
		result.References = len(refs)
	}
	type marker struct {
		displayIndex   int
		literalsBefore int
		payloadBytes   int
		leading        bool
	}
	var markers []marker
	complete := true
	for count := 0; len(data) > 0 && count < maxInspectedFields; count++ {
		shape, value, rest, ok := parseField(data)
		if !ok {
			result.FieldsTruncated, complete = true, false
			break
		}
		if len(result.Fields) < maxFields {
			result.Fields = append(result.Fields, shape)
		} else {
			result.FieldsTruncated = true
		}
		if shape.Number != 1 || shape.Wire != int(protowire.BytesType) {
			result.OnlyTopLevelChunks = false
		}
		if shape.Number == 1 && shape.Wire == int(protowire.BytesType) {
			chunk, payload, tail, valid := parseField(value)
			if !valid || len(tail) != 0 || chunk.Wire != int(protowire.BytesType) || chunk.Number != 1 && chunk.Number != 2 {
				complete = false
			} else {
				allOne := chunk.Number == 2 && len(payload) > 0
				if allOne {
					for _, value := range payload {
						if value != 1 {
							allOne = false
							break
						}
					}
				}
				displayIndex := -1
				if len(result.Chunks) < maxFields {
					displayIndex = len(result.Chunks)
					result.Chunks = append(result.Chunks, chunkShape{Field: chunk, IsSingleByteOne: len(payload) == 1 && payload[0] == 1, PayloadBytes: len(payload), AllBytesOne: allOne})
				} else {
					result.ChunksTruncated = true
				}
				if chunk.Number == 1 {
					result.LiteralChunkCount++
				} else {
					result.MarkerCount++
					markers = append(markers, marker{displayIndex, result.LiteralChunkCount, len(payload), count == 0})
				}
			}
		}
		if shape.Number == 2 && shape.Wire == int(protowire.BytesType) {
			union, external, tail, valid := parseField(value)
			if valid && len(tail) == 0 && union.Number == 2 && union.Wire == int(protowire.BytesType) {
				if len(result.Descriptors) < maxFields {
					result.Descriptors = append(result.Descriptors, descriptor(external))
				} else {
					result.DescriptorsTruncated = true
				}
			}
		}
		data = rest
	}
	if len(data) != 0 {
		result.FieldsTruncated, result.DescriptorsTruncated, result.ChunksTruncated, complete = true, true, true, false
	}
	result.SequenceComplete = complete
	for _, marker := range markers {
		if marker.displayIndex >= 0 {
			chunk := &result.Chunks[marker.displayIndex]
			chunk.FollowingLiteralChunks = result.LiteralChunkCount - marker.literalsBefore
			chunk.LeadingUniqueMarker = complete && marker.leading && len(markers) == 1
			chunk.VectorCardinalityMatches = complete && marker.payloadBytes > 0 && marker.payloadBytes == chunk.FollowingLiteralChunks
		}
	}
	return result
}

func describe(kind string, data []byte) (objectShape, error) {
	result := objectShape{Kind: kind, EncodedBytes: len(data)}
	if kind == "cas" {
		value := new(cas.CASObject)
		if err := proto.Unmarshal(data, value); err != nil {
			return result, err
		}
		result.EnvelopeRefs = len(value.References)
		blob, inline := value.GetBlob().GetContents().(*cas.CASBytes_Data)
		if !inline {
			return result, errors.New("non-inline fixture CAS object")
		}
		result.BlobBytes, result.Node = len(blob.Data), describeNode(blob.Data)
	} else {
		value := new(kv.Value)
		if err := proto.Unmarshal(data, value); err != nil {
			return result, err
		}
		result.Entries = len(value.Entries)
		refs, err := xcodecache.ActionReferences(value)
		result.ActionAccepted = err == nil
		if err == nil {
			result.ActionReferences = len(refs)
		}
		if node, exists := value.Entries["value"]; exists {
			result.Node = describeNode(node)
		}
	}
	return result, nil
}

// Reference context starts at accepted action roots. Raw bytes are interpreted
// as a native node only after an incoming reference explicitly declares it.
type privateRecord struct {
	kind string
	data []byte
}
type pendingReference struct {
	ordinal int
	ref     xcodecache.GraphReference
}

func deriveReferenceContext(ctx context.Context, records []privateRecord, casOrdinals map[string]int, result *report) error {
	acceptedEdges := make(map[int][]xcodecache.GraphReference)
	var queue []pendingReference
	discovered := map[int]bool{}
	enqueue := func(refs []xcodecache.GraphReference) {
		for _, ref := range refs {
			ordinal, found := casOrdinals[hex.EncodeToString(ref.ID)]
			if found {
				if ref.NativeNode {
					result.Objects[ordinal-1].ExpectedNativeNode = true
				}
				priorNative, known := discovered[ordinal]
				if known && (priorNative || !ref.NativeNode) {
					continue
				}
				discovered[ordinal] = ref.NativeNode
				queue = append(queue, pendingReference{ordinal, ref})
			}
		}
	}
	for index, record := range records {
		if record.kind != "kv" {
			continue
		}
		value := new(kv.Value)
		if err := proto.Unmarshal(record.data, value); err != nil {
			return err
		}
		refs, err := xcodecache.ActionReferences(value)
		if err == nil {
			acceptedEdges[index] = refs
			enqueue(refs)
		}
	}
	seen := map[int]bool{}
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		next := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		priorNative, known := seen[next.ordinal]
		if known && (priorNative || !next.ref.NativeNode) {
			continue
		}
		seen[next.ordinal] = next.ref.NativeNode
		object := new(cas.CASObject)
		if err := proto.Unmarshal(records[next.ordinal-1].data, object); err != nil {
			return err
		}
		refs, err := xcodecache.ObjectReferences(next.ref, object)
		if err == nil {
			acceptedEdges[next.ordinal-1] = refs
			enqueue(refs)
		}
	}
	for index, refs := range acceptedEdges {
		result.Objects[index].LinksTruncated = len(refs) > maxFields
		for position, ref := range refs {
			// Classification traversed every accepted edge above. This cap only
			// bounds the displayed list; it never hides a node from traversal.
			if position >= maxFields {
				break
			}
			ordinal, found := casOrdinals[hex.EncodeToString(ref.ID)]
			result.Objects[index].Links = append(result.Objects[index].Links, linkedReference{ordinal, found, ref.NativeNode})
		}
	}
	return nil
}

func inspect(ctx context.Context, store, project, profile string) (report, error) {
	result := report{Version: 1, Available: true, Objects: []objectShape{}}
	if !filepath.IsAbs(store) || filepath.Clean(store) != store || project != "github.com/layercache/xcode-standalone-qa" || !strings.HasPrefix(profile, "xcode-standalone-v1-") {
		return result, errors.New("invalid isolated fixture scope")
	}
	metadata := filepath.Join(store, "metadata.db")
	info, err := os.Lstat(metadata)
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("invalid closed fixture database")
	}
	// The Python caller requires live stop acknowledgement and process exit.
	// Read-only immutable mode cannot checkpoint or create a WAL in this store.
	databaseURL := &url.URL{Scheme: "file", Path: metadata, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return result, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT native_key,digest,size FROM cache_entries WHERE integration='xcode' AND project=? AND compatibility=? AND version='1' AND ref_scope='' ORDER BY native_key LIMIT 257`, project, profile)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var records []privateRecord
	casOrdinals := map[string]int{}
	for rows.Next() {
		result.Rows++
		if result.Rows > maxRows {
			return result, errors.New("fixture diagnostic row limit exceeded")
		}
		var key, encodedDigest string
		var size int64
		if err := rows.Scan(&key, &encodedDigest, &size); err != nil {
			return result, err
		}
		kind, nativeDigest, ok := strings.Cut(key, "-")
		if !ok || kind != "cas" && kind != "kv" || !hexDigest.MatchString(nativeDigest) || !hexDigest.MatchString(encodedDigest) || size < 0 || size > xcodecache.MaxMessageBytes || size > maxReadBytes-int64(result.InspectedBytes) {
			return result, errors.New("invalid or oversized fixture blob")
		}
		path := filepath.Join(store, "blobs", "sha256", encodedDigest[:2], encodedDigest[2:])
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != size {
			return result, errors.New("invalid fixture blob")
		}
		file, err := os.Open(path)
		if err != nil {
			return result, err
		}
		data, err := io.ReadAll(io.LimitReader(file, size+1))
		file.Close()
		sum := sha256.Sum256(data)
		if err != nil || int64(len(data)) != size || hex.EncodeToString(sum[:]) != encodedDigest {
			return result, errors.New("fixture blob verification failed")
		}
		result.InspectedBytes += len(data)
		shape, err := describe(kind, data)
		if err != nil {
			return result, err
		}
		shape.Ordinal, shape.Links = result.Rows, []linkedReference{}
		result.Objects = append(result.Objects, shape)
		records = append(records, privateRecord{kind, data})
		if kind == "cas" {
			casOrdinals[nativeDigest] = shape.Ordinal
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := deriveReferenceContext(ctx, records, casOrdinals, &result); err != nil {
		return result, err
	}
	return result, nil
}

func encodeReport(value report) ([]byte, error) {
	var output bytes.Buffer
	if err := json.NewEncoder(&output).Encode(value); err != nil {
		return nil, err
	}
	if output.Len() > maxReportBytes {
		return nil, errors.New("fixture diagnostic output limit exceeded")
	}
	return output.Bytes(), nil
}

func main() {
	// Raw errors can contain paths or digests, so only this fixed error is emitted.
	if len(os.Args) != 4 {
		os.Stderr.WriteString("closed fixture shape diagnostic unavailable\n")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	value, err := inspect(ctx, os.Args[1], os.Args[2], os.Args[3])
	if err == nil {
		var output []byte
		output, err = encodeReport(value)
		if err == nil {
			_, err = os.Stdout.Write(output)
		}
	}
	if err != nil {
		os.Stderr.WriteString("closed fixture shape diagnostic unavailable\n")
		os.Exit(1)
	}
}
