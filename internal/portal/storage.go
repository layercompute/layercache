package portal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/layercache/layercache/internal/config"
)

const legacyStorageID = "legacy"

// StoragePlan reserves project capacities within one Team. Its limits and pool
// are frozen when the Team is created; changing an entitlement is a separate
// operation, not a side effect of editing the service defaults.
type StoragePlan struct {
	PoolID          string `json:"poolId"`
	ProjectMaxBytes int64  `json:"projectMaxBytes"`
	TeamMaxBytes    int64  `json:"teamMaxBytes"`
	MaxProjects     int    `json:"maxProjects"`
}

type storagePool struct {
	template    config.Config
	fingerprint string
}

type storagePolicy struct {
	legacy       storagePool
	pools        map[string]storagePool
	plans        map[string]StoragePlan
	defaultPlan  string
	creatorPlans map[string]string
}

var storageIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var postgresSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ValidateStorageConfiguration validates protected operator policy without
// connecting to a database or object store.
func ValidateStorageConfiguration(cfg Config) error {
	_, err := newStoragePolicy(cfg)
	return err
}

func newStoragePolicy(cfg Config) (*storagePolicy, error) {
	if cfg.ProjectTemplate.BazelRemote != nil {
		return nil, errors.New("managed project templates cannot inherit a Bazel backend")
	}
	legacy, err := cloneStorageTemplate(cfg.ProjectTemplate)
	if err != nil {
		return nil, err
	}
	fingerprint, err := storageBackendFingerprint(legacy, cfg.DataDir, false)
	if err != nil {
		return nil, err
	}
	p := &storagePolicy{legacy: storagePool{legacy, fingerprint}, pools: map[string]storagePool{}, plans: map[string]StoragePlan{}, creatorPlans: map[string]string{}, defaultPlan: cfg.DefaultStoragePlan}
	if len(cfg.HostedStoragePools) == 0 && len(cfg.StoragePlans) == 0 && cfg.DefaultStoragePlan == "" && len(cfg.TeamCreatorStoragePlans) == 0 {
		return p, nil
	}
	if len(cfg.HostedStoragePools) == 0 || len(cfg.HostedStoragePools) > 128 || len(cfg.StoragePlans) == 0 || len(cfg.StoragePlans) > 128 {
		return nil, errors.New("hosted storage requires one to 128 pools and plans")
	}
	for id, template := range cfg.HostedStoragePools {
		if !validStorageID(id) || template.Role != "team" || template.BazelRemote != nil || template.CloudPostgresURL == "" || template.CloudS3Endpoint == "" {
			return nil, errors.New("hosted storage pools require distinct valid IDs and private PostgreSQL/S3 templates without Bazel backends")
		}
		if err := template.Validate(); err != nil {
			return nil, fmt.Errorf("invalid hosted storage pool %s template", id)
		}
		cloned, err := cloneStorageTemplate(template)
		if err != nil {
			return nil, err
		}
		fingerprint, err := storageBackendFingerprint(cloned, "", true)
		if err != nil {
			return nil, fmt.Errorf("invalid hosted storage pool %s: %w", id, err)
		}
		p.pools[id] = storagePool{cloned, fingerprint}
	}
	for id, plan := range cfg.StoragePlans {
		pool, exists := p.pools[plan.PoolID]
		if !validStorageID(id) || !exists || plan.ProjectMaxBytes <= 0 || plan.ProjectMaxBytes > math.MaxInt64/128 || plan.TeamMaxBytes < plan.ProjectMaxBytes || plan.TeamMaxBytes > math.MaxInt64/128 || plan.MaxProjects < 1 || plan.MaxProjects > 10 || pool.template.CacheSoftBytes > plan.ProjectMaxBytes {
			return nil, errors.New("storage plans require a configured pool, bounded positive capacities and one to ten projects")
		}
		p.plans[id] = plan
	}
	if _, exists := p.plans[p.defaultPlan]; !exists {
		return nil, errors.New("defaultStoragePlan must identify a configured storage plan")
	}
	for creator, plan := range cfg.TeamCreatorStoragePlans {
		if !validRepositoryID(creator) {
			return nil, errors.New("teamCreatorStoragePlans requires canonical numeric GitHub user IDs")
		}
		if _, exists := p.plans[plan]; !exists {
			return nil, errors.New("teamCreatorStoragePlans references an unknown storage plan")
		}
		p.creatorPlans[creator] = plan
	}
	return p, nil
}

func validStorageID(id string) bool { return id != legacyStorageID && storageIDPattern.MatchString(id) }

func cloneStorageTemplate(cfg config.Config) (config.Config, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return config.Config{}, errors.New("copy storage template")
	}
	var cloned config.Config
	if err := json.Unmarshal(data, &cloned); err != nil {
		return config.Config{}, errors.New("copy storage template")
	}
	return cloned, nil
}

func storageFingerprint(value any) string {
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// The PostgreSQL namespace must be explicit for hosted pools. Otherwise rotating
// the login role can change PostgreSQL's implicit "$user" search path and select
// another set of metadata tables even though host/database remain unchanged.
func storageBackendFingerprint(cfg config.Config, localRoot string, hosted bool) (string, error) {
	if cfg.CloudPostgresURL == "" {
		return storageFingerprint([]string{"local-v1", localRoot}), nil
	}
	db, err := pgx.ParseConfig(cfg.CloudPostgresURL)
	if err != nil {
		return "", errors.New("invalid storage PostgreSQL identity")
	}
	schema := db.RuntimeParams["search_path"]
	if hosted && (!postgresSchemaPattern.MatchString(schema) || db.RuntimeParams["options"] != "") {
		return "", errors.New("hosted PostgreSQL requires an explicit single-schema search_path and no options override")
	}
	role := ""
	if !postgresSchemaPattern.MatchString(schema) {
		role = db.User // An implicit $user namespace changes when the role changes.
	}
	endpoint, err := url.Parse(cfg.CloudS3Endpoint)
	if err != nil || endpoint.Host == "" {
		return "", errors.New("invalid storage S3 identity")
	}
	hosts := []string{net.JoinHostPort(strings.ToLower(db.Host), strconv.Itoa(int(db.Port)))}
	for _, fallback := range db.Fallbacks {
		host := net.JoinHostPort(strings.ToLower(fallback.Host), strconv.Itoa(int(fallback.Port)))
		if host != hosts[len(hosts)-1] {
			hosts = append(hosts, host)
		}
	}
	return storageFingerprint(struct {
		Version, Endpoint, Bucket, Region, Database, Schema, ImplicitRole, Options string
		Hosts                                                                      []string
	}{"s3-pg-v1", strings.ToLower(endpoint.Scheme + "://" + endpoint.Host), cfg.CloudS3Bucket, cfg.CloudS3Region, db.Database, schema, role, db.RuntimeParams["options"], hosts}), nil
}

func (p *storagePolicy) newTeamPlan(creator string) (string, string) {
	id := p.defaultPlan
	if override, ok := p.creatorPlans[creator]; ok {
		id = override
	}
	if id == "" {
		return legacyStorageID, ""
	}
	return id, storageFingerprint(p.plans[id])
}

func (p *storagePolicy) teamPlan(id, fingerprint string) (StoragePlan, error) {
	if id == legacyStorageID && fingerprint == "" {
		return StoragePlan{}, nil
	}
	plan, exists := p.plans[id]
	if !exists || fingerprint != storageFingerprint(plan) {
		return StoragePlan{}, errors.New("persisted Team storage plan is missing or changed; explicit entitlement migration is required")
	}
	return plan, nil
}

// configureStorage upgrades only unbound legacy fingerprints. Existing Teams
// retain their legacy assignment; defaults apply exclusively to future Teams.
func (s *Store) configureStorage(ctx context.Context, policy *storagePolicy) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT storage_plan_id,storage_plan_fingerprint FROM lc_portal_teams`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, fingerprint string
		if err = rows.Scan(&id, &fingerprint); err != nil {
			break
		}
		if _, err = policy.teamPlan(id, fingerprint); err != nil {
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	projects, err := tx.QueryContext(ctx, `SELECT p.id,p.storage_pool_id,p.allocated_cache_bytes,p.storage_backend_fingerprint,COALESCE(t.storage_plan_id,'legacy'),COALESCE(t.storage_plan_fingerprint,'') FROM lc_portal_projects p LEFT JOIN lc_portal_teams t ON t.id=p.team_id`)
	if err != nil {
		return err
	}
	var legacyIDs []string
	for projects.Next() {
		var project Project
		var planID, planFingerprint string
		if err = projects.Scan(&project.ID, &project.StoragePoolID, &project.AllocatedCacheBytes, &project.StorageBackendFingerprint, &planID, &planFingerprint); err != nil {
			break
		}
		if project.StoragePoolID == legacyStorageID && project.StorageBackendFingerprint == "" && project.AllocatedCacheBytes == 0 && planID == legacyStorageID {
			legacyIDs = append(legacyIDs, project.ID)
			continue
		}
		plan, planErr := policy.teamPlan(planID, planFingerprint)
		if planErr != nil {
			err = planErr
			break
		}
		if _, err = policy.projectTemplate(project, plan); err != nil {
			break
		}
	}
	err = errors.Join(err, projects.Err(), projects.Close())
	if err != nil {
		return err
	}
	for _, id := range legacyIDs {
		if _, err = tx.ExecContext(ctx, `UPDATE lc_portal_projects SET storage_backend_fingerprint=$2 WHERE id=$1`, id, policy.legacy.fingerprint); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.storage = policy
	return nil
}

func (p *storagePolicy) projectTemplate(project Project, plan StoragePlan) (config.Config, error) {
	pool, exists := p.pools[project.StoragePoolID]
	if project.StoragePoolID == legacyStorageID {
		pool, exists = p.legacy, plan.PoolID == "" && project.AllocatedCacheBytes == 0
	} else if project.StoragePoolID != plan.PoolID || project.AllocatedCacheBytes != plan.ProjectMaxBytes {
		exists = false
	}
	if !exists || pool.fingerprint != project.StorageBackendFingerprint {
		return config.Config{}, errors.New("persisted project storage backend is missing or changed; explicit storage migration is required")
	}
	cfg, err := cloneStorageTemplate(pool.template)
	if project.StoragePoolID != legacyStorageID {
		cfg.MaxBytes = project.AllocatedCacheBytes
	}
	return cfg, err
}

func (s *Store) projectTemplate(ctx context.Context, project Project) (config.Config, error) {
	var planID, fingerprint string
	if err := s.db.QueryRowContext(ctx, `SELECT storage_plan_id,storage_plan_fingerprint FROM lc_portal_teams WHERE id=$1`, project.TeamID).Scan(&planID, &fingerprint); err != nil {
		// Older databases could contain an orphaned project. Preserve its
		// explicit legacy binding without granting it any Team membership.
		if !errors.Is(err, sql.ErrNoRows) || project.StoragePoolID != legacyStorageID {
			return config.Config{}, errors.New("read project storage assignment")
		}
		planID = legacyStorageID
	}
	plan, err := s.storage.teamPlan(planID, fingerprint)
	if err != nil {
		return config.Config{}, err
	}
	return s.storage.projectTemplate(project, plan)
}

func (s *Store) projectStorage(ctx context.Context, tx *sql.Tx, team string, count int) (string, int64, string, error) {
	if s.storage == nil {
		return legacyStorageID, 0, "", nil
	}
	var id, fingerprint string
	if err := tx.QueryRowContext(ctx, `SELECT storage_plan_id,storage_plan_fingerprint FROM lc_portal_teams WHERE id=$1`, team).Scan(&id, &fingerprint); err != nil {
		return "", 0, "", err
	}
	plan, err := s.storage.teamPlan(id, fingerprint)
	if err != nil {
		return "", 0, "", err
	}
	if id == legacyStorageID {
		return legacyStorageID, 0, s.storage.legacy.fingerprint, nil
	}
	var allocated int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(allocated_cache_bytes),0) FROM lc_portal_projects WHERE team_id=$1`, team).Scan(&allocated); err != nil {
		return "", 0, "", err
	}
	if count >= plan.MaxProjects || allocated < 0 || allocated > plan.TeamMaxBytes || plan.ProjectMaxBytes > plan.TeamMaxBytes-allocated {
		return "", 0, "", ErrLimit
	}
	return plan.PoolID, plan.ProjectMaxBytes, s.storage.pools[plan.PoolID].fingerprint, nil
}
