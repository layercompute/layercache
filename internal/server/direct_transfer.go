package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/directtransfer"
	"github.com/layercache/layercache/internal/measurement"
)

// Direct transfer moves a hosted object's bytes between a LayerCache client and
// object storage. The gateway still runs every authorization, lookup, quota and
// commit step; it only stops carrying the bytes.
func (server *Server) turboTransferRoutes() {
	server.mux.Handle("POST /v8/artifacts/{hash}/transfers/upload", server.requireToken(http.HandlerFunc(server.beginTurboUpload)))
	server.mux.Handle("POST /v8/artifacts/{hash}/transfers/{upload}/commit", server.requireToken(http.HandlerFunc(server.commitTurboUpload)))
	server.mux.Handle("POST /v8/artifacts/{hash}/transfers/{lease}/complete", server.requireToken(http.HandlerFunc(server.completeTurboRead)))
}

func (server *Server) hostsDirectTransfer() bool {
	return server.cloudStore != nil && server.team == nil && server.config.Role == "team"
}

// serveDirectTurboRead answers an opted-in GET with a read descriptor. It
// returns false when the caller must serve the proxied response instead.
func (server *Server) serveDirectTurboRead(
	writer http.ResponseWriter,
	request *http.Request,
	key artifact.Key,
	startedAt time.Time,
) bool {
	if !server.hostsDirectTransfer() {
		return false
	}
	read, err := server.cloudStore.IssueDirectRead(request.Context(), key, time.Time{})
	if errors.Is(err, artifact.ErrNotFound) {
		server.recordTurboLookupMiss(request, key, startedAt, time.Now().UTC(), false)
		writer.WriteHeader(http.StatusNotFound)
		return true
	}
	if err != nil {
		if !errors.Is(err, cloud.ErrDirectTransferUnavailable) {
			// Store errors carry no presigned URL; it is never created on
			// these paths.
			log.Printf("direct Turbo read fell back to the proxied response: %v", err)
		}
		return false
	}
	entry := read.Entry
	header := writer.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Type", directtransfer.MediaType)
	header.Set("x-artifact-duration", strconv.FormatInt(entry.Metadata.DurationMS, 10))
	if entry.Metadata.Tag != "" {
		header.Set("x-artifact-tag", entry.Metadata.Tag)
	}
	header.Set("x-layercache-digest", directtransfer.Digest(entry.Digest))
	header.Set("x-layercache-source", server.config.Role)
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(directtransfer.Descriptor{
		Transfer: directtransfer.Kind, Method: http.MethodGet, URL: read.URL.String(),
		Digest: directtransfer.Digest(entry.Digest), Size: entry.Size,
		ExpiresAt: read.ExpiresAt, Lease: read.Lease,
	})
	// Issuing is not a hit: the read stays unknown until a verified completion.
	if runID := access.RunID(request.Context()); runID != "" {
		finishedAt := time.Now().UTC()
		_ = server.measurements.ObserveTurbo(measurement.TurboObservation{
			RunID:      runID,
			ArtifactID: measurement.TurboArtifactIdentity(key.Project, key.Compatibility, key.Native),
			Result:     measurement.ResultUnknown, Source: measurement.SourceUnattributed,
			StartedAt: startedAt, FinishedAt: finishedAt,
			Timing: measurement.Timing{Lookup: finishedAt.Sub(startedAt)},
		})
	}
	return true
}

// completeTurboRead consumes a direct read's lease after the client verified
// the complete digest and size, and only then records the verified read and
// the hit.
func (server *Server) completeTurboRead(writer http.ResponseWriter, request *http.Request) {
	if !server.hostsDirectTransfer() {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	key, ok := server.turboRequestKey(writer, request)
	if !ok {
		return
	}
	var body directtransfer.Completion
	if err := decodeCloudJSON(writer, request, &body); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid transfer completion"})
		return
	}
	err := server.cloudStore.CompleteDirectRead(request.Context(), key, request.PathValue("lease"), body.Digest, body.Size)
	if errors.Is(err, artifact.ErrNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "transfer lease is not active"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "transfer completion failed"})
		return
	}
	now := time.Now().UTC()
	server.recordTurboHit(request, key, artifact.Entry{Size: body.Size}, server.config.Role, now, now, false)
	writer.WriteHeader(http.StatusNoContent)
}

// beginTurboUpload admits a declared object and returns a presigned PUT for a
// fresh staging key. A 404 means direct write is unavailable; the client then
// uses the proxied PUT.
func (server *Server) beginTurboUpload(writer http.ResponseWriter, request *http.Request) {
	if server.config.Role == "public" {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !server.hostsDirectTransfer() {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "direct transfer is unavailable"})
		return
	}
	key, ok := server.turboRequestKey(writer, request)
	if !ok {
		return
	}
	var body directtransfer.Declaration
	if err := decodeCloudJSON(writer, request, &body); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid transfer declaration"})
		return
	}
	digest, valid := directtransfer.ParseDigest(body.Digest)
	if !valid || body.Size < 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid transfer declaration"})
		return
	}
	upload, err := server.cloudStore.BeginDirectUpload(request.Context(), key, digest, body.Size)
	switch {
	case errors.Is(err, cloud.ErrDirectTransferUnavailable):
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "direct transfer is unavailable"})
		return
	case errors.Is(err, cloud.ErrDirectUploadLimit):
		writeJSON(writer, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, artifact.ErrQuota):
		writeJSON(writer, http.StatusInsufficientStorage, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, artifact.ErrCorrupt):
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid transfer declaration"})
		return
	case err != nil:
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "cache write failed"})
		return
	}
	headers := make(map[string]string, len(upload.Header))
	for name := range upload.Header {
		headers[name] = upload.Header.Get(name)
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, directtransfer.Upload{
		URL: upload.URL.String(), Headers: headers, Upload: upload.Upload, ExpiresAt: upload.ExpiresAt,
	})
}

// commitTurboUpload publishes a staged direct upload with the same
// first-writer, quota and eviction rules as the proxied PUT.
func (server *Server) commitTurboUpload(writer http.ResponseWriter, request *http.Request) {
	startedAt := time.Now().UTC()
	if server.config.Role == "public" {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !server.hostsDirectTransfer() {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "direct transfer is unavailable"})
		return
	}
	key, ok := server.turboRequestKey(writer, request)
	if !ok {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(request.Body, 64<<10))
	entry, _, err := server.cloudStore.CommitDirectUpload(request.Context(), key, turboRequestMetadata(request), request.PathValue("upload"))
	switch {
	case errors.Is(err, artifact.ErrConflict):
		writeJSON(writer, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, artifact.ErrQuota):
		writeJSON(writer, http.StatusInsufficientStorage, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, artifact.ErrCorrupt):
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "staged object does not match its declaration"})
		return
	case errors.Is(err, artifact.ErrNotFound), errors.Is(err, cloud.ErrDirectTransferUnavailable):
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "transfer upload is not active"})
		return
	case err != nil:
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "cache write failed"})
		return
	}
	server.recordTurboMiss(request, key, entry, startedAt, false)
	writeJSON(writer, http.StatusOK, map[string]bool{"stored": true})
}

// turboInstallError marks a local store failure while installing a Team
// download, as distinct from a failure to obtain the download.
type turboInstallError struct{ err error }

func (err *turboInstallError) Error() string { return err.err.Error() }
func (err *turboInstallError) Unwrap() error { return err.err }

// exactSizeReader fails a body that is shorter or longer than its declared
// size, so the local store never installs a truncated or padded download.
func exactSizeReader(body io.Reader, size int64) io.Reader {
	return &exactSize{body: body, remaining: size}
}

type exactSize struct {
	body      io.Reader
	remaining int64
	err       error
}

func (reader *exactSize) Read(buffer []byte) (int, error) {
	if reader.err != nil {
		return 0, reader.err
	}
	if int64(len(buffer)) > reader.remaining+1 {
		buffer = buffer[:reader.remaining+1]
	}
	count, err := reader.body.Read(buffer)
	reader.remaining -= int64(count)
	switch {
	case reader.remaining < 0:
		reader.err = artifact.ErrCorrupt
	case err == io.EOF && reader.remaining != 0:
		reader.err = artifact.ErrCorrupt
	default:
		return count, err
	}
	return 0, reader.err
}
