package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/compatibility"
)

const gradleCachePrefix = "/v1/gradle-cache/"

// Gradle sends preemptive Basic authentication. The username is a fixed label;
// only the password's signed capability supplies project and profile authority.
// Keep this translation confined to Gradle's route.
func gradleCapability(request *http.Request) (string, bool) {
	values := request.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) > 32<<10 {
		return "", false
	}
	username, token, ok := request.BasicAuth()
	return token, ok && username == "layercache" && token != "" && !strings.ContainsAny(token, " \t\r\n")
}

func (server *Server) requireGradleToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "private, no-store")
		if server.config.Role == "public" {
			http.NotFound(writer, request)
			return
		}
		token, ok := gradleCapability(request)
		bearer := request.Clone(request.Context())
		bearer.Header.Set("Authorization", "Bearer "+token)
		claims, authenticated := server.authenticateRequest(bearer, "gradle")
		if !ok || !authenticated || claims.Compatibility == "" || compatibility.Validate(claims.Compatibility) != nil ||
			claims.Integration != "" && claims.Integration != "gradle" ||
			!server.authorizesSelectors(request, claims) ||
			!claims.Allows(access.CapabilityRead) || !server.currentMembershipAllows(request.Context(), claims, access.CapabilityRead) {
			server.recordAuthorizationDenial(request)
			writer.Header().Set("WWW-Authenticate", `Basic realm="Layer Cache"`)
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

func validGradleHash(hash string) bool {
	if hash == "" || hash == "." || hash == ".." || len(hash) > 1024 {
		return false
	}
	for _, character := range hash {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func gradleArtifactPath(path string) bool {
	hash, matches := strings.CutPrefix(path, gradleCachePrefix)
	return matches && validGradleHash(hash)
}

func (server *Server) gradleArtifact(writer http.ResponseWriter, request *http.Request) {
	hash := request.PathValue("hash")
	if !validGradleHash(hash) || request.URL.RawPath != "" {
		http.Error(writer, "invalid cache hash", http.StatusBadRequest)
		return
	}
	claims, _ := access.ClaimsFromContext(request.Context())
	key := artifact.Key{Integration: "gradle", Project: claims.Project, Compatibility: claims.Compatibility, Native: hash}
	switch request.Method {
	case http.MethodGet:
		server.getGradle(writer, request, key)
	case http.MethodPut:
		server.putGradle(writer, request, key)
	default:
		writer.Header().Set("Allow", "GET, PUT")
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (server *Server) getGradle(writer http.ResponseWriter, request *http.Request, key artifact.Key) {
	startedAt := time.Now().UTC()
	entry, body, err := server.store.Get(request.Context(), key)
	if errors.Is(err, artifact.ErrNotFound) || errors.Is(err, artifact.ErrCorrupt) {
		if errors.Is(err, artifact.ErrCorrupt) {
			_ = server.store.Delete(context.WithoutCancel(request.Context()), key)
		}
		server.observeNative(request, "missed", key.Project, key.Compatibility, key.Native, "", startedAt, 0)
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
	count, copyErr := io.Copy(writer, body)
	if copyErr == nil && count == entry.Size {
		server.observeNative(request, "delivered", key.Project, key.Compatibility, key.Native, entry.Digest, startedAt, count)
	}
}

func (server *Server) putGradle(writer http.ResponseWriter, request *http.Request, key artifact.Key) {
	startedAt := time.Now().UTC()
	defer request.Body.Close()
	maximum, err := server.cacheLimit(request.Context())
	if err != nil || maximum <= 0 || maximum == math.MaxInt64 {
		http.Error(writer, "cache quota unavailable", http.StatusServiceUnavailable)
		return
	}
	if request.ContentLength > maximum {
		http.Error(writer, "cache archive exceeds capacity", http.StatusRequestEntityTooLarge)
		return
	}
	reader := &gradleUploadReader{ctx: request.Context(), body: io.LimitReader(request.Body, maximum+1), expected: request.ContentLength, maximum: maximum}
	entry, created, err := server.store.Put(request.Context(), key, artifact.Metadata{}, reader)
	if reader.terminalErr != nil {
		// Cloud error redaction may remove wrapping. The reader owns the
		// canonical request failure, independently of backend error formatting.
		err = reader.terminalErr
	}
	if errors.Is(err, artifact.ErrConflict) {
		// Gradle archives include origin metadata, so equal task keys can have
		// different bytes. Retain the committed first winner and acknowledge the
		// duplicate without replacing it or claiming byte equality.
		writer.WriteHeader(http.StatusOK)
		return
	}
	if errors.Is(err, artifact.ErrQuota) {
		http.Error(writer, "cache capacity unavailable", http.StatusRequestEntityTooLarge)
		return
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		http.Error(writer, "cache upload incomplete", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(writer, "cache write failed", http.StatusInternalServerError)
		return
	}
	if created {
		server.observeNative(request, "published", key.Project, key.Compatibility, key.Native, entry.Digest, startedAt, entry.Size)
	}
	writer.WriteHeader(http.StatusOK)
}

// Check length and cancellation before Put sees EOF and commits. Unknown-length
// native uploads are accepted, while every stream is limited to capacity + 1.
type gradleUploadReader struct {
	ctx         context.Context
	body        io.Reader
	expected    int64
	maximum     int64
	received    int64
	terminalErr error
}

func (reader *gradleUploadReader) Read(buffer []byte) (count int, err error) {
	defer func() {
		if err != nil && !errors.Is(err, io.EOF) {
			if reader.terminalErr == nil {
				reader.terminalErr = err
			}
			// The S3 SDK treats exact io.ErrUnexpectedEOF as a valid final
			// multipart part. A distinct wrapper preserves rejection, including
			// when io.ReadFull consumes a full buffer and discards this error.
			err = fmt.Errorf("read Gradle upload: %w", reader.terminalErr)
		}
	}()
	if reader.terminalErr != nil {
		return 0, reader.terminalErr
	}
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	count, err = reader.body.Read(buffer)
	reader.received += int64(count)
	if reader.received > reader.maximum {
		return count, artifact.ErrQuota
	}
	if reader.expected >= 0 && (reader.received > reader.expected || errors.Is(err, io.EOF) && reader.received != reader.expected) {
		return count, io.ErrUnexpectedEOF
	}
	if contextErr := reader.ctx.Err(); contextErr != nil {
		return count, contextErr
	}
	return count, err
}
