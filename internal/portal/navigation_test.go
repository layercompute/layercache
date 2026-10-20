package portal

import (
	"net/http"
	"strings"
	"testing"
)

func TestDashboardDeepLinksServeOnlyThePublicShell(t *testing.T) {
	q := newPortalQA(t)
	for _, path := range []string{
		"/projects/github.com%2Fexample%2Fmobile/overview?period=7d",
		"/projects/project-id/activity", "/projects/project-id/infrastructure",
		"/projects/project-id/storage", "/projects/project-id/settings",
		"/teams/team-id/settings",
	} {
		response := q.request("GET", path, "", nil, nil)
		requireStatus(t, response, http.StatusOK)
		if !strings.Contains(response.Body.String(), `id="app"`) || !strings.Contains(response.Body.String(), `src="/app.js"`) {
			t.Fatalf("deep link %s did not serve the public shell", path)
		}
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("deep links lost browser isolation headers")
		}
	}
	for _, path := range []string{"/projects/project-id/secrets", "/teams/team-id/members", "/unknown"} {
		requireStatus(t, q.request("GET", path, "", nil, nil), http.StatusNotFound)
	}
	// An unauthenticated deep link receives no project data or authority.
	requireStatus(t, q.request("GET", "/api/projects/project-id/snapshot", "", nil, nil), http.StatusUnauthorized)
}
