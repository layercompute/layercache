package cloud

import (
	"context"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
)

func TestCloudIntegrationStatsPartitionDeduplicatedBytesWithinProject(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := context.Background()
	fixture := newActionsHTTPFixture(t, 1<<20)
	store := fixture.store
	put := func(integration, native, payload string) artifact.Key {
		t.Helper()
		key := artifact.Key{Integration: integration, Project: store.config.Project, Compatibility: "profile", Native: native}
		if _, _, err := store.Put(ctx, key, artifact.Metadata{}, strings.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
		return key
	}
	empty, err := store.Stats(ctx)
	if err != nil || empty.StorageByIntegration == nil || len(empty.StorageByIntegration) != 0 {
		t.Fatalf("empty supported partition = %+v, %v", empty, err)
	}
	first := put("gradle", "first", "same")
	second := put("gradle", "second", "same")
	turbo := put("turbo", "third", "same")
	put("nx", "fourth", "nxonly")
	put("private-A", "fifth", "one")
	put("private-A", "sixth", "two")
	put("private-B", "seventh", "two")
	put("gradle", "empty", "")
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []artifact.IntegrationStats{
		{Integration: "gradle", UsageBytes: 0, Artifacts: 1, Entries: 1},
		{Integration: "nx", UsageBytes: 6, Artifacts: 1, Entries: 1},
		{Integration: "other", UsageBytes: 3, Artifacts: 1, Entries: 1},
		{Integration: "shared", UsageBytes: 7, Artifacts: 2, Entries: 5},
	}
	if !reflect.DeepEqual(stats.StorageByIntegration, want) || stats.UsageBytes != 16 || stats.Artifacts != 5 || stats.Entries != 8 {
		t.Fatalf("cloud partition = %+v, want %+v", stats, want)
	}
	assertCloudStoragePartition(t, stats)
	// Identical bytes in another project are charged independently. Its
	// integration cannot change this project's category or expose its references.
	otherConfig := store.config
	otherConfig.Project = "github.com/acme/other"
	other, err := Open(ctx, otherConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	otherKey := artifact.Key{Integration: "actions", Project: otherConfig.Project, Compatibility: "profile", Native: "same"}
	if _, _, err := other.Put(ctx, otherKey, artifact.Metadata{}, strings.NewReader("same")); err != nil {
		t.Fatal(err)
	}
	otherStats, err := other.Stats(ctx)
	if err != nil || !reflect.DeepEqual(otherStats.StorageByIntegration, []artifact.IntegrationStats{{Integration: "actions", UsageBytes: 4, Artifacts: 1, Entries: 1}}) {
		t.Fatalf("other project = %+v, %v", otherStats, err)
	}
	afterOther, err := store.Stats(ctx)
	if err != nil || !reflect.DeepEqual(afterOther, stats) {
		t.Fatalf("foreign project changed partition = %+v, %v", afterOther, err)
	}
	if err := store.Delete(ctx, turbo); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := store.Stats(ctx)
	if err != nil || afterDelete.StorageByIntegration[0] != (artifact.IntegrationStats{Integration: "gradle", UsageBytes: 4, Artifacts: 2, Entries: 3}) {
		t.Fatalf("sole integration after deletion = %+v, %v", afterDelete, err)
	}
	assertCloudStoragePartition(t, afterDelete)
	for _, key := range []artifact.Key{first, second} {
		if err := store.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	afterLast, err := store.Stats(ctx)
	if err != nil || afterLast.UsageBytes != 12 || afterLast.Artifacts != 4 || afterLast.Entries != 5 {
		t.Fatalf("last-reference accounting = %+v, %v", afterLast, err)
	}
	assertCloudStoragePartition(t, afterLast)
	var pending int
	if err := store.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM layercache_artifacts_v1 WHERE project_id=$1 AND ref_count=0`, store.config.Project).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("expected uncharged pending GC object = %d, %v", pending, err)
	}
}

func assertCloudStoragePartition(t *testing.T, stats artifact.Stats) {
	t.Helper()
	var used, blobs, entries int64
	for _, item := range stats.StorageByIntegration {
		used += item.UsageBytes
		blobs += item.Artifacts
		entries += item.Entries
	}
	if used != stats.UsageBytes || blobs != stats.Artifacts || entries != stats.Entries {
		t.Fatalf("cloud partition totals %d/%d/%d differ from %+v", used, blobs, entries, stats)
	}
}

func TestCloudIntegrationStatsKeepOneSnapshotDuringReferenceChanges(t *testing.T) {
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := context.Background()
	store := newActionsHTTPFixture(t, 1<<20).store
	stable := artifact.Key{Integration: "turbo", Project: store.config.Project, Compatibility: "profile", Native: "stable"}
	if _, _, err := store.Put(ctx, stable, artifact.Metadata{}, strings.NewReader("same")); err != nil {
		t.Fatal(err)
	}
	changing := stable
	changing.Integration, changing.Native = "gradle", "changing"
	var writers sync.WaitGroup
	writerError := make(chan error, 1)
	writers.Add(1)
	go func() {
		defer writers.Done()
		for range 30 {
			if _, _, err := store.Put(ctx, changing, artifact.Metadata{}, strings.NewReader("same")); err != nil {
				writerError <- err
				return
			}
			if err := store.Delete(ctx, changing); err != nil {
				writerError <- err
				return
			}
		}
	}()
	// Always join the writer, including a failing reader assertion, before the
	// fixture drops its schema and HTTP object endpoint.
	t.Cleanup(writers.Wait)
	for range 60 {
		stats, err := store.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		assertCloudStoragePartition(t, stats)
		if stats.UsageBytes != 4 || stats.Artifacts != 1 || len(stats.StorageByIntegration) != 1 {
			t.Fatalf("reference mutation changed single physical blob: %+v", stats)
		}
		category := stats.StorageByIntegration[0]
		if !(category.Integration == "turbo" && category.Entries == 1 || category.Integration == "shared" && category.Entries == 2) {
			t.Fatalf("mixed reference snapshots: %+v", stats)
		}
	}
	writers.Wait()
	select {
	case err := <-writerError:
		t.Fatal(err)
	default:
	}
}
