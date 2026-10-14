package uploadqueue_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/uploadqueue"
)

func TestAlreadyPresentOutcomePersistsReleasesQuotaAndPreservesRepairFencing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := uploadqueue.Config{Path: filepath.Join(t.TempDir(), "uploads.db"), MaxQueuedBytes: 4}
	queue, err := uploadqueue.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close() })
	request := turboUpload("same-task-different-logs", strings.Repeat("a", 64), 4)
	queued, err := queue.Enqueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := queue.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := queue.CompleteAlreadyPresent(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != uploadqueue.StateCompleted || completed.Outcome != uploadqueue.OutcomeAlreadyPresent || completed.Digest != request.Digest || completed.Size != request.Size {
		t.Fatalf("existing Team artifact was mislabeled as a Local upload: %+v", completed)
	}
	if _, err := queue.CompleteAlreadyPresent(ctx, lease); err != nil {
		t.Fatalf("repeated already-present completion: %v", err)
	}
	if _, err := queue.Complete(ctx, lease); !errors.Is(err, uploadqueue.ErrLeaseLost) {
		t.Fatalf("uploaded completion relabeled an already-present outcome: %v", err)
	}
	if _, err := queue.Retry(ctx, lease, "offline"); !errors.Is(err, uploadqueue.ErrLeaseLost) {
		t.Fatalf("retry revived a finalized conflict: %v", err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	queue, err = uploadqueue.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := queue.Get(ctx, queued.Job.ID)
	if err != nil || recovered.Outcome != uploadqueue.OutcomeAlreadyPresent || recovered.State != uploadqueue.StateCompleted {
		t.Fatalf("restart lost the truthful terminal outcome: %+v, %v", recovered, err)
	}
	stats, err := uploadqueue.ReadPersistedStats(ctx, cfg.Path, cfg.MaxQueuedBytes)
	if err != nil || stats.QueuedJobs != 0 || stats.QueuedBytes != 0 || stats.CompletedJobs != 1 {
		t.Fatalf("stopped-runtime status still charges a finalized conflict: %+v, %v", stats, err)
	}
	if _, err := queue.Enqueue(ctx, turboUpload("useful-new-result", strings.Repeat("b", 64), 4)); err != nil {
		t.Fatalf("terminal conflict kept the byte quota charged: %v", err)
	}
	if _, err := queue.Requeue(ctx, request); !errors.Is(err, uploadqueue.ErrQueueFull) {
		t.Fatalf("repair bypassed quota after terminal conflict: %v", err)
	}
	otherLease, err := queue.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Complete(ctx, otherLease); err != nil {
		t.Fatal(err)
	}
	repaired, err := queue.Requeue(ctx, request)
	if err != nil || repaired.State != uploadqueue.StatePending || repaired.Outcome != "" || repaired.LeaseGeneration != lease.Generation {
		t.Fatalf("repair of an evicted existing result changed fencing: %+v, %v", repaired, err)
	}
	newLease, err := queue.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if newLease.Generation != lease.Generation+1 {
		t.Fatalf("repair reset the lease generation: %+v", newLease)
	}
	if _, err := queue.CompleteAlreadyPresent(ctx, lease); !errors.Is(err, uploadqueue.ErrLeaseLost) {
		t.Fatalf("stale conflict completion finalized a repair: %v", err)
	}
	if _, err := queue.Complete(ctx, newLease); err != nil {
		t.Fatalf("repaired artifact could not later upload: %v", err)
	}
}
