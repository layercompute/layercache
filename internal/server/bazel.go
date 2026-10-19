package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
)

const bazelCachePrefix = "/v1/bazel-cache/"

// The native engine owns ActionResult validation, CAS digests and eviction.
// LayerCache supplies current private project authority, never an instance-name
// prefix: bazel-remote's CAS is shared across instance names within one engine.
func (server *Server) requireBazelToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "private, no-store")
		backend := server.config.BazelRemote
		if backend == nil || server.config.Role == "public" {
			http.NotFound(writer, request)
			return
		}
		claims, authenticated := server.authenticateRequest(request, "bazel")
		if !authenticated || claims.Integration != "bazel" || claims.Project != backend.ProjectID ||
			claims.Compatibility != backend.CompatibilityID || !server.authorizesSelectors(request, claims) ||
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

func bazelArtifactPath(path string) bool {
	selected, matches := strings.CutPrefix(path, bazelCachePrefix)
	kind, hash, separator := strings.Cut(selected, "/")
	if !matches || !separator || kind != "ac" && kind != "cas" || len(hash) != 64 {
		return false
	}
	for _, character := range hash {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func (server *Server) bazelArtifact(writer http.ResponseWriter, request *http.Request) {
	if !bazelArtifactPath(request.URL.Path) || request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
		http.Error(writer, "invalid Bazel cache path", http.StatusBadRequest)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodPut {
		writer.Header().Set("Allow", "GET, HEAD, PUT")
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if encoding := request.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		http.Error(writer, "only uncompressed native HTTP requests are supported", http.StatusBadRequest)
		return
	}
	backend := server.config.BazelRemote
	ctx, cancel := context.WithTimeout(request.Context(), backend.RequestTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	controller := http.NewResponseController(writer)
	_ = controller.SetWriteDeadline(deadline)
	defer controller.SetWriteDeadline(time.Time{})
	var body io.Reader
	var size int64
	if request.Method == http.MethodPut {
		// Reading the complete bounded upload first prevents a backend response
		// from acknowledging an interrupted client body or its terminal error.
		_ = controller.SetReadDeadline(deadline)
		defer controller.SetReadDeadline(time.Time{})
		staged, cleanup, err := server.stageBazelUpload(ctx, request)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, artifact.ErrQuota) || errors.Is(err, artifact.ErrMinFreeSpace) {
				status = http.StatusRequestEntityTooLarge
			} else if errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
			}
			http.Error(writer, "native cache upload was not admitted", status)
			return
		}
		defer cleanup()
		body = staged
		info, err := staged.Stat()
		if err != nil {
			http.Error(writer, "native cache upload unavailable", http.StatusInternalServerError)
			return
		}
		size = info.Size()
	}
	upstream, err := http.NewRequestWithContext(ctx, request.Method, backend.URL+"/"+strings.TrimPrefix(request.URL.Path, bazelCachePrefix), body)
	if err != nil {
		http.Error(writer, "native cache backend unavailable", http.StatusBadGateway)
		return
	}
	upstream.ContentLength = size
	upstream.Header.Set("Accept-Encoding", "identity")
	if request.Method == http.MethodPut {
		upstream.Header.Set("Content-Type", "application/octet-stream")
	}
	// RoundTrip never follows redirects. A fresh request forwards no client
	// credentials, cookies, selectors or proxy headers to the native engine.
	response, err := server.bazelTransport.RoundTrip(upstream)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		http.Error(writer, "native cache backend unavailable", status)
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 && response.StatusCode <= 599 {
		http.Error(writer, "native cache backend declined request", response.StatusCode)
		return
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" {
		http.Error(writer, "unexpected native cache backend response", http.StatusBadGateway)
		return
	}
	if request.Method != http.MethodPut {
		if response.ContentLength < 0 || response.ContentLength > backend.MaxBlobBytes {
			http.Error(writer, "native cache response exceeds configured bound", http.StatusBadGateway)
			return
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
	}
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = io.Copy(writer, response.Body)
	}
}

func (server *Server) stageBazelUpload(ctx context.Context, request *http.Request) (*os.File, func(), error) {
	defer request.Body.Close()
	maximum := server.config.BazelRemote.MaxBlobBytes
	if request.ContentLength > maximum {
		return nil, nil, artifact.ErrQuota
	}
	reservation := request.ContentLength
	if reservation < 0 {
		reservation = maximum
	}
	lease, err := server.localStore.ReserveUploadStaging(reservation)
	if err != nil {
		return nil, nil, err
	}
	staged, err := os.CreateTemp(filepath.Join(server.config.DataDir, "staging"), "bazel-upload-*")
	if err != nil {
		lease.Release()
		return nil, nil, err
	}
	cleanup := func() {
		_ = staged.Close()
		_ = os.Remove(staged.Name())
		lease.Release()
	}
	destination := &bazelUploadWriter{destination: server.localStore.SpaceCheckedWriter(ctx, staged), remaining: reservation}
	written, err := io.Copy(destination, io.LimitReader(request.Body, maximum+1))
	if err == nil && (written > maximum || request.ContentLength >= 0 && written > request.ContentLength) {
		err = artifact.ErrQuota
	}
	if err == nil && request.ContentLength >= 0 && written != request.ContentLength {
		err = io.ErrUnexpectedEOF
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = server.localStore.SyncStaged(ctx, staged)
	}
	if err == nil {
		_, err = staged.Seek(0, io.SeekStart)
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return staged, cleanup, nil
}

// Check the reserved bound before touching disk, including a dishonest sized
// body. Unknown-length uploads reserve their full configured maximum.
type bazelUploadWriter struct {
	destination io.Writer
	remaining   int64
}

func (writer *bazelUploadWriter) Write(body []byte) (int, error) {
	if int64(len(body)) > writer.remaining {
		return 0, artifact.ErrQuota
	}
	written, err := writer.destination.Write(body)
	writer.remaining -= int64(written)
	return written, err
}
