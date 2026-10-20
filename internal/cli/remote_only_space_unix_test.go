//go:build linux || darwin

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/layercache/layercache/internal/config"
	"golang.org/x/sys/unix"
)

func TestRemoteOnlyDisconnectWithoutRecoveryWrites(t *testing.T) {
	for _, withCredential := range []bool{false, true} {
		t.Run(fmt.Sprintf("credential-%v", withCredential), func(t *testing.T) {
			root := t.TempDir()
			cfg, err := config.Defaults()
			if err != nil {
				t.Fatal(err)
			}
			cfg.RemoteOnly = true
			cfg.TeamURL = "https://cache.example"
			cfg.DataDir = filepath.Join(root, "unused")
			if withCredential {
				cfg.GitHubCredentialAccount, err = newGitHubCredentialAccount(cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "client.json")
			if err = config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			// Create the empty configuration lock before the child forbids file growth.
			unlock, err := lockConfiguration(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			unlock()
			command := exec.Command(os.Args[0], "-test.run=^TestRemoteOnlyNoCredentialWriteLimitChild$")
			command.Env = append(os.Environ(), "LAYERCACHE_WRITE_LIMIT_CONFIG="+path)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("bounded write-limit child failed: %v %s", err, output)
			}
			if withCredential {
				assertFileContents(t, path, before)
			} else if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("credential-free disconnect retained configuration")
			}
			if _, err = os.Stat(cfg.DataDir); !os.IsNotExist(err) {
				t.Fatal("disconnect touched unused artifact directory")
			}
		})
	}
}

func TestRemoteOnlyNoCredentialWriteLimitChild(t *testing.T) {
	path := os.Getenv("LAYERCACHE_WRITE_LIMIT_CONFIG")
	if path == "" {
		return
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	if err = unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(filepath.Dir(path), "cannot-write-recovery")
	if err = os.WriteFile(probe, []byte("would fail any recovery copy"), 0600); !errors.Is(err, syscall.EFBIG) {
		t.Fatalf("fixture did not prohibit recovery writes: %v", err)
	}
	_ = os.Remove(probe)
	var output bytes.Buffer
	if cfg.GitHubCredentialAccount == "" {
		if err = runUninstall(context.Background(), []string{"--config", path, "--preserve-cache", "--json"}, &output, &output); err != nil {
			t.Fatalf("credential-free disconnect required backup writes: %v", err)
		}
	} else {
		calls := 0
		unlock, lockErr := lockConfiguration(context.Background(), path)
		if lockErr != nil {
			t.Fatal(lockErr)
		}
		defer unlock()
		removed, removeErr := uninstallRemoteOnlyClient(context.Background(), path, cfg, func(context.Context, string) error { calls++; return nil })
		if removeErr == nil || removed || calls != 0 {
			t.Fatal("credential-backed disconnect bypassed recovery prerequisite")
		}
	}
	os.Exit(0)
}
