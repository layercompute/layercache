package dashboard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardMaintenanceRequiresExplicitPolicyAndConfirmedSameOriginRequest(t *testing.T) {
	const authority = "127.0.0.1:7438"
	const valid = `{"projectId":"project","cache":"local","targetBytes":0,"confirm":true}`
	calls := 0
	handler := NewWithOptions(authority, "session", Options{Prune: func(_ context.Context, request PruneRequest) (PruneResult, error) {
		calls++
		if request.ProjectID != "project" || request.Cache != "local" || request.TargetBytes != 0 || !request.Confirm {
			t.Fatalf("request: %#v", request)
		}
		return PruneResult{BeforeBytes: 5, FreedBytes: 5}, nil
	}})
	for _, test := range []struct {
		name, body, origin, contentType, token, host, site string
		code                                               int
	}{
		{"accepted", valid, "http://" + authority, "application/json", "session", authority, "same-origin", 200},
		{"no authentication", valid, "http://" + authority, "application/json", "", authority, "", 401},
		{"foreign host", valid, "http://" + authority, "application/json", "session", "attacker.test", "", 403},
		{"foreign origin", valid, "https://attacker.test", "application/json", "session", authority, "", 403},
		{"missing origin", valid, "", "application/json", "session", authority, "", 403},
		{"cross site", valid, "http://" + authority, "application/json", "session", authority, "cross-site", 403},
		{"form body", valid, "http://" + authority, "text/plain", "session", authority, "", 415},
		{"unknown field", strings.TrimSuffix(valid, "}") + `,"url":"https://attacker.test"}`, "http://" + authority, "application/json", "session", authority, "", 400},
		{"trailing JSON", valid + `{}`, "http://" + authority, "application/json", "session", authority, "", 400},
		{"unconfirmed", strings.ReplaceAll(valid, `true`, `false`), "http://" + authority, "application/json", "session", authority, "", 400},
		{"missing target", `{"projectId":"project","cache":"local","confirm":true}`, "http://" + authority, "application/json", "session", authority, "", 400},
		{"null target", strings.ReplaceAll(valid, `:0`, `:null`), "http://" + authority, "application/json", "session", authority, "", 400},
		{"negative target", strings.ReplaceAll(valid, `:0`, `:-1`), "http://" + authority, "application/json", "session", authority, "", 400},
		{"public selector", strings.ReplaceAll(valid, `"local"`, `"public"`), "http://" + authority, "application/json", "session", authority, "", 400},
		{"oversized", valid + strings.Repeat(" ", 4096), "http://" + authority, "application/json", "session", authority, "", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/prune", strings.NewReader(test.body))
			r.Host = test.host
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Content-Type", test.contentType)
			r.Header.Set("Sec-Fetch-Site", test.site)
			if test.token != "" {
				r.Header.Set("Authorization", "Bearer "+test.token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.code {
				t.Fatalf("status %d, body %s", w.Code, w.Body.String())
			}
		})
	}
	if calls != 1 {
		t.Fatalf("mutations = %d", calls)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/prune", strings.NewReader(valid))
	r.Host = authority
	r.Header.Set("Authorization", "Bearer session")
	r.Header.Set("Origin", "http://"+authority)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	New(authority, "session", nil).ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("read-only default allowed mutation: %d", w.Code)
	}
}

func TestDashboardMaintenanceNeverProjectsCallbackErrors(t *testing.T) {
	for _, test := range []struct {
		err  error
		code int
	}{{ErrPruneForbidden, 403}, {ErrPruneInvalid, 400}, {errors.New("protected-token internal/path"), 502}} {
		handler := NewWithOptions("127.0.0.1:7438", "session", Options{Prune: func(context.Context, PruneRequest) (PruneResult, error) { return PruneResult{}, test.err }})
		r := httptest.NewRequest(http.MethodPost, "/api/prune", strings.NewReader(`{"projectId":"foreign","cache":"team","targetBytes":0,"confirm":true}`))
		r.Host = "127.0.0.1:7438"
		r.Header.Set("Origin", "http://"+r.Host)
		r.Header.Set("Authorization", "Bearer session")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.code || strings.Contains(w.Body.String(), "protected-token") || strings.Contains(w.Body.String(), "internal/path") {
			t.Fatalf("unsafe mutation result: %d %s", w.Code, w.Body.String())
		}
	}
}
