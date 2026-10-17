package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/githubauth"
	"github.com/layercache/layercache/internal/retained"
)

func (server *Server) githubRetainedCapabilityExchange(writer http.ResponseWriter, request *http.Request, input githubOIDCExchangeInput, identity githubauth.ActionsIdentity) {
	if !server.config.RetainedArtifactsEnabled {
		http.NotFound(writer, request)
		return
	}
	if server.retainedStore == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "retained persistence unavailable"})
		return
	}
	if (input.Mode != "read" && input.Mode != "publish") || identity.ActorID == "" || identity.Actor == "" || identity.RepositoryID == "" ||
		len(identity.WorkflowRef) > 512 || strings.ContainsAny(identity.WorkflowRef, "\x00\r\n") ||
		!strings.HasPrefix(strings.ToLower(identity.WorkflowRef), strings.ToLower(identity.Repository)+"/.github/workflows/") || !strings.Contains(identity.WorkflowRef, "@") {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	switch identity.EventName {
	case "push", "workflow_dispatch":
		if !strings.HasPrefix(identity.Ref, "refs/heads/") {
			server.writeCapabilityExchangeFailure(writer, request)
			return
		}
	case "pull_request":
		if githubauth.VerifySameRepositoryRun(request.Context(), server.config.GitHubAPIURL, input.GitHubToken, identity) != nil {
			server.writeCapabilityExchangeFailure(writer, request)
			return
		}
	default:
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	if server.projectAuthority != nil {
		storedID, err := server.projectAuthority.RepositoryID(request.Context(), server.config.ProjectID)
		if err != nil || storedID != identity.RepositoryID {
			server.writeCapabilityExchangeFailure(writer, request)
			return
		}
	}
	now := time.Now().UTC()
	lifetime := 15 * time.Minute
	if input.TTLSeconds > 0 {
		lifetime = time.Duration(input.TTLSeconds) * time.Second
	}
	claims := access.Claims{
		Subject: "github-actions:" + identity.Subject, Project: server.config.ProjectID, Integration: "retained",
		Compatibility: input.Compatibility, Repository: strings.ToLower(identity.Repository), RepositoryID: identity.RepositoryID,
		SourceCommit: strings.ToLower(identity.Commit), WorkflowRef: identity.WorkflowRef,
		ActionsRunID: identity.RunID, RunAttempt: identity.RunAttempt, CheckRunID: identity.CheckRunID,
		ActionsActorID: identity.ActorID, ActionsActor: strings.ToLower(identity.Actor),
		Capabilities: []access.Capability{access.CapabilityRead}, ExpiresAt: now.Add(lifetime),
	}
	if input.Mode == "publish" {
		claims.Capabilities = []access.Capability{access.CapabilityWrite}
	}
	required := access.CapabilityRead
	if input.Mode == "publish" {
		required = access.CapabilityWrite
	}
	if !server.retainedMembershipAllows(request.Context(), claims, required) {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	token, err := access.MintCapabilityToken(server.config.LocalToken, claims, now)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "issue retained capability"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"token": token, "teamToken": token, "expiresAt": claims.ExpiresAt, "producer": retainedProducer(claims), "mode": input.Mode})
}

func retainedProducer(claims access.Claims) retained.Producer {
	return retained.Producer{Project: claims.Project, Repository: claims.Repository, RepositoryID: claims.RepositoryID, SourceCommit: claims.SourceCommit,
		WorkflowRef: claims.WorkflowRef, RunID: claims.ActionsRunID, RunAttempt: claims.RunAttempt, JobID: claims.CheckRunID, Profile: claims.Compatibility}
}

func (server *Server) retainedMembershipAllows(ctx context.Context, claims access.Claims, required access.Capability) bool {
	var role string
	var err error
	if server.projectAuthority != nil {
		role, err = server.projectAuthority.GitHubActionsRole(ctx, server.config.ProjectID, claims.ActionsActorID)
	} else if server.cloudStore != nil {
		role, err = server.cloudStore.MemberRole(ctx, server.config.ProjectID, "github:"+claims.ActionsActor)
	} else {
		role = server.config.TeamMembers[claims.ActionsActor]
	}
	capabilities, ok := capabilitiesForRole(role)
	return err == nil && ok && (access.Claims{Capabilities: capabilities}).Allows(required)
}

func (server *Server) requireRetainedToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "private, no-store")
		if !server.config.RetainedArtifactsEnabled || server.config.Role != "team" || server.retainedStore == nil {
			http.NotFound(writer, request)
			return
		}
		claims, ok := server.authenticateRequest(request, "retained")
		if !ok || claims.Integration != "retained" || !claims.Allows(access.CapabilityRead) || !server.retainedMembershipAllows(request.Context(), claims, access.CapabilityRead) {
			server.writeAuthorizationFailure(writer, request)
			return
		}
		required := access.CapabilityRead
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			required = access.CapabilityWrite
		}
		if !claims.Allows(required) || !server.retainedMembershipAllows(request.Context(), claims, required) {
			writeJSON(writer, http.StatusForbidden, map[string]string{"error": "retained publication is read-only"})
			return
		}
		next.ServeHTTP(writer, request.WithContext(access.WithClaims(request.Context(), claims)))
	})
}
