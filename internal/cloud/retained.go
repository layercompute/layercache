package cloud

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/retained"
)

// Each receipt reserves a fixed metadata envelope covering its declaration,
// restrictive claim, pin and tombstone, independently of blob deduplication.
const retainedMetadataBytes int64 = 8 << 10
const retainedMaxReceipts = 1024
const retainedMaxReservations = 8
const retainedPinNamespace = "retained-publication-v1"

type retainedContextKey struct{}
type retainedUpload struct {
	id      string
	expires time.Time
	size    int64
}

func retainedPublicationFromContext(ctx context.Context) string {
	upload, _ := ctx.Value(retainedContextKey{}).(retainedUpload)
	return upload.id
}
func retainedEntryMetadataBytes(project string) int64 {
	return estimateMetadataBytes(retainedKey(project, "0000000000000000000000000000000000000000000000000000000000000000"), []byte(`{}`))
}

func retainedKey(project, id string) artifact.Key {
	return artifact.Key{Project: project, Integration: "retained", Compatibility: retained.SchemaVersion, Native: id, Version: "1"}
}

// ReserveRetained admits the complete declared payload and bounded metadata
// before the caller may send bytes. Exact retries never extend either deadline.
func (store *Store) ReserveRetained(ctx context.Context, declaration retained.Declaration) (retained.Receipt, error) {
	if err := retained.Validate(declaration); err != nil {
		return retained.Receipt{}, err
	}
	if declaration.Producer.Project != store.config.Project {
		return retained.Receipt{}, ErrNotFound
	}
	declaration.RetainUntil = declaration.RetainUntil.UTC()
	id := retained.ID(declaration)
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return retained.Receipt{}, store.safeError("begin retained admission", err)
	}
	defer tx.Rollback()
	usage, err := store.lockProject(ctx, tx)
	if err != nil {
		return retained.Receipt{}, err
	}
	if err = store.expireRetainedTx(ctx, tx); err != nil {
		return retained.Receipt{}, err
	}
	existing, err := store.readRetained(ctx, tx, id)
	if err == nil {
		encoded, _ := json.Marshal(declaration)
		old, _ := json.Marshal(existing.Declaration)
		if string(encoded) != string(old) {
			return retained.Receipt{}, ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return retained.Receipt{}, store.safeError("commit retained retry", err)
		}
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return retained.Receipt{}, err
	}
	now := store.config.Now().UTC()
	if !declaration.RetainUntil.After(now) || declaration.RetainUntil.After(now.Add(retained.MaxRetention)) {
		return retained.Receipt{}, errors.New("retained deadline must be within the next 24 hours")
	}
	// Expiry may have changed reservation and metadata counters.
	usage, err = store.lockProject(ctx, tx)
	if err != nil {
		return retained.Receipt{}, err
	}
	var total, active int
	var held int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE state IN ('reserved','staging') OR staging_active),COALESCE(SUM(reserved_bytes),0) FROM layercache_retained_receipts_v1 WHERE project_id=$1`, store.config.Project).Scan(&total, &active, &held); err != nil {
		return retained.Receipt{}, store.safeError("count retained admissions", err)
	}
	if total >= retainedMaxReceipts || active >= retainedMaxReservations || declaration.Size > usage.quota || held+declaration.Size > usage.quota {
		return retained.Receipt{}, ErrQuota
	}
	// Include the generic entry envelope in the reservation's metadata charge;
	// its real charge replaces this allowance on commit.
	if usage.used+usage.reserved+declaration.Size > usage.quota || usage.metadataUsed+usage.reservedMetadata+retainedMetadataBytes+retainedEntryMetadataBytes(store.config.Project) > usage.metadataQuota {
		return retained.Receipt{}, ErrQuota
	}
	encoded, _ := json.Marshal(declaration)
	createdAt := now.Truncate(time.Microsecond)
	reservedUntil := createdAt.Add(retained.ReservationTTL)
	if declaration.RetainUntil.Before(reservedUntil) {
		reservedUntil = declaration.RetainUntil
	}
	receipt := retained.Receipt{SchemaVersion: retained.SchemaVersion, ID: id, State: retained.Reserved, Declaration: declaration, ReservedUntil: reservedUntil, CreatedAt: createdAt}
	_, err = tx.ExecContext(ctx, `INSERT INTO layercache_retained_receipts_v1(project_id,receipt_id,declaration_json,state,reserved_bytes,metadata_bytes,reserved_until,retain_until,created_at,tombstone_until) VALUES($1,$2,$3::jsonb,'reserved',$4,$5,$6,$7,$8,$9)`, store.config.Project, id, string(encoded), declaration.Size, retainedMetadataBytes, reservedUntil, declaration.RetainUntil, createdAt, declaration.RetainUntil.Add(retained.MaxRetention))
	if err != nil {
		return retained.Receipt{}, store.safeError("record retained admission", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE layercache_projects_v1 SET metadata_bytes=metadata_bytes+$2 WHERE project_id=$1`, store.config.Project, retainedMetadataBytes); err != nil {
		return retained.Receipt{}, store.safeError("account retained metadata", err)
	}
	if err = tx.Commit(); err != nil {
		return retained.Receipt{}, store.safeError("commit retained admission", err)
	}
	return receipt, nil
}

func (store *Store) readRetained(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (retained.Receipt, error) {
	if !retained.ValidID(id) {
		return retained.Receipt{}, ErrNotFound
	}
	receipt := retained.Receipt{ID: id, SchemaVersion: retained.SchemaVersion}
	var encoded []byte
	var published sql.NullTime
	err := query.QueryRowContext(ctx, `SELECT declaration_json,state,reserved_until,created_at,published_at FROM layercache_retained_receipts_v1 WHERE project_id=$1 AND receipt_id=$2`, store.config.Project, id).Scan(&encoded, &receipt.State, &receipt.ReservedUntil, &receipt.CreatedAt, &published)
	if errors.Is(err, sql.ErrNoRows) {
		return retained.Receipt{}, ErrNotFound
	}
	if err != nil {
		return retained.Receipt{}, store.safeError("read retained receipt", err)
	}
	if err = json.Unmarshal(encoded, &receipt.Declaration); err != nil {
		return retained.Receipt{}, store.safeError("decode retained declaration", err)
	}
	receipt.ReservedUntil = receipt.ReservedUntil.UTC()
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	if published.Valid {
		v := published.Time.UTC()
		receipt.PublishedAt = &v
	}
	return receipt, nil
}

// InspectRetained resolves unknown commit outcomes using SQL's durable state.
func (store *Store) InspectRetained(ctx context.Context, id string) (retained.Receipt, error) {
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return retained.Receipt{}, store.safeError("begin retained inspection", err)
	}
	defer tx.Rollback()
	if _, err = store.lockProject(ctx, tx); err != nil {
		return retained.Receipt{}, err
	}
	if err = store.expireRetainedTx(ctx, tx); err != nil {
		return retained.Receipt{}, err
	}
	receipt, err := store.readRetained(ctx, tx, id)
	if err != nil {
		return retained.Receipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return retained.Receipt{}, store.safeError("commit retained inspection", err)
	}
	return receipt, nil
}

// PublishRetained streams once through the production S3 SDK. The receipt,
// restrictive claim and system pin commit with the generic artifact entry.
func (store *Store) PublishRetained(ctx context.Context, id string, body io.Reader) (retained.Receipt, error) {
	receipt, err := store.startRetained(ctx, id)
	if err != nil {
		return retained.Receipt{}, err
	}
	if receipt.State == retained.Published {
		return receipt, nil
	}
	// Bound upload lifetime as well as bytes. Reservation expiry cannot leave
	// an indefinitely running request consuming new staging capacity.
	duration := receipt.ReservedUntil.Sub(store.config.Now().UTC())
	uploadCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	uploadCtx = context.WithValue(uploadCtx, retainedContextKey{}, retainedUpload{id: id, expires: receipt.ReservedUntil, size: receipt.Declaration.Size})
	exact := &retainedExactReader{body: body, remaining: receipt.Declaration.Size}
	_, _, err = store.putWithCommit(uploadCtx, retainedKey(store.config.Project, id), artifact.Metadata{}, exact, receipt.Declaration.Digest, func(ctx context.Context, tx *sql.Tx, entry artifact.Entry) error {
		current, err := store.readRetained(ctx, tx, id)
		if err != nil {
			return err
		}
		now := store.config.Now().UTC()
		if current.State != retained.Staging || !current.ReservedUntil.After(now) || !current.Declaration.RetainUntil.After(now) {
			return retained.ErrInactive
		}
		if entry.Size != current.Declaration.Size || entry.Digest != current.Declaration.Digest {
			return ErrCorrupt
		}
		var entryID int64
		if err = tx.QueryRowContext(ctx, `SELECT entry_id FROM layercache_entries_v1 WHERE project_id=$1 AND integration='retained' AND compatibility=$2 AND native_key=$3 AND version='1' AND ref_scope=''`, store.config.Project, retained.SchemaVersion, id).Scan(&entryID); err != nil {
			return store.safeError("find retained claim target", err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO layercache_retained_claims_v1(project_id,receipt_id,entry_id) VALUES($1,$2,$3)`, store.config.Project, id, entryID); err != nil {
			return store.safeError("publish retained claim", err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO layercache_entry_pins_v1(project_id,namespace,owner,entry_id,created_at) VALUES($1,$2,$3,$4,$5)`, store.config.Project, retainedPinNamespace, id, entryID, now); err != nil {
			return store.safeError("publish retained pin", err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE layercache_retained_receipts_v1 SET state='published',published_at=$3 WHERE project_id=$1 AND receipt_id=$2`, store.config.Project, id, now.Truncate(time.Microsecond)); err != nil {
			return store.safeError("publish retained receipt", err)
		}
		return nil
	})
	if err != nil {
		// Never undo a potentially committed receipt after a lost acknowledgement.
		// Reset only a still-staging session; Inspect is authoritative for callers.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = store.database.ExecContext(cleanupCtx, `UPDATE layercache_retained_receipts_v1 SET state=CASE WHEN state='staging' THEN 'reserved' ELSE state END, staging_active=false, reserved_bytes=CASE WHEN state IN ('expired','revoked') THEN 0 ELSE reserved_bytes END WHERE project_id=$1 AND receipt_id=$2 AND NOT EXISTS (SELECT 1 FROM layercache_uploads_v1 u WHERE u.project_id=$1 AND u.retained_receipt_id=$2)`, store.config.Project, id)
		return retained.Receipt{}, err
	}
	published, err := store.InspectRetained(ctx, id)
	if err != nil {
		return retained.Receipt{}, err
	}
	if published.State != retained.Published {
		return published, retained.ErrInactive
	}
	return published, nil
}

func (store *Store) startRetained(ctx context.Context, id string) (retained.Receipt, error) {
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return retained.Receipt{}, store.safeError("begin retained staging", err)
	}
	defer tx.Rollback()
	if _, err = store.lockProject(ctx, tx); err != nil {
		return retained.Receipt{}, err
	}
	if err = store.expireRetainedTx(ctx, tx); err != nil {
		return retained.Receipt{}, err
	}
	receipt, err := store.readRetained(ctx, tx, id)
	if err != nil {
		return retained.Receipt{}, err
	}
	if receipt.State == retained.Staging {
		return retained.Receipt{}, retained.ErrBusy
	}
	if receipt.State != retained.Published && receipt.State != retained.Reserved {
		return retained.Receipt{}, retained.ErrInactive
	}
	if receipt.State == retained.Reserved {
		if _, err = tx.ExecContext(ctx, `UPDATE layercache_retained_receipts_v1 SET state='staging',staging_active=true WHERE project_id=$1 AND receipt_id=$2`, store.config.Project, id); err != nil {
			return retained.Receipt{}, store.safeError("start retained staging", err)
		}
		receipt.State = retained.Staging
	}
	if err = tx.Commit(); err != nil {
		return retained.Receipt{}, store.safeError("commit retained staging", err)
	}
	return receipt, nil
}

type retainedExactReader struct {
	body      io.Reader
	remaining int64
}

func (reader *retainedExactReader) Read(p []byte) (int, error) {
	if reader.remaining < 0 {
		return 0, ErrCorrupt
	}
	// Read one byte beyond the declaration to reject oversized bodies before
	// publication, even when an HTTP Content-Length assertion was incorrect.
	if int64(len(p)) > reader.remaining+1 {
		p = p[:reader.remaining+1]
	}
	n, err := reader.body.Read(p)
	reader.remaining -= int64(n)
	if reader.remaining < 0 {
		return n, ErrCorrupt
	}
	if err == io.EOF && reader.remaining != 0 {
		return n, ErrCorrupt
	}
	return n, err
}

// OpenRetained admits a read and its blob lease in the same project-locked
// transaction that checks the deadline. Already admitted streams may finish.
func (store *Store) OpenRetained(ctx context.Context, id string) (retained.Receipt, io.ReadCloser, error) {
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return retained.Receipt{}, nil, store.safeError("begin retained read", err)
	}
	defer tx.Rollback()
	if _, err = store.lockProject(ctx, tx); err != nil {
		return retained.Receipt{}, nil, err
	}
	if err = store.expireRetainedTx(ctx, tx); err != nil {
		return retained.Receipt{}, nil, err
	}
	receipt, err := store.readRetained(ctx, tx, id)
	if err != nil {
		return retained.Receipt{}, nil, err
	}
	if receipt.State != retained.Published {
		return receipt, nil, retained.ErrInactive
	}
	leaseID, err := randomToken("read-")
	if err != nil {
		return retained.Receipt{}, nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO layercache_blob_read_leases_v1(lease_id,project_id,digest,expires_at) VALUES($1,$2,$3,$4)`, leaseID, store.config.Project, receipt.Declaration.Digest, store.config.Now().UTC().Add(readLeaseTTL)); err != nil {
		return retained.Receipt{}, nil, store.safeError("admit retained read", err)
	}
	if err = tx.Commit(); err != nil {
		return retained.Receipt{}, nil, store.safeError("commit retained read admission", err)
	}
	body, err := store.blobs.Open(ctx, store.blobKey(receipt.Declaration.Digest), receipt.Declaration.Digest, receipt.Declaration.Size)
	if err != nil {
		store.releaseReadLease(leaseID)
		if errors.Is(err, ErrCorrupt) {
			store.markRetainedUnavailable(id)
		}
		return receipt, nil, err
	}
	leased := &leasedReadCloser{body: body, store: store, leaseID: leaseID, digest: receipt.Declaration.Digest, nextRenewal: store.config.Now().UTC().Add(readLeaseTTL / 2)}
	return receipt, &retainedReadCloser{ReadCloser: leased, store: store, id: id}, nil
}

type retainedReadCloser struct {
	io.ReadCloser
	store *Store
	id    string
}

func (reader *retainedReadCloser) Read(p []byte) (int, error) {
	n, err := reader.ReadCloser.Read(p)
	if errors.Is(err, ErrCorrupt) {
		reader.store.markRetainedUnavailable(reader.id)
	}
	return n, err
}
func (store *Store) markRetainedUnavailable(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Keep the restrictive claim and bytes for diagnosis until expiry/revocation.
	_, _ = store.database.ExecContext(ctx, `UPDATE layercache_retained_receipts_v1 SET state='unavailable' WHERE project_id=$1 AND receipt_id=$2 AND state='published'`, store.config.Project, id)
}

// RevokeRetained is a storage operation; the authenticated adapter alone grants
// administrator authority. Tombstones preserve exact identity without pinning.
func (store *Store) RevokeRetained(ctx context.Context, id string) (retained.Receipt, error) {
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return retained.Receipt{}, store.safeError("begin retained revocation", err)
	}
	defer tx.Rollback()
	if _, err = store.lockProject(ctx, tx); err != nil {
		return retained.Receipt{}, err
	}
	if err = store.expireRetainedTx(ctx, tx); err != nil {
		return retained.Receipt{}, err
	}
	receipt, err := store.readRetained(ctx, tx, id)
	if err != nil {
		return retained.Receipt{}, err
	}
	if receipt.State != retained.Expired && receipt.State != retained.Revoked {
		if err = store.releaseRetainedTx(ctx, tx, id, retained.Revoked); err != nil {
			return retained.Receipt{}, err
		}
		receipt.State = retained.Revoked
	}
	if err = tx.Commit(); err != nil {
		return retained.Receipt{}, store.safeError("commit retained revocation", err)
	}
	return receipt, nil
}
func (store *Store) releaseRetainedTx(ctx context.Context, tx *sql.Tx, id, state string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM layercache_retained_claims_v1 WHERE project_id=$1 AND receipt_id=$2`, store.config.Project, id); err != nil {
		return store.safeError("release retained claim", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM layercache_entry_pins_v1 WHERE project_id=$1 AND namespace=$2 AND owner=$3`, store.config.Project, retainedPinNamespace, id); err != nil {
		return store.safeError("release retained pin", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE layercache_retained_receipts_v1 SET state=$3,reserved_bytes=CASE WHEN staging_active AND (reserved_until > $4 OR EXISTS (SELECT 1 FROM layercache_uploads_v1 u WHERE u.project_id=$1 AND u.retained_receipt_id=$2)) THEN reserved_bytes ELSE 0 END WHERE project_id=$1 AND receipt_id=$2`, store.config.Project, id, state, store.config.Now().UTC()); err != nil {
		return store.safeError("close retained obligation", err)
	}
	return nil
}
func (store *Store) expireRetainedTx(ctx context.Context, tx *sql.Tx) error {
	now := store.config.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE layercache_retained_receipts_v1 SET reserved_bytes=0,staging_active=false WHERE project_id=$1 AND ((state IN ('expired','revoked') AND reserved_until <= $2) OR state='published') AND NOT EXISTS (SELECT 1 FROM layercache_uploads_v1 u WHERE u.project_id=$1 AND u.retained_receipt_id=layercache_retained_receipts_v1.receipt_id)`, store.config.Project, now); err != nil {
		return store.safeError("release expired retained staging hold", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT receipt_id FROM layercache_retained_receipts_v1 WHERE project_id=$1 AND ((state IN ('reserved','staging') AND reserved_until <= $2) OR (state IN ('published','unavailable') AND retain_until <= $2))`, store.config.Project, now)
	if err != nil {
		return store.safeError("find expired retained obligations", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = store.releaseRetainedTx(ctx, tx, id, retained.Expired); err != nil {
			return err
		}
	}
	var released int64
	if err = tx.QueryRowContext(ctx, `WITH removed AS (DELETE FROM layercache_retained_receipts_v1 WHERE project_id=$1 AND state IN ('expired','revoked') AND tombstone_until <= $2 AND NOT staging_active AND NOT EXISTS (SELECT 1 FROM layercache_uploads_v1 u WHERE u.project_id=$1 AND u.retained_receipt_id=layercache_retained_receipts_v1.receipt_id) RETURNING metadata_bytes) SELECT COALESCE(SUM(metadata_bytes),0) FROM removed`, store.config.Project, now).Scan(&released); err != nil {
		return store.safeError("collect retained tombstones", err)
	}
	if released > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE layercache_projects_v1 SET metadata_bytes=metadata_bytes-$2 WHERE project_id=$1`, store.config.Project, released); err != nil {
			return store.safeError("account retained tombstone collection", err)
		}
	}
	return nil
}
func (store *Store) maintainRetained(ctx context.Context) error {
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return store.safeError("begin retained maintenance", err)
	}
	defer tx.Rollback()
	if _, err = store.lockProject(ctx, tx); err != nil {
		return err
	}
	if err = store.expireRetainedTx(ctx, tx); err != nil {
		return err
	}
	return store.safeError("commit retained maintenance", tx.Commit())
}

// Retained uploads keep their durable staging record and admitted bytes until
// object deletion succeeds. An object-store cleanup outage cannot enable an
// unbounded sequence of abandoned staging objects under one reservation.
func (store *Store) discardRetainedStage(uploadID, objectKey, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.blobs.Delete(ctx, objectKey); err != nil {
		return
	}
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err = store.lockProject(ctx, tx); err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM layercache_uploads_v1 WHERE project_id=$1 AND upload_id=$2`, store.config.Project, uploadID); err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, `UPDATE layercache_retained_receipts_v1 SET reserved_bytes=0,staging_active=false WHERE project_id=$1 AND receipt_id=$2 AND state IN ('published','expired','revoked')`, store.config.Project, id); err != nil {
		return
	}
	_ = tx.Commit()
}
