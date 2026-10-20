package cloud

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
)

func TestCloudCompatibilityIsolationSharesOnePayloadObject(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := context.Background()
	payload := []byte("identical portable output bytes")
	fixture := newActionsHTTPFixture(t, int64(len(payload)))
	first := artifact.Key{Project: fixture.store.config.Project, Integration: "turbo", Compatibility: "linux-amd64-test-v1", Native: "same-native-key"}
	second := first
	second.Compatibility = "linux-arm64-test-v1"
	if _, _, err := fixture.store.Put(ctx, first, artifact.Metadata{}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Head(ctx, second); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("unpublished architecture lookup = %v, want miss", err)
	}
	if _, _, err := fixture.store.Put(ctx, second, artifact.Metadata{}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	stats, err := fixture.store.Stats(ctx)
	if err != nil || stats.Artifacts != 1 || stats.Entries != 2 || stats.UsageBytes != int64(len(payload)) {
		t.Fatalf("hosted profile aliases = %+v, %v; want one charged blob", stats, err)
	}
	fixture.objects.mutex.Lock()
	objects := len(fixture.objects.objects)
	fixture.objects.mutex.Unlock()
	if objects != 1 {
		t.Fatalf("stored S3 objects = %d, want one payload for both profiles", objects)
	}
	if err := fixture.store.Delete(ctx, first); err != nil {
		t.Fatal(err)
	}
	_, body, err := fixture.store.Get(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("surviving hosted profile changed: read=%v close=%v", readErr, closeErr)
	}
	stats, err = fixture.store.Stats(ctx)
	if err != nil || stats.Artifacts != 1 || stats.Entries != 1 || stats.UsageBytes != int64(len(payload)) {
		t.Fatalf("profile deletion changed shared hosted bytes: %+v, %v", stats, err)
	}
}
