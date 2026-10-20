package portal

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/layercache/layercache/internal/config"
)

// OperatorProject explicitly attaches an existing private cache to a dashboard
// owner. Config is loaded only from a protected operator file. Dashboard
// membership does not replace the cache's existing SQL membership authority.
type OperatorProject struct {
	Config       config.Config `json:"-"`
	Name         string        `json:"name"`
	RepositoryID string        `json:"repositoryId"`
	Owner        User          `json:"owner"`
	TeamID       string        `json:"teamId"`
	TeamName     string        `json:"teamName"`
	Alias        string        `json:"alias,omitempty"`
}

var operatorProjectIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)

func operatorFingerprint(project OperatorProject) (string, string, error) {
	cfg := project.Config
	if !validName(project.Name) || !validName(project.TeamName) || !storageIDPattern.MatchString(project.TeamID) ||
		(project.Alias != "" && !storageIDPattern.MatchString(project.Alias)) ||
		!validRepositoryID(project.RepositoryID) || !validRepositoryID(project.Owner.ID) ||
		!loginPattern.MatchString(project.Owner.Login) || strings.ToLower(project.Owner.Login) != project.Owner.Login ||
		cfg.Role != "team" || cfg.CloudPostgresURL == "" || cfg.CloudS3Endpoint == "" ||
		cfg.TeamURL != "" || cfg.PublicURL != "" || !filepath.IsAbs(cfg.DataDir) ||
		!repositoryPattern.MatchString(cfg.ActionsRepository) ||
		len(cfg.ProjectID) > 256 || strings.TrimSpace(cfg.ProjectID) != cfg.ProjectID ||
		!operatorProjectIDPattern.MatchString(cfg.ProjectID) ||
		strings.ContainsAny(cfg.ProjectID, "\\%?#") || strings.IndexFunc(cfg.ProjectID, unicode.IsControl) >= 0 {
		return "", "", errors.New("invalid operator project identity or private cloud configuration")
	}
	for _, part := range strings.Split(cfg.ProjectID, "/") {
		if part == "" || part == "." || part == ".." {
			return "", "", errors.New("invalid operator project identity")
		}
	}
	if err := cfg.Validate(); err != nil {
		return "", "", errors.New("invalid operator project configuration")
	}
	backend, err := storageBackendFingerprint(cfg, "", true)
	if err != nil {
		return "", "", err
	}
	// Bind behavior and signing identity, while permitting credential rotation
	// and preserving current SQL memberships instead of reapplying this file.
	cfg.CloudPostgresURL, cfg.CloudS3Endpoint, cfg.CloudS3Bucket, cfg.CloudS3Region = "", "", "", ""
	cfg.CloudS3AccessKey, cfg.CloudS3SecretKey, cfg.TeamMembers = "", "", nil
	cfg.Listen, cfg.ActionsArchiveBaseURL, cfg.SnapshotCredentialsFile = "", "", ""
	return storageFingerprint(struct {
		Project OperatorProject
		Config  config.Config
		Backend string
	}{OperatorProject{Name: project.Name, RepositoryID: project.RepositoryID, Owner: project.Owner, TeamID: project.TeamID, TeamName: project.TeamName, Alias: project.Alias}, cfg, backend}), backend, nil
}

func validateOperatorProjects(projects []OperatorProject) error {
	if len(projects) > 128 {
		return errors.New("operator adoption is limited to 128 projects")
	}
	ids, secrets, directories, aliases := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	teams := map[string]string{}
	for _, project := range projects {
		if _, _, err := operatorFingerprint(project); err != nil {
			return err
		}
		cfg := project.Config
		alias := operatorAlias(project)
		if ids[cfg.ProjectID] || secrets[cfg.LocalToken] || directories[filepath.Clean(cfg.DataDir)] || aliases[alias] {
			return errors.New("operator projects require distinct identities, signing keys and directories")
		}
		ids[cfg.ProjectID], secrets[cfg.LocalToken], directories[filepath.Clean(cfg.DataDir)] = true, true, true
		aliases[alias] = true
		binding := storageFingerprint([]string{project.Owner.ID, project.Owner.Login, project.TeamName})
		if previous, exists := teams[project.TeamID]; exists && previous != binding {
			return errors.New("operator team owner and name must agree")
		}
		teams[project.TeamID] = binding
	}
	return nil
}

func operatorAlias(project OperatorProject) string {
	if project.Alias != "" {
		return project.Alias
	}
	return "operator-" + storageFingerprint(project.Config.ProjectID)[:32]
}

// adoptOperatorProjects is a bounded startup operation, never a public API.
// Repeated startup validates immutable bindings without recreating removed
// memberships, regranting permissions or importing a replacement cache.
func (s *Store) adoptOperatorProjects(ctx context.Context, projects []OperatorProject) error {
	if err := validateOperatorProjects(projects); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	bindings := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT project_id,fingerprint FROM lc_portal_operator_projects`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, fingerprint string
		if err = rows.Scan(&id, &fingerprint); err != nil {
			break
		}
		bindings[id] = fingerprint
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, project := range projects {
		fingerprint, backend, _ := operatorFingerprint(project)
		cfg := project.Config
		if existing, found := bindings[cfg.ProjectID]; found {
			if existing != fingerprint {
				return errors.New("operator project binding changed; explicit migration is required")
			}
			var matches int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lc_portal_projects p JOIN lc_portal_teams t ON p.team_id=t.id WHERE p.id=$1 AND p.operator_managed=TRUE AND t.operator_managed=TRUE AND p.team_id=$2 AND p.repository_id=$3 AND p.secret=$4 AND p.storage_backend_fingerprint=$5 AND t.created_by=$6 AND p.name=$7 AND p.repository=$8 AND p.default_ref=$9 AND t.name=$10 AND t.kind='team' AND p.storage_pool_id='legacy' AND p.allocated_cache_bytes=0`, cfg.ProjectID, project.TeamID, project.RepositoryID, cfg.LocalToken, backend, project.Owner.ID, project.Name, cfg.ActionsRepository, cfg.ActionsDefaultRef, project.TeamName).Scan(&matches); err != nil {
				return err
			}
			if matches != 1 {
				return errors.New("persisted operator project identity is missing or changed")
			}
			delete(bindings, cfg.ProjectID)
			continue
		}
		var total int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lc_portal_projects`).Scan(&total); err != nil {
			return err
		}
		if total >= 128 {
			return ErrLimit
		}
		var owner, name string
		var operator bool
		err = tx.QueryRowContext(ctx, `SELECT created_by,name,operator_managed FROM lc_portal_teams WHERE id=$1`, project.TeamID).Scan(&owner, &name, &operator)
		if errors.Is(err, sql.ErrNoRows) {
			var teams, owned int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lc_portal_teams`).Scan(&teams); err != nil {
				return err
			}
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lc_portal_teams WHERE created_by=$1`, project.Owner.ID).Scan(&owned); err != nil {
				return err
			}
			if teams >= 128 || owned >= 5 {
				return ErrLimit
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO lc_portal_users(id,login) VALUES($1,$2) ON CONFLICT(id) DO NOTHING`, project.Owner.ID, project.Owner.Login); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO lc_portal_teams(id,name,created_by,kind,operator_managed) VALUES($1,$2,$3,'team',TRUE)`, project.TeamID, project.TeamName, project.Owner.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO lc_portal_members(team_id,user_id,role) VALUES($1,$2,'admin')`, project.TeamID, project.Owner.ID); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if !operator || owner != project.Owner.ID || name != project.TeamName {
			return errors.New("operator team conflicts with an existing authority")
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lc_portal_projects WHERE team_id=$1`, project.TeamID).Scan(&count); err != nil {
			return err
		}
		if count >= 10 {
			return ErrLimit
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO lc_portal_projects(id,team_id,name,repository,repository_id,default_ref,secret,storage_backend_fingerprint,operator_managed) VALUES($1,$2,$3,$4,$5,$6,$7,$8,TRUE)`, cfg.ProjectID, project.TeamID, project.Name, cfg.ActionsRepository, project.RepositoryID, cfg.ActionsDefaultRef, cfg.LocalToken, backend); err != nil {
			return errors.New("operator project conflicts with an existing project")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO lc_portal_operator_projects(project_id,fingerprint) VALUES($1,$2)`, cfg.ProjectID, fingerprint); err != nil {
			return err
		}
		if err = audit(ctx, tx, project.TeamID, project.Owner.ID, "project.operator-adopt", cfg.ProjectID); err != nil {
			return err
		}
	}
	if len(bindings) != 0 {
		return errors.New("operator project configuration is missing; explicit removal is required")
	}
	return tx.Commit()
}
