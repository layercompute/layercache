package artifact

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Read leases bridge a verified lookup and the later reads of related objects.
// They expire without a consumer callback and never extend from ordinary reads.
const MaximumReadLeaseDuration = 2 * time.Minute

const maximumReadLeaseOwners = 4096
const maximumReadLeaseEntries = 262144
const maximumReadLeaseGraphEntries = 100000

// RetainForRead atomically checks exact entries and protects their digests from
// deletion and eviction for duration. Shared blobs count once in committed
// usage. The owner must identify one immutable object set: renewal with a
// different set fails rather than remove another reader's protection.
// No bytes are copied, and admission remains bounded across all namespaces.
func (store *Store) RetainForRead(ctx context.Context, namespace, owner string, entries []Entry, duration time.Duration) error {
	if err := validatePinIdentity("read lease namespace", namespace); err != nil {
		return err
	}
	if err := validatePinIdentity("read lease owner", owner); err != nil {
		return err
	}
	if duration <= 0 || duration > MaximumReadLeaseDuration {
		return errors.New("read lease duration must be positive and at most two minutes")
	}
	if len(entries) == 0 || len(entries) > maximumReadLeaseGraphEntries {
		return ErrQuota
	}
	digests := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if err := validatePin(namespace, Pin{Owner: owner, Key: entry.Key, Digest: entry.Digest, Size: entry.Size}); err != nil {
			return err
		}
		digests[entry.Digest] = true
	}
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Local Cache read lease: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifact_read_leases WHERE expires_at <= ?`, store.readLeaseNow().UnixNano()); err != nil {
		return fmt.Errorf("expire Local Cache read leases: %w", err)
	}
	for _, entry := range entries {
		var digest string
		var size int64
		err := tx.QueryRowContext(ctx, `SELECT digest, size FROM cache_entries
			WHERE integration = ? AND project = ? AND compatibility = ? AND native_key = ? AND version = ? AND ref_scope = ?`, keyArgs(entry.Key)...).Scan(&digest, &size)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("check Local Cache read lease target: %w", err)
		}
		if digest != entry.Digest || size != entry.Size {
			return ErrConflict
		}
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_read_leases WHERE namespace = ? AND owner = ?)`, namespace, owner).Scan(&exists); err != nil {
		return fmt.Errorf("check Local Cache read lease owner: %w", err)
	}
	if exists != 0 {
		rows, err := tx.QueryContext(ctx, `SELECT digest FROM artifact_read_lease_entries WHERE namespace = ? AND owner = ?`, namespace, owner)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var digest string
			if err := rows.Scan(&digest); err != nil {
				rows.Close()
				return err
			}
			if !digests[digest] {
				rows.Close()
				return ErrConflict
			}
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count != len(digests) {
			return ErrConflict
		}
	} else {
		var owners, members int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_read_leases`).Scan(&owners); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_read_lease_entries`).Scan(&members); err != nil {
			return err
		}
		if owners >= store.readLeaseOwners || len(digests) > store.readLeaseEntries-members {
			return ErrQuota
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_read_leases(namespace, owner, expires_at) VALUES(?, ?, ?)`, namespace, owner, store.readLeaseNow().Add(duration).UnixNano()); err != nil {
			return err
		}
		for digest := range digests {
			if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_read_lease_entries(namespace, owner, digest) VALUES(?, ?, ?)`, namespace, owner, digest); err != nil {
				return err
			}
		}
	}
	// Start the fetch window after validation and membership insertion, rather
	// than consume it while walking or admitting a large graph.
	if _, err := tx.ExecContext(ctx, `UPDATE artifact_read_leases SET expires_at = ? WHERE namespace = ? AND owner = ?`, store.readLeaseNow().Add(duration).UnixNano(), namespace, owner); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Local Cache read lease: %w", err)
	}
	return nil
}

// ResetReadLeases releases interrupted consumers for one subsystem. It must
// only be called after acquiring that subsystem's exclusive runtime ownership;
// opening another Store never resets a live reader's protection.
func (store *Store) ResetReadLeases(ctx context.Context, namespace string) error {
	if err := validatePinIdentity("read lease namespace", namespace); err != nil {
		return err
	}
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	if _, err := store.db.ExecContext(ctx, `DELETE FROM artifact_read_leases WHERE namespace = ?`, namespace); err != nil {
		return fmt.Errorf("reset Local Cache read leases: %w", err)
	}
	return nil
}
