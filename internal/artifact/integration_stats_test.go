package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIntegrationStatsPartitionSharedPayloadsAndReferences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, root, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	put := func(integration, native, payload string) Key {
		t.Helper()
		key := Key{Integration: integration, Project: "project", Compatibility: "profile", Native: native}
		if _, _, err := store.Put(ctx, key, Metadata{}, strings.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
		return key
	}
	first := put("gradle", "first", "same")
	put("gradle", "second", "same")
	turbo := put("turbo", "third", "same")
	put("nx", "fourth", "nxonly")
	put("private-integration-A", "fifth", "one")
	put("private-integration-A", "sixth", "two")
	put("private-integration-B", "seventh", "two")
	put("gradle", "empty", "")
	otherProject := first
	otherProject.Project = "another-project"
	if _, _, err := store.Put(ctx, otherProject, Metadata{}, strings.NewReader("same")); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []IntegrationStats{
		{Integration: "gradle", UsageBytes: 0, Artifacts: 1, Entries: 1},
		{Integration: "nx", UsageBytes: 6, Artifacts: 1, Entries: 1},
		{Integration: "other", UsageBytes: 3, Artifacts: 1, Entries: 1},
		{Integration: "shared", UsageBytes: 7, Artifacts: 2, Entries: 6},
	}
	if !reflect.DeepEqual(stats.StorageByIntegration, want) || stats.UsageBytes != 16 || stats.Artifacts != 5 || stats.Entries != 9 {
		t.Fatalf("partition = %+v, want %+v", stats, want)
	}
	assertStoragePartition(t, stats)
	encoded, err := json.Marshal(stats)
	if err != nil || bytes.Contains(encoded, []byte("private-integration")) {
		t.Fatalf("statistics exposed unbounded integration identity: %s, %v", encoded, err)
	}
	// Removing the only Turbo reference changes classification without removing
	// the shared bytes or the other project's compatible reference.
	if err := store.Delete(ctx, turbo); err != nil {
		t.Fatal(err)
	}
	stats, err = store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageByIntegration[0] != (IntegrationStats{Integration: "gradle", UsageBytes: 4, Artifacts: 2, Entries: 4}) {
		t.Fatalf("surviving aliases = %+v", stats.StorageByIntegration)
	}
	assertStoragePartition(t, stats)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	offline, err := ReadStats(ctx, root)
	if err != nil || !reflect.DeepEqual(offline.StorageByIntegration, stats.StorageByIntegration) || offline.UsageBytes != stats.UsageBytes {
		t.Fatalf("offline partition differs: %+v, %v", offline, err)
	}
}

func TestIntegrationStatsIncludeUnclassifiedArtifactsWithoutInventingEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	empty, err := store.Stats(ctx)
	if err != nil || empty.StorageByIntegration == nil || len(empty.StorageByIntegration) != 0 {
		t.Fatalf("empty supported partition = %+v, %v", empty, err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO artifacts(digest,size,created_at,last_accessed_at) VALUES(?,5,0,0)`, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []IntegrationStats{{Integration: "other", UsageBytes: 5, Artifacts: 1, Entries: 0}}
	if !reflect.DeepEqual(stats.StorageByIntegration, want) {
		t.Fatalf("unclassified artifacts = %+v, want %+v", stats.StorageByIntegration, want)
	}
	assertStoragePartition(t, stats)
}

func assertStoragePartition(t *testing.T, stats Stats) {
	t.Helper()
	var used, blobs, entries int64
	for _, item := range stats.StorageByIntegration {
		used += item.UsageBytes
		blobs += item.Artifacts
		entries += item.Entries
	}
	if used != stats.UsageBytes || blobs != stats.Artifacts || entries != stats.Entries {
		t.Fatalf("partition totals %d/%d/%d differ from %+v", used, blobs, entries, stats)
	}
}
