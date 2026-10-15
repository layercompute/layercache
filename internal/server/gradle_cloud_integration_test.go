package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/cloud"
)

// Exercise the real PostgreSQL metadata store and production minio SDK. Each
// rejected small stream must fail before S3 receives any object bytes. The
// endpoint accepts the SDK's multipart/copy operations, so a swallowed reader
// error causes a real metadata publication rather than a fixture error.
func TestGradleCloudUploadReaderFailuresAgainstPostgresAndS3(t *testing.T) {
	postgresURL := os.Getenv("LAYER_CACHE_QA_POSTGRES_URL")
	if postgresURL == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := sql.Open("pgx", postgresURL)
	if err != nil {
		t.Fatal("open disposable PostgreSQL")
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("gradle_upload_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("create isolated Gradle upload schema")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("drop isolated Gradle upload schema")
		}
	})
	scoped, err := url.Parse(postgresURL)
	if err != nil {
		t.Fatal("parse disposable PostgreSQL URL")
	}
	query := scoped.Query()
	query.Set("search_path", schema)
	scoped.RawQuery = query.Encode()

	var inspections, objectWrites atomic.Int64
	objects := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodHead && request.URL.Path == "/cache/":
			inspections.Add(1)
		case request.Method == http.MethodGet && request.URL.Query().Get("list-type") == "2":
			writer.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(writer, "<ListBucketResult><Name>cache</Name><IsTruncated>false</IsTruncated></ListBucketResult>")
		case request.Method == http.MethodPost && request.URL.Query().Has("uploads"):
			writer.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(writer, "<InitiateMultipartUploadResult><Bucket>cache</Bucket><Key>fixture</Key><UploadId>fixture</UploadId></InitiateMultipartUploadResult>")
		case request.Method == http.MethodPut && request.URL.Query().Get("partNumber") != "":
			objectWrites.Add(1)
			_, _ = io.Copy(io.Discard, request.Body)
			writer.Header().Set("ETag", `"fixture"`)
		case request.Method == http.MethodPost && request.URL.Query().Get("uploadId") != "":
			writer.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(writer, "<CompleteMultipartUploadResult><Location>/cache/fixture</Location><Bucket>cache</Bucket><Key>fixture</Key><ETag>\"fixture\"</ETag></CompleteMultipartUploadResult>")
		case request.Method == http.MethodPut && request.Header.Get("X-Amz-Copy-Source") != "":
			objectWrites.Add(1)
			writer.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(writer, "<CopyObjectResult><LastModified>2026-10-15T00:00:00.000Z</LastModified><ETag>\"fixture\"</ETag></CopyObjectResult>")
		case request.Method == http.MethodDelete:
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(objects.Close)
	cfg := gatewayTestConfig(t, "gradle-cloud-upload")
	cfg.MaxBytes = 64
	cfg.CloudPostgresURL, cfg.CloudS3Endpoint = scoped.String(), objects.URL
	cfg.CloudS3Bucket, cfg.CloudS3Region, cfg.CloudS3UsePathStyle = "cache", "us-east-1", true
	cfg.CloudS3AccessKey, cfg.CloudS3SecretKey = "fixture-access", "fixture-secret"
	instance, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if _, ok := instance.store.(*cloud.Store); !ok || inspections.Load() == 0 {
		t.Fatal("requires the actual cloud store and production S3 SDK")
	}
	token := gradleToken(t, cfg, cfg.CompatibilityID, "gradle", access.CapabilityWrite)
	for index, test := range []struct {
		name, body string
		size       int64
		want       int
		cancel     bool
		pool       bool
		bodyEOF    bool
	}{
		{name: "unknown length exceeds capacity", body: strings.Repeat("x", int(cfg.MaxBytes+1)), size: -1, want: 413},
		{name: "truncated declared stream", body: "partial", size: 20, want: 400},
		{name: "long declared stream", body: "extra", size: 1, want: 400},
		{name: "underlying unexpected EOF", body: "partial", size: -1, want: 400, bodyEOF: true},
		{name: "cancelled during body read", body: "cancelled", size: -1, want: 400, cancel: true},
		{name: "wrapped streaming pool exhaustion", body: "partial body", size: -1, want: 413, pool: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			hash := fmt.Sprintf("rejected-%d", index)
			key := artifact.Key{Integration: "gradle", Project: cfg.ProjectID, Compatibility: cfg.CompatibilityID, Native: hash}
			t.Cleanup(func() { _ = instance.store.Delete(ctx, key) })
			writesBefore := objectWrites.Load()
			requestContext, requestCancel := context.WithCancel(ctx)
			defer requestCancel()
			body := &nxDrainingBody{reader: strings.NewReader(test.body)}
			if test.bodyEOF {
				body.reader = io.MultiReader(strings.NewReader(test.body), gradleUnexpectedEOFBody{})
			}
			if test.cancel {
				body.afterFirst = requestCancel
			}
			var stream io.ReadCloser = body
			if test.pool {
				probe := filepath.Join(t.TempDir(), "pool-probe")
				if err := os.Mkdir(probe, 0700); err != nil {
					t.Fatal(err)
				}
				body.afterFirst = func() {
					if err := os.Remove(probe); err != nil {
						t.Error(err)
					}
				}
				stream = &poolAdmissionBody{ReadCloser: body, pool: StoragePoolConfig{Path: probe}}
			}
			request := httptest.NewRequest(http.MethodPut, gradleCachePrefix+hash, stream).WithContext(requestContext)
			request.ContentLength = test.size
			request.Header.Set("Authorization", gradleBasic(token))
			response := httptest.NewRecorder()
			instance.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Errorf("cloud upload HTTP %d, want %d; response=%q", response.Code, test.want, response.Body.String())
			}
			if !body.closed {
				t.Error("rejected upload body remained open")
			}
			if _, err := instance.store.Head(ctx, key); !errors.Is(err, artifact.ErrNotFound) {
				t.Errorf("rejected cloud upload published lookup metadata: %v", err)
			}
			stats, err := instance.store.Stats(ctx)
			if err != nil || stats.Entries != 0 || stats.Artifacts != 0 || stats.UsageBytes != 0 {
				t.Errorf("rejected cloud upload retained artifact metadata: %+v, error=%v", stats, err)
			}
			if got := gradleRequest(instance, http.MethodGet, gradleCachePrefix+hash, token, ""); got.Code != 404 {
				t.Errorf("rejected cloud upload became visible: HTTP %d", got.Code)
			}
			var stages int
			if err := admin.QueryRowContext(ctx, "SELECT count(*) FROM "+schema+".layercache_uploads_v1").Scan(&stages); err != nil || stages != 0 {
				t.Errorf("rejected cloud upload retained staging rows: count=%d, error=%v", stages, err)
			}
			if objectWrites.Load() != writesBefore {
				t.Error("rejected cloud upload sent object bytes to S3")
			}
		})
	}
}

type gradleUnexpectedEOFBody struct{}

func (gradleUnexpectedEOFBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestGradleUploadReaderTerminalFailureSurvivesFullBufferRead(t *testing.T) {
	for _, failure := range []error{io.ErrUnexpectedEOF, context.Canceled, fmt.Errorf("streaming pool: %w", artifact.ErrQuota)} {
		t.Run(failure.Error(), func(t *testing.T) {
			body := &gradleFullBufferFailure{err: failure}
			reader := &gradleUploadReader{ctx: context.Background(), body: body, expected: -1, maximum: 64}
			// Like the multipart SDK, ReadFull discards a simultaneous error
			// when the returned bytes exactly fill its buffer.
			if count, err := io.ReadFull(reader, make([]byte, 3)); count != 3 || err != nil {
				t.Fatalf("full initial buffer: count=%d, error=%v", count, err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				count, err := reader.Read(make([]byte, 3))
				if count != 0 || !errors.Is(err, failure) || err == failure {
					t.Fatalf("terminal failure was lost or unwrapped: count=%d, error=%v", count, err)
				}
			}
			if body.reads != 1 || reader.terminalErr != failure {
				t.Fatalf("terminal failure retried the body or changed identity: reads=%d, error=%v", body.reads, reader.terminalErr)
			}
		})
	}
}

type gradleFullBufferFailure struct {
	err   error
	reads int
}

func (body *gradleFullBufferFailure) Read(buffer []byte) (int, error) {
	body.reads++
	if body.reads == 1 {
		return copy(buffer, "abc"), body.err
	}
	return 0, io.EOF
}
