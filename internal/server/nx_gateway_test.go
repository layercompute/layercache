package server

import (
	"testing"

	"github.com/layercache/layercache/internal/access"
)

func TestProjectGatewayNxIsolationAndCurrentMembership(t *testing.T) {
	gateway, projects := gatewayFixture(t, nil)
	auth := map[string]string{}
	for alias, cfg := range projects {
		auth[alias] = "Bearer " + gatewayToken(t, cfg, "github:alice", "nx", access.CapabilityWrite)
		if response := gatewayRequest(gateway, "PUT", "/v1/cache/same-hash", auth[alias], alias+"-private", nil); response.Code != 200 {
			t.Fatalf("%s publication HTTP %d: %s", alias, response.Code, response.Body.String())
		}
	}
	for alias := range projects {
		response := gatewayRequest(gateway, "GET", "/v1/cache/same-hash", auth[alias], "", nil)
		if response.Code != 200 || response.Body.String() != alias+"-private" {
			t.Fatalf("project %s exposed another project's bytes: HTTP %d %s", alias, response.Code, response.Body.String())
		}
	}
	turbo := "Bearer " + gatewayToken(t, projects["alpha"], "github:alice", "turbo", access.CapabilityWrite)
	if response := gatewayRequest(gateway, "GET", "/v1/cache/same-hash", turbo, "", nil); response.Code != 401 {
		t.Fatalf("Turbo capability accessed Nx bytes: HTTP %d", response.Code)
	}
	delete(gateway.projects[projects["alpha"].ProjectID].config.TeamMembers, "alice")
	if response := gatewayRequest(gateway, "GET", "/v1/cache/same-hash", auth["alpha"], "", nil); response.Code != 401 {
		t.Fatalf("revoked member retained Nx access: HTTP %d", response.Code)
	}
}
