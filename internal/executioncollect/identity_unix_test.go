//go:build linux || darwin

package executioncollect

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInstallationIdentityStableAcrossWorkspacesAndOrigins(t *testing.T) {
	root := t.TempDir()
	configuration := filepath.Join(root, "user-config")
	t.Setenv("XDG_CONFIG_HOME", configuration)
	// Darwin's native configuration directory is derived from HOME.
	t.Setenv("HOME", filepath.Join(root, "user-home"))
	first, err := LoadIdentity(context.Background(), "https://CACHE.EXAMPLE:443/")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first-worktree", "second-worktree"} {
		workspace := filepath.Join(root, name)
		if err := os.Mkdir(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Chdir(workspace)
		current, err := LoadIdentity(context.Background(), "https://cache.example")
		if err != nil || current.InstallationID() != first.InstallationID() {
			t.Fatalf("workspace affected client identity: %v", err)
		}
	}
	other, err := LoadIdentity(context.Background(), "https://other.example")
	if err != nil || other.InstallationID() == first.InstallationID() {
		t.Fatal("origin did not isolate installation identity", err)
	}
	if first.FilesystemID("device:one") == other.FilesystemID("device:one") || first.FilesystemID("device:one") == first.InstallationID() {
		t.Fatal("filesystem and origin domains were not isolated")
	}
	decoded, err := hex.DecodeString(first.InstallationID())
	if err != nil || len(decoded) != 32 || strings.Contains(first.InstallationID(), root) {
		t.Fatal("identifier is not an opaque digest")
	}
}

func TestInstallationSeedConcurrentCreationAndPermissions(t *testing.T) {
	directory := t.TempDir()
	const workers = 24
	identities := make(chan string, workers)
	failures := make(chan error, workers)
	var work sync.WaitGroup
	for range workers {
		work.Add(1)
		go func() {
			defer work.Done()
			identity, err := loadIdentityAt(context.Background(), directory, "https://cache.example")
			if err != nil {
				failures <- err
				return
			}
			identities <- identity.InstallationID()
		}()
	}
	work.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	close(identities)
	first := ""
	for id := range identities {
		if first == "" {
			first = id
		} else if id != first {
			t.Fatal("concurrent clients received distinct identities")
		}
	}
	base := filepath.Join(directory, "layercache", "client-identity")
	for _, relative := range []string{"layercache", "layercache/client-identity"} {
		info, err := os.Stat(filepath.Join(directory, relative))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("identity directory permissions: %v", err)
		}
	}
	for _, name := range []string{"seed", "identity.lock"} {
		info, err := os.Stat(filepath.Join(base, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("identity file permissions: %v", err)
		}
	}
	seed, err := os.ReadFile(filepath.Join(base, "seed"))
	if err != nil || len(seed) != 32 {
		t.Fatal("seed was incomplete", err)
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 2 {
		t.Fatal("identity staging files remained", err)
	}
}

func TestInstallationIdentityRefusesUnsafeFilesAndCanceledLock(t *testing.T) {
	for _, name := range []string{"directory-symlink", "seed-symlink", "seed-permissions", "seed-hardlink", "seed-size"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "configuration")
			base := filepath.Join(directory, "layercache", "client-identity")
			if err := os.MkdirAll(base, 0o700); err != nil {
				t.Fatal(err)
			}
			seed := filepath.Join(base, "seed")
			outside := filepath.Join(root, "outside")
			if err := os.WriteFile(outside, []byte(strings.Repeat("x", 32)), 0o600); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "directory-symlink":
				if err := os.Remove(base); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root, base); err != nil {
					t.Fatal(err)
				}
			case "seed-symlink":
				if err := os.Symlink(outside, seed); err != nil {
					t.Fatal(err)
				}
			case "seed-hardlink":
				if err := os.Link(outside, seed); err != nil {
					t.Fatal(err)
				}
			case "seed-permissions":
				if err := os.WriteFile(seed, []byte(strings.Repeat("x", 32)), 0o640); err != nil {
					t.Fatal(err)
				}
			case "seed-size":
				if err := os.WriteFile(seed, []byte("partial"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadIdentityAt(context.Background(), directory, "https://cache.example"); err == nil {
				t.Fatal("unsafe identity state was accepted")
			}
			contents, err := os.ReadFile(outside)
			if err != nil || string(contents) != strings.Repeat("x", 32) {
				t.Fatal("outside file was modified")
			}
		})
	}
	directory := t.TempDir()
	if _, err := loadIdentityAt(context.Background(), directory, "https://cache.example"); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(directory, "layercache", "client-identity", "identity.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := loadIdentityAt(ctx, directory, "https://cache.example"); err == nil {
		t.Fatal("contended identity ignored cancellation")
	}
}

func TestIdentityRejectsInvalidServiceOriginsWithoutCreatingState(t *testing.T) {
	for _, origin := range []string{"file:///tmp/cache", "https://user:secret@cache.example", "https://cache.example/path", "https://cache.example?query=value", "https://cache.example/#fragment"} {
		directory := filepath.Join(t.TempDir(), "uncreated")
		if _, err := loadIdentityAt(context.Background(), directory, origin); err == nil {
			t.Fatal("invalid service origin accepted")
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("invalid origin created installation state")
		}
	}
}

func TestCommittedIdentitySurvivesInterruptedPostRenameCleanup(t *testing.T) {
	const fixture = "11111111111111111111111111111111"
	if base := os.Getenv("LAYERCACHE_IDENTITY_COMMIT_CRASH"); base != "" {
		file, err := os.CreateTemp(base, ".seed-")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(fixture)); err != nil {
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := commitIdentitySeed(fd, filepath.Base(file.Name())); err != nil {
			t.Fatal(err)
		}
		// Kill only this fixture process at the real commit seam, before caller
		// defers can run. The next client must accept the committed seed.
		_ = unix.Kill(os.Getpid(), unix.SIGKILL)
		os.Exit(99)
	}
	directory := t.TempDir()
	base := filepath.Join(directory, "layercache", "client-identity")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestCommittedIdentitySurvivesInterruptedPostRenameCleanup$")
	child.Env = append(os.Environ(), "LAYERCACHE_IDENTITY_COMMIT_CRASH="+base)
	if err := child.Run(); err == nil {
		t.Fatal("commit fixture did not terminate at the crash point")
	}
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var state unix.Stat_t
	if err := unix.Fstatat(fd, "seed", &state, unix.AT_SYMLINK_NOFOLLOW); err != nil || state.Nlink != 1 {
		t.Fatal("committed seed retained a staging hardlink", err)
	}
	var seed [32]byte
	copy(seed[:], fixture)
	expected := Identity{seed: seed, origin: "https://cache.example"}
	second, err := loadIdentityAt(context.Background(), directory, "https://cache.example")
	if err != nil || expected.digest("installation", "") != second.InstallationID() {
		t.Fatal("post-commit restart lost the stable installation", err)
	}
}
