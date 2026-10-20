package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
)

func TestDashboardStatusUsesCurrentAdministrationAuthority(t *testing.T) {
	gateway, projects := gatewayFixture(t, nil)
	cfg := projects["alpha"]
	runtime := gateway.projects[cfg.ProjectID]
	runtime.config.TeamMembers["admin"] = "admin"
	for _, test := range []struct {
		subject string
		cap     access.Capability
		want    bool
	}{
		{"admin", access.CapabilityAdmin, true},
		{"admin", access.CapabilityRead, false},
		{"alice", access.CapabilityWrite, false},
		{"reader", access.CapabilityRead, false},
	} {
		token := gatewayToken(t, cfg, "github:"+test.subject, "", test.cap)
		response := gatewayRequest(gateway, "GET", "/v1/status", "Bearer "+token, "", nil)
		var status struct {
			CanPrune   bool   `json:"canPrune"`
			Visibility string `json:"visibility"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.CanPrune != test.want || status.Visibility != "unknown" {
			t.Fatalf("%s/%s status: %d %s", test.subject, test.cap, response.Code, response.Body.String())
		}
	}
	// The same unexpired capability loses administrative access on demotion.
	token := gatewayToken(t, cfg, "github:admin", "", access.CapabilityAdmin)
	runtime.config.TeamMembers["admin"] = "reader"
	response := gatewayRequest(gateway, "GET", "/v1/status", "Bearer "+token, "", nil)
	var status struct {
		CanPrune bool `json:"canPrune"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.CanPrune {
		t.Fatalf("demoted administrator: %d %s", response.Code, response.Body.String())
	}
}

func TestDashboardPruningRecoversAccountingAtLowHeadroom(t *testing.T) {
	gateway, projects := gatewayFixture(t, nil)
	cfg := projects["alpha"]
	runtime := gateway.projects[cfg.ProjectID]
	ctx := context.Background()
	put := func(native, body string) artifact.Entry {
		t.Helper()
		entry, _, err := runtime.store.Put(ctx, artifact.Key{Integration: "turbo", Project: cfg.ProjectID, Compatibility: "linux-amd64-schema1", Native: native}, artifact.Metadata{}, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return entry
	}
	protected := put("protected", "keep")
	put("disposable", "discard")
	if err := runtime.store.Pin(ctx, "dashboard-test", artifact.Pin{Owner: "active", Key: protected.Key, Digest: protected.Digest, Size: protected.Size}); err != nil {
		t.Fatal(err)
	}
	// Change only this isolated gateway's admission probe after initialization.
	// An exhausted pool still permits authenticated maintenance, not new growth.
	gateway.pool.Path, gateway.pool.MaxBytes, gateway.pool.MinFreeBytes = t.TempDir(), 1<<62, 1<<62-1
	writer := gatewayToken(t, cfg, "github:alice", "turbo", access.CapabilityWrite)
	if response := gatewayRequest(gateway, "PUT", "/v8/artifacts/new-growth", "Bearer "+writer, "new", nil); response.Code != http.StatusInsufficientStorage {
		t.Fatalf("growth at exhausted pool: %d %s", response.Code, response.Body.String())
	}
	reader := gatewayToken(t, cfg, "github:reader", "", access.CapabilityRead)
	if response := gatewayRequest(gateway, "POST", "/v1/gc", "Bearer "+reader, `{"targetBytes":4}`, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("reader maintenance: %d %s", response.Code, response.Body.String())
	}
	response := gatewayRequest(gateway, "POST", "/v1/gc", "Bearer "+cfg.LocalToken, `{"targetBytes":4}`, map[string]string{"X-LayerCache-Project": cfg.ProjectID})
	var result struct {
		BeforeBytes int64 `json:"beforeBytes"`
		AfterBytes  int64 `json:"afterBytes"`
		FreedBytes  int64 `json:"freedBytes"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.BeforeBytes != 11 || result.AfterBytes != 4 || result.FreedBytes != 7 {
		t.Fatalf("maintenance at exhausted pool: %d %s", response.Code, response.Body.String())
	}
	if entry, err := runtime.store.Head(ctx, protected.Key); err != nil || entry.Digest != protected.Digest {
		t.Fatalf("protected entry changed: %v", err)
	}
	other, err := gateway.projects[projects["beta"].ProjectID].store.Stats(ctx)
	if err != nil || other.UsageBytes != 0 {
		t.Fatalf("other project changed: %#v %v", other, err)
	}
}
