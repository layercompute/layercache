package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/layercache/layercache/internal/artifact"
)

// Each Server serves one project, so the warning budget belongs to that
// project and is shared by both verification quota causes and all requests.
type verificationWarningLimiter struct {
	mu   sync.Mutex
	next time.Time
}

func (limiter *verificationWarningLimiter) allow(now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if now.Before(limiter.next) {
		return false
	}
	limiter.next = now.Add(time.Minute)
	return true
}

func (server *Server) warnRefusedVerifiedRead(entry artifact.Entry, err error) {
	if !server.verifyWarnings.allow(time.Now()) {
		return
	}
	cause := "quota"
	switch {
	case errors.Is(err, artifact.ErrMinFreeSpace):
		cause = "min-free-space"
		if server.localStore != nil {
			available, reserve, probeErr := server.localStore.FreeSpace()
			if probeErr == nil {
				log.Printf("WARNING: verified read refused project=%q cause=%s artifact_bytes=%d available_bytes=%d reserve_bytes=%d", server.config.ProjectID, cause, entry.Size, available, reserve)
				return
			}
		}
	case errors.Is(err, artifact.ErrStagingQuota):
		cause = "staging-quota"
	}
	log.Printf("WARNING: verified read refused project=%q cause=%s artifact_bytes=%d", server.config.ProjectID, cause, entry.Size)
}

// stageVerifiedArtifactRead keeps cloud-backed bytes behind the HTTP boundary
// until their complete size and digest have been verified. Streaming an S3
// object directly cannot retract an HTTP 200 after a checksum failure at EOF.
func (server *Server) stageVerifiedArtifactRead(
	ctx context.Context,
	entry artifact.Entry,
	body io.Reader,
) (*os.File, func(), error) {
	if server.localStore == nil {
		return nil, nil, errors.New("Local Cache staging is unavailable")
	}
	lease, err := server.localStore.ReserveVerificationStaging(entry.Size)
	if err != nil {
		return nil, nil, err
	}
	temporary, err := os.CreateTemp(filepath.Join(server.config.DataDir, "staging"), "cloud-read-*")
	if err != nil {
		lease.Release()
		return nil, nil, err
	}
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
			lease.Release()
		})
	}
	succeeded := false
	defer func() {
		if !succeeded {
			cleanup()
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return nil, nil, err
	}

	hasher := sha256.New()
	destination := io.MultiWriter(server.localStore.SpaceCheckedWriter(ctx, temporary), hasher)
	written, err := io.CopyN(destination, body, entry.Size)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil, artifact.ErrCorrupt
		}
		return nil, nil, err
	}
	if written != entry.Size {
		return nil, nil, artifact.ErrCorrupt
	}
	// Read once beyond the signed length. Besides rejecting trailing bytes, this
	// forces digest-verifying cloud readers to report a checksum failure at EOF.
	extra, err := io.ReadAll(io.LimitReader(body, 1))
	if err != nil {
		return nil, nil, err
	}
	if len(extra) != 0 || hex.EncodeToString(hasher.Sum(nil)) != entry.Digest {
		return nil, nil, artifact.ErrCorrupt
	}
	if err := server.localStore.SyncStaged(ctx, temporary); err != nil {
		return nil, nil, err
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	succeeded = true
	return temporary, cleanup, nil
}
