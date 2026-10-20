//go:build linux || darwin

package access

import (
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func readSnapshotCredentialFile(path string) ([]byte, error) {
	directory, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrSnapshotCredentials
	}
	defer unix.Close(directory)
	var parent unix.Stat_t
	if unix.Fstat(directory, &parent) != nil || parent.Mode&0o077 != 0 || parent.Uid != uint32(os.Geteuid()) {
		return nil, ErrSnapshotCredentials
	}
	fd, err := unix.Openat(directory, filepath.Base(path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrSnapshotCredentials
	}
	file := os.NewFile(uintptr(fd), "snapshot-credentials")
	defer file.Close()
	var state unix.Stat_t
	if unix.Fstat(fd, &state) != nil || state.Mode&unix.S_IFMT != unix.S_IFREG || state.Nlink != 1 ||
		state.Mode&0o077 != 0 || state.Uid != uint32(os.Geteuid()) || state.Size <= 0 || state.Size > maximumSnapshotFileBytes {
		return nil, ErrSnapshotCredentials
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumSnapshotFileBytes+1))
	if err != nil || len(data) > maximumSnapshotFileBytes {
		return nil, ErrSnapshotCredentials
	}
	return data, nil
}
