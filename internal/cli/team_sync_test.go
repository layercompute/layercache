package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/config"
)

func TestSyncTeamRejectsMissingInconsistentOrOversizeConfirmation(t *testing.T) {
	for _, test := range []struct {
		name, body string
		wantError  bool
	}{
		{"confirmed", `{"requested":2,"ready":2,"pending":0}`, false},
		{"pending", `{"requested":2,"ready":1,"pending":1}`, true},
		{"wrong-selection", `{"requested":1,"ready":1,"pending":0}`, true},
		{"negative", `{"requested":2,"ready":3,"pending":-1}`, true},
		{"missing", `{}`, true},
		{"trailing", `{"requested":2,"ready":2,"pending":0} {}`, true},
		{"oversize", `{"requested":2,"ready":2,"pending":0}` + strings.Repeat(" ", 64<<10), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/sync/turbo" || r.Header.Get("Authorization") != "Bearer local-owner" || r.Header.Get("X-LayerCache-Compatibility") != "linux-node24.21.0-tests-v2" {
					t.Errorf("unexpected synchronization request")
				}
				_, _ = io.WriteString(w, test.body)
			}))
			defer daemon.Close()
			cfg := config.Config{Listen: strings.TrimPrefix(daemon.URL, "http://"), LocalToken: "local-owner", TeamURL: "https://team.example", TeamToken: "team-writer", CompatibilityID: "linux-node24.21.0-tests-v2"}
			err := syncTurboTeamResults(context.Background(), cfg, []string{"first", "second"}, time.Second)
			if (err != nil) != test.wantError {
				t.Fatalf("confirmation error = %v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestRunSyncTeamRejectsInvalidPolicyBeforeExecutingChild(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--sync-timeout", "0s", "--", "must-not-run"},
		{"run", "--sync-timeout", "61s", "--", "must-not-run"},
	} {
		_, _, err := callCLI(args...)
		if err == nil || !strings.Contains(err.Error(), "--sync-timeout") {
			t.Fatalf("invalid timeout accepted: %v", err)
		}
	}
	path, _ := setupLocalConfig(t)
	_, _, err := callCLI("run", "--config", path, "--sync-team", "--bypass", "turbo", "--", "must-not-run")
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("sync with Turbo bypass accepted: %v", err)
	}
}
