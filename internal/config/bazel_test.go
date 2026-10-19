package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/config"
)

func TestBazelRemoteBindsDedicatedPrivateProjectAndProfile(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*config.Config)
	}{
		{"copied project", func(cfg *config.Config) { cfg.ProjectID = "another-project" }},
		{"copied profile", func(cfg *config.Config) { cfg.CompatibilityID = "another-profile" }},
		{"public", func(cfg *config.Config) { cfg.Role = "public" }},
		{"unbound project", func(cfg *config.Config) { cfg.BazelRemote.ProjectID = "" }},
		{"zero blob bound", func(cfg *config.Config) { cfg.BazelRemote.MaxBlobBytes = 0 }},
		{"oversized staging bound", func(cfg *config.Config) { cfg.BazelRemote.MaxBlobBytes = cfg.MaxBytes + 1 }},
		{"zero timeout", func(cfg *config.Config) { cfg.BazelRemote.RequestTimeout = 0 }},
		{"excessive timeout", func(cfg *config.Config) { cfg.BazelRemote.RequestTimeout = time.Hour }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := bazelConfig(t)
			test.change(&cfg)
			if err := cfg.ValidateBazelRemote(); err == nil {
				t.Fatal("unsafe native engine binding was accepted")
			}
		})
	}
	for _, role := range []string{"local", "team"} {
		t.Run(role, func(t *testing.T) {
			cfg := bazelConfig(t)
			cfg.Role = role
			path := filepath.Join(t.TempDir(), "config.json")
			if err := config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			loaded, err := config.Load(path)
			if err != nil || loaded.BazelRemote == nil || *loaded.BazelRemote != *cfg.BazelRemote {
				t.Fatalf("native engine binding did not survive save/load: %+v %v", loaded.BazelRemote, err)
			}
		})
	}
}

func TestBazelRemoteAcceptsOnlyBareLiteralLoopbackOrigins(t *testing.T) {
	for _, candidate := range []string{
		"http://example.com:8080", "http://localhost:8080", "https://127.0.0.1:8080", "http://127.0.0.1",
		"http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:08080",
		"http://127.0.0.1:8080/", "http://127.0.0.1:8080/ac", "http://127.0.0.1:8080?",
		"http://127.0.0.1:8080?project=other", "http://127.0.0.1:8080#fragment", "http://127.0.0.1:8080#",
		"http://user:password@127.0.0.1:8080", " http://127.0.0.1:8080", "http://127.0.0.2:8080",
	} {
		t.Run(candidate, func(t *testing.T) {
			cfg := bazelConfig(t)
			cfg.BazelRemote.URL = candidate
			if err := cfg.ValidateBazelRemote(); err == nil {
				t.Fatal("unsafe native engine origin was accepted")
			}
		})
	}
	cfg := bazelConfig(t)
	cfg.BazelRemote.URL = "http://[::1]:8080"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func bazelConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.BazelRemote = &config.BazelRemoteConfig{
		ProjectID: cfg.ProjectID, CompatibilityID: cfg.CompatibilityID, URL: "http://127.0.0.1:8080",
		MaxBlobBytes: 1 << 20, RequestTimeout: 30 * time.Second,
	}
	return cfg
}
