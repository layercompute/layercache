//go:build linux || darwin

package xcodecache

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"runtime"
	"time"
)

var errDirectoryInUse = errors.New("another Layer Cache owner is using the data directory")
var errUnsafePath = errors.New("Xcode cache path is not private")

func privateDirectory(path string, create bool) (*os.File, error) {
	if create {
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		// An unavailable directory does not establish an unsafe path. Check the
		// leaf without opening it so aliases and known ownership/mode violations
		// remain fatal even when the open itself fails, for example with EACCES.
		var info unix.Stat_t
		if statErr := unix.Lstat(path, &info); statErr == nil &&
			(info.Mode&unix.S_IFMT == unix.S_IFLNK || info.Mode&unix.S_IFMT == unix.S_IFDIR &&
				(info.Uid != uint32(os.Geteuid()) || info.Mode&0777 != 0700)) {
			return nil, fmt.Errorf("%w: directory must be current-owned with mode 0700 and not a symlink", errUnsafePath)
		}
		return nil, fmt.Errorf("private directory unavailable: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		file.Close()
		return nil, fmt.Errorf("inspect private directory: %w", err)
	}
	if info.Uid != uint32(os.Geteuid()) || info.Mode&0777 != 0700 {
		file.Close()
		return nil, fmt.Errorf("%w: directory must be current-owned with mode 0700", errUnsafePath)
	}
	return file, nil
}

func privateRegularFile(fd int) error {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != uint32(os.Geteuid()) || info.Mode&0777 != 0600 || info.Nlink != 1 {
		return fmt.Errorf("%w: file must be current-owned, single-link and mode 0600", errUnsafePath)
	}
	return nil
}

func lockPrivateFile(directory *os.File, name string) (*os.File, error) {
	fd, err := openLockLeaf(int(directory.Fd()), name)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: lock leaf is a symlink", errUnsafePath)
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if err = privateRegularFile(fd); err == nil {
		for {
			err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
			if !errors.Is(err, unix.EINTR) {
				break
			}
		}
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			err = errDirectoryInUse
		}
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func openLockLeaf(directory int, name string) (int, error) {
	for attempt := 0; ; {
		fd, err := unix.Openat(directory, name, unix.O_CLOEXEC|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_RDWR, 0600)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		attempt++
		// The same Darwin first-create ENOENT observed by installation identity
		// can affect concurrent lock creators. Never retry other filesystem errors.
		if runtime.GOOS != "darwin" || !errors.Is(err, unix.ENOENT) || attempt >= 3 {
			return fd, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Share the runtime lock name: both owners can reconcile the same store.
func lockDirectory(root string) (*os.File, error) {
	directory, err := privateDirectory(root, true)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	file, err := lockPrivateFile(directory, ".layercache-runtime.lock")
	if errors.Is(err, errDirectoryInUse) {
		return nil, fmt.Errorf("%w: %s", err, root)
	}
	// Close releases flock; unlinking would allow a second lock inode.
	return file, err
}
