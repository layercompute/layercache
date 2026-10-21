package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/measurement"
)

func operationDocument(id, policy, preparation string, start time.Time, duration int64) measurement.OperationReport {
	phase := func(n int64) *int64 { return &n }
	return measurement.OperationReport{
		Version: 1, OperationID: "run-operation-" + id + "-000000000000",
		SessionID: "run-session-000000000000", WorkloadDigest: "sha256:" + strings.Repeat("a", 64),
		CoverageDigest: "sha256:" + strings.Repeat("b", 64), EnvironmentDigest: "sha256:" + strings.Repeat("c", 64),
		OtherCachesDigest: "sha256:" + strings.Repeat("d", 64), OutputState: "fresh",
		CachePolicy: policy, CachePreparation: preparation, StartedAt: start, FinishedAt: start.Add(time.Duration(duration) * time.Millisecond),
		ElapsedMS: duration, Status: "success",
		PhaseTimingMS: measurement.OperationPhaseTiming{Setup: phase(duration / 10), Restore: phase(duration / 10), Work: phase(duration * 6 / 10), Close: phase(duration / 10), Publish: phase(duration / 10)},
	}
}

func operationHTTP(t *testing.T, instance *Server, claims access.Claims, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, measurement.OperationPath, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+nativeToken(t, instance, claims))
	response := httptest.NewRecorder()
	(&ProjectGateway{projects: map[string]*Server{instance.config.ProjectID: instance}}).ServeHTTP(response, request)
	return response
}

func postOperation(t *testing.T, instance *Server, claims access.Claims, report measurement.OperationReport) (*httptest.ResponseRecorder, measurement.OperationReceipt) {
	t.Helper()
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	response := operationHTTP(t, instance, claims, body)
	var receipt measurement.OperationReceipt
	if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &receipt) != nil {
		t.Fatal("invalid operation acknowledgement")
	}
	return response, receipt
}

func TestOperationGatewayReadAuthorityImmutableRetryAndMembership(t *testing.T) {
	instance, _ := gradleServer(t, "team")
	claims := nativeClaims(t, instance, "gradle", "operation-reader")
	instance.config.TeamMembers["alice"] = "reader"
	report := operationDocument("reader", "enabled", "miss", time.Now().UTC().Add(-3*time.Second), 1000)
	var first string
	for _, repeated := range []bool{false, true} {
		response, receipt := postOperation(t, instance, claims, report)
		if response.Code != http.StatusOK || !receipt.Recorded || receipt.Repeated != repeated || receipt.OperationID != report.OperationID || len(receipt.ReceiptID) != 64 {
			t.Fatalf("status=%d acknowledgement=%+v", response.Code, receipt)
		}
		if first != "" && first != receipt.ReceiptID {
			t.Fatal("identical retry changed receipt")
		}
		first = receipt.ReceiptID
	}
	changed := report
	changed.Status = "failed"
	if response, _ := postOperation(t, instance, claims, changed); response.Code != http.StatusConflict {
		t.Fatalf("changed final status=%d", response.Code)
	}
	changedClaims := claims
	changedClaims.SourceCommit = strings.Repeat("e", 40)
	if response, _ := postOperation(t, instance, changedClaims, report); response.Code != http.StatusConflict {
		t.Fatalf("changed source status=%d", response.Code)
	}
	if response := gradleRequest(instance, http.MethodPut, gradleCachePrefix+strings.Repeat("a", 32), nativeToken(t, instance, claims), "not-authorized"); response.Code != http.StatusForbidden {
		t.Fatalf("report reader gained cache writes=%d", response.Code)
	}
	delete(instance.config.TeamMembers, "alice")
	if response, _ := postOperation(t, instance, claims, report); response.Code != http.StatusUnauthorized {
		t.Fatalf("removed member retained reporting=%d", response.Code)
	}
}

func TestOperationGatewayReconcilesRetainedReceiptBeyondAdmissionWindow(t *testing.T) {
	instance, _ := gradleServer(t, "team")
	claims := nativeClaims(t, instance, "gradle", "operation-late-retry")
	authority, valid := instance.nativeReportAuthority(claims)
	if !valid {
		t.Fatal("fixture has no native authority")
	}
	report := operationDocument("late-retry", "enabled", "miss", time.Now().UTC().Add(-48*time.Hour), 1000)
	first, err := instance.operationReports.RecordOperation(context.Background(), authority, report, report.FinishedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	response, receipt := postOperation(t, instance, claims, report)
	if response.Code != http.StatusOK || !receipt.Repeated || receipt.ReceiptID != first.ReceiptID {
		t.Fatalf("retained retry blocked: %d %+v", response.Code, receipt)
	}
	changed := report
	changed.Status = "failed"
	if response, _ := postOperation(t, instance, claims, changed); response.Code != http.StatusConflict {
		t.Fatalf("late changed content did not conflict: %d", response.Code)
	}
	changedClaims := claims
	changedClaims.SourceCommit = strings.Repeat("e", 40)
	if response, _ := postOperation(t, instance, changedClaims, report); response.Code != http.StatusConflict {
		t.Fatalf("late changed source did not conflict: %d", response.Code)
	}
	report.OperationID = "run-operation-stale-new-000000000000"
	if response, _ := postOperation(t, instance, claims, report); response.Code != http.StatusBadRequest {
		t.Fatalf("new stale operation admitted: %d", response.Code)
	}
	future := operationDocument("future", "enabled", "unknown", time.Now().UTC().Add(time.Minute), 1000)
	if response, _ := postOperation(t, instance, claims, future); response.Code != http.StatusBadRequest {
		t.Fatalf("new future operation admitted: %d", response.Code)
	}
	expired := operationDocument("expired", "enabled", "miss", time.Now().UTC().Add(-31*24*time.Hour), 1000)
	if _, err := instance.operationReports.RecordOperation(context.Background(), authority, expired, expired.FinishedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if response, _ := postOperation(t, instance, claims, expired); response.Code != http.StatusBadRequest {
		t.Fatalf("expired receipt resurrected: %d", response.Code)
	}
}

func TestOperationPairDoesNotChangeNativeOrGenericTotals(t *testing.T) {
	instance, _ := gradleServer(t, "team")
	claims := nativeClaims(t, instance, "gradle", "operation-pair")
	start := time.Now().UTC().Add(-20 * time.Second)
	native := nativeReportDocument("coexisting-native", "fromCache", strings.Repeat("a", 32), start, start.Add(time.Second))
	if response := postNative(t, instance, claims, native); response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	baseline := operationDocument("baseline", "bypass", "bypassed", start, 1000)
	response, receipt := postOperation(t, instance, claims, baseline)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	candidate := operationDocument("candidate", "enabled", "hit", start.Add(3*time.Second), 2000)
	candidate.BaselineReceiptID = receipt.ReceiptID
	if response, _ := postOperation(t, instance, claims, candidate); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	path := "/v1/reports?from=" + start.Add(-time.Second).Format(time.RFC3339) + "&to=" + time.Now().UTC().Format(time.RFC3339)
	response = nativeHTTP(instance, http.MethodGet, path, instance.config.LocalToken, nil)
	var report measurement.PeriodReport
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &report) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	if report.Hits != 0 || len(report.NativeReports) != 1 || report.NativeReports[0].Hits != 1 || report.NativeReports[0].EligibilityUnknown != 0 {
		t.Fatalf("operation changed existing counters: generic=%d native=%+v", report.Hits, report.NativeReports)
	}
	operations := report.OperationReports
	if operations == nil || operations.StoredObservations != 2 || len(operations.Comparisons) != 1 {
		t.Fatalf("operation projection=%+v", operations)
	}
	var found bool
	for _, comparison := range operations.Comparisons {
		if comparison.BaselineReceiptID == receipt.ReceiptID {
			found = comparison.Matched && comparison.ObservedDifferenceMS != nil && *comparison.ObservedDifferenceMS == -1000
		}
	}
	if !found {
		t.Fatalf("negative observed difference lost: %+v", operations.Comparisons)
	}
	instance.operationReports = failedOperationReports{}
	response = nativeHTTP(instance, http.MethodGet, path, instance.config.LocalToken, nil)
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &report) != nil || !report.OperationReportsUnavailable || report.NativeReports[0].Hits != 1 {
		t.Fatal("optional operation failure hid existing reports")
	}
}

type failedOperationReports struct{}

func (failedOperationReports) RecordOperation(context.Context, measurement.NativeAuthority, measurement.OperationReport, time.Time) (measurement.OperationReceipt, error) {
	return measurement.OperationReceipt{}, errors.New("unavailable")
}
func (failedOperationReports) OperationPeriod(context.Context, time.Time, time.Time) (measurement.OperationPeriodReport, error) {
	return measurement.OperationPeriodReport{}, errors.New("unavailable")
}

func TestOperationStrictInputAndSnapshotIsolation(t *testing.T) {
	instance, _ := gradleServer(t, "team")
	claims := nativeClaims(t, instance, "xcode", "operation-input")
	report := operationDocument("input", "enabled", "unknown", time.Now().UTC().Add(-3*time.Second), 1000)
	body, _ := json.Marshal(report)
	malformed := append(append([]byte{}, body[:len(body)-1]...), []byte(`,"command":"must-not-be-uploaded"}`)...)
	if response := operationHTTP(t, instance, claims, malformed); response.Code != http.StatusBadRequest {
		t.Fatalf("unrecognized private field status=%d", response.Code)
	}
	if response := operationHTTP(t, instance, claims, bytes.Repeat([]byte("x"), measurement.OperationMaximumBytes+1)); response.Code != http.StatusBadRequest {
		t.Fatalf("oversized report status=%d", response.Code)
	}
	request := httptest.NewRequest(http.MethodPost, measurement.OperationPath, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+instance.config.LocalToken)
	response := httptest.NewRecorder()
	instance.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unscoped administrator could choose operation authority=%d", response.Code)
	}
	snapshot, bootstrap, grant, key := snapshotAuthFixture(t)
	token := snapshotResult(t, snapshotExchange(t, snapshot.Handler(), bootstrap, grant.Project, grant.Compatibility, key, "write"))
	request = httptest.NewRequest(http.MethodPost, measurement.OperationPath, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token.Token)
	response = httptest.NewRecorder()
	snapshot.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("snapshot grant gained operation reporting=%d", response.Code)
	}
}
