package server

import (
	"testing"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/config"
)

func TestOnlyTestedStockTurboVersionsGetARedirect(t *testing.T) {
	for userAgent, want := range map[string]bool{
		"turbo 2.9.14 1.95.0-nightly linux x86_64":   true,
		"turbo 2.10.12 1.97.0-nightly linux x86_64":  true,
		"turbo 3.0.0 1.99.0 darwin aarch64":          true,
		"turbo 2.9.13 1.95.0-nightly linux x86_64":   false,
		"turbo 2.10.12-canary.1 1.97.0 linux x86_64": false,
		"turbo":                 false,
		"":                      false,
		"Go-http-client/1.1":    false,
		"node":                  false,
		"turbo-proxy 2.10.12":   false,
		"layercache turbo 2.10": false,
	} {
		if got := stockTurboFollowsRedirects(userAgent); got != want {
			t.Errorf("%q = %v, want %v", userAgent, got, want)
		}
	}
}

func TestTurboRedirectSwitchIsSeparateAndForcedOffByEnvironment(t *testing.T) {
	direct := config.DirectTransfer{Read: true, Write: true, TurboRedirect: true}
	if policy := directTransferPolicy(direct); !policy.Read || !policy.TurboRedirect {
		t.Fatalf("configured policy = %+v", policy)
	}
	if policy := directTransferPolicy(config.DirectTransfer{Read: true}); policy.TurboRedirect {
		t.Fatalf("redirect on without its own switch: %+v", policy)
	}
	t.Setenv(turboRedirectEnvironment, "off")
	if policy := directTransferPolicy(direct); !policy.Read || !policy.Write || policy.TurboRedirect {
		t.Fatalf("%s=off left %+v, want only the redirect off", turboRedirectEnvironment, policy)
	}
}

func turboRedirectKey(native string) artifact.Key {
	return artifact.Key{Integration: "turbo", Project: "project", Compatibility: "abi", Native: native}
}

func TestUploadAfterRedirectSendsThatKeyThroughTheProxiedRead(t *testing.T) {
	guard := newTurboRedirectGuard()
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	key := turboRedirectKey("a")

	if guard.upload(key, now) {
		t.Fatal("an upload with no redirect paused redirects")
	}
	if !guard.allows(key, now) {
		t.Fatal("first read was refused")
	}
	guard.issue(key, now)
	guard.upload(key, now.Add(time.Minute))
	if guard.allows(key, now.Add(2*time.Minute)) {
		t.Fatal("the key was redirected again right after a suspected failure")
	}
	if !guard.allows(turboRedirectKey("b"), now.Add(2*time.Minute)) {
		t.Fatal("one failed key stopped redirects for another key")
	}
	if !guard.allows(key, now.Add(time.Minute+turboRedirectWatch)) {
		t.Fatal("the key stayed proxied after the watch ended")
	}
}

func TestUploadAfterTheWatchIsNotAFailure(t *testing.T) {
	guard := newTurboRedirectGuard()
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	key := turboRedirectKey("a")
	guard.issue(key, now)
	guard.upload(key, now.Add(turboRedirectWatch))
	if !guard.allows(key, now.Add(turboRedirectWatch+time.Minute)) {
		t.Fatal("a late upload counted as a failed redirect")
	}
}

func TestRepeatedSuspectedFailuresPauseEveryRedirect(t *testing.T) {
	guard := newTurboRedirectGuard()
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	for index, native := range []string{"a", "b", "c"} {
		key := turboRedirectKey(native)
		guard.issue(key, now)
		paused := guard.upload(key, now.Add(time.Duration(index)*time.Minute))
		if want := index == turboRedirectTripFailures-1; paused != want {
			t.Fatalf("failure %d paused = %v, want %v", index+1, paused, want)
		}
	}
	tripped := now.Add(2 * time.Minute)
	fresh := turboRedirectKey("fresh")
	if guard.allows(fresh, tripped.Add(turboRedirectPause-time.Second)) {
		t.Fatal("a read was redirected during the pause")
	}
	if !guard.allows(fresh, tripped.Add(turboRedirectPause)) {
		t.Fatal("redirects did not resume after the pause")
	}
}

func TestFailuresOutsideThePauseWindowDoNotAccumulate(t *testing.T) {
	guard := newTurboRedirectGuard()
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	for index, native := range []string{"a", "b", "c", "d"} {
		key := turboRedirectKey(native)
		at := now.Add(time.Duration(index) * turboRedirectPause)
		guard.issue(key, at)
		if guard.upload(key, at.Add(time.Second)) {
			t.Fatalf("spaced failure %d paused redirects", index+1)
		}
	}
}

func TestTrackingLimitFallsBackToProxiedReads(t *testing.T) {
	guard := newTurboRedirectGuard()
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	for index := range turboRedirectMaxTracked {
		guard.issued[artifact.Key{Native: string(rune(index + 1))}] = now.Add(turboRedirectWatch)
	}
	if guard.allows(turboRedirectKey("over"), now) {
		t.Fatal("a read was admitted past the tracking limit")
	}
	if !guard.allows(turboRedirectKey("over"), now.Add(turboRedirectWatch)) {
		t.Fatal("expired entries still held the tracking limit")
	}
}
