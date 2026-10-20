//go:build linux || darwin

package executioncollect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

func loadIdentitySeed(ctx context.Context, configurationDirectory string) ([32]byte, error) {
	var empty [32]byte
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := os.MkdirAll(configurationDirectory, 0o700); err != nil {
		return empty, errors.New("client identity directory unavailable")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	root, err := unix.Open(configurationDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return empty, errors.New("client identity directory unavailable")
	}
	defer unix.Close(root)
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	layercache, err := privateIdentityDirectory(root, "layercache")
	if err != nil {
		return empty, err
	}
	defer unix.Close(layercache)
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	directory, err := privateIdentityDirectory(layercache, "client-identity")
	if err != nil {
		return empty, err
	}
	defer unix.Close(directory)
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	lock, err := openIdentityLock(ctx, directory, runtime.GOOS, unix.Openat)
	if err != nil {
		return empty, err
	}
	defer unix.Close(lock)
	if err := privateIdentityFile(lock, -1); err != nil {
		return empty, err
	}
	for {
		err = identityLockSyscall(ctx, "flock", func() error {
			return unix.Flock(lock, unix.LOCK_EX|unix.LOCK_NB)
		})
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return empty, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return empty, ctx.Err()
		case <-timer.C:
		}
	}
	defer unix.Flock(lock, unix.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if seed, readErr := readIdentitySeed(directory); !errors.Is(readErr, os.ErrNotExist) {
		return seed, readErr
	}
	var seed [32]byte
	if _, err := io.ReadFull(rand.Reader, seed[:]); err != nil {
		return empty, errors.New("client identity randomness unavailable")
	}
	var suffix [8]byte
	if _, err := io.ReadFull(rand.Reader, suffix[:]); err != nil {
		return empty, errors.New("client identity randomness unavailable")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	temporary := ".seed-" + hex.EncodeToString(suffix[:])
	fd, err := unix.Openat(directory, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return empty, errors.New("client identity could not be created")
	}
	defer unix.Unlinkat(directory, temporary, 0)
	file := os.NewFile(uintptr(fd), "client-identity-seed")
	if _, err = file.Write(seed[:]); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return empty, errors.New("client identity could not be persisted")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	// All clients hold this directory's exclusive lock. Rename commits a single
	// link atomically, so interruption cannot leave a two-link seed that fails
	// subsequent private-file validation.
	if err := commitIdentitySeed(directory, temporary); err != nil {
		return empty, errors.New("client identity could not be committed")
	}
	if err := unix.Fsync(directory); err != nil {
		return empty, errors.New("client identity could not be synced")
	}
	return seed, nil
}

func openIdentityLock(ctx context.Context, directory int, platform string, openat func(int, string, int, uint32) (int, error)) (int, error) {
	for attempt := 1; ; attempt++ {
		lock := -1
		err := identityLockSyscall(ctx, "openat", func() error {
			var openErr error
			lock, openErr = openat(directory, "identity.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			return openErr
		})
		if err == nil {
			return lock, nil
		}
		// Darwin can return ENOENT during concurrent first creation of this
		// leaf. Retry only that operation against the same validated parent;
		// persistent missing paths and every other error still fail promptly.
		// This also matches https://github.com/Gentleman-Programming/gentle-ai/pull/4277.
		if platform != "darwin" || !errors.Is(err, unix.ENOENT) || attempt >= 3 {
			return -1, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return -1, ctx.Err()
		case <-timer.C:
		}
	}
}

func identityLockSyscall(ctx context.Context, operation string, call func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := call()
		// Like os.OpenFile and cmd/go's filelock implementation, retry only
		// interrupted syscalls. Keep other errnos and their stage available
		// for diagnosis without including identity paths or seed contents.
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("client identity lock %s: %w", operation, err)
		}
		return nil
	}
}

func commitIdentitySeed(directory int, temporary string) error {
	return unix.Renameat(directory, temporary, directory, "seed")
}

func privateIdentityDirectory(parent int, name string) (int, error) {
	createErr := unix.Mkdirat(parent, name, 0o700)
	if createErr != nil && !errors.Is(createErr, unix.EEXIST) {
		return -1, errors.New("client identity directory unavailable")
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, errors.New("client identity directory unavailable")
	}
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil || info.Uid != uint32(os.Geteuid()) || info.Mode&0o777 != 0o700 {
		unix.Close(fd)
		return -1, errors.New("client identity directory is not private")
	}
	if createErr == nil {
		if err := unix.Fsync(parent); err != nil {
			unix.Close(fd)
			return -1, errors.New("client identity directory could not be synced")
		}
	}
	return fd, nil
}

func privateIdentityFile(fd int, expectedSize int64) error {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != uint32(os.Geteuid()) || info.Mode&0o777 != 0o600 || info.Nlink != 1 || expectedSize >= 0 && info.Size != expectedSize {
		return errors.New("client identity file is not private")
	}
	return nil
}

func readIdentitySeed(directory int) ([32]byte, error) {
	var seed [32]byte
	fd, err := unix.Openat(directory, "seed", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return seed, os.ErrNotExist
		}
		return seed, errors.New("client identity seed unavailable")
	}
	file := os.NewFile(uintptr(fd), "client-identity-seed")
	defer file.Close()
	if err := privateIdentityFile(fd, int64(len(seed))); err != nil {
		return seed, err
	}
	if _, err := io.ReadFull(file, seed[:]); err != nil {
		return seed, errors.New("client identity seed unavailable")
	}
	return seed, nil
}
