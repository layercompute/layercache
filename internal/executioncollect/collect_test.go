package executioncollect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/execution"
)

func testFilesystemID(value string) string {
	digest := sha256.Sum256([]byte("private-test-key\x00" + value))
	return hex.EncodeToString(digest[:])
}

func TestCollectDeduplicatesFilesystemAliasesWithoutExportingPaths(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("filesystem observations unsupported on this platform")
	}
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	var keys []string
	sample := Collect([]Root{{root, "workspace"}, {alias, "localCache"}, {root, "staging"}, {root, "workspace"}, {filepath.Join(root, "missing"), "workspace"}, {root, "arbitrary-private-label"}}, func(key string) string { keys = append(keys, key); return testFilesystemID(key) })
	if len(sample.Filesystems) != 1 || len(keys) != 1 {
		t.Fatalf("aliases were not deduplicated: %+v, keys=%d", sample.Filesystems, len(keys))
	}
	fs := sample.Filesystems[0]
	if strings.Join(fs.Roles, ",") != "localCache,staging,workspace" {
		t.Fatalf("roles=%v", fs.Roles)
	}
	if fs.CapacityBytes == nil || fs.FreeBytes == nil || fs.AvailableBytes == nil || *fs.CapacityBytes < *fs.FreeBytes || *fs.FreeBytes < *fs.AvailableBytes {
		t.Fatalf("invalid filesystem capacities: %+v", fs)
	}
	data, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, alias, keys[0], "arbitrary-private-label"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("local value exported: %q", private)
		}
	}
	authority := execution.Authority{Context: *execution.ClientContext("github-id:123", "test-run"), Project: "test-project", RunID: "test-run"}
	request := execution.ObservationRequest{Version: execution.Version, Phase: "start", InstallationID: strings.Repeat("a", 64), Sample: sample}
	if err := request.Validate(authority); err != nil {
		t.Fatalf("collector produced invalid sample at %s: %v, %+v", time.Now().UTC(), err, sample)
	}
}

func TestCollectDoesNotEmitInvalidFilesystemIDs(t *testing.T) {
	for _, callback := range []func(string) string{nil, func(string) string { return "bad-id" }, func(string) string { return strings.Repeat("A", 64) }} {
		if sample := Collect([]Root{{t.TempDir(), "workspace"}}, callback); len(sample.Filesystems) != 0 {
			t.Fatalf("invalid ID emitted: %+v", sample.Filesystems)
		}
	}
}

func TestResourceNumbersRemainJSONSafe(t *testing.T) {
	for _, value := range []int64{-1, 0, execution.MaximumSafeInteger + 1} {
		if number(value) != nil {
			t.Fatalf("accepted invalid positive number %d", value)
		}
	}
	if bytesNumber(^uint64(0), 4096) != nil || bytesNumber(1, -1) != nil {
		t.Fatal("accepted overflowing filesystem capacity")
	}
	if value := bytesNumber(0, 4096); value == nil || *value != 0 {
		t.Fatal("zero free space must remain observable")
	}
}
