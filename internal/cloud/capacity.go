package cloud

import (
	"context"
	"errors"
	"strconv"
)

var ErrCapacityBounds = errors.New("capacity must fit the assigned ceiling and retention target")

// SetCapacity changes the operating quota below an assigned ceiling. Unlike
// SetQuota, it never evicts an entry. The project lock also fences publication,
// retained admission and changes made by another process.
func (store *Store) SetCapacity(ctx context.Context, actor string, expected, maximum, ceiling int64) (Quota, error) {
	if err := validateIdentity("capacity actor", actor, 512); err != nil {
		return Quota{}, err
	}
	if expected <= 0 || maximum <= 0 || maximum > ceiling || ceiling > 1<<53-1 || maximum < store.config.SoftBytes {
		return Quota{}, ErrCapacityBounds
	}
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return Quota{}, store.safeError("begin capacity change", err)
	}
	defer tx.Rollback()
	usage, err := store.lockProject(ctx, tx)
	if err != nil {
		return Quota{}, err
	}
	if usage.quota != expected {
		return Quota{}, ErrConflict
	}
	metadataLimit := metadataBudget(maximum)
	if usage.used > maximum || usage.reserved > maximum-usage.used || usage.retainedStaging > maximum || usage.metadataUsed > metadataLimit || usage.reservedMetadata > metadataLimit-usage.metadataUsed {
		return Quota{}, ErrQuota
	}
	if _, err := tx.ExecContext(ctx, `UPDATE layercache_projects_v1 SET quota_bytes=$2, metadata_quota_bytes=$3, updated_at=$4 WHERE project_id=$1`, store.config.Project, maximum, metadataLimit, store.config.Now().UTC()); err != nil {
		return Quota{}, store.safeError("change capacity", err)
	}
	if err := store.appendAuditTx(ctx, tx, AuditEvent{
		Project: store.config.Project, Actor: actor, Action: "capacity.set", Resource: "project", Outcome: "allowed",
		Attributes: map[string]string{"previousBytes": strconv.FormatInt(expected, 10), "maxBytes": strconv.FormatInt(maximum, 10), "ceilingBytes": strconv.FormatInt(ceiling, 10)},
		CreatedAt:  store.config.Now().UTC(),
	}); err != nil {
		return Quota{}, err
	}
	if err := tx.Commit(); err != nil {
		return Quota{}, store.safeError("commit capacity change", err)
	}
	return store.Quota(ctx)
}
