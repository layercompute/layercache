package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/execution"
	"github.com/layercache/layercache/internal/measurement"
)

func nativeClaims(t *testing.T, instance *Server, integration, run string) access.Claims {
	t.Helper()
	instance.config.TeamMembers = map[string]string{"alice": "writer"}
	return access.Claims{Subject: "github:alice", Project: instance.config.ProjectID, Integration: integration, Compatibility: "native-profile-v1", Repository: instance.config.ActionsRepository, SourceCommit: strings.Repeat("a", 40), RunID: "github-local:github:alice:run-" + run + "-000000000000", WorkspaceID: "github-local:github:alice:sha256:" + strings.Repeat("a", 64), Capabilities: []access.Capability{access.CapabilityRead}, ExpiresAt: time.Now().Add(time.Hour), Execution: execution.ClientContext("github-id:42", run)}
}
func nativeReportDocument(invocation, outcome, key string, start, end time.Time) measurement.NativeReport {
	task := measurement.NativeTask{WorkRef: "sha256:" + strings.Repeat("a", 64), NativeKey: key, Outcome: outcome, StartedAt: start, FinishedAt: end}
	if outcome == "executed" {
		elapsed := end.Sub(start).Milliseconds()
		task.ExecutionMS = &elapsed
	}
	return measurement.NativeReport{Version: 1, InvocationID: "run-" + invocation + "-000000000000", StartedAt: start, FinishedAt: end, Status: "success", Tasks: []measurement.NativeTask{task}}
}
func nativeToken(t *testing.T, instance *Server, claims access.Claims) string {
	t.Helper()
	token, err := access.MintCapabilityToken(instance.config.LocalToken, claims, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func postNative(t *testing.T, instance *Server, claims access.Claims, report measurement.NativeReport) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return nativeHTTP(instance, "POST", measurement.NativePath, nativeToken(t, instance, claims), body)
}
func nativeHTTP(instance *Server, method, path, token string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	gateway := &ProjectGateway{projects: map[string]*Server{instance.config.ProjectID: instance}}
	if path == measurement.NativePath {
		gateway.ServeHTTP(response, request)
	} else {
		instance.Handler().ServeHTTP(response, request)
	}
	return response
}

func TestNativeReportReadAuthorityReplayAndCurrentMembership(t *testing.T) {
	instance, _ := gradleServer(t, "team")
	claims := nativeClaims(t, instance, "gradle", "reader")
	instance.config.TeamMembers["alice"] = "reader"
	end := time.Now().UTC().Add(-time.Second)
	report := nativeReportDocument("reader", "fromCache", strings.Repeat("b", 32), end.Add(-time.Second), end)
	for _, repeated := range []bool{false, true} {
		response := postNative(t, instance, claims, report)
		var receipt measurement.NativeReceipt
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &receipt) != nil || receipt.Repeated != repeated || !receipt.Recorded {
			t.Fatalf("report status=%d receipt=%s", response.Code, response.Body.String())
		}
	}
	changed := report
	changed.Status = "failed"
	if response := postNative(t, instance, claims, changed); response.Code != 409 {
		t.Fatalf("changed final=%d %s", response.Code, response.Body.String())
	}
	changedClaims := claims
	changedClaims.SourceCommit = strings.Repeat("c", 40)
	if response := postNative(t, instance, changedClaims, report); response.Code != 409 {
		t.Fatalf("changed source=%d", response.Code)
	}
	changedClaims = claims
	changedClaims.WorkspaceID = "github-local:github:alice:sha256:" + strings.Repeat("b", 64)
	if response := postNative(t, instance, changedClaims, report); response.Code != 200 {
		t.Fatalf("matrix workspace collided=%d", response.Code)
	}
	if response := gradleRequest(instance, "PUT", gradleCachePrefix+strings.Repeat("b", 32), nativeToken(t, instance, claims), "archive"); response.Code != 403 {
		t.Fatalf("report reader gained writes=%d", response.Code)
	}
	delete(instance.config.TeamMembers, "alice")
	if response := postNative(t, instance, claims, report); response.Code != 401 {
		t.Fatalf("removed member retained report=%d", response.Code)
	}
}

func TestNativeReportOIDCExactJobScope(t *testing.T) {
	instance, _ := gradleServer(t, "team")
	instance.config.ActionsRepository = "acme/widget"
	claims := access.Claims{Subject: "github-actions:repo:acme/widget:ref:refs/pull/7/merge", Project: instance.config.ProjectID, Integration: "gradle", Compatibility: "native-profile-v1", Repository: instance.config.ActionsRepository, SourceCommit: strings.Repeat("a", 40), RunID: "github-actions:acme/widget:123:2", WorkspaceID: "github-actions-check:acme/widget:456", Capabilities: []access.Capability{access.CapabilityRead}, ExpiresAt: time.Now().Add(time.Hour), Execution: &execution.Context{Kind: "githubActions", OwnerID: "github-id:42", ExecutionID: "github-actions:12:123:2:456", RunnerEnvironment: "self-hosted"}}
	end := time.Now().UTC().Add(-time.Second)
	report := nativeReportDocument("oidc-reader", "fromCache", "", end.Add(-time.Second), end)
	if response := postNative(t, instance, claims, report); response.Code != 200 {
		t.Fatalf("PR report rejected=%d %s", response.Code, response.Body.String())
	}
	for name, modify := range map[string]func(*access.Claims){
		"workspace check mismatch": func(c *access.Claims) { c.WorkspaceID = "github-actions-check:acme/widget:789" },
		"attempt mismatch":         func(c *access.Claims) { c.RunID = "github-actions:acme/widget:123:1" },
		"repository mismatch":      func(c *access.Claims) { c.Repository = "other/widget" },
		"missing execution":        func(c *access.Claims) { c.Execution = nil },
		"other integration":        func(c *access.Claims) { c.Integration = "xcode" },
		"mutable source":           func(c *access.Claims) { c.SourceCommit = "main" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := claims
			modify(&invalid)
			if response := postNative(t, instance, invalid, report); response.Code != 401 {
				t.Fatalf("invalid authority=%d", response.Code)
			}
		})
	}
}

func TestNativeXcodeReportOnlyAndLocalInstallation(t *testing.T) {
	instance, _ := gradleServer(t, "team")
	claims := nativeClaims(t, instance, "xcode", "apple-import")
	end := time.Now().UTC().Add(-time.Second)
	count := int64(7)
	report := measurement.NativeReport{Version: 1, InvocationID: "run-xcode-import-00000001", StartedAt: end.Add(-time.Second), FinishedAt: end, Status: "success", Tasks: []measurement.NativeTask{}, Counters: &measurement.NativeCounters{RPC: &measurement.NativeRPCCounters{ActionHits: &count}}}
	if response := postNative(t, instance, claims, report); response.Code != 200 {
		t.Fatalf("Xcode import=%d %s", response.Code, response.Body.String())
	}
	invalid := report
	invalid.Tasks = nativeReportDocument("xcode-invalid", "fromCache", "", report.StartedAt, report.FinishedAt).Tasks
	if response := postNative(t, instance, claims, invalid); response.Code != 400 {
		t.Fatalf("invented Xcode native tasks=%d", response.Code)
	}
	token := nativeToken(t, instance, claims)
	for _, route := range []struct {
		method, path string
		status       int
	}{{"GET", "/v8/artifacts/status", 404}, {"GET", "/v1/status", 401}, {"POST", "/v1/gc", 401}, {"POST", execution.Path, 401}} {
		if response := nativeHTTP(instance, route.method, route.path, token, nil); response.Code != route.status {
			t.Fatalf("Xcode token accessed %s status=%d", route.path, response.Code)
		}
	}
	if response := gradleRequest(instance, "GET", gradleCachePrefix+strings.Repeat("b", 32), token, ""); response.Code != 401 {
		t.Fatalf("Xcode token read Gradle=%d", response.Code)
	}
	encoded, _ := json.Marshal(report)
	if response := nativeHTTP(instance, "POST", measurement.NativePath, instance.config.LocalToken, encoded); response.Code != 404 {
		t.Fatalf("unscoped admin allowed report=%d", response.Code)
	}
	direct := httptest.NewRequest("POST", measurement.NativePath, bytes.NewReader(encoded))
	direct.Header.Set("Authorization", "Bearer "+instance.config.LocalToken)
	rejected := httptest.NewRecorder()
	instance.Handler().ServeHTTP(rejected, direct)
	if rejected.Code != 401 {
		t.Fatalf("direct unscoped admin gained workload report authority=%d", rejected.Code)
	}
	snapshot, bootstrap, grant, key := snapshotAuthFixture(t)
	snapshotToken := snapshotResult(t, snapshotExchange(t, snapshot.Handler(), bootstrap, grant.Project, grant.Compatibility, key, "write"))
	if response := nativeHTTP(snapshot, "POST", measurement.NativePath, snapshotToken.Token, encoded); response.Code != 401 {
		t.Fatalf("archive snapshot grant gained report authority=%d", response.Code)
	}
	local, _ := gradleServer(t, "local")
	own := access.Claims{Subject: local.config.InstallationID, Project: local.config.ProjectID, Integration: "xcode", Compatibility: "native-profile-v1", RunID: "run-local-import-00000001", WorkspaceID: "sha256:" + strings.Repeat("b", 64), Capabilities: []access.Capability{access.CapabilityRead}, ExpiresAt: time.Now().Add(time.Hour)}
	if response := postNative(t, local, own, report); response.Code != 200 {
		t.Fatalf("own local import=%d %s", response.Code, response.Body.String())
	}
	own.Subject = "other-installation"
	if response := postNative(t, local, own, report); response.Code != 401 {
		t.Fatalf("foreign local import=%d", response.Code)
	}
	public, _ := gradleServer(t, "public")
	own = nativeClaims(t, public, "xcode", "public-import")
	if response := postNative(t, public, own, report); response.Code != 401 {
		t.Fatalf("public accepted workloadreport=%d", response.Code)
	}
}

func TestNativeGradleFirstPublicationAndCompletedDelivery(t *testing.T) {
	instance, _ := gradleServer(t, "team")
	publisher := nativeClaims(t, instance, "gradle", "producer")
	publisher.Capabilities = []access.Capability{access.CapabilityWrite}
	key := strings.Repeat("b", 32)
	originalObservation := executionRequestFixture()
	originalObservation.InstallationID = strings.Repeat("d", 64)
	body, _ := json.Marshal(originalObservation)
	if response := executionCall(t, instance, publisher, "POST", body); response.Code != 201 {
		t.Fatalf("original resource observation=%d", response.Code)
	}
	producerStart := time.Now().UTC().Add(-time.Second)
	if response := gradleRequest(instance, "PUT", gradleCachePrefix+key, nativeToken(t, instance, publisher), "original archive"); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	producerEnd := time.Now().UTC()
	originalObservation.Phase = "finish"
	originalObservation.Sample.Arch = "arm64"
	body, _ = json.Marshal(originalObservation)
	if response := executionCall(t, instance, publisher, "POST", body); response.Code != 201 {
		t.Fatalf("later resource observation=%d", response.Code)
	}

	if response := postNative(t, instance, publisher, nativeReportDocument("producer", "executed", key, producerStart, producerEnd)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	duplicate := nativeClaims(t, instance, "gradle", "duplicate")
	duplicate.Capabilities = []access.Capability{access.CapabilityWrite}
	if response := gradleRequest(instance, "PUT", gradleCachePrefix+key, nativeToken(t, instance, duplicate), "different origin archive"); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := postNative(t, instance, duplicate, nativeReportDocument("duplicate", "executed", key, producerStart, producerEnd)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	reader := nativeClaims(t, instance, "gradle", "reader-consumer")
	readerStart := time.Now().UTC()
	response := gradleRequest(instance, "GET", gradleCachePrefix+key, nativeToken(t, instance, reader), "")
	if response.Code != 200 || response.Body.String() != "original archive" {
		t.Fatalf("restore changed=%d", response.Code)
	}
	readerEnd := time.Now().UTC()
	if response := postNative(t, instance, reader, nativeReportDocument("consumer", "fromCache", key, readerStart, readerEnd)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	rows, err := instance.nativeReports.PeriodReport(context.Background(), producerStart.Add(-time.Second), time.Now().Add(time.Second))
	if err != nil || len(rows) != 1 {
		t.Fatalf("native report unavailable=%v", err)
	}
	result := rows[0]
	if result.Hits != 1 || result.ProducerResources.Known != 1 || result.ProducerTiming.Known != 1 || result.GrossAvoidedTaskTime.Milliseconds == nil || *result.GrossAvoidedTaskTime.Milliseconds != producerEnd.Sub(producerStart).Milliseconds() || len(result.Sources) != 1 || result.Sources[0].Source != measurement.SourceTeamCache {
		t.Fatalf("first publication not matched: %+v", result)
	}
	if result.AllocatedCPUSecondsSaved != nil || result.MeasuredCPUSecondsSaved != nil || result.CostSaved != nil || result.NetEstimatedBuildTimeSaved.Milliseconds != nil {
		t.Fatal("invented CPU/cost/critical path")
	}
}

// A server-side completed write can support attribution only together with a
// native accepted outcome. A failed response write supplies no delivery proof.
type failingNativeWriter struct{ header http.Header }

func (w *failingNativeWriter) Header() http.Header     { return w.header }
func (*failingNativeWriter) WriteHeader(int)           {}
func (*failingNativeWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestNativeGradlePartialHTTPDeliveryRemainsUnattributed(t *testing.T) {
	instance, _ := gradleServer(t, "team")
	writer := nativeClaims(t, instance, "gradle", "partial-producer")
	writer.Capabilities = []access.Capability{access.CapabilityWrite}
	key := strings.Repeat("b", 32)
	if response := gradleRequest(instance, "PUT", gradleCachePrefix+key, nativeToken(t, instance, writer), "archive"); response.Code != 200 {
		t.Fatal(response.Code)
	}
	reader := nativeClaims(t, instance, "gradle", "partial-reader")
	started := time.Now().UTC()
	request := httptest.NewRequest("GET", gradleCachePrefix+key, nil)
	request.SetBasicAuth("layercache", nativeToken(t, instance, reader))
	instance.Handler().ServeHTTP(&failingNativeWriter{header: make(http.Header)}, request)
	finished := time.Now().UTC()
	if response := postNative(t, instance, reader, nativeReportDocument("partial-reader", "fromCache", key, started, finished)); response.Code != 200 {
		t.Fatal(response.Code)
	}
	rows, err := instance.nativeReports.PeriodReport(context.Background(), started.Add(-time.Second), finished.Add(time.Second))
	if err != nil || len(rows) != 1 || len(rows[0].Sources) != 1 || rows[0].Sources[0].Source != measurement.SourceUnattributed || rows[0].ProducerTiming.Known != 0 {
		t.Fatalf("partial HTTP response became accepted Team delivery: %+v %v", rows, err)
	}
}
