//go:build linux || darwin

package access

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func snapshotFileFixture(t *testing.T) (string, SnapshotCredential) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "snapshot-credentials.json")
	grant := SnapshotCredential{ID: "trusted-writer", SecretSHA256: SnapshotSecretDigest("lcs1.trusted-writer." + strings.Repeat("a", 64)), Project: "acme/widgets", Compatibility: "darwin-arm64-xcode26", KeyPrefix: "xcode-v1/", Role: "writer", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	writeSnapshotFileFixture(t, path, []SnapshotCredential{grant})
	return path, grant
}

func writeSnapshotFileFixture(t *testing.T, path string, grants []SnapshotCredential) {
	t.Helper()
	body, err := json.Marshal(snapshotCredentialFile{Version: 1, Credentials: grants})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotCredentialFileReloadAndRevocation(t *testing.T) {
	path, grant := snapshotFileFixture(t)
	original, err := ReadSnapshotCredentials(path)
	if err != nil || len(original) != 1 || original[0].Digest() != grant.Digest() {
		t.Fatal("protected grant unavailable", err)
	}
	for _, mutate := range []func(*SnapshotCredential){
		func(g *SnapshotCredential) { g.SecretSHA256 = strings.Repeat("b", 64) },
		func(g *SnapshotCredential) { g.Role = "reader" },
		func(g *SnapshotCredential) { g.Compatibility = "darwin-arm64-other" },
		func(g *SnapshotCredential) { g.KeyPrefix = "other/" },
		func(g *SnapshotCredential) { g.ExpiresAt = g.ExpiresAt.Add(-time.Minute) },
	} {
		changed := grant
		mutate(&changed)
		writeSnapshotFileFixture(t, path, []SnapshotCredential{changed})
		current, err := ReadSnapshotCredentials(path)
		if err != nil || len(current) != 1 || current[0].Digest() == original[0].Digest() {
			t.Fatal("grant edit failed to invalidate generation", err)
		}
	}
	writeSnapshotFileFixture(t, path, []SnapshotCredential{})
	current, err := ReadSnapshotCredentials(path)
	if err != nil || len(current) != 0 {
		t.Fatal("empty revocation list rejected", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshotCredentials(path); !errors.Is(err, ErrSnapshotCredentials) {
		t.Fatal("missing file retained previous grants", err)
	}
}

func TestSnapshotCredentialFileRejectsUnsafeMetadata(t *testing.T) {
	for _, name := range []string{"file-mode", "directory-mode", "file-symlink", "parent-symlink", "hardlink", "directory", "fifo", "oversize"} {
		t.Run(name, func(t *testing.T) {
			path, _ := snapshotFileFixture(t)
			switch name {
			case "file-mode":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "directory-mode":
				if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
			case "file-symlink":
				target := path + ".original"
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				link := filepath.Join(t.TempDir(), "linked-parent")
				if err := os.Symlink(filepath.Dir(path), link); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(link, filepath.Base(path))
			case "hardlink":
				if err := os.Link(path, path+".alias"); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(path, []byte(strings.Repeat(" ", maximumSnapshotFileBytes+1)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadSnapshotCredentials(path); !errors.Is(err, ErrSnapshotCredentials) {
				t.Fatal("unsafe protected file accepted", err)
			}
		})
	}
}

func TestSnapshotCredentialFileRejectsMalformedOrAmbiguousGrants(t *testing.T) {
	path, grant := snapshotFileFixture(t)
	for _, body := range []string{`{}`, `{"version":2,"credentials":[]}`, `{"version":1,"credentials":null}`, `{"version":1,"credentials":[],"secret":"unrecognized"}`, `{"version":1,"credentials":[]} {}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSnapshotCredentials(path); !errors.Is(err, ErrSnapshotCredentials) {
			t.Fatal("malformed protected file accepted", err)
		}
	}
	for name, mutate := range map[string]func(*SnapshotCredential){
		"id":      func(g *SnapshotCredential) { g.ID = "../other" },
		"secret":  func(g *SnapshotCredential) { g.SecretSHA256 = "plaintext-secret" },
		"project": func(g *SnapshotCredential) { g.Project = "invalid project" },
		"profile": func(g *SnapshotCredential) { g.Compatibility = "UNSCOPED" },
		"prefix":  func(g *SnapshotCredential) { g.KeyPrefix = "ambiguous" },
		"role":    func(g *SnapshotCredential) { g.Role = "admin" },
		"expiry":  func(g *SnapshotCredential) { g.ExpiresAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := grant
			mutate(&changed)
			writeSnapshotFileFixture(t, path, []SnapshotCredential{changed})
			if _, err := ReadSnapshotCredentials(path); !errors.Is(err, ErrSnapshotCredentials) {
				t.Fatal("unsafe grant accepted", err)
			}
		})
	}
	writeSnapshotFileFixture(t, path, []SnapshotCredential{grant, grant})
	if _, err := ReadSnapshotCredentials(path); !errors.Is(err, ErrSnapshotCredentials) {
		t.Fatal("duplicate grants accepted", err)
	}
	many := make([]SnapshotCredential, maximumSnapshotCredentials+1)
	writeSnapshotFileFixture(t, path, many)
	if _, err := ReadSnapshotCredentials(path); !errors.Is(err, ErrSnapshotCredentials) {
		t.Fatal("excess grant count accepted", err)
	}
}
