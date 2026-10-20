package measurement

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func validNativeReportForTest() NativeReport {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ms := int64(500)
	return NativeReport{Version: 1, InvocationID: "run-native-validation-0001", StartedAt: now.Add(-time.Second), FinishedAt: now, Status: "success", Tasks: []NativeTask{{WorkRef: "sha256:" + strings.Repeat("a", 64), NativeKey: strings.Repeat("b", 32), Outcome: "executed", StartedAt: now.Add(-time.Second), FinishedAt: now, ExecutionMS: &ms}}}
}

func TestNativeReportBoundedDecoderAndClock(t *testing.T) {
	valid := validNativeReportForTest()
	encoded, _ := json.Marshal(valid)
	decoded, err := DecodeNativeReport(bytes.NewReader(encoded))
	if err != nil || ValidateNativeReport(decoded, valid.FinishedAt) != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	for name, body := range map[string][]byte{
		"oversized":          bytes.Repeat([]byte(" "), NativeMaximumBytes+1),
		"trailing JSON":      append(append([]byte{}, encoded...), []byte(" {}")...),
		"unknown authority":  bytes.Replace(encoded, []byte(`"version":1`), []byte(`"version":1,"sourceCommit":"forged"`), 1),
		"unknown task field": bytes.Replace(encoded, []byte(`"outcome":"executed"`), []byte(`"outcome":"executed","path":"private"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeNativeReport(bytes.NewReader(body)); !errors.Is(err, ErrNativeInvalid) {
				t.Fatalf("unsafe JSON accepted: %v", err)
			}
		})
	}
	for name, change := range map[string]func(*NativeReport){
		"null tasks":               func(r *NativeReport) { r.Tasks = nil },
		"empty observation":        func(r *NativeReport) { r.Tasks = []NativeTask{} },
		"duplicate opaque work":    func(r *NativeReport) { r.Tasks = append(r.Tasks, r.Tasks[0]) },
		"raw path work":            func(r *NativeReport) { r.Tasks[0].WorkRef = ":app:private" },
		"invalid eligibility":      func(r *NativeReport) { r.Tasks[0].CacheEligibility = "ENABLED" },
		"disabled contradicts key": func(r *NativeReport) { r.Tasks[0].CacheEligibility = "ineligible" },
		"invalid native key":       func(r *NativeReport) { r.Tasks[0].NativeKey = "arbitrary" },
		"negative elapsed":         func(r *NativeReport) { n := int64(-1); r.Tasks[0].ExecutionMS = &n },
		"elapsed exceeds window":   func(r *NativeReport) { n := int64(2000); r.Tasks[0].ExecutionMS = &n },
		"cached execution claim":   func(r *NativeReport) { r.Tasks[0].Outcome = "fromCache" },
		"task outside invocation":  func(r *NativeReport) { r.Tasks[0].StartedAt = r.StartedAt.Add(-6 * time.Second) },
		"old report":               func(r *NativeReport) { r.StartedAt = r.StartedAt.Add(-25 * time.Hour) },
		"future report":            func(r *NativeReport) { r.FinishedAt = r.FinishedAt.Add(6 * time.Second) },
		"unknown native outcome":   func(r *NativeReport) { r.Tasks[0].Outcome = "HIT" },
		"overflow phase":           func(r *NativeReport) { n := int64(24*60*60*1000 + 1); r.TimingMS = &NativePhaseTiming{Compile: &n} },
		"RPC inconsistent": func(r *NativeReport) {
			a, b := int64(1), int64(2)
			r.Counters = &NativeCounters{RPC: &NativeRPCCounters{ActionReads: &a, ActionHits: &b}}
		},
		"complete compiler contradicts parser limit": func(r *NativeReport) {
			zero := int64(0)
			r.Counters = &NativeCounters{Compiler: &NativeCompilerCounters{Scope: "clang-pch", Coverage: "complete", Reason: "truncated", Hits: &zero, Misses: &zero}}
		},
		"complete compiler missing counts": func(r *NativeReport) {
			r.Counters = &NativeCounters{Compiler: &NativeCompilerCounters{Scope: "clang-pch", Coverage: "complete"}}
		},
		"too many tasks": func(r *NativeReport) { r.Tasks = make([]NativeTask, NativeMaximumTasks+1) },
	} {
		t.Run(name, func(t *testing.T) {
			var r NativeReport
			_ = json.Unmarshal(encoded, &r)
			change(&r)
			if !errors.Is(ValidateNativeReport(r, valid.FinishedAt), ErrNativeInvalid) {
				t.Fatal("unsafe observation accepted")
			}
		})
	}
}

func TestNativeCounterOnlyPreservesUnknown(t *testing.T) {
	report := validNativeReportForTest()
	report.Tasks = []NativeTask{}
	zero, hits := int64(0), int64(17)
	report.Counters = &NativeCounters{RPC: &NativeRPCCounters{ActionHits: &hits}, Compiler: &NativeCompilerCounters{Scope: "clang-pch", Coverage: "partial", Reason: "truncated", Hits: &zero}}
	report.TimingMS = &NativePhaseTiming{Compile: &zero}
	encoded, _ := json.Marshal(report)
	got, err := DecodeNativeReport(bytes.NewReader(encoded))
	if err != nil || got.Counters.RPC.ActionReads != nil || got.Counters.Compiler.Misses != nil || got.TimingMS.Restore != nil || got.TimingMS.Compile == nil || *got.TimingMS.Compile != 0 {
		t.Fatalf("unknown and zero collapsed: %v %+v", err, got)
	}
}
