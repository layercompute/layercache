//go:build linux || darwin

package cli

import (
	"bytes"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/server"
)

func TestCloudPasswordConfigurationDoesNotRequireOAuthSecrets(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(root, "template.json")
	if err = config.Save(template, cfg); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "session-key")
	if err = os.WriteFile(key, bytes.Repeat([]byte{42}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	accounts := filepath.Join(root, "password-accounts.json")
	if err = os.WriteFile(accounts, []byte(`{"accounts":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	file := cloudFile{Listen: "127.0.0.1:8080", Origin: "https://cloud.example", DataDir: filepath.Join(root, "data"), TeamCreatorIDs: []string{"123"}, PasswordAccountsFile: accounts, SessionKeyFile: key, ProjectTemplateFile: template, StoragePool: server.StoragePoolConfig{Path: root, MaxBytes: 1 << 30}}
	filename := filepath.Join(root, "cloud.json")
	save := func() {
		t.Helper()
		data, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filename, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	_, loaded, err := loadCloud(filename)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PasswordAccountsFile != accounts || loaded.GitHubClientID != "" || loaded.GitHubClientSecret != "" {
		t.Fatal("password-only mode invented OAuth credentials")
	}
	file.GitHubClientID = "missing-secret"
	save()
	if _, _, err = loadCloud(filename); err == nil {
		t.Fatal("partial OAuth config accepted")
	}
	file.GitHubClientID = ""
	file.PasswordAccountsFile = "relative.json"
	save()
	if _, _, err = loadCloud(filename); err == nil {
		t.Fatal("relative password file accepted")
	}
	file.PasswordAccountsFile = accounts
	os.Chmod(accounts, 0644)
	save()
	if _, _, err = loadCloud(filename); err == nil {
		t.Fatal("unprotected password file accepted")
	}
	os.Chmod(accounts, 0600)
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	file.PasswordAccountsFile = fifo
	save()
	if _, _, err = loadCloud(filename); err == nil {
		t.Fatal("FIFO password file accepted")
	}
	file.PasswordAccountsFile = ""
	save()
	if _, _, err = loadCloud(filename); err == nil {
		t.Fatal("cloud accepted without any sign-in method")
	}
}
