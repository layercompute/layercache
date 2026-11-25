package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/removable"
)

func TestLocalSSDMissingNeverUsesTheUnderlyingDirectory(t *testing.T) {
	root := t.TempDir()
	mount := filepath.Join(root, "mount")
	lookalike := filepath.Join(mount, "cache")
	if err := os.MkdirAll(lookalike, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := gatewayTestConfig(t, "removable")
	cfg.Role, cfg.DataDir, cfg.MaxBytes = "local", filepath.Join(root, "internal"), 16
	cfg.Metro = true
	cfg.SSD = &removable.Config{
		MountPath: mount, Directory: "cache", ID: strings.Repeat("a", 32), MaxBytes: 4096, Only: true,
	}
	instance, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	do := func(method, path, value string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(value))
		req.Header.Set("Authorization", "Bearer "+cfg.LocalToken)
		reply := httptest.NewRecorder()
		instance.Handler().ServeHTTP(reply, req)
		if reply.Code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, reply.Code, want, reply.Body.String())
		}
		return reply
	}
	do(http.MethodPut, "/v8/artifacts/oversized", strings.Repeat("x", 17), http.StatusInsufficientStorage)
	do(http.MethodGet, "/v8/artifacts/oversized", "", http.StatusNotFound)
	do(http.MethodPut, "/v8/artifacts/key", "turbo", http.StatusOK)
	metroPath := metroCachePrefix + strings.Repeat("f", 32)
	do(http.MethodPut, metroPath, "metro", http.StatusNoContent)
	for path, want := range map[string]string{"/v8/artifacts/key": "turbo", metroPath: "metro"} {
		if body := do(http.MethodGet, path, "", http.StatusOK).Body.String(); body != want {
			t.Fatalf("GET %s = %q, want %q", path, body, want)
		}
	}
	var status struct {
		UsageBytes int64
		SSD        struct {
			Attached bool
			Only     bool `json:"onlyWhileAttached"`
		}
	}
	if err := json.Unmarshal(do(http.MethodGet, "/v1/status", "", http.StatusOK).Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.SSD.Attached || !status.SSD.Only || status.UsageBytes != 10 {
		t.Fatalf("unexpected status: %+v", status)
	}
	probe := do(http.MethodGet, "/v1/status?runtime-only=1", "", http.StatusOK)
	if strings.Contains(probe.Body.String(), `"ssd":`) {
		t.Fatal("fast runtime probe should skip removable-volume inspection")
	}
	entries, err := os.ReadDir(lookalike)
	if err != nil || len(entries) != 0 {
		t.Fatalf("underlying mountpoint was written: %v, %v", entries, err)
	}
}
