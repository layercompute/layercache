package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/retained"
)

func retainedFixture(t *testing.T, quota int64) *actionsHTTPFixture {
	t.Helper()
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("set LAYER_CACHE_QA_POSTGRES_URL for retained PostgreSQL/S3 SDK qualification")
	}
	return newActionsHTTPFixture(t, quota)
}
func declarationFor(f *actionsHTTPFixture, name string, payload []byte) retained.Declaration {
	digest := sha256.Sum256(payload)
	return retained.Declaration{Producer: retained.Producer{Project: f.store.config.Project, Repository: "acme/phone", RepositoryID: "42", SourceCommit: strings.Repeat("a", 40), WorkflowRef: "acme/phone/.github/workflows/check.yml@refs/heads/main", RunID: "100", RunAttempt: "1", JobID: "200", Profile: "debug-x86_64"}, Name: name, Digest: hex.EncodeToString(digest[:]), Size: int64(len(payload)), RetainUntil: f.store.config.Now().Add(time.Hour)}
}
func publishRetained(t *testing.T, f *actionsHTTPFixture, d retained.Declaration, payload []byte) retained.Receipt {
	t.Helper()
	receipt, err := f.store.ReserveRetained(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = f.store.PublishRetained(context.Background(), receipt.ID, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != retained.Published || receipt.PublishedAt == nil {
		t.Fatalf("not published: %+v", receipt)
	}
	return receipt
}

func TestRetainedImmutableReceiptRecoveryAndDeletionFence(t *testing.T) {
	f := retainedFixture(t, 16)
	ctx := context.Background()
	payload := []byte("required")
	d := declarationFor(f, "apk", payload)
	receipt := publishRetained(t, f, d, payload)
	// A response lost after commit is resolved without consuming a retry body.
	retry, err := f.store.PublishRetained(ctx, receipt.ID, panicReader{})
	if err != nil || retry.ID != receipt.ID {
		t.Fatalf("exact committed retry: %+v %v", retry, err)
	}
	retry, err = f.store.ReserveRetained(ctx, d)
	if err != nil || !retry.Declaration.RetainUntil.Equal(receipt.Declaration.RetainUntil) {
		t.Fatalf("exact reservation retry: %+v %v", retry, err)
	}
	changed := d
	changed.Size++
	if _, err = f.store.ReserveRetained(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("size conflict: %v", err)
	}
	changed = d
	changed.RetainUntil = changed.RetainUntil.Add(time.Minute)
	if _, err = f.store.ReserveRetained(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("deadline renewal: %v", err)
	}
	changed = d
	changed.Producer.Project = "foreign"
	if _, err = f.store.ReserveRetained(ctx, changed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign project: %v", err)
	}
	replica, err := Open(ctx, f.store.config)
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	recovered, err := replica.InspectRetained(ctx, receipt.ID)
	if err != nil || recovered.State != retained.Published {
		t.Fatalf("restart inspect: %+v %v", recovered, err)
	}
	_, body, err := replica.OpenRetained(ctx, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("verified fresh consumption %q %v", got, err)
	}
	key := retainedKey(f.store.config.Project, receipt.ID)
	if err = f.store.Delete(ctx, key); !errors.Is(err, ErrConflict) {
		t.Fatalf("explicit deletion escaped claim: %v", err)
	}
	if _, err = f.store.database.ExecContext(ctx, `DELETE FROM layercache_entries_v1 WHERE project_id=$1 AND native_key=$2`, f.store.config.Project, receipt.ID); err == nil {
		t.Fatal("restrictive FK allowed direct deletion")
	}
	// Even an accidental system pin removal must not release the obligation.
	if err = f.store.Unpin(ctx, retainedPinNamespace, receipt.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.GC(ctx, 0); !errors.Is(err, ErrQuota) {
		t.Fatalf("GC escaped restrictive claim: %v", err)
	}
	if _, err = f.store.SetQuota(ctx, "admin", 7); !errors.Is(err, ErrQuota) {
		t.Fatalf("quota shrink escaped claim: %v", err)
	}
	stats, err := f.store.Stats(ctx)
	if err != nil || stats.UsageBytes != 8 || stats.MetadataBytes < retainedMetadataBytes {
		t.Fatalf("retained accounting: %+v %v", stats, err)
	}
	if _, err = f.store.RevokeRetained(ctx, receipt.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.OpenRetained(ctx, receipt.ID); !errors.Is(err, retained.ErrInactive) {
		t.Fatalf("revoked read: %v", err)
	}
	if err = f.store.GC(ctx, 0); err != nil {
		t.Fatal(err)
	}
	f.now.Add(int64(50 * time.Hour))
	if _, err = f.store.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err = f.store.Stats(ctx)
	if err != nil || stats.MetadataBytes != 0 || stats.UsageBytes != 0 {
		t.Fatalf("tombstone cleanup accounting: %+v %v", stats, err)
	}
	if _, err = f.store.InspectRetained(ctx, receipt.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("collected history: %v", err)
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("committed retry read its body") }

func TestRetainedReservationsFenceOrdinaryWritesQuotaAndConcurrentAdmission(t *testing.T) {
	f := retainedFixture(t, 8)
	ctx := context.Background()
	payload := []byte("12345678")
	d := declarationFor(f, "required", payload)
	r, err := f.store.ReserveRetained(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	key := artifact.Key{Project: f.store.config.Project, Integration: "gradle", Compatibility: "fixture", Native: "unrelated"}
	if _, _, err = f.store.Put(ctx, key, artifact.Metadata{}, strings.NewReader("x")); !errors.Is(err, ErrQuota) {
		t.Fatalf("ordinary cache consumed reserved bytes: %v", err)
	}
	if _, err = f.store.SetQuota(ctx, "admin", 7); !errors.Is(err, ErrQuota) {
		t.Fatalf("shrink consumed reserved bytes: %v", err)
	}
	stats, _ := f.store.Stats(ctx)
	if stats.Entries != 0 || stats.MetadataBytes != retainedMetadataBytes {
		t.Fatalf("failed write changed durable usage: %+v", stats)
	}
	// Admission itself must not evict previously valid cache data.
	if _, err = f.store.PublishRetained(ctx, r.ID, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	other := d
	other.Name = "second"
	if _, err = f.store.ReserveRetained(ctx, other); !errors.Is(err, ErrQuota) {
		t.Fatalf("admitted over live obligation: %v", err)
	}
	if _, err = f.store.RevokeRetained(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReserveRetained(ctx, other); !errors.Is(err, ErrQuota) {
		t.Fatalf("declaration evicted cache: %v", err)
	}
	if err = f.store.GC(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int64
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			candidate := declarationFor(f, fmt.Sprintf("race-%d", n), []byte("1234"))
			_, err := f.store.ReserveRetained(ctx, candidate)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrQuota) {
				t.Errorf("concurrent admission: %v", err)
			}
		}(n)
	}
	wg.Wait()
	if successes.Load() != 2 {
		t.Fatalf("admitted %d payloads into8 bytes", successes.Load())
	}
	f.now.Add(int64(16 * time.Minute))
	if _, err = f.store.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.Put(ctx, key, artifact.Metadata{}, strings.NewReader("12345678")); err != nil {
		t.Fatalf("expired reservation retained budget: %v", err)
	}
}

func TestRetainedRejectsPartialCorruptAndUnavailablePayloads(t *testing.T) {
	for _, mode := range []string{"short", "long", "digest", "failed-copy"} {
		t.Run(mode, func(t *testing.T) {
			f := retainedFixture(t, 32)
			ctx := context.Background()
			payload := []byte("required")
			d := declarationFor(f, "apk", payload)
			r, err := f.store.ReserveRetained(ctx, d)
			if err != nil {
				t.Fatal(err)
			}
			input := payload
			switch mode {
			case "short":
				input = input[:3]
			case "long":
				input = append(append([]byte{}, input...), byte('x'))
			case "digest":
				input = []byte("corrupt!")
			case "failed-copy":
				f.objects.failCopy.Store(true)
			}
			if _, err = f.store.PublishRetained(ctx, r.ID, bytes.NewReader(input)); err == nil {
				t.Fatal("invalid publication succeeded")
			}
			inspected, err := f.store.InspectRetained(ctx, r.ID)
			if err != nil || inspected.State != retained.Reserved {
				t.Fatalf("failed body became publication: %+v %v", inspected, err)
			}
			stats, _ := f.store.Stats(ctx)
			if stats.Entries != 0 || stats.UsageBytes != 0 {
				t.Fatalf("failed body visible: %+v", stats)
			}
			f.objects.failCopy.Store(false)
			if _, err = f.store.PublishRetained(ctx, r.ID, bytes.NewReader(payload)); err != nil {
				t.Fatalf("retry after verified failure: %v", err)
			}
			f.objects.mutex.Lock()
			key := f.store.blobKey(d.Digest)
			object := f.objects.objects[key]
			object.data = []byte("corrupt!")
			f.objects.objects[key] = object
			f.objects.mutex.Unlock()
			_, body, err := f.store.OpenRetained(ctx, r.ID)
			if err == nil {
				_, err = io.ReadAll(body)
				body.Close()
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("corrupt download accepted: %v", err)
			}
			inspected, err = f.store.InspectRetained(ctx, r.ID)
			if err != nil || inspected.State != retained.Unavailable {
				t.Fatalf("corrupt state: %+v %v", inspected, err)
			}
		})
	}
}

func TestRetainedExpiryDuringAdmittedReadAndStagingIsolation(t *testing.T) {
	f := retainedFixture(t, 32)
	ctx := context.Background()
	payload := []byte("required")
	d := declarationFor(f, "apk", payload)
	d.RetainUntil = f.store.config.Now().Add(time.Minute)
	f.store.config.BlobGrace = time.Nanosecond
	r := publishRetained(t, f, d, payload)
	_, body, err := f.store.OpenRetained(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	f.now.Add(int64(2 * time.Minute))
	if _, err = f.store.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	inspected, err := f.store.InspectRetained(ctx, r.ID)
	if err != nil || inspected.State != retained.Expired {
		t.Fatalf("expiry state: %+v %v", inspected, err)
	}
	if _, _, err = f.store.OpenRetained(ctx, r.ID); !errors.Is(err, retained.ErrInactive) {
		t.Fatalf("new read after expiry: %v", err)
	}
	if err = f.store.GC(ctx, 0); err != nil {
		t.Fatal(err)
	}
	// Force blob collection eligibility while the admitted 15-minute lease is
	// still live. The lease, rather than an entry/claim or cleanup grace, protects it.
	f.now.Add(int64(time.Second))
	if _, err = f.store.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	f.objects.mutex.Lock()
	_, present := f.objects.objects[f.store.blobKey(d.Digest)]
	f.objects.mutex.Unlock()
	if !present {
		t.Fatal("collection removed an admitted read's object")
	}
	// The already admitted stream must still verify complete bytes.
	got, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("admitted read did not finish: %q %v", got, err)
	}
	body.Close()
	if _, err = f.store.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	f.objects.mutex.Lock()
	_, present = f.objects.objects[f.store.blobKey(d.Digest)]
	f.objects.mutex.Unlock()
	if present {
		t.Fatal("completed read kept an expired unreferenced object")
	}
	// A second publisher cannot begin while the admitted body is in progress.
	d = declarationFor(f, "next", payload)
	r, err = f.store.ReserveRetained(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	pipeReader, pipeWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { _, err := f.store.PublishRetained(ctx, r.ID, pipeReader); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, err := f.store.InspectRetained(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.State == retained.Staging {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("staging never admitted")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = f.store.PublishRetained(ctx, r.ID, panicReader{}); !errors.Is(err, retained.ErrBusy) {
		t.Fatalf("duplicate concurrent upload: %v", err)
	}
	pipeWriter.CloseWithError(io.ErrUnexpectedEOF)
	if err = <-done; err == nil {
		t.Fatal("partial stream succeeded")
	}
	pipeReader.Close()
}

type retainedCleanupFailure struct {
	blobStore
	fail atomic.Bool
}

func (b *retainedCleanupFailure) Delete(ctx context.Context, key string) error {
	if b.fail.Load() && strings.Contains(key, "/staging/") {
		return errors.New("fixture staged cleanup outage")
	}
	return b.blobStore.Delete(ctx, key)
}
func TestRetainedStagingCleanupOutageRemainsBoundedAndVisible(t *testing.T) {
	f := retainedFixture(t, 8)
	ctx := context.Background()
	payload := []byte("required")
	backend := &retainedCleanupFailure{blobStore: f.store.blobs}
	backend.fail.Store(true)
	f.store.blobs = backend
	r := publishRetained(t, f, declarationFor(f, "apk", payload), payload)
	quota, err := f.store.Quota(ctx)
	if err != nil || quota.RetainedBytes != 8 || quota.ReservedBytes != 0 || quota.RetainedStagingBytes != 8 {
		t.Fatalf("pending staging accounting %+v %v", quota, err)
	}
	if _, err = f.store.PublishRetained(ctx, r.ID, panicReader{}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReserveRetained(ctx, declarationFor(f, "other", payload)); !errors.Is(err, ErrQuota) {
		t.Fatalf("cleanup outage admitted more staging: %v", err)
	}
	backend.fail.Store(false)
	f.now.Add(int64(16 * time.Minute))
	if _, err = f.store.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	quota, err = f.store.Quota(ctx)
	if err != nil || quota.RetainedStagingBytes != 0 || quota.RetainedBytes != 8 {
		t.Fatalf("cleanup did not release staging only %+v %v", quota, err)
	}
	r, err = f.store.InspectRetained(ctx, r.ID)
	if err != nil || r.State != retained.Published {
		t.Fatalf("cleanup invalidated publication %+v %v", r, err)
	}
}
func TestRetainedEarlyRevocationDoesNotReleaseAnActiveUploadBudget(t *testing.T) {
	f := retainedFixture(t, 8)
	ctx := context.Background()
	payload := []byte("required")
	r, err := f.store.ReserveRetained(ctx, declarationFor(f, "apk", payload))
	if err != nil {
		t.Fatal(err)
	}
	input, writer := io.Pipe()
	defer input.Close()
	done := make(chan error, 1)
	go func() { _, err := f.store.PublishRetained(ctx, r.ID, input); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, err := f.store.InspectRetained(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.State == retained.Staging {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("staging not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = f.store.RevokeRetained(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReserveRetained(ctx, declarationFor(f, "other", payload)); !errors.Is(err, ErrQuota) {
		t.Fatalf("revocation released active staging: %v", err)
	}
	writer.CloseWithError(io.ErrUnexpectedEOF)
	if err = <-done; err == nil {
		t.Fatal("revoked partial publication succeeded")
	}
	if _, err = f.store.ReserveRetained(ctx, declarationFor(f, "other", payload)); err != nil {
		t.Fatalf("completed failed upload kept revoked budget: %v", err)
	}
}

func TestRetainedDeduplicatedClaimsAndInterruptedReservationRestart(t *testing.T) {
	f := retainedFixture(t, 24)
	ctx := context.Background()
	payload := []byte("required")
	first := publishRetained(t, f, declarationFor(f, "first", payload), payload)
	second := publishRetained(t, f, declarationFor(f, "second", payload), payload)
	quota, err := f.store.Quota(ctx)
	if err != nil || quota.UsedBytes != 8 || quota.RetainedBytes != 8 || quota.RetainedMetadataBytes != 2*retainedMetadataBytes {
		t.Fatalf("deduplicated obligations %+v %v", quota, err)
	}
	if _, err = f.store.RevokeRetained(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Delete(ctx, retainedKey(f.store.config.Project, first.ID)); err != nil {
		t.Fatal(err)
	}
	_, body, err := f.store.OpenRetained(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("one revocation damaged shared content %q %v", got, err)
	}
	pending, err := f.store.ReserveRetained(ctx, declarationFor(f, "interrupted", payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.startRetained(ctx, pending.ID); err != nil {
		t.Fatal(err)
	}
	replica, err := Open(ctx, f.store.config)
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	pending, err = replica.InspectRetained(ctx, pending.ID)
	if err != nil || pending.State != retained.Staging {
		t.Fatalf("restart lost pending obligation %+v %v", pending, err)
	}
	if _, err = replica.PublishRetained(ctx, pending.ID, panicReader{}); !errors.Is(err, retained.ErrBusy) {
		t.Fatalf("restart duplicated staging: %v", err)
	}
	f.now.Add(int64(16 * time.Minute))
	if _, err = replica.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err = replica.InspectRetained(ctx, pending.ID)
	if err != nil || pending.State != retained.Expired {
		t.Fatalf("restart reservation did not expire %+v %v", pending, err)
	}
	quota, err = replica.Quota(ctx)
	if err != nil || quota.ReservedBytes != 0 || quota.RetainedBytes != 8 {
		t.Fatalf("restart accounting %+v %v", quota, err)
	}
}
