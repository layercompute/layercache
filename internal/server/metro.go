package server

import (
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
)

const metroCachePrefix = "/v1/metro-cache/"
const maxMetroValueBytes = 64 << 20

// Metro's HttpStore sends its own gzip representation of an opaque transform
// result. Preserve the native key and representation byte for byte.
func (server *Server) metroArtifact(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	hash := r.PathValue("key")
	if r.URL.RawPath != "" || len(hash) < 16 || len(hash) > 128 || hash != strings.ToLower(hash) {
		http.Error(w, "invalid Metro key", http.StatusBadRequest)
		return
	}
	if _, err := hex.DecodeString(hash); err != nil {
		http.Error(w, "invalid Metro key", http.StatusBadRequest)
		return
	}
	claims, _ := access.ClaimsFromContext(r.Context())
	key := artifact.Key{Integration: "metro", Project: claims.Project, Compatibility: claims.Compatibility, Native: hash, Version: "httpstore-v1"}
	switch r.Method {
	case http.MethodGet:
		entry, body, err := server.store.Get(r.Context(), key)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer body.Close()
		w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
		_, _ = io.Copy(w, body)
	case http.MethodPut:
		defer r.Body.Close()
		body := http.MaxBytesReader(w, r.Body, maxMetroValueBytes)
		_, _, err := server.store.Put(artifact.WithSizeHint(r.Context(), r.ContentLength), key, artifact.Metadata{}, body)
		var oversized *http.MaxBytesError
		switch {
		case err == nil, errors.Is(err, artifact.ErrConflict):
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, artifact.ErrQuota), errors.As(err, &oversized):
			w.WriteHeader(http.StatusInsufficientStorage)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	default:
		w.Header().Set("Allow", "GET, PUT")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
