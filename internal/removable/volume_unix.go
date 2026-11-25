//go:build darwin || linux

package removable

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

var errMountUnavailable = errors.New("enrolled cache filesystem is unavailable")

// Volume uses a directory descriptor. Renaming or unplugging the mount can
// invalidate access, but cannot redirect operations to a replacement path.
type Volume struct {
	Root                 *os.Root
	lock                 *os.File
	mountInfo, cacheInfo os.FileInfo
}

const mountReadOnly = 1

func mount(cfg Config, internalDir string) (*os.Root, error) {
	leaf, err := os.Lstat(cfg.MountPath)
	if err != nil || !leaf.IsDir() {
		return nil, errMountUnavailable
	}
	parent, err := os.Stat(filepath.Dir(cfg.MountPath))
	if err != nil {
		return nil, err
	}
	internal, err := os.Stat(internalDir)
	if err != nil {
		return nil, err
	}
	dev := leaf.Sys().(*syscall.Stat_t).Dev
	if dev == parent.Sys().(*syscall.Stat_t).Dev || dev == internal.Sys().(*syscall.Stat_t).Dev {
		return nil, errMountUnavailable
	}
	root, err := os.OpenRoot(cfg.MountPath)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil || !os.SameFile(leaf, info) {
		root.Close()
		return nil, errMountUnavailable
	}
	return root, nil
}

func cacheRoot(mounted *os.Root, name string) (*os.Root, error) {
	leaf, err := mounted.Lstat(name)
	if err != nil || !private(leaf, 0700) || !leaf.IsDir() {
		return nil, errMountUnavailable
	}
	root, err := mounted.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	mountedInfo, mountErr := mounted.Stat(".")
	if err != nil || mountErr != nil || !os.SameFile(leaf, info) ||
		info.Sys().(*syscall.Stat_t).Dev != mountedInfo.Sys().(*syscall.Stat_t).Dev {
		root.Close()
		return nil, errMountUnavailable
	}
	return root, nil
}

func private(info os.FileInfo, mode os.FileMode) bool {
	return info != nil && info.Mode().Perm() == mode && info.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid())
}

// Enroll requires a new dedicated directory. A companion marker on the internal
// drive binds the on-disk payload to the cache which owns its SQLite metadata.
func Enroll(ctx context.Context, cfg *Config, internalDir string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	mounted, err := mount(*cfg, internalDir)
	if err != nil {
		return err
	}
	defer mounted.Close()
	cfg.VolumeUUID, err = volumeUUID(ctx, cfg.MountPath)
	if err != nil {
		return err
	}
	current, err := os.Lstat(cfg.MountPath)
	opened, openErr := mounted.Stat(".")
	if err != nil || openErr != nil || !os.SameFile(current, opened) {
		return errMountUnavailable
	}
	if err := mounted.Mkdir(cfg.Directory, 0700); err != nil {
		return err
	}
	root, err := cacheRoot(mounted, cfg.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	internal := cfg.MetadataDir(internalDir)
	if err := os.MkdirAll(filepath.Dir(internal), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(internal, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(internal, markerName), []byte(cfg.ID), 0600); err != nil {
		return err
	}
	file, err := root.OpenFile(markerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.WriteString(file, cfg.ID)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func readMarker(root *os.Root, id string) bool {
	file, err := root.OpenFile(markerName, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !private(info, 0600) || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(len(id)+1)))
	return err == nil && string(data) == id
}

func Open(ctx context.Context, cfg Config, internalDir string) (*Volume, error) {
	mounted, err := mount(cfg, internalDir)
	if err != nil {
		return nil, err
	}
	defer mounted.Close()
	root, err := cacheRoot(mounted, cfg.Directory)
	if err != nil {
		return nil, err
	}
	volume := &Volume{Root: root}
	fail := func(err error) (*Volume, error) {
		root.Close()
		return nil, err
	}
	internal, err := os.OpenRoot(cfg.MetadataDir(internalDir))
	if err != nil {
		return fail(err)
	}
	defer internal.Close()
	volume.mountInfo, err = mounted.Stat(".")
	if err != nil {
		return fail(err)
	}
	volume.cacheInfo, err = root.Stat(".")
	if err != nil {
		return fail(err)
	}
	if !readMarker(root, cfg.ID) || !readMarker(internal, cfg.ID) {
		return fail(errMountUnavailable)
	}
	uuid, err := volumeUUID(ctx, cfg.MountPath)
	if err != nil || uuid != cfg.VolumeUUID || !volume.Attached(cfg) {
		return fail(errMountUnavailable)
	}
	dir, err := root.Open(".")
	if err != nil {
		return fail(err)
	}
	defer dir.Close()
	var stats unix.Statfs_t
	if err := unix.Fstatfs(int(dir.Fd()), &stats); err != nil || stats.Flags&mountReadOnly != 0 {
		return fail(errMountUnavailable)
	}
	fd, err := unix.Openat(int(dir.Fd()), ".layercache-runtime.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fail(err)
	}
	lock := os.NewFile(uintptr(fd), ".layercache-runtime.lock")
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || !private(info, 0600) || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		lock.Close()
		return fail(errMountUnavailable)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return fail(err)
	}
	volume.lock = lock
	return volume, nil
}

// Attached validates the named mount and cache directory against their open
// counterparts. A new filesystem at this path needs independent enrollment.
func (v *Volume) Attached(cfg Config) bool {
	mounted, err := os.Lstat(cfg.MountPath)
	if err != nil || !mounted.IsDir() || !os.SameFile(mounted, v.mountInfo) {
		return false
	}
	current, err := os.Lstat(filepath.Join(cfg.MountPath, cfg.Directory))
	return err == nil && current.IsDir() && private(current, 0700) && os.SameFile(current, v.cacheInfo)
}

func (v *Volume) SameMount(other *Volume) bool {
	return os.SameFile(v.mountInfo, other.mountInfo) && os.SameFile(v.cacheInfo, other.cacheInfo)
}

func (v *Volume) ReleaseLock() error { return v.lock.Close() }

func (v *Volume) Close() error {
	return errors.Join(v.Root.Close(), v.ReleaseLock())
}
