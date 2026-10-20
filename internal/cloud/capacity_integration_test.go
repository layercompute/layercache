package cloud

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/retained"
)

func TestCloudCapacityKeepsArtifactsAndPersistsAuditedChanges(t *testing.T) {
	f := retainedFixture(t, 32)
	ctx := context.Background()
	key := artifact.Key{Project: f.store.config.Project, Integration: "gradle", Compatibility: "fixture", Native: "valuable-output"}
	payload := []byte("1234567890abcdef")
	entry, _, err := f.store.Put(ctx, key, artifact.Metadata{}, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SetCapacity(ctx, "capacity-admin", 32, 8, 32); !errors.Is(err, ErrQuota) {
		t.Fatalf("shrink below unpinned committed bytes: %v", err)
	}
	if _, err = f.store.SetCapacity(ctx, "capacity-admin", 31, 24, 32); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale displayed capacity: %v", err)
	}
	quota, err := f.store.Quota(ctx)
	if err != nil || quota.MaxBytes != 32 || quota.UsedBytes != int64(len(payload)) {
		t.Fatalf("rejected changes altered quota: %+v %v", quota, err)
	}
	unchanged, err := f.store.Head(ctx, key)
	if err != nil || unchanged.Digest != entry.Digest || unchanged.Size != entry.Size {
		t.Fatalf("rejected shrink evicted or replaced the entry: %+v %v", unchanged, err)
	}
	quota, err = f.store.SetCapacity(ctx, "capacity-admin", 32, 24, 32)
	if err != nil || quota.MaxBytes != 24 || quota.UsedBytes != 16 {
		t.Fatalf("non-destructive shrink: %+v %v", quota, err)
	}

	// The configured 32-byte allowance initializes new projects. Opening another
	// process must retain the administrator's 24-byte operating limit.
	reopened, err := Open(ctx, f.store.config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	quota, err = reopened.Quota(ctx)
	if err != nil || quota.MaxBytes != 24 || quota.UsedBytes != 16 {
		t.Fatalf("restart replaced the operating limit: %+v %v", quota, err)
	}
	if _, err = reopened.Head(ctx, key); err != nil {
		t.Fatalf("restart lost an accepted entry: %v", err)
	}
	quota, err = reopened.SetCapacity(ctx, "capacity-admin", 24, 32, 32)
	if err != nil || quota.MaxBytes != 32 || quota.UsedBytes != 16 {
		t.Fatalf("restore assigned capacity: %+v %v", quota, err)
	}
	current, err := f.store.Quota(ctx)
	if err != nil || current.MaxBytes != 32 {
		t.Fatalf("another process did not observe the increase: %+v %v", current, err)
	}
	events, err := f.store.ReadAudit(ctx, AuditQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var changes []AuditEvent
	for _, event := range events {
		if event.Action == "capacity.set" {
			changes = append(changes, event)
		}
	}
	if len(changes) != 2 {
		t.Fatalf("expected only the two committed capacity audit events: %+v", changes)
	}
	for index, wanted := range []map[string]string{
		{"previousBytes": "32", "maxBytes": "24", "ceilingBytes": "32"},
		{"previousBytes": "24", "maxBytes": "32", "ceilingBytes": "32"},
	} {
		event := changes[index]
		if event.Project != f.store.config.Project || event.Actor != "capacity-admin" || event.Outcome != "allowed" || event.Resource != "project" {
			t.Fatalf("capacity audit authority: %+v", event)
		}
		for name, value := range wanted {
			if event.Attributes[name] != value {
				t.Fatalf("capacity audit %s: got %q, want %q", name, event.Attributes[name], value)
			}
		}
	}
}

func TestCloudCapacityHonorsAssignedCeilingAndRetentionTarget(t *testing.T) {
	f := retainedFixture(t, 32)
	f.store.config.SoftBytes = 8
	ctx := context.Background()
	for _, input := range []struct {
		name                       string
		expected, maximum, ceiling int64
	}{
		{"above assigned capacity", 32, 33, 32},
		{"below retention target", 32, 7, 32},
		{"zero capacity", 32, 0, 32},
		{"negative capacity", 32, -1, 32},
		{"unsafe browser integer", 32, 1 << 53, 1 << 53},
	} {
		t.Run(input.name, func(t *testing.T) {
			if _, err := f.store.SetCapacity(ctx, "capacity-admin", input.expected, input.maximum, input.ceiling); err == nil {
				t.Fatal("invalid capacity was accepted")
			}
			quota, err := f.store.Quota(ctx)
			if err != nil || quota.MaxBytes != 32 {
				t.Fatalf("rejection changed the operating limit: %+v %v", quota, err)
			}
		})
	}
	if quota, err := f.store.SetCapacity(ctx, "capacity-admin", 32, 8, 32); err != nil || quota.MaxBytes != 8 {
		t.Fatalf("exact retention target was rejected: %+v %v", quota, err)
	}
}

func TestCloudCapacityProtectsRetainedReservationBeforePublication(t *testing.T) {
	f := retainedFixture(t, 16)
	ctx := context.Background()
	key := artifact.Key{Project: f.store.config.Project, Integration: "gradle", Compatibility: "fixture", Native: "existing-output"}
	if _, _, err := f.store.Put(ctx, key, artifact.Metadata{}, bytes.NewBufferString("1234")); err != nil {
		t.Fatal(err)
	}
	payload := []byte("required")
	receipt, err := f.store.ReserveRetained(ctx, declarationFor(f, "queued-handoff", payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SetCapacity(ctx, "capacity-admin", 16, 11, 16); !errors.Is(err, ErrQuota) {
		t.Fatalf("shrink consumed the eight-byte retained reservation: %v", err)
	}
	current, err := f.store.InspectRetained(ctx, receipt.ID)
	if err != nil || current.State != retained.Reserved {
		t.Fatalf("rejected shrink changed the retained receipt: %+v %v", current, err)
	}
	if _, err = f.store.SetCapacity(ctx, "capacity-admin", 16, 12, 16); err != nil {
		t.Fatalf("exact committed plus reserved limit: %v", err)
	}
	current, err = f.store.PublishRetained(ctx, receipt.ID, bytes.NewReader(payload))
	if err != nil || current.State != retained.Published {
		t.Fatalf("accepted reservation could not publish after capacity change: %+v %v", current, err)
	}
	quota, err := f.store.Quota(ctx)
	if err != nil || quota.MaxBytes != 12 || quota.UsedBytes != 12 || quota.ReservedBytes != 0 {
		t.Fatalf("published obligations: %+v %v", quota, err)
	}
	if _, err = f.store.Head(ctx, key); err != nil {
		t.Fatalf("publication evicted pre-existing bytes: %v", err)
	}
}

func TestCloudCapacityProtectsRetainedStagingAfterDeduplicatedPublication(t *testing.T) {
	f := retainedFixture(t, 32)
	ctx := context.Background()
	backend := &retainedCleanupFailure{blobStore: f.store.blobs}
	backend.fail.Store(true)
	f.store.blobs = backend
	payload := []byte("required")
	publishRetained(t, f, declarationFor(f, "first-handoff", payload), payload)
	publishRetained(t, f, declarationFor(f, "second-handoff", payload), payload)
	quota, err := f.store.Quota(ctx)
	if err != nil || quota.UsedBytes != 8 || quota.RetainedStagingBytes != 16 {
		t.Fatalf("expected one committed blob with two pending staging cleanups: %+v %v", quota, err)
	}
	if _, err = f.store.SetCapacity(ctx, "capacity-admin", 32, 12, 32); !errors.Is(err, ErrQuota) {
		t.Fatalf("shrink ignored outstanding staging obligations: %v", err)
	}
	quota, err = f.store.Quota(ctx)
	if err != nil || quota.MaxBytes != 32 || quota.UsedBytes != 8 || quota.RetainedStagingBytes != 16 {
		t.Fatalf("rejected staging shrink changed obligations: %+v %v", quota, err)
	}
}

func TestCloudCapacityConcurrentCompareAndSetHasOneWinner(t *testing.T) {
	f := retainedFixture(t, 64)
	ctx := context.Background()
	replica, err := Open(ctx, f.store.config)
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for index, store := range []*Store{f.store, replica} {
		workers.Add(1)
		go func(store *Store, maximum int64) {
			defer workers.Done()
			<-start
			_, changeErr := store.SetCapacity(ctx, "capacity-admin", 64, maximum, 64)
			results <- changeErr
		}(store, int64(32+index*16))
	}
	close(start)
	workers.Wait()
	close(results)
	winners, conflicts := 0, 0
	for changeErr := range results {
		switch {
		case changeErr == nil:
			winners++
		case errors.Is(changeErr, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent change error: %v", changeErr)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("lost update: winners=%d conflicts=%d", winners, conflicts)
	}
	quota, err := f.store.Quota(ctx)
	if err != nil || quota.MaxBytes != 32 && quota.MaxBytes != 48 {
		t.Fatalf("concurrent operating limit: %+v %v", quota, err)
	}
}
