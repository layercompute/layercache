package portal

import (
	"testing"

	"github.com/layercache/layercache/internal/config"
)

func TestHostedPoolDirectTransferPolicyReachesProjectsWithoutChangingTheBackend(t *testing.T) {
	cfg := storageTestConfig(t)
	off, err := newStoragePolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool := cfg.HostedStoragePools["r2-primary"]
	if pool.DirectTransfer != (config.DirectTransfer{}) {
		t.Fatal("direct transfer must be off by default")
	}
	pool.DirectTransfer = config.DirectTransfer{Read: true, Write: true, MaxURLSeconds: 60}
	cfg.HostedStoragePools["r2-primary"] = pool
	on, err := newStoragePolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Toggling the switch is not a storage migration.
	if on.pools["r2-primary"].fingerprint != off.pools["r2-primary"].fingerprint {
		t.Fatal("the direct transfer switch changed the pool's backend fingerprint")
	}
	plan := on.plans["beta"]
	template, err := on.projectTemplate(Project{
		StoragePoolID: "r2-primary", AllocatedCacheBytes: plan.ProjectMaxBytes,
		StorageBackendFingerprint: on.pools["r2-primary"].fingerprint,
	}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if template.DirectTransfer != pool.DirectTransfer {
		t.Fatalf("project template direct transfer = %+v, want %+v", template.DirectTransfer, pool.DirectTransfer)
	}

	pool.DirectTransfer.MaxURLSeconds = config.MaximumDirectTransferURLSeconds + 1
	cfg.HostedStoragePools["r2-primary"] = pool
	if _, err := newStoragePolicy(cfg); err == nil {
		t.Fatal("a pool accepted an unbounded presigned URL lifetime")
	}
}
