//go:build !linux && !darwin

package executioncollect

import "github.com/layercache/layercache/internal/execution"

func collectPlatform(_ *execution.CPU, _ *execution.Memory) {}
func filesystem(string) (string, execution.Filesystem, bool) {
	return "", execution.Filesystem{}, false
}
