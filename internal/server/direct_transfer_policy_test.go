package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/config"
)

func TestDirectTransferPolicyIsOffByDefaultAndForcedOffByEnvironment(t *testing.T) {
	cfg := config.Config{}
	if policy := cloudStoreConfig(cfg).DirectTransfer; policy != (cloud.DirectTransferPolicy{}) {
		t.Fatalf("default policy = %+v, want off", policy)
	}
	cfg.DirectTransfer = config.DirectTransfer{Read: true, Write: true, MaxURLSeconds: 60}
	if policy := cloudStoreConfig(cfg).DirectTransfer; !policy.Read || !policy.Write || policy.MaxURL != time.Minute {
		t.Fatalf("configured policy = %+v", policy)
	}
	t.Setenv(directTransferEnvironment, "off")
	if policy := cloudStoreConfig(cfg).DirectTransfer; policy != (cloud.DirectTransferPolicy{}) {
		t.Fatalf("%s=off left policy %+v", directTransferEnvironment, policy)
	}
	t.Setenv(directTransferEnvironment, "OFF ")
	if policy := cloudStoreConfig(cfg).DirectTransfer; policy.Read || policy.Write {
		t.Fatal("the override must be case and whitespace insensitive")
	}
}

func TestDirectReadCompletionNeedsOnlyReadAuthority(t *testing.T) {
	for path, want := range map[string]access.Capability{
		"/v8/artifacts/hash/transfers/read-1/complete":                                     access.CapabilityRead,
		"/_apis/artifactcache/caches/7/archive":                                            access.CapabilityRead,
		"/_layercache/compatibility/linux-x64-node24/_apis/artifactcache/caches/7/archive": access.CapabilityRead,
		"/v8/artifacts/hash/transfers/upload":                                              access.CapabilityWrite,
		"/v8/artifacts/hash/transfers/upload-1/commit":                                     access.CapabilityWrite,
		"/v8/artifacts/hash/transfers/read-1/complete/extra":                               access.CapabilityWrite,
		"/_apis/artifactcache/caches/7":                                                    access.CapabilityWrite,
	} {
		request, err := http.NewRequest(http.MethodPost, "http://gateway"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := requiredRequestCapability(request); got != want {
			t.Errorf("POST %s requires %q, want %q", path, got, want)
		}
	}
}

func TestDirectTransferConfigValidation(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DirectTransfer = config.DirectTransfer{Read: true}
	if err := cfg.Validate(); err == nil {
		t.Fatal("a local runtime accepted direct transfer")
	}
	cfg.DirectTransfer = config.DirectTransfer{MaxURLSeconds: config.MaximumDirectTransferURLSeconds + 1}
	if err := cfg.Validate(); err == nil {
		t.Fatal("an unbounded URL lifetime was accepted")
	}
	cfg.DirectTransfer = config.DirectTransfer{MaxURLSeconds: -1}
	if err := cfg.Validate(); err == nil {
		t.Fatal("a negative URL lifetime was accepted")
	}
}
