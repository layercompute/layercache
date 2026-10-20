//go:build linux || darwin

package portal

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func passwordDirectory(path string) (int, error) {
	fd, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Mode&077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		unix.Close(fd)
		return -1, errors.New("password directory must be private and owned by the service user")
	}
	return fd, nil
}
func readPasswordAccountsFile(path string) ([]byte, error) {
	dir, err := passwordDirectory(path)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, filepath.Base(path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Size > maximumPasswordFileBytes {
		return nil, errors.New("password accounts must be a bounded private single-link file owned by the service user")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumPasswordFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximumPasswordFileBytes {
		return nil, errors.New("password account file exceeds limit")
	}
	return data, nil
}
func writePasswordAccountsFile(path string, data []byte) error {
	if len(data) > maximumPasswordFileBytes {
		return errors.New("password account file exceeds limit")
	}
	dir, err := passwordDirectory(path)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	name := ".password-" + randomID("")
	fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	defer unix.Unlinkat(dir, name, 0)
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = unix.Renameat(dir, name, dir, filepath.Base(path)); err != nil {
		return err
	}
	return unix.Fsync(dir)
}
