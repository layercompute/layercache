//go:build linux || darwin

package storageinspect

import (
	"fmt"
	"io/fs"
	"math"
	"syscall"

	"golang.org/x/sys/unix"
)

const platformSupported = true

func metadata(info fs.FileInfo) fileMetadata {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileMetadata{}
	}
	meta := fileMetadata{identity: fileIdentity{device: uint64(stat.Dev), inode: stat.Ino}, known: true}
	if stat.Blocks >= 0 && uint64(stat.Blocks) <= math.MaxUint64/512 {
		allocated := uint64(stat.Blocks) * 512
		meta.allocated = &allocated
	}
	return meta
}

func filesystem(path string) (Filesystem, error) {
	result := Filesystem{Type: "unknown"}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return result, err
	}
	result.Type = fmt.Sprintf("0x%x", uint64(stat.Type))
	blockSize := uint64(stat.Bsize)
	if blockSize == 0 || uint64(stat.Blocks) > math.MaxUint64/blockSize || uint64(stat.Bavail) > math.MaxUint64/blockSize {
		return result, fmt.Errorf("filesystem byte count unavailable or overflowing")
	}
	capacity := uint64(stat.Blocks) * blockSize
	available := uint64(stat.Bavail) * blockSize
	result.CapacityBytes, result.AvailableBytes = &capacity, &available
	return result, nil
}
