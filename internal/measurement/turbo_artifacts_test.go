package measurement_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/measurement"
)

func TestSuccessfulTurboArtifactsExcludeFailedUncacheableAndUnexecutedTasks(t *testing.T) {
	start := time.Now().UTC()
	task := func(id, hash, status string, cache bool, exit any) map[string]any {
		return map[string]any{
			"taskId": id, "hash": hash,
			"cache":                  map[string]any{"status": status},
			"resolvedTaskDefinition": map[string]any{"cache": cache},
			"execution":              map[string]any{"startTime": start.UnixMilli(), "endTime": start.Add(time.Second).UnixMilli(), "exitCode": exit},
		}
	}
	summary, err := json.Marshal(map[string]any{
		"id": "current", "version": "1",
		"execution": map[string]any{"startTime": start.UnixMilli(), "endTime": start.Add(time.Second).UnixMilli()},
		"tasks": []any{
			task("build", "bbb", "MISS", true, 0),
			task("unit", "aaa", "HIT", true, 0),
			task("duplicate", "aaa", "MISS", true, 0),
			task("failed", "failed", "MISS", true, 7),
			task("live", "live", "MISS", false, 0),
			task("not-executed", "not-executed", "MISS", true, nil),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := measurement.SuccessfulTurboArtifactHashes([][]byte{summary}, start, start.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"aaa", "bbb"}) {
		t.Fatalf("publishable hashes = %v", got)
	}
	if _, err := measurement.SuccessfulTurboArtifactHashes([][]byte{summary}, start.Add(time.Hour), start.Add(2*time.Hour)); err == nil {
		t.Fatal("a stale summary was accepted as the current invocation")
	}
	if _, err := measurement.SuccessfulTurboArtifactHashes([][]byte{append(summary, []byte(` {"extra":true}`)...)}, start, start.Add(2*time.Second)); err == nil {
		t.Fatal("trailing summary data was accepted")
	}
}
