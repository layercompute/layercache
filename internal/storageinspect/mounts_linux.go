package storageinspect

import (
	"fmt"
	"os"
	"strings"
)

// Linux bind mounts can share the parent's device ID. The mount table also
// excludes those nested mounts, without probing or modifying any mount.
func mountPoints() (map[string]bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	return parseMountPoints(string(data))
}

func parseMountPoints(data string) (map[string]bool, error) {
	mounts := make(map[string]bool)
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(data, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[4] == "" || !strings.HasPrefix(fields[4], "/") {
			return nil, fmt.Errorf("invalid filesystem mount table")
		}
		mounts[unescape.Replace(fields[4])] = true
	}
	return mounts, nil
}
