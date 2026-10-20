// Package executioncollect samples the reporting process without exporting
// filesystem paths or operating-system machine identifiers.
package executioncollect

import (
	"encoding/hex"
	"runtime"
	"sort"

	"github.com/layercache/layercache/internal/execution"
)

type Root struct {
	Path string
	Role string
}

// Collect reports only explicit roots. Filesystem aliases share one opaque ID;
// a missing root or unsupported platform leaves that information unavailable.
func Collect(roots []Root, filesystemID func(string) string) execution.Sample {
	sample := execution.Sample{
		OS: normalizedOS(), Arch: normalizedArch(),
		CPU:         execution.CPU{VisibleLogical: number(int64(runtime.NumCPU())), LimitKnowledge: "unknown"},
		Memory:      execution.Memory{LimitKnowledge: "unknown"},
		Filesystems: []execution.Filesystem{},
	}
	collectPlatform(&sample.CPU, &sample.Memory)
	if filesystemID == nil {
		return sample
	}
	byDevice := make(map[string]int)
	seenIDs := make(map[string]bool)
	for _, root := range roots {
		if root.Path == "" || !validRole(root.Role) {
			continue
		}
		key, fs, ok := filesystem(root.Path)
		if !ok {
			continue
		}
		if index, exists := byDevice[key]; exists {
			roles := sample.Filesystems[index].Roles
			found := false
			for _, role := range roles {
				found = found || role == root.Role
			}
			if !found {
				sample.Filesystems[index].Roles = append(roles, root.Role)
				sort.Strings(sample.Filesystems[index].Roles)
			}
			continue
		}
		if len(sample.Filesystems) == execution.MaximumFilesystems {
			continue
		}
		id := filesystemID(key)
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != id || seenIDs[id] {
			continue
		}
		fs.ID, fs.Roles = id, []string{root.Role}
		byDevice[key] = len(sample.Filesystems)
		seenIDs[id] = true
		sample.Filesystems = append(sample.Filesystems, fs)
	}
	return sample
}

func validRole(role string) bool {
	return role == "workspace" || role == "localCache" || role == "staging"
}

func number(value int64) *int64 {
	if value <= 0 || value > execution.MaximumSafeInteger {
		return nil
	}
	return &value
}

func bytesNumber(blocks uint64, blockSize int64) *int64 {
	if blockSize <= 0 || blocks > uint64(execution.MaximumSafeInteger)/uint64(blockSize) {
		return nil
	}
	value := int64(blocks * uint64(blockSize))
	return &value
}

func normalizedOS() string {
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		return runtime.GOOS
	default:
		return "other"
	}
}

func normalizedArch() string {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH
	default:
		return "other"
	}
}
