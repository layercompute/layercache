package xcodecache

import (
	"errors"

	"github.com/layercache/layercache/internal/artifact"
	"google.golang.org/protobuf/encoding/protowire"
)

// graphRejectReason names one specific validation rule enforced while decoding
// an action's output graph. Reasons are reported in statistics; they never
// carry node bytes, literal contents, CAS IDs, digests, file names or paths.
type graphRejectReason string

const (
	reasonInlineLeafCountExceeded            graphRejectReason = "inlineLeafCountExceeded"
	reasonInlineMarkerWithEnvelopeReferences graphRejectReason = "inlineMarkerWithEnvelopeReferences"
	reasonUnexpectedProtobufField            graphRejectReason = "unexpectedProtobufField"
	reasonMalformedVarint                    graphRejectReason = "malformedVarint"
	reasonInvalidOrdinalIndex                graphRejectReason = "invalidOrdinalIndex"
	reasonLiteralCountMismatch               graphRejectReason = "literalCountMismatch"
	reasonNativeBlobNotInline                graphRejectReason = "nativeBlobNotInline"
	reasonInvalidReferenceId                 graphRejectReason = "invalidReferenceId"
	reasonGraphObjectLimitExceeded           graphRejectReason = "graphObjectLimitExceeded"
	reasonNativeNodeTooLarge                 graphRejectReason = "nativeNodeTooLarge"
	reasonInvalidNativeDigestLength          graphRejectReason = "invalidNativeDigestLength"
)

// graphRejectError identifies the rule a node failed. It still unwraps to
// artifact.ErrCorrupt so every existing caller and test keeps working.
type graphRejectError struct {
	reason graphRejectReason
	sample graphRejectSample
}

func (e *graphRejectError) Error() string {
	return "xcodecache: rejected native output graph reference: " + string(e.reason)
}

func (e *graphRejectError) Unwrap() error { return artifact.ErrCorrupt }

func rejectReason(reason graphRejectReason) error {
	return &graphRejectError{reason: reason}
}

// fieldOccurrence reports how many times a top-level protobuf field number
// appeared in a rejected node. It never reports the field's content.
type fieldOccurrence struct {
	Number int `json:"number"`
	Count  int `json:"count"`
}

// graphRejectSample is a bounded, structure-only shape of one rejected node.
// It never contains node bytes, literal contents, CAS IDs, digests, file
// names or paths - only lengths, counts and booleans.
type graphRejectSample struct {
	Reason              string            `json:"reason"`
	DataBytes           int               `json:"dataBytes"`
	EnvelopeReferences  int               `json:"envelopeReferences"`
	InlineMarkerPresent bool              `json:"inlineMarkerPresent"`
	Fields              []fieldOccurrence `json:"fields,omitempty"`
	DeclaredOrdinals    int               `json:"declaredOrdinals,omitempty"`
	LiteralCount        int               `json:"literalCount,omitempty"`
	MaxLiteralBytes     int               `json:"maxLiteralBytes,omitempty"`
}

const maxGraphRejectSampleFields = 32
const maxGraphRejectScanBytes = 4 << 20

// withGraphRejectSample attaches a freshly computed structural sample to a
// graphRejectError, keyed off the top-level node bytes and CAS envelope
// reference count available at this call site. Any other error (already
// sampled, or not a graphRejectError at all) is returned unchanged.
func withGraphRejectSample(err error, data []byte, envelopeReferences int) error {
	var reject *graphRejectError
	if !errors.As(err, &reject) {
		return err
	}
	return &graphRejectError{reason: reject.reason, sample: buildGraphRejectSample(reject.reason, data, envelopeReferences)}
}

func buildGraphRejectSample(reason graphRejectReason, data []byte, envelopeReferences int) graphRejectSample {
	sample := graphRejectSample{Reason: string(reason), DataBytes: len(data), EnvelopeReferences: envelopeReferences}
	scan := data
	if len(scan) > maxGraphRejectScanBytes {
		scan = scan[:maxGraphRejectScanBytes]
	}
	if len(scan) == 0 {
		return sample
	}
	sample.InlineMarkerPresent = hasNativeInlineMarker(scan)
	sample.Fields = scanTopLevelFields(scan)
	if sample.InlineMarkerPresent {
		sample.DeclaredOrdinals, sample.LiteralCount, sample.MaxLiteralBytes = scanInlineMarkerShape(scan)
	}
	return sample
}

// scanTopLevelFields is a best-effort, bounded scan: it stops at the first
// malformed field instead of failing, since it only feeds a diagnostic
// sample for an already-rejected node.
func scanTopLevelFields(data []byte) []fieldOccurrence {
	counts := make(map[int]int)
	var order []int
	for len(data) > 0 {
		number, kind, consumed := protowire.ConsumeTag(data)
		if consumed < 0 {
			break
		}
		length := protowire.ConsumeFieldValue(number, kind, data[consumed:])
		if length < 0 {
			break
		}
		field := int(number)
		if _, tracked := counts[field]; !tracked && len(order) >= maxGraphRejectSampleFields {
			data = data[consumed+length:]
			continue
		}
		counts[field]++
		if counts[field] == 1 {
			order = append(order, field)
		}
		data = data[consumed+length:]
	}
	result := make([]fieldOccurrence, 0, len(order))
	for _, field := range order {
		result = append(result, fieldOccurrence{Number: field, Count: counts[field]})
	}
	return result
}

// scanInlineMarkerShape best-effort decodes the declared ordinal count and
// literal leaf shape of an inline-marker node, independent of whether the
// node is actually well formed.
func scanInlineMarkerShape(data []byte) (ordinals, literals, maxLiteralBytes int) {
	number, first, data, err := bytesField(data)
	if err != nil || number != 1 {
		return 0, 0, 0
	}
	field, indices, remaining, err := bytesField(first)
	if err != nil || field != 2 || len(remaining) != 0 {
		return 0, 0, 0
	}
	for len(indices) > 0 {
		_, consumed := protowire.ConsumeVarint(indices)
		if consumed < 0 {
			break
		}
		ordinals++
		indices = indices[consumed:]
	}
	for len(data) > 0 {
		outerNumber, outerValue, rest, err := bytesField(data)
		if err != nil || outerNumber != 1 {
			break
		}
		innerField, payload, innerRemaining, err := bytesField(outerValue)
		if err != nil || innerField != 1 || len(innerRemaining) != 0 {
			break
		}
		literals++
		if len(payload) > maxLiteralBytes {
			maxLiteralBytes = len(payload)
		}
		data = rest
	}
	return
}
