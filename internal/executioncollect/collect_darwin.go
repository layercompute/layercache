//go:build darwin

package executioncollect

import (
	"fmt"
	"os"
	"syscall"

	"github.com/layercache/layercache/internal/execution"
	"golang.org/x/sys/unix"
)

func collectPlatform(_ *execution.CPU, memory *execution.Memory) {
	if bytes, err := unix.SysctlUint64("hw.memsize"); err == nil && bytes <= uint64(execution.MaximumSafeInteger) {
		memory.VisibleTotalBytes = number(int64(bytes))
	}
}

func filesystem(path string) (string, execution.Filesystem, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", execution.Filesystem{}, false
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", execution.Filesystem{}, false
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return "", execution.Filesystem{}, false
	}
	blockSize := int64(fs.Bsize)
	result := execution.Filesystem{CapacityBytes: bytesNumber(fs.Blocks, blockSize), FreeBytes: bytesNumber(fs.Bfree, blockSize), AvailableBytes: bytesNumber(fs.Bavail, blockSize)}
	if result.CapacityBytes == nil || *result.CapacityBytes == 0 || result.FreeBytes == nil || result.AvailableBytes == nil || *result.FreeBytes > *result.CapacityBytes || *result.AvailableBytes > *result.FreeBytes {
		return "", execution.Filesystem{}, false
	}
	return fmt.Sprintf("darwin-device:%d", status.Dev), result, true
}
