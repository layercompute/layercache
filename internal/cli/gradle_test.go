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

func TestRunScopesGradleConnectionAndClearsInheritedCredentialsOnFallback(t *testing.T) {
	for _, test := range []struct {
		name        string
		bypass      string
		persisted   bool
		unavailable bool
		wrongScope  bool
		policies    bool
	}{
		{name: "scoped local connection with default cache policies"},
		{name: "scoped local connection with explicit cache policies", policies: true},
		{name: "Gradle bypass", bypass: "gradle", policies: true},
		{name: "all bypass", bypass: "all", policies: true},
		{name: "persisted Gradle bypass", bypass: "gradle", persisted: true, policies: true},
		{name: "unavailable daemon", unavailable: true, policies: true},
		{name: "wrong repository", wrongScope: true, policies: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clone := filepath.Join(t.TempDir(), "clone")
			commit := initCLIRepository(t, clone, "main", "gradle")
			t.Chdir(clone)
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, `{}`) }))
			t.Cleanup(daemon.Close)
			configPath, _ := setupConfigWithAddress(t, strings.TrimPrefix(daemon.URL, "http://"), "local-owner")
			cfg, err := config.Load(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if test.persisted {
				runCLI(t, "bypass", "--config", configPath, "--adapter", test.bypass, "--json")
			}
			if test.unavailable {
				daemon.Close()
			}
			if test.wrongScope {
				runCLIGit(t, clone, "remote", "set-url", "origin", "https://github.com/other/project.git")
			}
			t.Setenv("LAYERCACHE_GRADLE_CHILD", "1")
			t.Setenv("LAYER_CACHE_GRADLE_URL", "https://unrelated.example/v1/gradle-cache/")
			t.Setenv("LAYER_CACHE_GRADLE_TOKEN", "unrelated-credential")
			t.Setenv("LAYER_CACHE_GRADLE_PUSH", "")
			t.Setenv("LAYER_CACHE_GRADLE_LOCAL_ENABLED", "")
			if test.policies {
				t.Setenv("LAYER_CACHE_GRADLE_PUSH", "1")
				t.Setenv("LAYER_CACHE_GRADLE_LOCAL_ENABLED", "0")
			}
			t.Setenv("LAYER_CACHE_GRADLE_TEST_UNRELATED", "preserved")
			args := []string{"run", "--config", configPath}
			if test.bypass != "" && !test.persisted {
				args = append(args, "--bypass", test.bypass)
			}
			args = append(args, "--", os.Args[0], "-test.run=^TestGradleConnectionChild$")
			stdout, stderr, err := callCLI(args...)
			if err != nil {
				t.Fatalf("Gradle child failed: %v\n%s", err, stderr)
			}
			var observed map[string]string
			if err := json.Unmarshal([]byte(stdout), &observed); err != nil {
				t.Fatal(err)
			}
			if observed["unrelated"] != "preserved" {
				t.Fatal("Gradle scope handling changed an unrelated variable")
			}
			if test.bypass != "" || test.unavailable || test.wrongScope {
				for _, field := range []string{"url", "token", "push", "local"} {
					if observed[field] != "" {
						t.Fatalf("fallback retained Gradle field %q", field)
					}
				}
				return
			}
			claims, err := access.ParseCapabilityToken(cfg.LocalToken, observed["token"], time.Now().UTC())
			if err != nil {
				t.Fatalf("Gradle capability is invalid: %v", err)
			}
			if observed["url"] != daemon.URL+"/v1/gradle-cache/" || claims.Integration != "gradle" ||
				claims.Project != cfg.ProjectID || claims.Compatibility != cfg.CompatibilityID ||
				claims.SourceCommit != commit || !claims.Allows(access.CapabilityRead) || !claims.Allows(access.CapabilityWrite) ||
				claims.ExpiresAt.After(time.Now().Add(12*time.Hour)) {
				t.Fatalf("Gradle connection is not scoped to this invocation: url=%q claims=%+v", observed["url"], claims)
			}
			if test.policies && (observed["push"] != "1" || observed["local"] != "0") {
				t.Fatal("scoped connection did not preserve explicit Gradle cache policies")
			}
			if !test.policies && (observed["push"] != "" || observed["local"] != "") {
				t.Fatal("scoped connection changed default Gradle cache policies")
			}
		})
	}
}

func TestGradleConnectionChild(t *testing.T) {
	if os.Getenv("LAYERCACHE_GRADLE_CHILD") != "1" {
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
		"url":       os.Getenv("LAYER_CACHE_GRADLE_URL"),
		"token":     os.Getenv("LAYER_CACHE_GRADLE_TOKEN"),
		"push":      os.Getenv("LAYER_CACHE_GRADLE_PUSH"),
		"local":     os.Getenv("LAYER_CACHE_GRADLE_LOCAL_ENABLED"),
		"unrelated": os.Getenv("LAYER_CACHE_GRADLE_TEST_UNRELATED"),
	})
	os.Exit(0)
}
