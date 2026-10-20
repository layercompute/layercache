package config_test

import (
	"path/filepath"
	"testing"

	"github.com/layercache/layercache/internal/config"
)

func TestSnapshotCredentialsRequirePrivateBackendAndPreserveFileBinding(t *testing.T) {
	base, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"local", "public", "team"} {
		for _, path := range []string{"relative.json", filepath.Join(t.TempDir(), "grants.json")} {
			cfg := base
			cfg.Role = role
			cfg.SnapshotCredentialsFile = path
			err := cfg.ValidateSnapshotCredentials()
			if role != "team" || !filepath.IsAbs(path) {
				if err == nil {
					t.Fatal("unsafe grant file binding accepted")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			location := filepath.Join(t.TempDir(), "config.json")
			if err := config.Save(location, cfg); err != nil {
				t.Fatal(err)
			}
			loaded, err := config.Load(location)
			if err != nil || loaded.SnapshotCredentialsFile != path {
				t.Fatal("grant file binding lost after reload", err)
			}
		}
	}
}
