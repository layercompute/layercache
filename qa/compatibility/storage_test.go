package compatibility_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
)

func TestCompatibilityIsolationDoesNotDuplicateIdenticalPayloads(t *testing.T) {
	ctx := context.Background()
	payload := []byte("identical portable output bytes")
	store, err := artifact.Open(ctx, t.TempDir(), int64(len(payload)), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first := artifact.Key{Project: "github.com/acme/widget", Integration: "turbo", Compatibility: "linux-amd64-test-v1", Native: "same-native-key"}
	second := first
	second.Compatibility = "linux-arm64-test-v1"
	if _, _, err := store.Put(ctx, first, artifact.Metadata{}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	// Matching native keys and equal potential outputs do not grant a logical
	// hit in a profile that has never published the artifact.
	if _, err := store.Head(ctx, second); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("unpublished architecture lookup = %v, want miss", err)
	}
	if _, _, err := store.Put(ctx, second, artifact.Metadata{}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats(ctx)
	if err != nil || stats.Artifacts != 1 || stats.Entries != 2 || stats.UsageBytes != int64(len(payload)) {
		t.Fatalf("profile aliases = %+v, %v; want two entries and one charged blob", stats, err)
	}
	if err := store.Delete(ctx, first); err != nil {
		t.Fatal(err)
	}
	_, body, err := store.Get(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("surviving architecture reference changed: read=%v close=%v", readErr, closeErr)
	}
	stats, err = store.Stats(ctx)
	if err != nil || stats.Artifacts != 1 || stats.Entries != 1 || stats.UsageBytes != int64(len(payload)) {
		t.Fatalf("one profile removal changed shared blob accounting: %+v, %v", stats, err)
	}
}
