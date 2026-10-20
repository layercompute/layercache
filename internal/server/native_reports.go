package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/compatibility"
	"github.com/layercache/layercache/internal/execution"
	"github.com/layercache/layercache/internal/measurement"
)

func (server *Server) nativeReportCapability() any {
	if server.nativeReports == nil || server.config.Role == "public" {
		return nil
	}
	return map[string]any{"version": measurement.NativeVersion, "path": measurement.NativePath}
}

func (server *Server) nativeReportAuthority(claims access.Claims) (measurement.NativeAuthority, bool) {
	authority := measurement.NativeAuthority{Project: claims.Project, RunID: claims.RunID, WorkspaceID: claims.WorkspaceID, Integration: measurement.Integration(claims.Integration), CompatibilityID: claims.Compatibility, Subject: claims.Subject, Repository: claims.Repository, SourceCommit: claims.SourceCommit, Execution: claims.Execution}
	if server.config.Role == "public" || claims.SnapshotScope != nil || claims.Project != server.config.ProjectID || (claims.Integration != "gradle" && claims.Integration != "xcode") || compatibility.Validate(claims.Compatibility) != nil || claims.SourceCommit != "" && !immutableCommit(claims.SourceCommit) {
		return authority, false
	}
	if server.config.Role == "local" && claims.Subject == server.config.InstallationID && claims.Subject != "" && validLocalRunNonce(claims.RunID) && validLocalWorkspace(claims.WorkspaceID) {
		authority.Kind = "local"
		return authority, true
	}
	memberPrefix := "github-local:" + claims.Subject + ":"
	if (strings.HasPrefix(claims.Subject, "github:") || strings.HasPrefix(claims.Subject, "github-id:")) && strings.HasPrefix(claims.RunID, memberPrefix) && validLocalRunNonce(strings.TrimPrefix(claims.RunID, memberPrefix)) && strings.HasPrefix(claims.WorkspaceID, memberPrefix) && validLocalWorkspace(strings.TrimPrefix(claims.WorkspaceID, memberPrefix)) {
		if server.config.ActionsRepository != "" && !strings.EqualFold(server.config.ActionsRepository, claims.Repository) {
			return authority, false
		}
		authority.Kind = "memberLocal"
		return authority, true
	}
	if claims.Integration != "gradle" || !strings.HasPrefix(claims.Subject, "github-actions:") || claims.Execution == nil || claims.Execution.Kind != "githubActions" || claims.Execution.Validate() != nil || claims.Repository == "" || !immutableCommit(claims.SourceCommit) {
		return authority, false
	}
	parts := strings.Split(claims.Execution.ExecutionID, ":")
	if len(parts) != 5 || claims.RunID != "github-actions:"+claims.Repository+":"+parts[2]+":"+parts[3] || claims.WorkspaceID != "github-actions-check:"+claims.Repository+":"+parts[4] || server.config.ActionsRepository != "" && !strings.EqualFold(server.config.ActionsRepository, claims.Repository) {
		return authority, false
	}
	authority.Kind = "githubActions"
	return authority, true
}

// A native report can describe only its own signed execution. Reporting read
// authority grants neither cache writes nor maintenance, including on PRs.
func (server *Server) nativeReportAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := server.authenticateRequest(r, "")
		_, scoped := server.nativeReportAuthority(claims)
		if !ok || !scoped || !claims.Allows(access.CapabilityRead) || !server.authorizesSelectors(r, claims) || !server.currentMembershipAllows(r.Context(), claims, access.CapabilityRead) {
			server.writeAuthorizationFailure(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(access.WithClaims(r.Context(), claims)))
	})
}

func (server *Server) recordNativeReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if server.nativeReports == nil {
		writeJSON(w, 503, map[string]string{"error": "native reports unavailable"})
		return
	}
	defer r.Body.Close()
	report, err := measurement.DecodeNativeReport(r.Body)
	if err != nil || measurement.ValidateNativeReport(report, time.Now().UTC()) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid bounded native report"})
		return
	}
	claims, _ := access.ClaimsFromContext(r.Context())
	authority, ok := server.nativeReportAuthority(claims)
	if !ok {
		server.writeAuthorizationFailure(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	receipt, err := server.nativeReports.RecordReport(ctx, authority, report, time.Now().UTC())
	switch {
	case errors.Is(err, measurement.ErrNativeInvalid):
		writeJSON(w, 400, map[string]string{"error": "invalid bounded native report"})
	case errors.Is(err, measurement.ErrNativeConflict):
		writeJSON(w, 409, map[string]string{"error": "native invocation already has a different final report"})
	case errors.Is(err, measurement.ErrNativeQuota):
		writeJSON(w, 429, map[string]string{"error": "native reporting capacity reached"})
	case err != nil:
		writeJSON(w, 503, map[string]string{"error": "native report could not be confirmed; retry the exact invocation"})
	default:
		writeJSON(w, 200, receipt)
	}
}

func (server *Server) observeNative(request *http.Request, kind string, keyProject, keyProfile, keyNative, digest string, startedAt time.Time, size int64) {
	if server.nativeReports == nil {
		return
	}
	claims, ok := access.ClaimsFromContext(request.Context())
	if !ok {
		return
	}
	authority, valid := server.nativeReportAuthority(claims)
	if !valid || authority.Integration != measurement.IntegrationGradle || keyProject != authority.Project || keyProfile != authority.CompatibilityID {
		return
	}
	source := measurement.SourceTeamCache
	if server.config.Role == "local" {
		source = measurement.SourceLocalCache
	}
	if kind == "missed" || kind == "published" {
		source = measurement.SourceNone
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 100*time.Millisecond)
	defer cancel()
	finishedAt := time.Now().UTC()
	evidence := measurement.NativeTransport{Authority: authority, Kind: kind, ArtifactID: measurement.NativeArtifactIdentity(keyProject, keyProfile, measurement.IntegrationGradle, keyNative), Digest: digest, StartedAt: startedAt, FinishedAt: finishedAt, Source: source, Bytes: size}
	if kind == "published" && authority.Execution != nil {
		if provider, ok := server.executions.(interface {
			ResourceSnapshot(context.Context, execution.Authority, time.Time) (*execution.ResourceSnapshot, error)
		}); ok {
			evidence.Resource, _ = provider.ResourceSnapshot(ctx, execution.Authority{Context: *authority.Execution, Project: authority.Project, RunID: authority.RunID, WorkspaceID: authority.WorkspaceID}, finishedAt)
		}
	}
	_ = server.nativeReports.RecordTransport(ctx, evidence)
}
