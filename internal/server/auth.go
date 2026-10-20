package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/compatibility"
	"github.com/layercache/layercache/internal/githubauth"
	"github.com/layercache/layercache/internal/publicbuild"
)

const projectCapabilityLifetime = time.Hour

const actionsPublicToolchain = "actions/cache@6.2.0"

var oidcVerifiers sync.Map

type githubOIDCExchangeInput struct {
	Project       string `json:"project"`
	Integration   string `json:"integration,omitempty"`
	TTLSeconds    int    `json:"ttlSeconds,omitempty"`
	Compatibility string `json:"compatibility"`
	IDToken       string `json:"idToken"`
	RecipeDigest  string `json:"recipeDigest,omitempty"`
	Target        string `json:"target,omitempty"`
	Platform      string `json:"platform,omitempty"`
	Toolchain     string `json:"toolchain,omitempty"`
	Builder       string `json:"builder,omitempty"`
	Mode          string `json:"mode,omitempty"`
	GitHubToken   string `json:"githubToken,omitempty"`
}

func (server *Server) githubCapabilityExchange(writer http.ResponseWriter, request *http.Request) {
	if server.config.Role != "team" && server.config.Role != "public" {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
	defer request.Body.Close()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Project       string `json:"project"`
		Compatibility string `json:"compatibility"`
		GitHubToken   string `json:"githubToken"`
		Repository    string `json:"repository"`
		Ref           string `json:"ref"`
		DefaultRef    string `json:"defaultRef"`
		RunID         string `json:"runId,omitempty"`
		WorkspaceID   string `json:"workspaceId,omitempty"`
		SourceCommit  string `json:"sourceCommit,omitempty"`
	}
	if err := decoder.Decode(&input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid capability exchange request"})
		return
	}
	if err := rejectTrailingJSON(decoder); err != nil || input.Project != server.config.ProjectID || compatibility.Validate(input.Compatibility) != nil ||
		!strings.EqualFold(input.Repository, server.config.ActionsRepository) || input.DefaultRef != server.config.ActionsDefaultRef || !validGitRef(input.Ref) {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	localRun := input.RunID != "" || input.WorkspaceID != "" || input.SourceCommit != ""
	if localRun && (!validLocalRunNonce(input.RunID) || !validLocalWorkspace(input.WorkspaceID) || input.SourceCommit != "" && !immutableCommit(input.SourceCommit)) {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	user, err := githubauth.VerifyUser(request.Context(), server.config.GitHubAPIURL, input.GitHubToken)
	if err != nil {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	var role string
	subject := "github:" + strings.ToLower(user.Login)
	if server.projectAuthority != nil {
		subject = "github-id:" + strconv.FormatInt(user.ID, 10)
		role, err = server.projectAuthority.MemberRole(request.Context(), server.config.ProjectID, subject)
	} else if server.cloudStore != nil {
		role, err = server.cloudStore.MemberRole(
			request.Context(), server.config.ProjectID, "github:"+strings.ToLower(user.Login),
		)
	} else {
		var allowed bool
		role, allowed = server.config.TeamMembers[strings.ToLower(user.Login)]
		if !allowed {
			err = errors.New("GitHub user is not a project member")
		}
	}
	if err != nil {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	capabilities, ok := capabilitiesForRole(role)
	if !ok {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	localRunID, workspaceID := "", ""
	if localRun {
		// The caller controls its local context, never another member's run ID.
		localRunID = "github-local:" + subject + ":" + input.RunID
		workspaceID = "github-local:" + subject + ":" + input.WorkspaceID
	}
	now := time.Now().UTC()
	claims := access.Claims{
		Subject: subject,
		RunID:   localRunID, WorkspaceID: workspaceID, SourceCommit: input.SourceCommit,
		Project:       server.config.ProjectID,
		Compatibility: input.Compatibility,
		Repository:    strings.ToLower(input.Repository), Ref: input.Ref, DefaultRef: input.DefaultRef,
		Capabilities: capabilities,
		ExpiresAt:    now.Add(projectCapabilityLifetime),
	}
	token, err := access.MintCapabilityToken(server.config.LocalToken, claims, now)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "issue project capability"})
		return
	}
	result := map[string]any{
		"token": token, "expiresAt": now.Add(projectCapabilityLifetime),
		"capabilities": capabilities,
		"runId":        localRunID, "workspaceId": workspaceID,
	}
	if localRun {
		nativeTokens := make(map[string]string)
		for _, integration := range []string{"turbo", "actions", "nx", "gradle", "buildkit", "bazel"} {
			claims.Integration = integration
			// Native run credentials authorize reads/writes, never maintenance,
			// even when the human login has project administrator membership.
			if claims.Allows(access.CapabilityAdmin) {
				claims.Capabilities = []access.Capability{access.CapabilityWrite}
			}
			nativeToken, mintErr := access.MintCapabilityToken(server.config.LocalToken, claims, now)
			if mintErr != nil {
				writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "issue native run capability"})
				return
			}
			nativeTokens[integration] = nativeToken
		}
		result["nativeTokens"] = nativeTokens
	}
	if server.config.Role == "team" {
		result["teamToken"] = token
	} else {
		result["publicAccessToken"] = token
	}
	writeJSON(writer, http.StatusOK, result)
}

func (server *Server) githubOIDCCapabilityExchange(writer http.ResponseWriter, request *http.Request) {
	if server.config.Role != "team" {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 128<<10)
	defer request.Body.Close()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input githubOIDCExchangeInput
	if err := decoder.Decode(&input); err != nil || rejectTrailingJSON(decoder) != nil ||
		input.Project != server.config.ProjectID || compatibility.Validate(input.Compatibility) != nil {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	if input.Integration == "" {
		input.Integration = "actions"
	}
	if input.Integration == "retained" && !server.config.RetainedArtifactsEnabled {
		http.NotFound(writer, request)
		return
	}
	if (input.Integration != "actions" && input.Integration != "turbo" && input.Integration != "buildkit" && input.Integration != "nx" && input.Integration != "gradle" && input.Integration != "bazel" && input.Integration != "retained") ||
		input.TTLSeconds < 0 || input.TTLSeconds > 3600 ||
		(input.Integration != "retained" && (input.Mode != "" || input.GitHubToken != "")) ||
		(input.Integration == "actions" && input.TTLSeconds != 0) ||
		(input.Integration != "actions" && (input.RecipeDigest != "" || input.Target != "" ||
			input.Platform != "" || input.Toolchain != "" || input.Builder != "")) {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	issuer := server.config.GitHubOIDCIssuer
	if issuer == "" {
		issuer = githubauth.DefaultOIDCIssuer
	}
	loaded, _ := oidcVerifiers.LoadOrStore(issuer, &githubauth.OIDCVerifier{Issuer: issuer})
	verifier := loaded.(*githubauth.OIDCVerifier)
	identity, err := verifier.Verify(request.Context(), input.IDToken, "layercache:"+server.config.ProjectID, time.Now().UTC())
	if err != nil || !strings.EqualFold(identity.Repository, server.config.ActionsRepository) ||
		!immutableCommit(identity.Commit) || !validGitRef(identity.Ref) {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	if input.Integration == "retained" {
		server.githubRetainedCapabilityExchange(writer, request, input, identity)
		return
	}
	if server.projectAuthority != nil {
		// Managed projects bind the GitHub repository's numeric identity at
		// provisioning. A recycled owner/name must not inherit old cache bytes.
		if identity.RepositoryID == "" {
			server.writeCapabilityExchangeFailure(writer, request)
			return
		}
		storedID, err := server.projectAuthority.RepositoryID(request.Context(), server.config.ProjectID)
		if err != nil || storedID != identity.RepositoryID {
			server.writeCapabilityExchangeFailure(writer, request)
			return
		}
		role, err := server.projectAuthority.GitHubActionsRole(request.Context(), server.config.ProjectID, identity.ActorID)
		if err != nil || role != "writer" {
			server.writeCapabilityExchangeFailure(writer, request)
			return
		}
	}
	recipe, platform, toolchain, builder := "", "", "", ""
	validPublicIdentity := true
	if input.Integration == "actions" {
		recipe, platform, toolchain, builder, validPublicIdentity = server.actionsPublicOIDCIdentity(
			input.Compatibility, input.RecipeDigest, input.Platform, input.Toolchain, input.Builder,
		)
	}
	if !validPublicIdentity {
		server.writeCapabilityExchangeFailure(writer, request)
		return
	}
	target := ""
	if recipe != "" {
		_, job, err := publicbuild.ParseActionsWorkflowTarget(input.Target)
		if err != nil {
			server.writeCapabilityExchangeFailure(writer, request)
			return
		}
		target, err = publicbuild.ActionsWorkflowTarget(identity.Repository, identity.WorkflowRef, job)
		if err != nil || target != input.Target {
			server.writeCapabilityExchangeFailure(writer, request)
			return
		}
		targetRecipe, err := publicbuild.MaintainedRecipeDigest(publicbuild.IntegrationActions, target)
		if err != nil || targetRecipe != recipe {
			server.writeCapabilityExchangeFailure(writer, request)
			return
		}
	}
	now := time.Now().UTC()
	expiresAt := now.Add(15 * time.Minute)
	if input.TTLSeconds != 0 {
		expiresAt = now.Add(time.Duration(input.TTLSeconds) * time.Second)
	}
	capabilities := []access.Capability{access.CapabilityWrite}
	// Native task hashes and registry tags share a project namespace, unlike Actions
	// archives. A PR must not poison main's cache, including pull_request_target
	// tokens whose ref is main. Missing/unknown event claims fail closed to reads.
	trustedEvent := identity.EventName == "push" || identity.EventName == "workflow_dispatch" || identity.EventName == "schedule"
	if (input.Integration == "buildkit" || input.Integration == "turbo" || input.Integration == "nx" || input.Integration == "gradle" || input.Integration == "bazel") &&
		(identity.Ref != server.config.ActionsDefaultRef || !trustedEvent) {
		capabilities = []access.Capability{access.CapabilityRead}
	}
	runID := "github-actions:" + strings.ToLower(identity.Repository) + ":" + identity.RunID + ":" + identity.RunAttempt
	if input.Integration == "turbo" {
		// A summary replaces one job's complete task graph. Matrix jobs must
		// never reconcile or delete one another's observations and outcomes.
		runID += ":check:" + identity.CheckRunID
	}
	token, err := access.MintCapabilityToken(server.config.LocalToken, access.Claims{
		Subject: "github-actions:" + identity.Subject, Project: server.config.ProjectID, Integration: input.Integration,
		RunID:         runID,
		WorkspaceID:   "github-actions-check:" + strings.ToLower(identity.Repository) + ":" + identity.CheckRunID,
		Compatibility: input.Compatibility, Repository: strings.ToLower(identity.Repository),
		Ref: identity.Ref, DefaultRef: server.config.ActionsDefaultRef, SourceCommit: strings.ToLower(identity.Commit),
		ActionsActorID: identity.ActorID,
		RecipeDigest:   recipe, Target: target, Platform: platform, Toolchain: toolchain, Builder: builder,
		Capabilities: capabilities, ExpiresAt: expiresAt,
	}, now)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "issue project capability"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"token": token, "teamToken": token, "expiresAt": expiresAt,
		"repository": identity.Repository, "ref": identity.Ref, "commit": identity.Commit,
	})
}

func (server *Server) writeCapabilityExchangeFailure(writer http.ResponseWriter, request *http.Request) {
	server.recordAuthorizationDenial(request)
	writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}

func (server *Server) actionsPublicOIDCIdentity(
	compatibilityID string,
	requestedRecipe string,
	requestedPlatform string,
	requestedToolchain string,
	requestedBuilder string,
) (recipe string, platform string, toolchain string, builder string, valid bool) {
	// Team-only cache traffic does not need a Public identity. The action sends
	// platform/toolchain defaults on every exchange, so recipe and builder are
	// the explicit signal that verified Public Cache access was requested.
	if requestedRecipe == "" && requestedBuilder == "" {
		return "", "", "", "", true
	}
	if server.config.ActionsPublicRecipeDigest == "" || server.config.ActionsPublicBuilder == "" ||
		requestedRecipe != server.config.ActionsPublicRecipeDigest ||
		requestedBuilder != server.config.ActionsPublicBuilder || requestedToolchain != actionsPublicToolchain {
		return "", "", "", "", false
	}
	if requestedPlatform != "linux/amd64" && requestedPlatform != "linux/arm64" {
		return "", "", "", "", false
	}
	if !strings.HasPrefix(compatibilityID, strings.ReplaceAll(requestedPlatform, "/", "-")+"-") {
		return "", "", "", "", false
	}
	return server.config.ActionsPublicRecipeDigest, requestedPlatform, actionsPublicToolchain,
		server.config.ActionsPublicBuilder, true
}

func immutableCommit(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 {
		return false
	}
	for _, character := range commit {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func validGitRef(ref string) bool {
	return (strings.HasPrefix(ref, "refs/heads/") || strings.HasPrefix(ref, "refs/tags/") || strings.HasPrefix(ref, "refs/pull/")) &&
		!strings.ContainsAny(ref, "\x00\r\n") && !strings.HasSuffix(ref, "/")
}

func capabilitiesForRole(role string) ([]access.Capability, bool) {
	switch strings.ToLower(role) {
	case "reader":
		return []access.Capability{access.CapabilityRead}, true
	case "writer":
		return []access.Capability{access.CapabilityWrite}, true
	case "admin":
		return []access.Capability{access.CapabilityAdmin}, true
	default:
		return nil, false
	}
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}

func validLocalRunNonce(value string) bool {
	if !strings.HasPrefix(value, "run-") || len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validLocalWorkspace(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, c := range value[7:] {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
