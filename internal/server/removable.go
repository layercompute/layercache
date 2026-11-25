package server

import (
	"context"
	"io"

	"github.com/layercache/layercache/internal/artifact"
)

// Keep unrelated integrations on the original store. All participating
// clients use the same quotas, verification, eviction, and physical pools.
type localPoolStore struct {
	cacheStore
	pools *artifact.Pools
}

func usesRemovable(key artifact.Key) bool {
	switch key.Integration {
	case "turbo", "gradle", "metro":
		return true
	default:
		return false
	}
}

func (s localPoolStore) Get(ctx context.Context, key artifact.Key) (artifact.Entry, io.ReadCloser, error) {
	if usesRemovable(key) {
		return s.pools.Get(ctx, key)
	}
	return s.cacheStore.Get(ctx, key)
}

func (s localPoolStore) Head(ctx context.Context, key artifact.Key) (artifact.Entry, error) {
	if usesRemovable(key) {
		return s.pools.Head(ctx, key)
	}
	return s.cacheStore.Head(ctx, key)
}

func (s localPoolStore) Put(ctx context.Context, key artifact.Key, m artifact.Metadata, body io.Reader) (artifact.Entry, bool, error) {
	return s.PutVerified(ctx, key, m, body, "")
}

func (s localPoolStore) PutVerified(ctx context.Context, key artifact.Key, m artifact.Metadata, body io.Reader, digest string) (artifact.Entry, bool, error) {
	if usesRemovable(key) {
		return s.pools.PutVerified(ctx, key, m, body, digest)
	}
	return s.cacheStore.PutVerified(ctx, key, m, body, digest)
}

func (s localPoolStore) Delete(ctx context.Context, key artifact.Key) error {
	if usesRemovable(key) {
		return s.pools.Delete(ctx, key)
	}
	return s.cacheStore.Delete(ctx, key)
}

func (s localPoolStore) Pin(ctx context.Context, namespace string, pin artifact.Pin) error {
	if namespace == teamUploadPinNamespace {
		return s.pools.Pin(ctx, namespace, pin)
	}
	return s.cacheStore.Pin(ctx, namespace, pin)
}

func (s localPoolStore) Unpin(ctx context.Context, namespace, owner string) error {
	if namespace == teamUploadPinNamespace {
		return s.pools.Unpin(ctx, namespace, owner)
	}
	return s.cacheStore.Unpin(ctx, namespace, owner)
}

func (server *Server) artifactLimit(ctx context.Context) (int64, error) {
	if server.pools != nil {
		return server.pools.MaxBytes(), nil
	}
	return server.cacheLimit(ctx)
}

func (server *Server) prepareRemovableStore(ctx context.Context, store *artifact.Store) error {
	if server.uploads == nil {
		return nil
	}
	pins, err := server.turboUploadPins(ctx)
	if err != nil {
		return err
	}
	_, err = store.ReconcilePins(ctx, teamUploadPinNamespace, pins)
	return err
}

func (server *Server) getTurboUpload(ctx context.Context, key artifact.Key, digest string, size int64) (artifact.Entry, io.ReadCloser, error) {
	if server.pools != nil {
		return server.pools.GetExact(ctx, key, digest, size)
	}
	return server.store.Get(ctx, key)
}
