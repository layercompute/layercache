package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/execution"
)

func (server *Server) executionRoutes() {
	server.mux.Handle("POST "+execution.Path, server.executionAccess(http.HandlerFunc(server.recordExecution)))
	server.mux.Handle("GET "+execution.Path, server.executionAccess(http.HandlerFunc(server.executionInventory)))
}

// Inventory reporting accepts a read-only native credential's own signed
// execution. It does not turn that credential into artifact write authority.
func (server *Server) executionAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := server.authenticateRequest(r, "")
		if !ok || !claims.Allows(access.CapabilityRead) || !server.authorizesSelectors(r, claims) || !server.currentMembershipAllows(r.Context(), claims, access.CapabilityRead) {
			server.writeAuthorizationFailure(w, r)
			return
		}
		if claims.Execution != nil && strings.HasPrefix(claims.Execution.OwnerID, "local:") && server.config.Role != "local" {
			server.writeAuthorizationFailure(w, r)
			return
		}
		if r.Method == http.MethodPost && (claims.Execution == nil || claims.Execution.Validate() != nil || claims.RunID == "" || claims.WorkspaceID == "") {
			server.writeAuthorizationFailure(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(access.WithClaims(r.Context(), claims)))
	})
}
func (server *Server) recordExecution(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if server.executions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "execution observations unavailable"})
		return
	}
	claims, _ := access.ClaimsFromContext(r.Context())
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, execution.MaximumRequestBytes))
	decoder.DisallowUnknownFields()
	defer r.Body.Close()
	var report execution.ObservationRequest
	if decoder.Decode(&report) != nil || rejectTrailingJSON(decoder) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid execution observation"})
		return
	}
	authority := execution.Authority{Context: *claims.Execution, Project: claims.Project, RunID: claims.RunID, WorkspaceID: claims.WorkspaceID}
	if report.Validate(authority) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid execution observation"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	err := server.executions.Record(ctx, authority, report, time.Now().UTC())
	if errors.Is(err, execution.ErrInvalid) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid execution observation"})
		return
	}
	if errors.Is(err, execution.ErrConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "execution observation authority conflict"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "execution observations unavailable"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": execution.Version, "recorded": true})
}
func (server *Server) executionInventory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if server.executions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "execution observations unavailable"})
		return
	}
	period := 24 * time.Hour
	switch r.URL.Query().Get("period") {
	case "", "24h":
	case "7d":
		period = 7 * 24 * time.Hour
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid inventory period"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	now := time.Now().UTC()
	result, err := server.executions.Inventory(ctx, server.config.ProjectID, now.Add(-period), now)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "execution inventory unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
