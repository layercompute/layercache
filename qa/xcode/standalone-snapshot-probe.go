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
type nodeShape struct {
	Bytes                int               `json:"bytes"`
	StrictAccepted       bool              `json:"strictAccepted"`
	References           int               `json:"references"`
	Fields               []fieldShape      `json:"fields"`
	FieldsTruncated      bool              `json:"fieldsTruncated"`
	Descriptors          []descriptorFlags `json:"descriptors"`
	DescriptorsTruncated bool              `json:"descriptorsTruncated"`
}
type objectShape struct {
	Kind             string     `json:"kind"`
	EncodedBytes     int        `json:"encodedBytes"`
	EnvelopeRefs     int        `json:"envelopeReferences"`
	BlobBytes        int        `json:"blobBytes"`
	Entries          int        `json:"entries"`
	ActionAccepted   bool       `json:"actionAccepted"`
	ActionReferences int        `json:"actionReferences"`
	Node             *nodeShape `json:"node,omitempty"`
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
	result := &nodeShape{Bytes: len(data), StrictAccepted: err == nil, Fields: []fieldShape{}, Descriptors: []descriptorFlags{}}
	if err == nil {
		result.References = len(refs)
	}
	for count := 0; len(data) != 0 && count < maxInspectedFields; count++ {
		shape, value, rest, ok := parseField(data)
		if !ok {
			result.FieldsTruncated = true
			return result
		}
		if len(result.Fields) < maxFields {
			result.Fields = append(result.Fields, shape)
		} else {
			result.FieldsTruncated = true
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
		result.FieldsTruncated, result.DescriptorsTruncated = true, true
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
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(metadata)+"?mode=ro&immutable=1")
	if err != nil {
		return result, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT native_key,digest,size FROM cache_entries WHERE integration='xcode' AND project=? AND compatibility=? AND version='1' AND ref_scope='' ORDER BY native_key LIMIT 257`, project, profile)
	if err != nil {
		return result, err
	}
	defer rows.Close()
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
		result.Objects = append(result.Objects, shape)
	}
	return result, rows.Err()
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
