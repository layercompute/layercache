package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestS3StageRejectsFailedStreams(t *testing.T) {
	for _, mode := range []string{"small", "full buffer", "after uploaded part"} {
		t.Run(mode, func(t *testing.T) {
			endpoint := newMemoryS3Endpoint(t)
			storage := streamTestS3(t, endpoint)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			body := failedStreamBody(ctx, endpoint, mode)
			staged, err := storage.Stage(ctx, "fixture", body, 64<<20, "application/octet-stream")
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("failed input must preserve its error: staged size=%d, error=%v", staged.Size, err)
			}
			endpoint.mutex.Lock()
			defer endpoint.mutex.Unlock()
			if len(endpoint.objects) != 0 || len(endpoint.parts) != 0 {
				t.Errorf("failed input retained completed objects or multipart uploads: objects=%d uploads=%d", len(endpoint.objects), len(endpoint.parts))
			}
		})
	}
}

func TestS3StagePreservesCompleteStreamsAndQuota(t *testing.T) {
	for _, size := range []int{0, 7, (16 << 20) + 7} {
		endpoint := newMemoryS3Endpoint(t)
		storage := streamTestS3(t, endpoint)
		payload := bytes.Repeat([]byte("x"), size)
		staged, err := storage.Stage(context.Background(), "complete", bytes.NewReader(payload), int64(size), "application/octet-stream")
		if err != nil {
			t.Fatal(err)
		}
		expected := sha256.Sum256(payload)
		if staged.Size != int64(size) || staged.Digest != hex.EncodeToString(expected[:]) {
			t.Fatal("complete stream changed its size or digest")
		}
		endpoint.mutex.Lock()
		restored, exists := endpoint.objects["complete"]
		matches := exists && bytes.Equal(restored.data, payload) && len(endpoint.parts) == 0
		endpoint.mutex.Unlock()
		if !matches {
			t.Fatal("complete stream did not retain exact bytes or left a multipart upload")
		}
	}
	endpoint := newMemoryS3Endpoint(t)
	storage := streamTestS3(t, endpoint)
	if _, err := storage.Stage(context.Background(), "oversize", strings.NewReader("oversize"), 2, "application/octet-stream"); !errors.Is(err, ErrQuota) {
		t.Fatalf("capacity refusal must remain ErrQuota: %v", err)
	}
	endpoint.mutex.Lock()
	defer endpoint.mutex.Unlock()
	if len(endpoint.objects) != 0 || len(endpoint.parts) != 0 {
		t.Fatal("capacity refusal retained object bytes or multipart uploads")
	}
}

// Check the publication boundary as well as S3 staging. These native keys use
// the actual PostgreSQL metadata store, production SDK and existing S3 fixture.
func TestCloudFailedStreamsNeverPublish(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	for _, integration := range []string{"nx", "turbo"} {
		for _, mode := range []string{"small", "after uploaded part"} {
			t.Run(integration+"/"+mode, func(t *testing.T) {
				fixture := newActionsHTTPFixture(t, 64<<20)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				key := artifact.Key{Project: fixture.store.config.Project, Integration: integration, Compatibility: "linux-stream-schema1", Native: "interrupted"}
				_, created, err := fixture.store.Put(ctx, key, artifact.Metadata{}, failedStreamBody(ctx, fixture.objects, mode))
				if err == nil || created {
					t.Error("failed input was acknowledged as a committed artifact")
				}
				if _, err := fixture.store.Head(ctx, key); !errors.Is(err, ErrNotFound) {
					t.Errorf("failed input published lookup metadata: %v", err)
				}
				stats, err := fixture.store.Stats(ctx)
				if err != nil || stats.Entries != 0 || stats.Artifacts != 0 || stats.UsageBytes != 0 {
					t.Errorf("failed input retained metadata: %+v, error=%v", stats, err)
				}
				var uploads int
				if err := fixture.store.database.QueryRowContext(ctx, "SELECT count(*) FROM layercache_uploads_v1").Scan(&uploads); err != nil || uploads != 0 {
					t.Errorf("failed input retained staging rows: count=%d, error=%v", uploads, err)
				}
				fixture.objects.mutex.Lock()
				defer fixture.objects.mutex.Unlock()
				if len(fixture.objects.objects) != 0 || len(fixture.objects.parts) != 0 {
					t.Error("failed input retained S3 object bytes or multipart uploads")
				}
			})
		}
	}
}

func streamTestS3(t *testing.T, endpoint *memoryS3Endpoint) *s3BlobStore {
	t.Helper()
	client, err := minio.New(strings.TrimPrefix(endpoint.server.URL, "http://"), &minio.Options{
		Creds:  credentials.NewStaticV4("fixture-access", "fixture-secret", ""),
		Region: "us-east-1", BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &s3BlobStore{client: client, bucket: "cache", namespace: "fixture"}
}

func failedStreamBody(ctx context.Context, endpoint *memoryS3Endpoint, mode string) io.Reader {
	switch mode {
	case "small":
		return io.MultiReader(strings.NewReader("partial"), &streamFailure{})
	case "full buffer":
		return &streamFailure{remaining: 16 << 20, simultaneous: true}
	default:
		return &streamFailure{remaining: 16 << 20, beforeFailure: func() error {
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				endpoint.mutex.Lock()
				hasPart := false
				for _, parts := range endpoint.parts {
					hasPart = hasPart || len(parts) != 0
				}
				endpoint.mutex.Unlock()
				if hasPart {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
				}
			}
		}}
	}
}

type streamFailure struct {
	remaining       int
	simultaneous    bool
	beforeFailure   func() error
	failureReturned bool
}

func (reader *streamFailure) Read(buffer []byte) (int, error) {
	if reader.failureReturned {
		return 0, io.EOF
	}
	if reader.remaining > 0 {
		count := min(len(buffer), reader.remaining)
		for index := range buffer[:count] {
			buffer[index] = 'x'
		}
		reader.remaining -= count
		if reader.remaining == 0 && reader.simultaneous {
			reader.failureReturned = true
			return count, io.ErrUnexpectedEOF
		}
		return count, nil
	}
	if reader.beforeFailure != nil {
		if err := reader.beforeFailure(); err != nil {
			return 0, err
		}
		reader.beforeFailure = nil
	}
	reader.failureReturned = true
	return 0, io.ErrUnexpectedEOF
}
