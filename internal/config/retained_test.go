package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/config"
)

func TestRetainedArtifactsRequireExplicitPrivatePersistence(t *testing.T) {
	base, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if base.RetainedArtifactsEnabled {
		t.Fatal("retained publication must not be enabled by default")
	}
	for _, role := range []string{"local", "public", "team"} {
		for _, persisted := range []bool{false, true} {
			t.Run(role+map[bool]string{false: "-filesystem", true: "-postgres"}[persisted], func(t *testing.T) {
				cfg := base
				cfg.Role = role
				cfg.RetainedArtifactsEnabled = true
				if persisted {
					cfg.CloudPostgresURL = "postgres://fixture@127.0.0.1:5432/fixture?sslmode=disable"
					cfg.CloudS3Endpoint = "http://127.0.0.1:9000"
					cfg.CloudS3Bucket = "retained-fixture"
					cfg.CloudS3Region = "us-east-1"
					cfg.CloudS3AccessKey = "fixture"
					cfg.CloudS3SecretKey = "fixture-only"
				}
				err := cfg.Validate()
				if role != "team" || !persisted {
					if err == nil || !strings.Contains(err.Error(), "retained artifacts require") {
						t.Fatalf("unqualified retained runtime accepted: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "config.json")
				if err := config.Save(path, cfg); err != nil {
					t.Fatal(err)
				}
				loaded, err := config.Load(path)
				if err != nil || !loaded.RetainedArtifactsEnabled {
					t.Fatalf("explicit enablement did not survive reload: %+v, %v", loaded.RetainedArtifactsEnabled, err)
				}
			})
		}
	}
}
