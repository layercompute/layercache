//go:build darwin || linux

package artifact

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/layercache/layercache/internal/removable"
)

// Pools owns the optional removable store. The caller owns Internal.
type Pools struct {
	Internal *Store
	config   removable.Config
	mu       sync.Mutex
	active   *poolMount
	closed   bool
	prepare  func(context.Context, *Store) error
	lookups  struct {
		internalHits, attachedHits, misses, errors atomic.Uint64
	}
}

type poolMount struct {
	store      *Store
	volume     *removable.Volume
	users      int
	idle       *time.Timer
	generation uint64
	invalid    bool
}

func NewPools(internal *Store, cfg removable.Config, prepare func(context.Context, *Store) error) *Pools {
	return &Pools{Internal: internal, config: cfg, prepare: prepare}
}

func (p *Pools) acquire(ctx context.Context) (*Store, bool, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	nothing := func() {}
	if p.closed || ctx.Err() != nil {
		return nil, false, nothing
	}
	m := p.active
	if m != nil && m.store.files != nil && !m.volume.Attached(p.config) {
		m.invalid = true
	}
	if m != nil && m.invalid {
		if m.users != 0 {
			return nil, p.config.WritesOnly && removable.Present(ctx, p.config, p.Internal.root), nothing
		}
		_ = p.closeMount()
		m = nil
	}
	if m == nil || m.store.files == nil {
		volume, present, err := removable.Open(ctx, p.config, p.Internal.root)
		if err != nil {
			if m != nil {
				_ = p.closeMount()
			}
			return nil, present, nothing
		}
		if m != nil && m.volume.SameMount(volume) {
			m.volume = volume
			m.store.files = volume.Root
		} else {
			if m != nil {
				_ = p.closeMount()
			}
			m, err = p.openMount(ctx, volume)
			if err != nil {
				return nil, true, nothing
			}
			p.active = m
		}
	}
	if m.idle != nil {
		m.idle.Stop()
		m.idle = nil
	}
	m.generation++
	m.users++
	return m.store, true, func() { p.release(m) }
}

func (p *Pools) openMount(ctx context.Context, volume *removable.Volume) (*poolMount, error) {
	m := &poolMount{volume: volume}
	store, err := openStore(ctx, volume.Root, p.config.MetadataDir(p.Internal.root), p.config.MaxBytes,
		p.Internal.minFreeBytes, func(_ string) (filesystemSpace, error) {
			file, err := m.volume.Root.Open(".")
			if err != nil {
				return filesystemSpace{}, err
			}
			defer file.Close()
			return probeOpenFilesystem(file)
		})
	if err != nil {
		_ = volume.Close()
		return nil, err
	}
	m.store = store
	err = store.SetEvictionPolicy(p.Internal.evictionPolicy)
	if err == nil && p.prepare != nil {
		err = p.prepare(ctx, store)
	}
	if err != nil {
		_ = store.Close()
		_ = volume.ReleaseLock()
		return nil, err
	}
	return m, nil
}

func (p *Pools) release(m *poolMount) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m.users--
	if m.users != 0 {
		return
	}
	if p.closed || m.invalid {
		_ = p.closeMount()
		return
	}
	// Permit normal eject between requests. Keep the internal SQLite handle,
	// but release every descriptor on the mounted volume before going idle.
	generation := m.generation
	m.idle = time.AfterFunc(time.Second, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.active == m && m.generation == generation && m.users == 0 {
			_ = m.volume.Close()
			m.store.files = nil
			m.idle = nil
		}
	})
}

func (p *Pools) closeMount() error {
	m := p.active
	p.active = nil
	if m.idle != nil {
		m.idle.Stop()
	}
	var lockErr error
	if m.store.files != nil {
		lockErr = m.volume.ReleaseLock()
	}
	return errors.Join(m.store.Close(), lockErr)
}

func (p *Pools) isAttached() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active.volume.Attached(p.config) {
		p.active.invalid = true
	}
	return !p.active.invalid
}

func (p *Pools) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.active != nil && p.active.users == 0 {
		return p.closeMount()
	}
	return nil
}

func (p *Pools) MaxBytes() int64 { return max(p.Internal.MaxBytes(), p.config.MaxBytes) }

func (p *Pools) FreeSpace() (int64, int64, error) {
	return p.Internal.FreeSpace()
}

type PoolStatus struct {
	Attached   bool            `json:"attached"`
	Only       bool            `json:"onlyWhileAttached"`
	WritesOnly bool            `json:"writesOnlyWhileAttached,omitempty"`
	MaxBytes   int64           `json:"maxBytes"`
	Stats      *Stats          `json:"stats,omitempty"`
	Lookups    PoolLookupStats `json:"lookups"`
}

// PoolLookupStats counts artifact lookups from this process, by actual location.
// Reopens of a known digest and metadata-only probes are excluded.
type PoolLookupStats struct {
	InternalHits uint64 `json:"internalHits"`
	AttachedHits uint64 `json:"attachedHits"`
	Misses       uint64 `json:"misses"`
	Errors       uint64 `json:"errors"`
}

func (p *Pools) Status(ctx context.Context) PoolStatus {
	ssd, present, release := p.acquire(ctx)
	defer release()
	result := PoolStatus{Attached: ssd != nil || p.config.WritesOnly && present, Only: p.config.Only, WritesOnly: p.config.WritesOnly, MaxBytes: p.config.MaxBytes}
	result.Lookups = PoolLookupStats{
		InternalHits: p.lookups.internalHits.Load(), AttachedHits: p.lookups.attachedHits.Load(),
		Misses: p.lookups.misses.Load(), Errors: p.lookups.errors.Load(),
	}
	if ssd != nil {
		if stats, err := ssd.Stats(ctx); err == nil {
			result.Stats = &stats
		}
	}
	return result
}

func (p *Pools) candidates(ssd *Store) []*Store {
	if ssd == nil {
		return []*Store{p.Internal}
	}
	if p.config.Only {
		return []*Store{ssd}
	}
	return []*Store{p.Internal, ssd}
}

type poolReader struct {
	io.ReadCloser
	release func()
}

func (r *poolReader) Close() error {
	err := r.ReadCloser.Close()
	if r.release != nil {
		r.release()
		r.release = nil
	}
	return err
}

func (p *Pools) Get(ctx context.Context, key Key) (Entry, io.ReadCloser, error) {
	entry, body, err := p.get(ctx, key, "", 0)
	switch {
	case err == nil && entry.poolStore == p.Internal:
		p.lookups.internalHits.Add(1)
	case err == nil:
		p.lookups.attachedHits.Add(1)
	case errors.Is(err, ErrNotFound):
		p.lookups.misses.Add(1)
	case !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded):
		p.lookups.errors.Add(1)
	}
	return entry, body, err
}

// GetExact can serve an already-authorized queued publication across policy
// changes. It must open the queued digest, even if another replica won later.
func (p *Pools) GetExact(ctx context.Context, key Key, digest string, size int64) (Entry, io.ReadCloser, error) {
	return p.get(ctx, key, digest, size)
}

func (p *Pools) get(ctx context.Context, key Key, digest string, size int64) (Entry, io.ReadCloser, error) {
	ssd, _, release := p.acquire(ctx)
	defer func() { release() }()
	stores := p.candidates(ssd)
	if digest != "" && ssd != nil {
		stores = []*Store{ssd, p.Internal}
	}
	for _, store := range stores {
		entry, body, err := store.Get(ctx, key)
		if store == ssd && !p.isAttached() {
			if body != nil {
				body.Close()
			}
			err = ErrNotFound
		}
		if err == nil {
			if digest != "" && (entry.Digest != digest || entry.Size != size) {
				body.Close()
				continue
			}
			if store == ssd {
				body = &poolReader{ReadCloser: body, release: release}
				release = func() {}
			}
			entry.poolStore = store
			return entry, body, nil
		}
		if ctx.Err() != nil {
			return Entry{}, nil, ctx.Err()
		}
		if errors.Is(err, ErrCorrupt) {
			_ = store.Delete(context.WithoutCancel(ctx), key)
		} else if store != ssd && !errors.Is(err, ErrNotFound) {
			return Entry{}, nil, err
		}
	}
	if ssd != nil && p.config.Only && digest == "" && !p.isAttached() {
		entry, body, err := p.Internal.Get(ctx, key)
		entry.poolStore = p.Internal
		return entry, body, err
	}
	return Entry{}, nil, ErrNotFound
}

func (p *Pools) Head(ctx context.Context, key Key) (Entry, error) {
	ssd, _, release := p.acquire(ctx)
	defer release()
	for _, store := range p.candidates(ssd) {
		entry, err := store.Head(ctx, key)
		if store == ssd && !p.isAttached() {
			return Entry{}, ErrNotFound
		}
		if err == nil || ctx.Err() != nil || store != ssd && !errors.Is(err, ErrNotFound) {
			return entry, err
		}
	}
	return Entry{}, ErrNotFound
}

type sizeHintKey struct{}

func WithSizeHint(ctx context.Context, bytes int64) context.Context {
	return context.WithValue(ctx, sizeHintKey{}, bytes)
}

type observedReader struct {
	io.Reader
	started bool
}

func (r *observedReader) Read(buf []byte) (int, error) {
	r.started = true
	return r.Reader.Read(buf)
}

func (p *Pools) Put(ctx context.Context, key Key, metadata Metadata, body io.Reader) (Entry, bool, error) {
	return p.PutVerified(ctx, key, metadata, body, "")
}

func (p *Pools) PutVerified(ctx context.Context, key Key, metadata Metadata, body io.Reader, digest string) (Entry, bool, error) {
	ssd, present, release := p.acquire(ctx)
	defer release()
	if ssd == nil {
		if p.config.WritesOnly && present {
			return Entry{}, false, ErrQuota
		}
		return p.Internal.PutVerified(ctx, key, metadata, body, digest)
	}
	// Preserve the readable first writer when both stores are in use.
	ssdDigest := digest
	size, _ := ctx.Value(sizeHintKey{}).(int64)
	if p.config.WritesOnly {
		entry, err := p.Internal.entry(ctx, key, false)
		if err == nil {
			if digest != "" && strings.TrimPrefix(digest, "sha256:") != entry.Digest {
				return Entry{}, false, ErrConflict
			}
			ssdDigest = entry.Digest
		} else if !errors.Is(err, ErrNotFound) {
			return Entry{}, false, err
		}
	} else if !p.config.Only {
		if _, err := p.Internal.Head(ctx, key); err == nil {
			return p.Internal.PutVerified(ctx, key, metadata, body, digest)
		}
	}
	requireSSD := p.config.Only || p.config.WritesOnly
	if available, reserve, err := ssd.FreeSpace(); err != nil || size > ssd.MaxBytes() || wouldViolateReserve(available, reserve, max(0, size)) {
		if p.config.Only || p.config.WritesOnly && (p.isAttached() || removable.Present(ctx, p.config, p.Internal.root)) {
			return Entry{}, false, ErrQuota
		}
		return p.Internal.PutVerified(ctx, key, metadata, body, digest)
	}
	var rewind func() bool
	if seeker, ok := body.(io.Seeker); ok {
		if offset, err := seeker.Seek(0, io.SeekCurrent); err == nil {
			rewind = func() bool {
				_, err := seeker.Seek(offset, io.SeekStart)
				return err == nil
			}
		}
	}
	reader := &observedReader{Reader: body}
	entry, created, err := ssd.PutVerified(ctx, key, metadata, reader, ssdDigest)
	if digest == "" && ssdDigest != "" && errors.Is(err, ErrCorrupt) {
		return Entry{}, false, ErrConflict
	}
	detached := !p.isAttached()
	if (!detached && err == nil) || ctx.Err() != nil || errors.Is(err, ErrConflict) || errors.Is(err, ErrCorrupt) {
		return entry, created, err
	}
	if (detached || !requireSSD && errors.Is(err, ErrQuota)) && (!reader.started || rewind != nil && rewind()) &&
		(!p.config.WritesOnly || !removable.Present(ctx, p.config, p.Internal.root)) {
		return p.Internal.PutVerified(ctx, key, metadata, body, digest)
	}
	if detached {
		// Native uploads can retry or discard a caching failure. Replaying a
		// consumed stream here would publish a truncated archive.
		return Entry{}, false, ErrQuota
	}
	return entry, created, err
}

func (p *Pools) Delete(ctx context.Context, key Key) error {
	ssd, _, release := p.acquire(ctx)
	defer release()
	for _, store := range p.candidates(ssd) {
		if _, err := store.Head(ctx, key); errors.Is(err, ErrNotFound) {
			continue
		}
		return store.Delete(ctx, key)
	}
	return nil
}

func (p *Pools) Pin(ctx context.Context, namespace string, pin Pin) error {
	ssd, _, release := p.acquire(ctx)
	defer release()
	for _, store := range []*Store{ssd, p.Internal} {
		if store == nil {
			continue
		}
		entry, err := store.Head(ctx, pin.Key)
		if err == nil && entry.Digest == pin.Digest && entry.Size == pin.Size {
			return store.Pin(ctx, namespace, pin)
		}
	}
	return ErrNotFound
}

func (p *Pools) Unpin(ctx context.Context, namespace, owner string) error {
	ssd, _, release := p.acquire(ctx)
	defer release()
	err := p.Internal.Unpin(ctx, namespace, owner)
	if ssd != nil {
		err = errors.Join(err, ssd.Unpin(ctx, namespace, owner))
	}
	return err
}

func (p *Pools) RetainForRead(ctx context.Context, namespace, owner string, entries []Entry, duration time.Duration) error {
	ssd, _, release := p.acquire(ctx)
	defer release()
	var internalEntries, ssdEntries []Entry
	for _, entry := range entries {
		switch {
		case entry.poolStore == p.Internal && (ssd == nil || !p.config.Only):
			internalEntries = append(internalEntries, entry)
		case ssd != nil && entry.poolStore == ssd:
			ssdEntries = append(ssdEntries, entry)
		default:
			return ErrNotFound
		}
	}
	if len(internalEntries) != 0 {
		if err := p.Internal.RetainForRead(ctx, namespace, owner, internalEntries, duration); err != nil {
			return err
		}
	}
	if len(ssdEntries) != 0 {
		// Each cohort is pinned before the action is reported as a hit. If a
		// second cohort fails admission, earlier pins expire normally.
		if err := ssd.RetainForRead(ctx, namespace, owner, ssdEntries, duration); err != nil {
			return err
		}
		if !p.isAttached() {
			return ErrNotFound
		}
	}
	return nil
}
