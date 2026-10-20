package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/access"
)

const snapshotExchangePath = "/v1/auth/snapshot/exchange"

func (server *Server) snapshotCapabilityExchange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if server.config.Role != "team" || server.config.SnapshotCredentialsFile == "" {
		http.NotFound(w, r)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	defer r.Body.Close()
	var input struct {
		Project       string `json:"project"`
		Compatibility string `json:"compatibility"`
		Key           string `json:"key"`
		Mode          string `json:"mode"`
	}
	if decoder.Decode(&input) != nil || rejectTrailingJSON(decoder) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid snapshot exchange request"})
		return
	}
	headers := r.Header.Values("Authorization")
	token := ""
	if len(headers) == 1 && strings.HasPrefix(headers[0], "Bearer ") {
		token = strings.TrimPrefix(headers[0], "Bearer ")
	}
	id, valid := access.SnapshotBootstrapID(token)
	grants, err := access.ReadSnapshotCredentials(server.config.SnapshotCredentialsFile)
	now := time.Now().UTC()
	if valid && err == nil && input.Project == server.config.ProjectID {
		for _, grant := range grants {
			if grant.ID != id || subtle.ConstantTimeCompare([]byte(grant.SecretSHA256), []byte(access.SnapshotSecretDigest(token))) != 1 ||
				!grant.Allows(input.Project, input.Compatibility, input.Key, input.Mode, now) {
				continue
			}
			nativeKey, keyErr := access.SnapshotNativeKey(input.Project, input.Compatibility, input.Key)
			if keyErr != nil {
				break
			}
			expiresAt := now.Add(access.SnapshotCapabilityLifetime)
			if grant.ExpiresAt.Before(expiresAt) {
				expiresAt = grant.ExpiresAt
			}
			capability := access.CapabilityRead
			if input.Mode == "write" {
				capability = access.CapabilityWrite
			}
			claims := access.Claims{
				Subject: "snapshot:" + grant.ID, Project: input.Project, Compatibility: input.Compatibility,
				Integration: "turbo", Capabilities: []access.Capability{capability}, ExpiresAt: expiresAt,
				SnapshotScope: &access.SnapshotScope{CredentialID: grant.ID, GrantDigest: grant.Digest(), NativeKey: nativeKey},
			}
			scopedToken, mintErr := access.MintCapabilityToken(server.config.LocalToken, claims, now)
			if mintErr != nil {
				break
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"version": 1, "token": scopedToken, "expiresAt": expiresAt, "project": input.Project,
				"compatibility": input.Compatibility, "nativeKey": nativeKey, "mode": input.Mode,
				"capabilities": claims.Capabilities, "credentialKind": "shared-snapshot-bootstrap",
			})
			return
		}
	}
	server.writeCapabilityExchangeFailure(w, r)
}

func (server *Server) currentSnapshotAllows(claims access.Claims, required access.Capability) bool {
	if server.config.Role != "team" || server.config.SnapshotCredentialsFile == "" || claims.SnapshotScope == nil ||
		required == access.CapabilityAdmin || !claims.Allows(required) {
		return false
	}
	grants, err := access.ReadSnapshotCredentials(server.config.SnapshotCredentialsFile)
	if err != nil {
		return false
	}
	for _, grant := range grants {
		if grant.ID == claims.SnapshotScope.CredentialID && grant.Project == claims.Project && grant.Compatibility == claims.Compatibility &&
			grant.ExpiresAt.After(time.Now()) && (required != access.CapabilityWrite || grant.Role == "writer") &&
			subtle.ConstantTimeCompare([]byte(grant.Digest()), []byte(claims.SnapshotScope.GrantDigest)) == 1 {
			return true
		}
	}
	return false
}

func (server *Server) snapshotRequestAllowed(r *http.Request, claims access.Claims) bool {
	if claims.SnapshotScope == nil || r.URL.RawPath != "" || r.URL.Path != "/v8/artifacts/"+claims.SnapshotScope.NativeKey {
		return false
	}
	required := access.CapabilityRead
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPut:
		required = access.CapabilityWrite
	default:
		return false
	}
	return server.currentSnapshotAllows(claims, required)
}
