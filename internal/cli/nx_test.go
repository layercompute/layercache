package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
)

func TestRunExportsScopedNativeNxConnectionAndHonorsBypass(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "clone")
	initCLIRepository(t, clone, "main", "nx")
	t.Chdir(clone)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, `{}`) }))
	t.Cleanup(daemon.Close)
	configPath, _ := setupConfigWithAddress(t, strings.TrimPrefix(daemon.URL, "http://"), "local-owner")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAYERCACHE_NX_CHILD", "1")
	for _, bypass := range []bool{false, true} {
		args := []string{"run", "--config", configPath}
		if bypass {
			args = append(args, "--bypass", "nx")
		}
		args = append(args, "--", os.Args[0], "-test.run=^TestNxConnectionChild$")
		stdout, stderr, err := callCLI(args...)
		if err != nil {
			t.Fatalf("Nx child failed: %v\n%s", err, stderr)
		}
		var observed map[string]string
		if err := json.Unmarshal([]byte(stdout), &observed); err != nil {
			t.Fatal(err)
		}
		if bypass {
			if observed["server"] != "" || observed["token"] != "" {
				t.Fatalf("bypassed Nx retained Layer Cache credentials: %+v", observed)
			}
			continue
		}
		claims, err := access.ParseCapabilityToken(cfg.LocalToken, observed["token"], time.Now().UTC())
		if err != nil || observed["server"] != daemon.URL || claims.Integration != "nx" || claims.Project != cfg.ProjectID || claims.Compatibility != cfg.CompatibilityID || !claims.Allows(access.CapabilityWrite) {
			t.Fatalf("native Nx connection server=%q claims=%+v error=%v", observed["server"], claims, err)
		}
	}
}

func TestNxConnectionChild(t *testing.T) {
	if os.Getenv("LAYERCACHE_NX_CHILD") != "1" {
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
		"server": os.Getenv("NX_SELF_HOSTED_REMOTE_CACHE_SERVER"),
		"token":  os.Getenv("NX_SELF_HOSTED_REMOTE_CACHE_ACCESS_TOKEN"),
	})
	os.Exit(0)
}
