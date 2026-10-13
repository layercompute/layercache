package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestTurboSyncPolicyPreservesCallerPoliciesAcrossNestedRuns(t *testing.T) {
	for _, policy := range []string{"", "local:rw,remote:r", "local:,remote:", turboSyncPolicy} {
		environment := replaceEnvironment([]string{"UNRELATED=keep"}, "TURBO_CACHE", policy)
		want := append([]string(nil), environment...)
		for depth := 0; depth < 4; depth++ {
			environment = withTurboSyncPolicy(environment)
			if got := environmentValue(environment, "TURBO_CACHE"); got != turboSyncPolicy {
				t.Fatalf("nested policy = %q", got)
			}
			if original, owned := originalTurboPolicy(environment); !owned || original != policy {
				t.Fatalf("depth %d lost original policy %q: %q, owned=%v", depth, policy, original, owned)
			}
		}
		if got := withoutTurboSyncPolicy(environment); !reflect.DeepEqual(got, want) {
			t.Fatalf("restored environment = %q, want %q", got, want)
		}
		changed := replaceEnvironment(withTurboSyncPolicy(want), "TURBO_CACHE", "local:rw,remote:w")
		if got := environmentValue(withoutTurboSyncPolicy(changed), "TURBO_CACHE"); got != "local:rw,remote:w" {
			t.Fatalf("bypass replaced caller's changed policy: %q", got)
		}
		changed = withTurboSyncPolicy(changed)
		if got := environmentValue(withoutTurboSyncPolicy(changed), "TURBO_CACHE"); got != "local:rw,remote:w" {
			t.Fatalf("nested sync lost caller's changed policy: %q", got)
		}
	}
}

func TestTurboSyncPolicyLeavesUnownedEnvironmentUntouched(t *testing.T) {
	for _, marker := range []string{"", "null", "{}", "invalid"} {
		environment := []string{"TURBO_CACHE=" + turboSyncPolicy, "TURBO_API=https://caller.example", "TURBO_TOKEN=caller-token"}
		environment = replaceEnvironment(environment, turboOriginalPolicyEnvironment, marker)
		if got := withoutTurboSyncPolicy(environment); !reflect.DeepEqual(got, environment) {
			t.Fatalf("unowned environment changed: %q", got)
		}
	}
}

func TestNestedRunRestoresTurboPolicyWhenCacheIsBypassed(t *testing.T) {
	root := canonicalTestTempDir(t)
	clone := filepath.Join(root, "clone")
	outside := filepath.Join(root, "different-project")
	initCLIRepository(t, clone, "main", "setup")
	initCLIRepository(t, outside, "main", "different")
	runCLIGit(t, outside, "remote", "set-url", "origin", "https://github.com/other/project.git")
	t.Chdir(clone)
	daemon := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprint(writer, `{}`)
	}))
	t.Cleanup(daemon.Close)
	configPath, _ := setupConfigWithAddress(t, strings.TrimPrefix(daemon.URL, "http://"), "local-owner")
	unavailablePath, _ := setupLocalConfig(t)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("LAYERCACHE_POLICY_CONFIG", configPath)
	t.Setenv("LAYERCACHE_POLICY_UNAVAILABLE_CONFIG", unavailablePath)
	t.Setenv("LAYERCACHE_POLICY_OUTSIDE", outside)

	for _, path := range []string{"scope", "adapter", "unavailable"} {
		for _, policy := range []string{"", "local:rw,remote:r", "local:,remote:", "changed"} {
			t.Run(path+"/"+policy, func(t *testing.T) {
				t.Setenv(turboOriginalPolicyEnvironment, "")
				t.Setenv("TURBO_CACHE", policy)
				t.Setenv("LAYERCACHE_POLICY_HELPER", "nested")
				t.Setenv("LAYERCACHE_POLICY_DEPTH", "2")
				t.Setenv("LAYERCACHE_POLICY_PATH", path)
				t.Setenv("LAYERCACHE_POLICY_CHANGE", "")
				want := policy
				if policy == "changed" {
					t.Setenv("TURBO_CACHE", "local:rw,remote:r")
					t.Setenv("LAYERCACHE_POLICY_CHANGE", "local:rw,remote:w")
					want = "local:rw,remote:w"
				}
				stdout, stderr, err := callCLI("run", "--config", configPath, "--sync-team", "--", os.Args[0], "-test.run=^TestTurboPolicyChildProcess$")
				if err != nil {
					t.Fatalf("nested run failed: %v\n%s", err, stderr)
				}
				var observed map[string]string
				if err := json.Unmarshal([]byte(stdout), &observed); err != nil {
					t.Fatalf("child output = %q: %v", stdout, err)
				}
				if observed["policy"] != want || observed["marker"] != "" || observed["api"] != "" || observed["token"] != "" {
					t.Fatalf("child observed environment = %+v, want restored policy %q and no owned credentials", observed, want)
				}
			})
		}
	}
}

func TestTurboPolicyChildProcess(t *testing.T) {
	switch os.Getenv("LAYERCACHE_POLICY_HELPER") {
	case "observe":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
			"policy": os.Getenv("TURBO_CACHE"), "marker": os.Getenv(turboOriginalPolicyEnvironment),
			"api": os.Getenv("TURBO_API"), "token": os.Getenv("TURBO_TOKEN"),
		})
		os.Exit(0)
	case "nested":
		depth, _ := strconv.Atoi(os.Getenv("LAYERCACHE_POLICY_DEPTH"))
		configPath := os.Getenv("LAYERCACHE_POLICY_CONFIG")
		args := []string{"run", "--config", configPath}
		if depth > 0 {
			_ = os.Setenv("LAYERCACHE_POLICY_DEPTH", strconv.Itoa(depth-1))
			args = append(args, "--sync-team")
		} else {
			_ = os.Setenv("LAYERCACHE_POLICY_HELPER", "observe")
			if changed := os.Getenv("LAYERCACHE_POLICY_CHANGE"); changed != "" {
				_ = os.Setenv("TURBO_CACHE", changed)
			}
			switch os.Getenv("LAYERCACHE_POLICY_PATH") {
			case "scope":
				if err := os.Chdir(os.Getenv("LAYERCACHE_POLICY_OUTSIDE")); err != nil {
					t.Fatal(err)
				}
			case "adapter":
				args = append(args, "--bypass", "turbo")
			case "unavailable":
				args[2] = os.Getenv("LAYERCACHE_POLICY_UNAVAILABLE_CONFIG")
			}
		}
		args = append(args, "--", os.Args[0], "-test.run=^TestTurboPolicyChildProcess$")
		err := Run(context.Background(), args, os.Stdout, os.Stderr)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}
