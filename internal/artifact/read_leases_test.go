package artifact

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/retention"
)

func TestReadLeaseAdmissionChecksEveryExactEntryAtomically(t *testing.T) {
	for _, failure := range []string{"missing", "digest", "size", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, t.TempDir(), 16, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			first := putRetentionEntry(t, store, "first", "keep", 0)
			second := putRetentionEntry(t, store, "second", "next", 0)
			expected := ErrConflict
			switch failure {
			case "missing":
				if err := store.Delete(ctx, second.Key); err != nil {
					t.Fatal(err)
				}
				expected = ErrNotFound
			case "digest":
				second.Digest = strings.Repeat("0", 64)
			case "size":
				second.Size++
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				expected = context.Canceled
			}
			if err := store.RetainForRead(ctx, "xcode", "root", []Entry{first, second}, time.Minute); !errors.Is(err, expected) {
				t.Fatalf("failed admission = %v, want %v", err, expected)
			}
			if err := store.GC(context.Background(), 0); err != nil {
				t.Fatalf("failed admission retained part of the graph: %v", err)
			}
		})
	}
}

func TestReadLeaseProtectsSharedDescendantsAndExpiresWithoutReadRenewal(t *testing.T) {
	for _, policy := range []retention.Policy{retention.LRU, retention.Impact} {
		t.Run(string(policy), func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, t.TempDir(), 12, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.SetEvictionPolicy(policy); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			store.readLeaseNow = func() time.Time { return now }
			shared := putRetentionEntry(t, store, "shared", "leaf", 0)
			first := putRetentionEntry(t, store, "root-one", "one!", 0)
			second := putRetentionEntry(t, store, "root-two", "two!", 0)
			if err := store.RetainForRead(ctx, "xcode", "first", []Entry{first, shared}, time.Minute); err != nil {
				t.Fatal(err)
			}
			now = now.Add(30 * time.Second)
			if err := store.RetainForRead(ctx, "xcode", "second", []Entry{second, shared}, time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := store.Delete(ctx, shared.Key); !errors.Is(err, ErrPinned) {
				t.Fatalf("leased deletion = %v", err)
			}
			if err := store.GC(ctx, 0); !errors.Is(err, ErrQuota) {
				t.Fatalf("leased GC = %v", err)
			}
			if usage, err := store.Usage(ctx); err != nil || usage != 12 {
				t.Fatalf("shared lease duplicated stored bytes: %d %v", usage, err)
			}
			now = now.Add(30 * time.Second)
			if err := store.GC(ctx, 8); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Head(ctx, first.Key); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired root survived: %v", err)
			}
			for _, entry := range []Entry{second, shared} {
				_, reader, err := store.Get(ctx, entry.Key)
				if err != nil {
					t.Fatalf("live shared graph disappeared: %v", err)
				}
				reader.Close()
			}
			// Reads immediately before expiry do not renew either root's lease.
			now = now.Add(30 * time.Second)
			if err := store.GC(ctx, 0); err != nil {
				t.Fatalf("abandoned/read-only lease did not expire: %v", err)
			}
		})
	}
}

func TestReadLeaseBoundsOwnersAndMembershipAcrossNamespaces(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), 16, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.readLeaseOwners, store.readLeaseEntries = 2, 3
	now := time.Now()
	store.readLeaseNow = func() time.Time { return now }
	first := putRetentionEntry(t, store, "first", "keep", 0)
	second := putRetentionEntry(t, store, "second", "next", 0)
	if err := store.RetainForRead(ctx, "xcode", "root", []Entry{first, first}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainForRead(ctx, "other", "root", []Entry{first, second}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainForRead(ctx, "third", "root", []Entry{first}, time.Minute); !errors.Is(err, ErrQuota) {
		t.Fatalf("global owner bound = %v", err)
	}
	if err := store.RetainForRead(ctx, "xcode", "root", []Entry{second}, time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("mutable graph owner = %v", err)
	}
	now = now.Add(30 * time.Second)
	if err := store.RetainForRead(ctx, "xcode", "root", []Entry{first}, time.Minute); err != nil {
		t.Fatalf("same immutable root renewal = %v", err)
	}
	if err := store.ResetReadLeases(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainForRead(ctx, "other", "too-large", []Entry{first, second}, time.Minute); err != nil {
		t.Fatal(err)
	}
	store.readLeaseOwners = 3
	if err := store.RetainForRead(ctx, "third", "members", []Entry{second}, time.Minute); !errors.Is(err, ErrQuota) {
		t.Fatalf("global membership bound = %v", err)
	}
	now = now.Add(time.Minute)
	if err := store.RetainForRead(ctx, "third", "after-expiry", []Entry{second}, time.Minute); err != nil {
		t.Fatalf("expired rows consumed admission budget: %v", err)
	}
	var owners, members int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_read_leases`).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_read_lease_entries`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if owners != 1 || members != 1 {
		t.Fatalf("stale leases remain: owners=%d members=%d", owners, members)
	}
}

func TestOpeningAnotherStoreDoesNotResetLiveReadLeasesOrDurablePins(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, root, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := putRetentionEntry(t, store, "live", "keep", 0)
	durable := putRetentionEntry(t, store, "upload", "next", 0)
	if err := store.Pin(ctx, "team-upload", Pin{Owner: "pending", Key: durable.Key, Digest: durable.Digest, Size: durable.Size}); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainForRead(ctx, "xcode", "root", []Entry{entry}, time.Minute); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, root, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Delete(ctx, entry.Key); !errors.Is(err, ErrPinned) {
		t.Fatalf("another Store removed a live lease: %v", err)
	}
	if err := other.GC(ctx, 0); !errors.Is(err, ErrQuota) {
		t.Fatalf("another Store evicted leased bytes: %v", err)
	}
	if err := other.ResetReadLeases(ctx, "xcode"); err != nil {
		t.Fatal(err)
	}
	if err := other.GC(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Head(ctx, entry.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reset reader survived: %v", err)
	}
	if _, err := other.Head(ctx, durable.Key); err != nil {
		t.Fatalf("reset disturbed durable upload pin: %v", err)
	}
}

func TestReadLeaseRejectsInvalidOrUnboundedIntervals(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := putRetentionEntry(t, store, "entry", "keep", 0)
	for _, duration := range []time.Duration{0, -time.Second, MaximumReadLeaseDuration + time.Nanosecond} {
		if err := store.RetainForRead(ctx, "xcode", "root", []Entry{entry}, duration); err == nil {
			t.Fatalf("unbounded/invalid duration accepted: %v", duration)
		}
	}
}

func TestConcurrentStoresCannotEvictAnAdmittedReadLease(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writer, err := Open(ctx, root, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(ctx, root, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for iteration := 0; iteration < 16; iteration++ {
		entry := putRetentionEntry(t, writer, "concurrent", "keep", 0)
		start := make(chan struct{})
		admitted := make(chan error, 1)
		collected := make(chan error, 1)
		go func() {
			<-start
			admitted <- reader.RetainForRead(ctx, "xcode", "root", []Entry{entry}, time.Minute)
		}()
		go func() {
			<-start
			collected <- writer.GC(ctx, 0)
		}()
		close(start)
		admissionErr, collectionErr := <-admitted, <-collected
		if admissionErr == nil {
			if !errors.Is(collectionErr, ErrQuota) {
				t.Fatalf("collector passed an admitted lease: %v", collectionErr)
			}
			if _, err := reader.Head(ctx, entry.Key); err != nil {
				t.Fatalf("admitted graph disappeared: %v", err)
			}
		} else if !errors.Is(admissionErr, ErrNotFound) || collectionErr != nil {
			t.Fatalf("concurrent admission=%v collection=%v", admissionErr, collectionErr)
		}
		if err := reader.ResetReadLeases(ctx, "xcode"); err != nil {
			t.Fatal(err)
		}
	}
}
