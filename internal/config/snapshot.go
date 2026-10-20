package config

import (
	"errors"
	"path/filepath"
)

// ValidateSnapshotCredentials checks the operator's backend-only file binding.
// The file is revalidated on every exchange and scoped request, so removal
// revokes grants without preventing unrelated project services from starting.
func (cfg Config) ValidateSnapshotCredentials() error {
	if cfg.SnapshotCredentialsFile == "" {
		return nil
	}
	if cfg.Role != "team" || !filepath.IsAbs(cfg.SnapshotCredentialsFile) || filepath.Clean(cfg.SnapshotCredentialsFile) != cfg.SnapshotCredentialsFile {
		return errors.New("snapshotCredentialsFile requires a private Team backend and an absolute clean path")
	}
	return nil
}
