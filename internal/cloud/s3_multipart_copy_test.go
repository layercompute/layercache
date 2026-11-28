package cloud

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCommitLargeS3ObjectCopiesRangesAndAbortsOnFailure(t *testing.T) {
	const size = 6_261_055_330
	const totalParts = (size + copyPartBytes - 1) / copyPartBytes
	const digest = "dcd3dd41d6d431675ca13a3bec85b49a101d2bfff929c86b5a6ddee9e479d3b07"
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%t", fail), func(t *testing.T) {
			var copied, aborted, completed atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				switch {
				case r.Method == http.MethodHead && r.URL.Path == "/cache/":
				case r.Method == http.MethodHead && r.URL.Path == "/cache/staging":
					w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
					w.Header().Set("ETag", `"source"`)
					w.Header().Set("Last-Modified", "Thu, 29 Oct 2026 10:00:00 GMT")
				case r.Method == http.MethodPost && query.Has("uploads"):
					if r.Header.Get("X-Amz-Meta-Sha256") != digest || r.Header.Get("X-Amz-Meta-Size") != strconv.Itoa(size) || r.Header.Get("Content-Type") != "application/gzip" {
						t.Error("multipart initiation lost committed object metadata")
					}
					_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><Bucket>cache</Bucket><Key>object</Key><UploadId>upload</UploadId></InitiateMultipartUploadResult>`)
				case r.Method == http.MethodPut && query.Get("uploadId") == "upload":
					part, err := strconv.Atoi(query.Get("partNumber"))
					if err != nil || part < 1 || part > totalParts {
						t.Error("invalid part number")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					start := int64(part-1) * copyPartBytes
					want := fmt.Sprintf("bytes=%d-%d", start, min(start+copyPartBytes, size)-1)
					if r.Header.Get("X-Amz-Copy-Source") != "cache/staging" || r.Header.Get("X-Amz-Copy-Source-Range") != want {
						t.Errorf("part %d did not use the expected object and range %s", part, want)
					}
					copied.Add(1)
					if fail && part == 2 {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `<Error><Code>InvalidRequest</Code></Error>`)
						return
					}
					_, _ = io.WriteString(w, `<CopyPartResult><ETag>"part"</ETag></CopyPartResult>`)
				case r.Method == http.MethodPost && query.Get("uploadId") == "upload":
					completed.Add(1)
					body, _ := io.ReadAll(r.Body)
					if strings.Count(string(body), "<PartNumber>") != totalParts {
						t.Error("multipart completion omitted parts")
					}
					_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><Bucket>cache</Bucket><Key>object</Key><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
				case r.Method == http.MethodDelete && query.Get("uploadId") == "upload":
					aborted.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected S3 request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer endpoint.Close()
			ctx := context.Background()
			storage, err := newS3BlobStore(ctx, Config{
				S3Endpoint: endpoint.URL, S3Bucket: "cache", S3Region: "us-east-1",
				S3AccessKey: "fixture", S3SecretKey: "fixture", S3PathStyle: true,
			}, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			err = storage.Commit(ctx, stagedBlob{Key: "staging", Digest: digest, MediaType: "application/gzip", Size: size}, "object")
			if fail {
				if err == nil || aborted.Load() != 1 || completed.Load() != 0 {
					t.Fatalf("failed publication did not abort: copied=%d, aborted=%d, completed=%d, error=%v", copied.Load(), aborted.Load(), completed.Load(), err)
				}
			} else if err != nil || copied.Load() != totalParts || aborted.Load() != 0 || completed.Load() != 1 {
				t.Fatalf("large object did not publish: copied=%d, aborted=%d, completed=%d, error=%v", copied.Load(), aborted.Load(), completed.Load(), err)
			}
		})
	}
}
