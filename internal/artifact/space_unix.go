//go:build linux || darwin

package artifact

import (
	"math"
	"os"

	"golang.org/x/sys/unix"
)

func defaultProbeSpace(path string) (filesystemSpace, error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return filesystemSpace{}, err
	}
	return spaceFromStatfs(stats), nil
}

func probeOpenFilesystem(file *os.File) (filesystemSpace, error) {
	var stats unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &stats); err != nil {
		return filesystemSpace{}, err
	}
	return spaceFromStatfs(stats), nil
}

func spaceFromStatfs(stats unix.Statfs_t) filesystemSpace {
	blockSize := uint64(stats.Bsize)
	return filesystemSpace{
		availableBytes: filesystemBytes(stats.Bavail, blockSize),
		totalBytes:     filesystemBytes(stats.Blocks, blockSize),
		allocationUnit: filesystemBytes(1, blockSize),
	}
}

func filesystemBytes(blocks, blockSize uint64) int64 {
	if blockSize != 0 && blocks > uint64(math.MaxInt64)/blockSize {
		return math.MaxInt64
	}
	return int64(blocks * blockSize)
}
