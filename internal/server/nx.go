package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/compatibility"
)

// Nx's native client supplies only a bearer credential and an opaque hash.
// Bind its profile to that credential, and use the client's plain-text error
// contract: 403 declines a read-only or capacity-limited write; 401 rejects auth.
func (server *Server) requireNxToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "private, no-store")
		if server.config.Role == "public" {
			http.NotFound(writer, request)
			return
		}
		claims, ok := server.authenticateRequest(request, "nx")
		if !ok || claims.Compatibility == "" || compatibility.Validate(claims.Compatibility) != nil ||
			claims.Integration != "" && claims.Integration != "nx" ||
			!server.authorizesSelectors(request, claims) ||
			!claims.Allows(access.CapabilityRead) || !server.currentMembershipAllows(request.Context(), claims, access.CapabilityRead) {
			server.recordAuthorizationDenial(request)
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodPut && (!claims.Allows(access.CapabilityWrite) || !server.currentMembershipAllows(request.Context(), claims, access.CapabilityWrite)) {
			server.recordAuthorizationDenial(request)
			http.Error(writer, "cache publication is read-only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(writer, request.WithContext(access.WithClaims(request.Context(), claims)))
	})
}

func validNxHash(hash string) bool {
	return hash != "" && hash != "quota" && hash != "pins" && len(hash) <= 1024 && !strings.ContainsAny(hash, "/\\\x00\r\n\t ")
}

func nxArtifactPath(path string) bool {
	hash, matches := strings.CutPrefix(path, "/v1/cache/")
	// More-specific administration routes share the prefix in the HTTP mux.
	return matches && validNxHash(hash)
}

func (server *Server) nxArtifact(writer http.ResponseWriter, request *http.Request) {
	hash := request.PathValue("hash")
	if !validNxHash(hash) {
		http.Error(writer, "invalid cache hash", http.StatusBadRequest)
		return
	}
	claims, _ := access.ClaimsFromContext(request.Context())
	key := artifact.Key{Integration: "nx", Project: claims.Project, Compatibility: claims.Compatibility, Native: hash}
	switch request.Method {
	case http.MethodGet:
		entry, body, err := server.store.Get(request.Context(), key)
		if errors.Is(err, artifact.ErrNotFound) || errors.Is(err, artifact.ErrCorrupt) {
			if errors.Is(err, artifact.ErrCorrupt) {
				_ = server.store.Delete(request.Context(), key)
			}
			http.NotFound(writer, request)
			return
		}
		if err != nil {
			http.Error(writer, "cache read failed", http.StatusInternalServerError)
			return
		}
		if server.cloudStore != nil {
			staged, cleanup, verifyErr := server.stageVerifiedArtifactRead(request.Context(), entry, body)
			closeErr := body.Close()
			if verifyErr == nil {
				verifyErr = closeErr
			}
			if verifyErr != nil {
				if cleanup != nil {
					cleanup()
				}
				if errors.Is(verifyErr, artifact.ErrCorrupt) {
					_ = server.store.Delete(context.WithoutCancel(request.Context()), key)
					http.NotFound(writer, request)
					return
				}
				if errors.Is(verifyErr, artifact.ErrQuota) {
					writer.Header().Set("x-layercache-degraded", "verification-staging-full")
					http.NotFound(writer, request)
					return
				}
				http.Error(writer, "cache read failed", http.StatusInternalServerError)
				return
			}
			body = staged
			defer cleanup()
		} else {
			defer body.Close()
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
		writer.Header().Set("x-layercache-digest", "sha256:"+entry.Digest)
		writer.WriteHeader(http.StatusOK)
		_, _ = io.Copy(writer, body)
	case http.MethodPut:
		server.putNx(writer, request, key)
	default:
		writer.Header().Set("Allow", "GET, PUT")
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (server *Server) putNx(writer http.ResponseWriter, request *http.Request, key artifact.Key) {
	defer request.Body.Close()
	// Nx requires every duplicate to conflict, even if its raw archive matches.
	// Put's atomic created result also fences simultaneous first writers.
	if _, err := server.store.Head(request.Context(), key); err == nil {
		writer.WriteHeader(http.StatusConflict)
		return
	} else if !errors.Is(err, artifact.ErrNotFound) {
		http.Error(writer, "cache metadata unavailable", http.StatusInternalServerError)
		return
	}
	maximum, err := server.cacheLimit(request.Context())
	if err != nil {
		http.Error(writer, "cache quota unavailable", http.StatusServiceUnavailable)
		return
	}
	if request.ContentLength < 0 {
		http.Error(writer, "a sized cache archive is required", http.StatusLengthRequired)
		return
	}
	if request.ContentLength > maximum {
		http.Error(writer, "cache archive exceeds capacity", http.StatusForbidden)
		return
	}
	_, created, err := server.store.Put(request.Context(), key, artifact.Metadata{}, io.LimitReader(request.Body, maximum+1))
	if errors.Is(err, artifact.ErrConflict) || err == nil && !created {
		writer.WriteHeader(http.StatusConflict)
		return
	}
	if errors.Is(err, artifact.ErrQuota) {
		http.Error(writer, "cache capacity unavailable", http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(writer, "cache write failed", http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(http.StatusOK)
}
