package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/measurement"
)

const (
	// A Turbo binary that fails to restore rebuilds and then uploads, so the
	// watch has to outlast a long build.
	turboRedirectWatch = time.Hour
	// Past this many tracked keys, reads stay proxied until entries expire.
	turboRedirectMaxTracked = 1 << 14
	// Three suspected failures within the pause window stop all redirects for
	// the project for that window.
	turboRedirectTripFailures = 3
	turboRedirectPause        = 10 * time.Minute
)

// minimumRedirectTurbo is the oldest version tested against a redirect. Before
// raising the Turbo pins, rerun qa/turbo-redirect/harness.mjs.
var minimumRedirectTurbo = []int{2, 9, 14}

// stockTurboFollowsRedirects recognises Turbo's own User-Agent, for example
// "turbo 2.10.12 1.97.0-nightly linux x86_64". LayerCache's own clients reject
// cross-origin redirects, so anything else must keep the proxied response. A
// pre-release version is not one that was tested.
func stockTurboFollowsRedirects(userAgent string) bool {
	fields := strings.Fields(userAgent)
	if len(fields) < 2 || fields[0] != "turbo" {
		return false
	}
	parts := strings.Split(fields[1], ".")
	if len(parts) != len(minimumRedirectTurbo) {
		return false
	}
	version := make([]int, len(parts))
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return false
		}
		version[index] = number
	}
	return slices.Compare(version, minimumRedirectTurbo) >= 0
}

// turboRedirectGuard infers failed redirects. Stock Turbo reports nothing when
// a restore fails: it rebuilds and uploads. A PUT for a key redirected recently
// is therefore the failure signal. The next read of that key goes through the
// proxied path, whose verification deletes a corrupt entry so the rebuild can
// replace it. A deliberate fresh rebuild looks the same, and only costs reads
// through the gateway for a while.
type turboRedirectGuard struct {
	mu          sync.Mutex
	issued      map[artifact.Key]time.Time
	proxied     map[artifact.Key]time.Time
	failures    []time.Time
	pausedUntil time.Time
}

func newTurboRedirectGuard() *turboRedirectGuard {
	return &turboRedirectGuard{issued: map[artifact.Key]time.Time{}, proxied: map[artifact.Key]time.Time{}}
}

func dropExpiredTurboRedirects(tracked map[artifact.Key]time.Time, now time.Time) {
	for key, expires := range tracked {
		if !now.Before(expires) {
			delete(tracked, key)
		}
	}
}

func (guard *turboRedirectGuard) allows(key artifact.Key, now time.Time) bool {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if now.Before(guard.pausedUntil) {
		return false
	}
	if expires, ok := guard.proxied[key]; ok {
		if now.Before(expires) {
			return false
		}
		delete(guard.proxied, key)
	}
	if len(guard.issued) < turboRedirectMaxTracked {
		return true
	}
	dropExpiredTurboRedirects(guard.issued, now)
	dropExpiredTurboRedirects(guard.proxied, now)
	return len(guard.issued) < turboRedirectMaxTracked
}

func (guard *turboRedirectGuard) issue(key artifact.Key, now time.Time) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.issued[key] = now.Add(turboRedirectWatch)
}

// upload records a PUT and reports whether it paused redirects.
func (guard *turboRedirectGuard) upload(key artifact.Key, now time.Time) bool {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	expires, ok := guard.issued[key]
	if !ok {
		return false
	}
	delete(guard.issued, key)
	if !now.Before(expires) {
		return false
	}
	if len(guard.proxied) >= turboRedirectMaxTracked {
		dropExpiredTurboRedirects(guard.proxied, now)
	}
	guard.proxied[key] = now.Add(turboRedirectWatch)
	guard.failures = slices.DeleteFunc(guard.failures, func(failure time.Time) bool {
		return now.Sub(failure) >= turboRedirectPause
	})
	guard.failures = append(guard.failures, now)
	if len(guard.failures) < turboRedirectTripFailures {
		return false
	}
	guard.failures = nil
	guard.pausedUntil = now.Add(turboRedirectPause)
	return true
}

func (server *Server) observeTurboUpload(key artifact.Key, now time.Time) {
	if server.turboRedirects.upload(key, now) {
		log.Printf("Turbo redirects paused for %s after repeated failed restores", turboRedirectPause)
	}
}

// serveTurboRedirect answers a stock Turbo GET with a 302 to a presigned object
// URL. It returns false when the caller must serve the proxied response, which
// also covers a miss so the proxied lookup can still consult Public.
func (server *Server) serveTurboRedirect(
	writer http.ResponseWriter,
	request *http.Request,
	key artifact.Key,
	startedAt time.Time,
) bool {
	if !stockTurboFollowsRedirects(request.UserAgent()) || !server.turboRedirects.allows(key, startedAt) {
		return false
	}
	read, err := server.cloudStore.IssueTurboRedirect(request.Context(), key)
	if err != nil {
		if !errors.Is(err, cloud.ErrDirectTransferUnavailable) && !errors.Is(err, artifact.ErrNotFound) &&
			!errors.Is(err, context.Canceled) {
			// Store errors carry no presigned URL; it is never created on
			// these paths.
			log.Printf("Turbo redirect fell back to the proxied response: %v", err)
		}
		return false
	}
	server.turboRedirects.issue(key, startedAt)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Location", read.URL.String())
	writer.Header().Set("x-layercache-source", server.config.Role)
	writer.WriteHeader(http.StatusFound)
	server.recordTurboRedirect(request, key, read.Entry, startedAt)
	return true
}

// recordTurboRedirect leaves provisional evidence for the Run Summary, which
// decides the result: a redirect the client failed to follow reconciles as a
// miss. It carries the producer duration because the object store cannot send
// x-artifact-duration, so Turbo reports the hit with timeSaved 0.
func (server *Server) recordTurboRedirect(request *http.Request, key artifact.Key, entry artifact.Entry, startedAt time.Time) {
	runID := access.RunID(request.Context())
	if runID == "" {
		return
	}
	finishedAt := time.Now().UTC()
	observation := measurement.TurboObservation{
		RunID:      runID,
		ArtifactID: measurement.TurboArtifactIdentity(key.Project, key.Compatibility, key.Native),
		Result:     measurement.ResultHit, Source: measurementSource(server.config.Role),
		StartedAt: startedAt, FinishedAt: finishedAt,
		Timing: measurement.Timing{Lookup: finishedAt.Sub(startedAt)},
		Bytes:  measurement.Bytes{Downloaded: entry.Size},
	}
	if entry.Metadata.DurationMS > 0 {
		duration := time.Duration(entry.Metadata.DurationMS) * time.Millisecond
		observation.ProducerDuration = &duration
	}
	_ = server.measurements.ObserveTurbo(observation)
}
