package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/measurement"
)

func hostedReportFixture(summaryID string) map[string]any {
	return map[string]any{
		"id": summaryID, "version": "1",
		"execution": map[string]any{"startTime": 10000, "endTime": 15000},
		"tasks": []any{
			map[string]any{
				"taskId": "pkg#build", "hash": "native-hash-unchanged", "dependencies": []string{"pkg#transit"},
				"cache":                  map[string]any{"local": false, "remote": false, "status": "MISS", "source": "", "timeSaved": 0},
				"execution":              map[string]any{"startTime": 11000, "endTime": 12000, "exitCode": 0},
				"resolvedTaskDefinition": map[string]any{"cache": true},
			},
			map[string]any{
				"taskId": "pkg#unexecuted", "hash": "unexecuted-hash", "dependencies": []string{},
				"cache":                  map[string]any{"local": false, "remote": false, "status": "MISS", "source": "", "timeSaved": 0},
				"execution":              map[string]any{"startTime": 0, "endTime": 0, "exitCode": nil},
				"resolvedTaskDefinition": map[string]any{"cache": false},
			},
		},
	}
}

func hostedReportDocument(t *testing.T, value any) []byte {
	t.Helper()
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n"))
}

func captureHostedReports(t *testing.T) (config.Config, <-chan []byte, *atomic.Int64) {
	t.Helper()
	requests := new(atomic.Int64)
	bodies := make(chan []byte, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/reports/turbo" ||
			request.Header.Get("Authorization") != "Bearer test-report-capability" ||
			request.Header.Get("X-LayerCache-Project") != "github.com/acme/widgets" {
			t.Error("incorrect hosted report request authority")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, (8<<20)+1))
		if err != nil || len(body) > 8<<20 {
			writer.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		bodies <- body
		_, _ = io.WriteString(writer, `{"tasks":1}`)
	}))
	t.Cleanup(endpoint.Close)
	return config.Config{RemoteOnly: true, TeamURL: endpoint.URL, TeamToken: "test-report-capability", ProjectID: "github.com/acme/widgets"}, bodies, requests
}

func TestRemoteTurboReportProjectsNativeFieldsBeforeHTTP(t *testing.T) {
	t.Parallel()
	fixture := hostedReportFixture("summary-private-fields")
	want := hostedReportDocument(t, map[string]any{"summaries": []any{fixture}})
	const canary = "unused-private-report-canary"
	fixture["globalCacheInputs"] = map[string]any{"environmentVariables": []string{canary}}
	fixture["environmentVariables"] = map[string]any{"PRIVATE_TOKEN": canary}
	fixture["execution"].(map[string]any)["command"] = canary
	for _, value := range fixture["tasks"].([]any) {
		task := value.(map[string]any)
		task["command"] = canary
		task["inputs"] = map[string]any{canary: canary}
		task["environmentVariables"] = map[string]any{"configured": []string{canary}}
		task["cache"].(map[string]any)["privateMetadata"] = canary
		task["execution"].(map[string]any)["privateMetadata"] = canary
		task["resolvedTaskDefinition"].(map[string]any)["env"] = []string{canary}
	}
	document := hostedReportDocument(t, fixture)
	original := bytes.Clone(document)
	cfg, bodies, _ := captureHostedReports(t)
	if count, err := reconcileRemoteTurboSummaries(context.Background(), cfg, [][]byte{document}); err != nil || count != 1 {
		t.Fatalf("project hosted report = %d, %v", count, err)
	}
	body := <-bodies
	if bytes.Contains(body, []byte(canary)) {
		t.Fatal("unused private input, environment or command metadata left the machine")
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(body, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatal("report changed native identity, dependencies, cache facts or unexecuted task timing")
	}
	if !bytes.Equal(document, original) {
		t.Fatal("report projection changed original summary bytes used by local publication")
	}
	hashes, err := measurement.SuccessfulTurboArtifactHashes([][]byte{document}, time.Time{}, time.Time{})
	if err != nil || !reflect.DeepEqual(hashes, []string{"native-hash-unchanged"}) {
		t.Fatalf("original native publication identities changed: %v, %v", hashes, err)
	}
}

func TestRemoteTurboReportLargeUnusedInputsDoNotConsumeWireBudget(t *testing.T) {
	t.Parallel()
	fixture := hostedReportFixture("summary-large-inputs")
	fixture["tasks"].([]any)[0].(map[string]any)["inputs"] = map[string]any{"unused-file": strings.Repeat("x", 9<<20)}
	document := hostedReportDocument(t, fixture)
	cfg, bodies, _ := captureHostedReports(t)
	if count, err := reconcileRemoteTurboSummaries(context.Background(), cfg, [][]byte{document}); err != nil || count != 1 {
		t.Fatalf("large raw summary with tiny report projection = %d, %v", count, err)
	}
	if body := <-bodies; len(body) > 4096 {
		t.Fatalf("unused input map was retained in %d report bytes", len(body))
	}
}

func TestRemoteTurboReportRejectsMalformedNativeSummaryBeforeHTTP(t *testing.T) {
	t.Parallel()
	outOfWindow := hostedReportFixture("outside-window")
	outOfWindow["tasks"].([]any)[0].(map[string]any)["execution"] = map[string]any{"startTime": 50000, "endTime": 51000, "exitCode": 0}
	for name, document := range map[string][]byte{
		"invalid JSON":         []byte(`{"id":`),
		"unsupported schema":   hostedReportDocument(t, map[string]any{"id": "wrong-schema", "version": "2", "execution": map[string]any{"startTime": 1, "endTime": 2}, "tasks": []any{}}),
		"invalid task shape":   []byte(`{"id":"wrong-task","version":"1","execution":{"startTime":1,"endTime":2},"tasks":[{"taskId":7}]}`),
		"multiple documents":   append(hostedReportDocument(t, hostedReportFixture("first")), []byte(` {"id":"second"}`)...),
		"task outside summary": hostedReportDocument(t, outOfWindow),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _, requests := captureHostedReports(t)
			if _, err := reconcileRemoteTurboSummaries(context.Background(), cfg, [][]byte{document}); err == nil {
				t.Fatal("malformed native report was accepted")
			}
			if requests.Load() != 0 {
				t.Fatal("malformed report reached the hosted API")
			}
		})
	}
}

func hostedReportTasks(t *testing.T, summaryID string, count int) []byte {
	t.Helper()
	fixture := hostedReportFixture(summaryID)
	tasks := make([]any, count)
	for index := range tasks {
		task := hostedReportFixture("unused")["tasks"].([]any)[0].(map[string]any)
		task["taskId"] = fmt.Sprintf("pkg#task-%d", index)
		tasks[index] = task
	}
	fixture["tasks"] = tasks
	return hostedReportDocument(t, fixture)
}

func TestRemoteTurboReportEnforcesNativeBatchLimitsBeforeHTTP(t *testing.T) {
	padding := func(document []byte, size int) []byte {
		result := bytes.Repeat([]byte(" "), size)
		copy(result, document)
		return result
	}
	cases := []struct {
		name      string
		documents func() [][]byte
		accepted  bool
	}{
		{"empty batch", func() [][]byte { return nil }, false},
		{"32 summaries", func() [][]byte {
			documents := make([][]byte, 32)
			for index := range documents {
				documents[index] = hostedReportDocument(t, hostedReportFixture(fmt.Sprintf("summary-%d", index)))
			}
			return documents
		}, true},
		{"33 summaries", func() [][]byte {
			documents := make([][]byte, 33)
			for index := range documents {
				documents[index] = hostedReportDocument(t, hostedReportFixture(fmt.Sprintf("summary-%d", index)))
			}
			return documents
		}, false},
		{"duplicate summary identities", func() [][]byte {
			document := hostedReportDocument(t, hostedReportFixture("duplicate"))
			return [][]byte{document, document}
		}, false},
		{"4096 tasks", func() [][]byte { return [][]byte{hostedReportTasks(t, "full-task-batch", 4096)} }, true},
		{"4097 tasks across summaries", func() [][]byte {
			return [][]byte{hostedReportTasks(t, "first-task-batch", 2048), hostedReportTasks(t, "second-task-batch", 2049)}
		}, false},
		{"64 MiB raw summary", func() [][]byte {
			return [][]byte{padding(hostedReportDocument(t, hostedReportFixture("raw-limit")), 64<<20)}
		}, true},
		{"more than 64 MiB raw summary", func() [][]byte {
			return [][]byte{padding(hostedReportDocument(t, hostedReportFixture("raw-limit")), (64<<20)+1)}
		}, false},
		{"more than 64 MiB cumulative raw summaries", func() [][]byte {
			return [][]byte{
				padding(hostedReportDocument(t, hostedReportFixture("raw-first")), 32<<20),
				padding(hostedReportDocument(t, hostedReportFixture("raw-second")), (32<<20)+1),
			}
		}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cfg, bodies, requests := captureHostedReports(t)
			count, err := reconcileRemoteTurboSummaries(context.Background(), cfg, test.documents())
			if test.accepted {
				if err != nil || count != 1 || requests.Load() != 1 {
					t.Fatalf("valid native report declined: count=%d, requests=%d, error=%v", count, requests.Load(), err)
				}
				<-bodies
			} else if err == nil || requests.Load() != 0 {
				t.Fatalf("unbounded native report reached HTTP: requests=%d, error=%v", requests.Load(), err)
			}
		})
	}
}

func hostedReportAtWireLimit(t *testing.T, size int) []byte {
	t.Helper()
	fixture := hostedReportFixture("summary-wire")
	tasks := make([]any, 12)
	remaining := 3000
	for index := range tasks {
		dependencies := make([]string, min(256, remaining))
		remaining -= len(dependencies)
		for dependency := range dependencies {
			dependencies[dependency] = strings.Repeat("x", 512)
		}
		task := hostedReportFixture("unused")["tasks"].([]any)[0].(map[string]any)
		task["taskId"] = fmt.Sprintf("pkg#task-%d", index)
		task["dependencies"] = dependencies
		tasks[index] = task
	}
	fixture["tasks"] = tasks
	encoded, err := json.Marshal(map[string]any{"summaries": []any{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	extra := size - len(encoded)
	if extra < 0 || extra/5 > 3000*512 {
		t.Fatal("invalid wire boundary fixture size")
	}
	fixture["id"] = fixture["id"].(string) + strings.Repeat("r", extra%5)
	escaped := extra / 5
	for _, value := range tasks {
		dependencies := value.(map[string]any)["dependencies"].([]string)
		for index := range dependencies {
			length := min(512, escaped)
			dependencies[index] = strings.Repeat("&", length) + strings.Repeat("x", 512-length)
			escaped -= length
		}
	}
	encoded, err = json.Marshal(map[string]any{"summaries": []any{fixture}})
	if err != nil || len(encoded) != size {
		t.Fatalf("wire boundary fixture = %d bytes, want %d: %v", len(encoded), size, err)
	}
	return hostedReportDocument(t, fixture)
}

func TestRemoteTurboReportEnforcesExactWireEnvelopeLimit(t *testing.T) {
	for _, size := range []int{8 << 20, (8 << 20) + 1} {
		t.Run(fmt.Sprintf("bytes-%d", size), func(t *testing.T) {
			cfg, bodies, requests := captureHostedReports(t)
			count, err := reconcileRemoteTurboSummaries(context.Background(), cfg, [][]byte{hostedReportAtWireLimit(t, size)})
			if size == 8<<20 {
				if err != nil || count != 1 {
					t.Fatalf("exact-limit report = %d, %v", count, err)
				}
				if body := <-bodies; len(body) != size {
					t.Fatalf("wire envelope = %d bytes, want %d", len(body), size)
				}
			} else if err == nil || requests.Load() != 0 {
				t.Fatalf("oversized wire report reached HTTP: requests=%d, error=%v", requests.Load(), err)
			}
		})
	}
}
