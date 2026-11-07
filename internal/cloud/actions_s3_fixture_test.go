package cloud

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/artifact"
)

// stageVerifiedTestRead stands in for the gateway's bounded verification
// staging. Like it, nothing is returned until the complete size and digest
// match, and mismatched bytes report ErrCorrupt.
func stageVerifiedTestRead(t *testing.T) ActionsReadStager {
	directory := t.TempDir()
	return func(_ context.Context, entry artifact.Entry, body io.Reader) (*os.File, func(), error) {
		file, err := os.CreateTemp(directory, "actions-read-*")
		if err != nil {
			return nil, nil, err
		}
		cleanup := func() { _ = file.Close(); _ = os.Remove(file.Name()) }
		hash := sha256.New()
		written, err := io.Copy(io.MultiWriter(file, hash), body)
		if err == nil && (written != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.Digest) {
			err = ErrCorrupt
		}
		if err == nil {
			_, err = file.Seek(0, io.SeekStart)
		}
		if err != nil {
			cleanup()
			if errors.Is(err, ErrCorrupt) {
				return nil, nil, ErrCorrupt
			}
			return nil, nil, err
		}
		return file, cleanup, nil
	}
}

// This fixture exercises the production S3 SDK without external S3 credentials.
// It implements only the object and multipart operations used by these tests.
type memoryS3Object struct {
	data     []byte
	metadata http.Header
}

type memoryS3Endpoint struct {
	server   *httptest.Server
	mutex    sync.Mutex
	objects  map[string]memoryS3Object
	parts    map[string]map[int][]byte
	nextID   int
	failCopy atomic.Bool
	readGate *memoryS3ReadGate
	// objectGets counts object body reads; metadata probes must add none.
	objectGets atomic.Int64
	// presigned counts requests made with a presigned URL.
	presigned atomic.Int64
}

type memoryS3ReadGate struct {
	key                    string
	entered, released      chan struct{}
	enterOnce, releaseOnce sync.Once
}

func (gate *memoryS3ReadGate) release() { gate.releaseOnce.Do(func() { close(gate.released) }) }

func newMemoryS3Endpoint(t *testing.T) *memoryS3Endpoint {
	t.Helper()
	endpoint := &memoryS3Endpoint{objects: make(map[string]memoryS3Object), parts: make(map[string]map[int][]byte)}
	endpoint.server = httptest.NewServer(http.HandlerFunc(endpoint.serve))
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

func (endpoint *memoryS3Endpoint) serve(writer http.ResponseWriter, request *http.Request) {
	endpoint.mutex.Lock()
	locked := true
	defer func() {
		if locked {
			endpoint.mutex.Unlock()
		}
	}()
	key := strings.TrimPrefix(request.URL.Path, "/cache/")
	query := request.URL.Query()
	writeXML := func(value string) {
		writer.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(writer, value)
	}
	if request.Method == http.MethodHead && request.URL.Path == "/cache/" {
		return
	}
	if request.Method == http.MethodPost && query.Has("uploads") {
		endpoint.nextID++
		id := strconv.Itoa(endpoint.nextID)
		endpoint.parts[id] = make(map[int][]byte)
		writeXML("<InitiateMultipartUploadResult><Bucket>cache</Bucket><Key>" + key + "</Key><UploadId>" + id + "</UploadId></InitiateMultipartUploadResult>")
		return
	}
	if id := query.Get("uploadId"); id != "" {
		switch request.Method {
		case http.MethodPut:
			data, err := readMemoryS3Body(request)
			if err != nil {
				http.Error(writer, "fixture read failed", http.StatusBadRequest)
				return
			}
			number, _ := strconv.Atoi(query.Get("partNumber"))
			endpoint.parts[id][number] = data
			hash := md5.Sum(data)
			writer.Header().Set("ETag", "\""+hex.EncodeToString(hash[:])+"\"")
		case http.MethodPost:
			numbers := make([]int, 0, len(endpoint.parts[id]))
			for number := range endpoint.parts[id] {
				numbers = append(numbers, number)
			}
			sort.Ints(numbers)
			var data []byte
			for _, number := range numbers {
				data = append(data, endpoint.parts[id][number]...)
			}
			endpoint.objects[key] = memoryS3Object{data: data, metadata: request.Header.Clone()}
			delete(endpoint.parts, id)
			writeXML("<CompleteMultipartUploadResult><Location>/cache/" + key + "</Location><Bucket>cache</Bucket><Key>" + key + "</Key><ETag>\"fixture\"</ETag></CompleteMultipartUploadResult>")
		case http.MethodDelete:
			delete(endpoint.parts, id)
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.Error(writer, "unsupported fixture multipart operation", http.StatusBadRequest)
		}
		return
	}
	if source := request.Header.Get("X-Amz-Copy-Source"); request.Method == http.MethodPut && source != "" {
		if endpoint.failCopy.Load() {
			writer.WriteHeader(http.StatusBadRequest)
			writeXML("<Error><Code>InvalidRequest</Code><Message>fixture copy failure</Message></Error>")
			return
		}
		source, _ = url.PathUnescape(source)
		object, exists := endpoint.objects[strings.TrimPrefix(strings.TrimPrefix(source, "/"), "cache/")]
		if !exists {
			http.NotFound(writer, request)
			return
		}
		object.metadata = request.Header.Clone()
		endpoint.objects[key] = object
		writeXML("<CopyObjectResult><LastModified>2026-10-14T00:00:00.000Z</LastModified><ETag>\"fixture\"</ETag></CopyObjectResult>")
		return
	}
	if signature := query.Get("X-Amz-Signature"); signature != "" {
		// Presigned requests: enforce expiry and, like R2, the signed
		// x-amz-checksum-sha256 header. Signatures themselves are not checked.
		signedAt, err := time.Parse("20060102T150405Z", query.Get("X-Amz-Date"))
		expires, _ := strconv.Atoi(query.Get("X-Amz-Expires"))
		if err != nil || !time.Now().Before(signedAt.Add(time.Duration(expires)*time.Second)) {
			http.Error(writer, "AccessDenied", http.StatusForbidden)
			return
		}
		endpoint.presigned.Add(1)
		if request.Method == http.MethodPut {
			signed := strings.Contains(query.Get("X-Amz-SignedHeaders"), "x-amz-checksum-sha256")
			declared := request.Header.Get("X-Amz-Checksum-Sha256")
			if signed && declared == "" {
				http.Error(writer, "SignatureDoesNotMatch", http.StatusForbidden)
				return
			}
			data, err := io.ReadAll(request.Body)
			if err != nil {
				http.Error(writer, "fixture read failed", http.StatusBadRequest)
				return
			}
			sum := sha256.Sum256(data)
			if declared != base64.StdEncoding.EncodeToString(sum[:]) {
				http.Error(writer, "BadDigest", http.StatusBadRequest)
				return
			}
			endpoint.objects[key] = memoryS3Object{data: data, metadata: http.Header{}}
			return
		}
	}
	if request.Method == http.MethodGet && (request.URL.Path == "/cache/" || request.URL.Path == "/cache") {
		type content struct {
			Key          string `xml:"Key"`
			Size         int    `xml:"Size"`
			LastModified string `xml:"LastModified"`
		}
		list := struct {
			XMLName     xml.Name  `xml:"ListBucketResult"`
			Name        string    `xml:"Name"`
			IsTruncated bool      `xml:"IsTruncated"`
			Contents    []content `xml:"Contents"`
		}{Name: "cache"}
		for name, object := range endpoint.objects {
			if strings.HasPrefix(name, query.Get("prefix")) {
				list.Contents = append(list.Contents, content{name, len(object.data), "2026-10-14T00:00:00.000Z"})
			}
		}
		writer.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(writer).Encode(list)
		return
	}
	object, exists := endpoint.objects[key]
	if request.Method == http.MethodDelete {
		delete(endpoint.objects, key)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if !exists {
		http.NotFound(writer, request)
		return
	}
	// Objects are immutable values. Release the map lock before a potentially
	// blocked download so maintenance can operate concurrently with its reader.
	gate := endpoint.readGate
	endpoint.mutex.Unlock()
	locked = false
	for name, values := range object.metadata {
		if strings.HasPrefix(strings.ToLower(name), "x-amz-meta-") {
			writer.Header()[name] = values
		}
	}
	writer.Header().Set("Content-Length", strconv.Itoa(len(object.data)))
	writer.Header().Set("Last-Modified", "Wed, 14 Oct 2026 00:00:00 GMT")
	writer.Header().Set("ETag", "\"fixture\"")
	if request.Method == http.MethodGet {
		endpoint.objectGets.Add(1)
		if gate != nil && key == gate.key {
			gate.enterOnce.Do(func() { close(gate.entered) })
			select {
			case <-gate.released:
			case <-request.Context().Done():
				return
			}
		}
		_, _ = writer.Write(object.data)
	} else if request.Method != http.MethodHead {
		http.Error(writer, "unsupported fixture operation", http.StatusBadRequest)
	}
}

func readMemoryS3Body(request *http.Request) ([]byte, error) {
	if !strings.Contains(request.Header.Get("Content-Encoding"), "aws-chunked") && request.Header.Get("X-Amz-Decoded-Content-Length") == "" {
		return io.ReadAll(request.Body)
	}
	reader := bufio.NewReader(request.Body)
	var data []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeText, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		size, err := strconv.ParseInt(sizeText, 16, 64)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			return data, nil
		}
		start := len(data)
		data = append(data, make([]byte, size)...)
		if _, err := io.ReadFull(reader, data[start:]); err != nil {
			return nil, err
		}
		if _, err := reader.ReadString('\n'); err != nil {
			return nil, err
		}
	}
}
