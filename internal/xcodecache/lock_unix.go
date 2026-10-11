//go:build linux || darwin

package xcodecache

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

var errDirectoryInUse = errors.New("another Layer Cache owner is using the data directory")

// Share the runtime lock name: both owners can reconcile the same store.
func lockDirectory(root string) (*os.File, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, ".layercache-runtime.lock")
	fd, err := unix.Open(path, unix.O_CLOEXEC|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	var info unix.Stat_t
	if err = unix.Fstat(fd, &info); err == nil && info.Mode&unix.S_IFMT != unix.S_IFREG {
		err = errors.New("data-directory lock must be a regular file")
	}
	if err == nil {
		err = unix.Fchmod(fd, 0600)
	}
	if err == nil {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			err = fmt.Errorf("%w: %s", errDirectoryInUse, root)
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	// Close releases flock; unlinking would allow a second lock inode.
	return file, nil
}
