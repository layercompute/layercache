package cloud

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/actionscache"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/directtransfer"
)

// DefaultDirectURLTTL bounds a presigned URL when the policy names no limit.
// Revocation reaches direct reads within this bound.
const DefaultDirectURLTTL = 2 * time.Minute

// A direct reader cannot renew its read lease, so the lease outlives the URL by
// the larger of the proxied allowance and a 1 MiB/s transfer of the object.
const directReadBytesPerSecond = int64(1 << 20)

// DirectRead is an issued read descriptor. URL is a bearer credential for one
// committed object: callers must never log or persist it.
type DirectRead struct {
	Entry     artifact.Entry
	URL       *url.URL
	ExpiresAt time.Time
	Lease     string
}

// DirectUpload is an issued staging upload. URL and Header are bearer
// credentials: the client must send Header unchanged so storage enforces the
// declared SHA-256.
type DirectUpload struct {
	URL       *url.URL
	Header    http.Header
	Upload    string
	ExpiresAt time.Time
}

// directPrincipal returns the authenticated subject a direct lease is issued
// to and the capability token's expiry, which caps every presigned URL.
func directPrincipal(ctx context.Context) (string, time.Time, bool) {
	claims, ok := access.ClaimsFromContext(ctx)
	if !ok || claims.Subject == "" {
		return "", time.Time{}, false
	}
	return claims.Subject, claims.ExpiresAt, true
}

// directExpiry caps a URL at the policy limit, the token expiry and notAfter,
// whichever is earliest. SigV4 expiry has one-second granularity, so the
// reported expiry is rounded down to match what storage enforces.
func (store *Store) directExpiry(ctx context.Context, notAfter time.Time) (string, time.Time, time.Duration, error) {
	principal, tokenExpiry, ok := directPrincipal(ctx)
	if !ok {
		return "", time.Time{}, 0, ErrDirectTransferUnavailable
	}
	now := store.config.Now().UTC()
	limit := store.config.DirectTransfer.MaxURL
	if limit <= 0 {
		limit = DefaultDirectURLTTL
	}
	expiresAt := now.Add(limit)
	for _, bound := range []time.Time{tokenExpiry, notAfter} {
		if !bound.IsZero() && bound.Before(expiresAt) {
			expiresAt = bound
		}
	}
	ttl := expiresAt.Sub(now).Truncate(time.Second)
	if ttl < time.Second {
		return "", time.Time{}, 0, ErrDirectTransferUnavailable
	}
	return principal, now.Add(ttl), ttl, nil
}

// IssueDirectRead runs the proxied read's lookup and lease transaction, then
// presigns a GET for the committed content-addressed object instead of
// opening it. The lease stays live until the transfer can no longer be in
// flight, and is consumed by CompleteDirectRead. It returns
// ErrDirectTransferUnavailable when the caller must use the proxied read.
func (store *Store) IssueDirectRead(ctx context.Context, key artifact.Key, notAfter time.Time) (DirectRead, error) {
	if !store.config.DirectTransfer.Read {
		return DirectRead{}, ErrDirectTransferUnavailable
	}
	principal, expiresAt, ttl, err := store.directExpiry(ctx, notAfter)
	if err != nil {
		return DirectRead{}, err
	}
	entry, leaseID, err := store.readEntryWithLeaseFor(ctx, key, readLease{
		principal: principal,
		resource:  cacheEntryAuditResource(key),
		expires: func(_ time.Time, entry artifact.Entry) time.Time {
			transfer := time.Duration(entry.Size/directReadBytesPerSecond) * time.Second
			return expiresAt.Add(max(readLeaseTTL, transfer))
		},
	})
	if err != nil {
		return DirectRead{}, err
	}
	// Public-origin entries keep the proxied path and its trust checks.
	if entry.Metadata.Values["origin"] == "public" {
		store.releaseReadLease(leaseID)
		return DirectRead{}, ErrDirectTransferUnavailable
	}
	// Only the key resolved from this project's committed entry is signed.
	location, err := store.blobs.PresignGet(ctx, store.blobKey(entry.Digest), ttl)
	if err != nil {
		store.releaseReadLease(leaseID)
		return DirectRead{}, ErrDirectTransferUnavailable
	}
	return DirectRead{Entry: entry, URL: location, ExpiresAt: expiresAt, Lease: leaseID}, nil
}

// CompleteDirectRead consumes a live lease issued to the same principal for the
// same entry, after the client verified the complete digest and size. Only
// then is the read counted for retention. Any other lease, a replay, or a
// mismatched report returns ErrNotFound.
func (store *Store) CompleteDirectRead(ctx context.Context, key artifact.Key, lease, digest string, size int64) error {
	if err := store.validateKey(key); err != nil {
		return err
	}
	principal, _, ok := directPrincipal(ctx)
	digest = strings.TrimPrefix(digest, "sha256:")
	if !ok || lease == "" || len(lease) > 128 || !validDigest(digest) || size < 0 {
		return ErrNotFound
	}
	result, err := store.database.ExecContext(ctx, `
		DELETE FROM layercache_blob_read_leases_v1 AS lease
		USING layercache_artifacts_v1 AS object
		WHERE lease.lease_id = $1 AND lease.project_id = $2 AND lease.principal = $3
			AND lease.entry_resource = $4 AND lease.digest = $5 AND lease.expires_at > $6
			AND object.project_id = lease.project_id AND object.digest = lease.digest
			AND object.size_bytes = $7`,
		lease, store.config.Project, principal, cacheEntryAuditResource(key), digest,
		store.config.Now().UTC(), size)
	if err != nil {
		return store.safeError("complete direct cloud read", err)
	}
	if completed, err := result.RowsAffected(); err != nil || completed != 1 {
		return ErrNotFound
	}
	store.recordVerifiedRead(digest)
	return nil
}

// BeginDirectUpload admits a declared object against the project quota,
// records the staged upload, and presigns a PUT for a fresh staging key that
// storage accepts only with the declared SHA-256.
func (store *Store) BeginDirectUpload(ctx context.Context, key artifact.Key, digest string, size int64) (DirectUpload, error) {
	if !store.config.DirectTransfer.Write {
		return DirectUpload{}, ErrDirectTransferUnavailable
	}
	if err := store.validateKey(key); err != nil {
		return DirectUpload{}, err
	}
	digest = strings.TrimPrefix(digest, "sha256:")
	decoded, err := hex.DecodeString(digest)
	if !validDigest(digest) || err != nil || size < 0 {
		return DirectUpload{}, ErrCorrupt
	}
	principal, expiresAt, ttl, err := store.directExpiry(ctx, time.Time{})
	if err != nil {
		return DirectUpload{}, err
	}
	quota, err := store.Quota(ctx)
	if err != nil {
		return DirectUpload{}, err
	}
	if size > quota.MaxBytes {
		return DirectUpload{}, ErrQuota
	}
	uploadID, err := randomToken("upload-")
	if err != nil {
		return DirectUpload{}, err
	}
	stageKey := "projects/" + store.namespace + "/staging/" + uploadID
	var checksum [32]byte
	copy(checksum[:], decoded)
	location, header, err := store.blobs.PresignPut(ctx, stageKey, checksum, ttl)
	if err != nil {
		return DirectUpload{}, ErrDirectTransferUnavailable
	}
	now := store.config.Now().UTC()
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO layercache_uploads_v1(
			upload_id, project_id, object_key, state, digest, size_bytes, created_at, expires_at,
			principal, entry_resource
		) VALUES($1, $2, $3, 'uploading', $4, $5, $6, $7, $8, $9)`,
		uploadID, store.config.Project, stageKey, digest, size, now, now.Add(store.config.StageTTL),
		principal, cacheEntryAuditResource(key)); err != nil {
		return DirectUpload{}, store.safeError("record direct cloud upload", err)
	}
	return DirectUpload{URL: location, Header: header, Upload: uploadID, ExpiresAt: expiresAt}, nil
}

// CommitDirectUpload claims an upload issued to the same principal for the same
// entry, checks the staged object's size, and runs the same commit as a
// proxied publication. Storage enforced the signed checksum, so the staged
// bytes are not read again. The staging object is always discarded.
func (store *Store) CommitDirectUpload(
	ctx context.Context,
	key artifact.Key,
	metadata artifact.Metadata,
	upload string,
) (artifact.Entry, bool, error) {
	if !store.config.DirectTransfer.Write {
		return artifact.Entry{}, false, ErrDirectTransferUnavailable
	}
	if err := store.validateKey(key); err != nil {
		return artifact.Entry{}, false, err
	}
	publication, err := preparePublication(metadata)
	if err != nil {
		return artifact.Entry{}, false, err
	}
	principal, _, ok := directPrincipal(ctx)
	if !ok || upload == "" || len(upload) > 128 {
		return artifact.Entry{}, false, ErrNotFound
	}
	staged := stagedBlob{MediaType: publication.mediaType}
	err = store.database.QueryRowContext(ctx, `
		UPDATE layercache_uploads_v1 SET state = 'staged'
		WHERE upload_id = $1 AND project_id = $2 AND principal = $3 AND entry_resource = $4
			AND state = 'uploading' AND expires_at > $5
		RETURNING object_key, digest, size_bytes`,
		upload, store.config.Project, principal, cacheEntryAuditResource(key), store.config.Now().UTC()).
		Scan(&staged.Key, &staged.Digest, &staged.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.Entry{}, false, ErrNotFound
	}
	if err != nil {
		return artifact.Entry{}, false, store.safeError("claim direct cloud upload", err)
	}
	defer store.discardStage(upload, staged.Key)
	info, err := store.blobs.Stat(ctx, staged.Key)
	if errors.Is(err, ErrNotFound) {
		return artifact.Entry{}, false, ErrCorrupt
	}
	if err != nil {
		return artifact.Entry{}, false, store.safeError("inspect direct cloud upload", err)
	}
	if info.Size != staged.Size {
		return artifact.Entry{}, false, ErrCorrupt
	}
	return store.commitStaged(ctx, key, metadata, publication, staged, nil)
}

// IssueDirectArchive issues a direct read of a committed Actions archive. It
// applies Open's scope and entry checks; Public-origin entries and any entry
// whose object no longer matches stay on the proxied, verified download.
func (storage *ActionsStorage) IssueDirectArchive(
	ctx context.Context,
	request actionscache.OpenRequest,
	notAfter time.Time,
) (directtransfer.Descriptor, error) {
	if err := storage.validateScope(request.Scope); err != nil {
		return directtransfer.Descriptor{}, err
	}
	entry, nativeKey, err := storage.entryByID(ctx, request)
	if err != nil {
		return directtransfer.Descriptor{}, err
	}
	if entry.Public != nil || entry.Origin == actionscache.SourcePublicCache {
		return directtransfer.Descriptor{}, ErrDirectTransferUnavailable
	}
	read, err := storage.store.IssueDirectRead(ctx, storage.openKey(request, entry, nativeKey), notAfter)
	if err != nil {
		return directtransfer.Descriptor{}, err
	}
	if read.Entry.Size != entry.Size {
		storage.store.releaseReadLease(read.Lease)
		return directtransfer.Descriptor{}, ErrDirectTransferUnavailable
	}
	return directtransfer.Descriptor{
		Transfer: directtransfer.Kind, Method: http.MethodGet, URL: read.URL.String(),
		Digest: directtransfer.Digest(read.Entry.Digest), Size: read.Entry.Size,
		ExpiresAt: read.ExpiresAt, Lease: read.Lease,
	}, nil
}

// CompleteDirectArchive consumes a direct Actions read lease once.
func (storage *ActionsStorage) CompleteDirectArchive(
	ctx context.Context,
	request actionscache.OpenRequest,
	lease, digest string,
	size int64,
) error {
	if err := storage.validateScope(request.Scope); err != nil {
		return actionscache.ErrNotFound
	}
	entry, nativeKey, err := storage.entryByID(ctx, request)
	if err != nil {
		return err
	}
	err = storage.store.CompleteDirectRead(ctx, storage.openKey(request, entry, nativeKey), lease, digest, size)
	if errors.Is(err, ErrNotFound) {
		return actionscache.ErrNotFound
	}
	return err
}

func (storage *ActionsStorage) openKey(request actionscache.OpenRequest, entry actionscache.Entry, nativeKey string) artifact.Key {
	return artifact.Key{
		Integration: "actions", Project: storage.store.config.Project,
		Compatibility: request.Scope.Compatibility, Native: nativeKey,
		Version: entry.Version, Ref: entry.Ref,
	}
}

var _ actionscache.DirectArchiveStorage = (*ActionsStorage)(nil)
