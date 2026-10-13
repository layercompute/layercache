package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/remote"
)

const maximumTurboSyncHashes = 4096

type turboSyncRequest struct {
	Hashes    []string `json:"hashes"`
	TimeoutMS int      `json:"timeoutMs"`
}

type turboSyncResult struct {
	Requested int `json:"requested"`
	Ready     int `json:"ready"`
	Pending   int `json:"pending"`
}

// syncTurbo confirms only the caller's task identities. A global queue drain
// would couple this invocation to unrelated uploads and permanent conflicts.
func (server *Server) syncTurbo(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	if server.config.Role != "local" {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "Turbo synchronization requires Local Cache"})
		return
	}
	var input turboSyncRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 3<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid Turbo synchronization request"})
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || len(input.Hashes) == 0 || len(input.Hashes) > maximumTurboSyncHashes || input.TimeoutMS < 0 || input.TimeoutMS > 60000 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid Turbo synchronization bounds"})
		return
	}
	if input.TimeoutMS == 0 {
		input.TimeoutMS = 30000
	}
	hashes := make([]string, 0, len(input.Hashes))
	seen := make(map[string]bool, len(input.Hashes))
	for _, hash := range input.Hashes {
		if !validTurboSyncHash(hash) {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid Turbo synchronization hash"})
			return
		}
		if !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash)
		}
	}
	if server.team == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "Team Cache is not configured"})
		return
	}
	compatibilityID, err := server.requestCompatibility(request)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid Turbo synchronization compatibility"})
		return
	}
	team := server.team
	if compatibilityID != server.config.CompatibilityID {
		team, err = remote.NewTurboClientForCompatibilityWithTimeouts(server.config.TeamURL, server.currentTeamToken(), compatibilityID, remote.Timeouts{
			Metadata: server.config.RemoteMetadataTimeout, TransferIdle: server.config.RemoteTransferIdleTimeout,
		})
		if err != nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "Team Cache configuration is unavailable"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(request.Context(), time.Duration(input.TimeoutMS)*time.Millisecond)
	defer cancel()
	lastReady := make(map[string]bool, len(hashes))
	pinned := make(map[string]preparedTurboUpload)
	check := func(hash string) bool {
		_, err := team.Head(ctx, hash)
		lastReady[hash] = err == nil
		return err == nil
	}
	// Try publication once. On failure the existing worker owns durable retries;
	// later passes only check availability, never re-upload every archive.
	for _, hash := range hashes {
		if ctx.Err() != nil {
			break
		}
		if check(hash) {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		key := artifact.Key{Integration: "turbo", Project: server.config.ProjectID, Compatibility: compatibilityID, Native: hash}
		if prepared := server.publishTurboForSync(ctx, team, key); prepared != nil {
			pinned[hash] = *prepared
		}
		if ctx.Err() == nil {
			check(hash)
		}
	}
	// A later publication can evict an earlier hit under Team's byte quota.
	// Recheck after our writes rather than accumulate incompatible historical
	// observations into an all-ready result.
	checkPass := func() map[string]bool {
		confirmed := make(map[string]bool, len(hashes))
		for _, hash := range hashes {
			if ctx.Err() != nil {
				break
			}
			if check(hash) {
				confirmed[hash] = true
			}
		}
		return confirmed
	}
	ready := checkPass()
	delay := 100 * time.Millisecond
	for len(ready) < len(hashes) && ctx.Err() == nil {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
			// Rebuild readiness from this pass. Background uploads can evict a
			// prior hit while another task becomes available.
			ready = checkPass()
		}
		if delay < time.Second {
			delay *= 2
		}
	}
	// A stalled HEAD or cancelled PUT may exhaust the confirmation deadline.
	// Preserve requested Local artifacts for durable retry without letting the
	// caller's cancellation undo the queue/pin handoff. One cleanup budget bounds
	// the entire set, rather than adding a fresh timeout per artifact.
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(request.Context()), time.Second)
	defer cancelCleanup()
	for hash, prepared := range pinned {
		if lastReady[hash] {
			server.releaseTurboUploadPin(prepared.pin.Owner)
		} else {
			_ = server.enqueuePinnedTurboUpload(cleanupCtx, prepared)
		}
	}
	for _, hash := range hashes {
		if cleanupCtx.Err() != nil {
			break
		}
		if lastReady[hash] {
			continue
		}
		if _, alreadyPinned := pinned[hash]; alreadyPinned {
			continue
		}
		key := artifact.Key{Integration: "turbo", Project: server.config.ProjectID, Compatibility: compatibilityID, Native: hash}
		if entry, body, err := server.store.Get(cleanupCtx, key); err == nil {
			_ = body.Close()
			_ = server.enqueueTurboUpload(cleanupCtx, key, entry)
		}
	}
	writeJSON(writer, http.StatusOK, turboSyncResult{Requested: len(hashes), Ready: len(ready), Pending: len(hashes) - len(ready)})
}

func validTurboSyncHash(hash string) bool {
	if hash == "" || len(hash) > 512 || hash == "." || hash == ".." || strings.ContainsAny(hash, "/\\") {
		return false
	}
	for _, character := range hash {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (server *Server) publishTurboForSync(ctx context.Context, team *remote.TurboClient, key artifact.Key) *preparedTurboUpload {
	entry, body, err := server.store.Get(ctx, key)
	if err != nil {
		return nil
	}
	defer body.Close()
	prepared, err := server.pinTurboUpload(ctx, key, entry)
	if err != nil {
		return nil
	}
	if err := team.Put(ctx, key.Native, entry, body); err != nil {
		if ctx.Err() != nil {
			return &prepared
		}
		_ = server.enqueuePinnedTurboUpload(ctx, prepared)
		return nil
	}
	server.releaseTurboUploadPin(prepared.pin.Owner)
	return nil
}
