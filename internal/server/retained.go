package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/retained"
)

type retainedRepository interface {
	ReserveRetained(context.Context, retained.Declaration) (retained.Receipt, error)
	PublishRetained(context.Context, string, io.Reader) (retained.Receipt, error)
	InspectRetained(context.Context, string) (retained.Receipt, error)
	OpenRetained(context.Context, string) (retained.Receipt, io.ReadCloser, error)
}

func (server *Server) retainedRoutes() {
	server.mux.Handle("POST /v1/retained-artifacts", server.requireRetainedToken(http.HandlerFunc(server.reserveRetained)))
	server.mux.Handle("GET /v1/retained-artifacts/{id}", server.requireRetainedToken(http.HandlerFunc(server.inspectRetained)))
	server.mux.Handle("PUT /v1/retained-artifacts/{id}/content", server.requireRetainedToken(http.HandlerFunc(server.publishRetained)))
	server.mux.Handle("GET /v1/retained-artifacts/{id}/content", server.requireRetainedToken(http.HandlerFunc(server.readRetained)))
}

func (server *Server) reserveRetained(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	request.Body = http.MaxBytesReader(writer, request.Body, 16<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var declaration retained.Declaration
	if decoder.Decode(&declaration) != nil || rejectTrailingJSON(decoder) != nil || retained.Validate(declaration) != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid retained declaration"})
		return
	}
	claims, _ := access.ClaimsFromContext(request.Context())
	if declaration.Producer != retainedProducer(claims) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "retained producer does not match signed authority"})
		return
	}
	if declaration.Size > math.MaxInt64/2 {
		writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": "retained artifact exceeds capacity"})
		return
	}
	// A full staged upload plus its committed destination must fit while the
	// project transaction separately reserves logical bytes and metadata quota.
	if !server.storagePool.admits(declaration.Size * 2) {
		writeRetainedError(writer, artifact.ErrQuota)
		return
	}
	started := time.Now()
	receipt, err := server.retainedStore.ReserveRetained(request.Context(), declaration)
	if err != nil {
		writeRetainedError(writer, err)
		return
	}
	writer.Header().Set("Server-Timing", "retained_admission;dur="+strconv.FormatFloat(float64(time.Since(started).Microseconds())/1000, 'f', 3, 64))
	writeJSON(writer, http.StatusOK, receipt)
}

func (server *Server) authorizedRetainedReceipt(writer http.ResponseWriter, request *http.Request, producerOnly bool) (retained.Receipt, bool) {
	id := request.PathValue("id")
	if !retained.ValidID(id) || request.URL.RawPath != "" {
		http.NotFound(writer, request)
		return retained.Receipt{}, false
	}
	receipt, err := server.retainedStore.InspectRetained(request.Context(), id)
	if err != nil {
		writeRetainedError(writer, err)
		return retained.Receipt{}, false
	}
	claims, _ := access.ClaimsFromContext(request.Context())
	expected := retainedProducer(claims)
	actual := receipt.Declaration.Producer
	if !producerOnly && !claims.Allows(access.CapabilityWrite) {
		producerAttempt, producerError := strconv.ParseUint(actual.RunAttempt, 10, 64)
		readerAttempt, readerError := strconv.ParseUint(expected.RunAttempt, 10, 64)
		if producerError != nil || readerError != nil || producerAttempt == 0 || producerAttempt > readerAttempt {
			http.NotFound(writer, request)
			return retained.Receipt{}, false
		}
		// A consumer-only rerun may name a retained receipt from an earlier
		// attempt of this exact run. There is no search for a previous/latest
		// receipt. Publisher capabilities retain their own exact signed job and
		// attempt for inspection as well as publication.
		expected.RunAttempt = actual.RunAttempt
		expected.JobID = actual.JobID
	}
	if actual != expected {
		http.NotFound(writer, request)
		return retained.Receipt{}, false
	}
	return receipt, true
}

func (server *Server) inspectRetained(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	receipt, ok := server.authorizedRetainedReceipt(writer, request, false)
	if ok {
		writer.Header().Set("Server-Timing", "retained_inspect;dur="+strconv.FormatFloat(float64(time.Since(started).Microseconds())/1000, 'f', 3, 64))
		writeJSON(writer, http.StatusOK, receipt)
	}
}

func (server *Server) publishRetained(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	receipt, ok := server.authorizedRetainedReceipt(writer, request, true)
	if !ok {
		return
	}
	if receipt.State == retained.Published {
		// The immutable declaration and committed receipt already establish the
		// outcome of an uncertain upload; no second publication is necessary.
		writeJSON(writer, http.StatusOK, receipt)
		return
	}
	if receipt.State != retained.Reserved && receipt.State != retained.Staging {
		writeRetainedError(writer, retained.ErrInactive)
		return
	}
	if request.ContentLength >= 0 && request.ContentLength != receipt.Declaration.Size {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "retained content length differs from declaration"})
		return
	}
	// The declaration was admitted before any bytes were accepted. A single
	// streamed request preserves backpressure and avoids serial per-chunk RPCs.
	request.Body = http.MaxBytesReader(writer, request.Body, receipt.Declaration.Size+1)
	uploadContext, cancel := context.WithDeadline(request.Context(), receipt.ReservedUntil)
	defer cancel()
	interrupted := make(chan struct{})
	stopInterrupt := context.AfterFunc(uploadContext, func() {
		defer close(interrupted)
		// Cancel a client stalled in Body.Read even when the storage SDK has not
		// reached a network operation that observes context cancellation yet.
		_ = http.NewResponseController(writer).SetReadDeadline(time.Now())
		_ = request.Body.Close()
	})
	defer func() {
		if !stopInterrupt() {
			<-interrupted
		}
	}()
	started := time.Now()
	published, err := server.retainedStore.PublishRetained(uploadContext, receipt.ID, retainedUploadReader{ctx: uploadContext, body: request.Body})
	if err != nil {
		writeRetainedError(writer, err)
		return
	}
	writer.Header().Set("Server-Timing", "retained_publish;dur="+strconv.FormatFloat(float64(time.Since(started).Microseconds())/1000, 'f', 3, 64))
	writeJSON(writer, http.StatusOK, published)
}

type retainedUploadReader struct {
	ctx  context.Context
	body io.Reader
}

func (reader retainedUploadReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := reader.body.Read(buffer)
	if canceled := reader.ctx.Err(); canceled != nil {
		return 0, canceled
	}
	return n, err
}

func (server *Server) readRetained(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	receipt, ok := server.authorizedRetainedReceipt(writer, request, false)
	if !ok {
		return
	}
	opened, body, err := server.retainedStore.OpenRetained(request.Context(), receipt.ID)
	if err != nil {
		writeRetainedError(writer, err)
		return
	}
	defer body.Close()
	if opened.Declaration != receipt.Declaration || opened.State != retained.Published {
		writeRetainedError(writer, artifact.ErrCorrupt)
		return
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Length", strconv.FormatInt(opened.Declaration.Size, 10))
	writer.Header().Set("X-Layercache-Digest", "sha256:"+opened.Declaration.Digest)
	writer.Header().Set("X-Layercache-Retained-ID", opened.ID)
	writer.Header().Set("Server-Timing", "retained_open;dur="+strconv.FormatFloat(float64(time.Since(started).Microseconds())/1000, 'f', 3, 64))
	writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(writer, body)
	// A reader verifies complete size and SHA256 before installing the file.
	// Once headers are sent, truncated/backend failures remain failed transfers.
}

func writeRetainedError(writer http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "retained persistence unavailable"
	switch {
	case errors.Is(err, artifact.ErrNotFound):
		status, message = http.StatusNotFound, "retained artifact not found"
	case errors.Is(err, artifact.ErrConflict):
		status, message = http.StatusConflict, "retained declaration conflicts"
	case errors.Is(err, artifact.ErrQuota):
		status, message = http.StatusInsufficientStorage, "retained admission capacity unavailable"
	case errors.Is(err, artifact.ErrCorrupt):
		status, message = http.StatusUnprocessableEntity, "retained content verification failed"
	case errors.Is(err, retained.ErrBusy):
		status, message = http.StatusConflict, "retained publication is staging; inspect before retry"
	case errors.Is(err, retained.ErrInactive):
		status, message = http.StatusGone, "retained artifact is inactive"
	}
	writeJSON(writer, status, map[string]string{"error": message})
}
