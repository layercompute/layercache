package access

import (
	"strings"
	"testing"
	"time"
)

func TestSnapshotKeyMatchesExistingNativeIdentity(t *testing.T) {
	for _, test := range []struct{ project, profile, key, want string }{
		{"acme/widgets", "darwin-arm64-xcode26", "xcode-cloud-v1/build-42", "native-v1-3dd66914e3425fa3ca1709168fb5a687b5b91afebf0d1c3b28e8df5b98178347"},
		{"a", "p", "s/<>&", "native-v1-ec0c1c1a1f5f91ba32bd8f9c61414751d76680495d4687e36eed19a2de6e4e59"},
	} {
		got, err := SnapshotNativeKey(test.project, test.profile, test.key)
		if err != nil || got != test.want {
			t.Fatalf("native identity changed: %q, %v, expected %s", got, err, test.want)
		}
	}
}

func snapshotClaimsFixture(now time.Time) Claims {
	return Claims{Subject: "snapshot:trusted-writer", Project: "acme/widgets", Integration: "turbo", Compatibility: "darwin-arm64-xcode26", Capabilities: []Capability{CapabilityWrite}, ExpiresAt: now.Add(SnapshotCapabilityLifetime),
		SnapshotScope: &SnapshotScope{CredentialID: "trusted-writer", GrantDigest: strings.Repeat("a", 64), NativeKey: "native-v1-" + strings.Repeat("b", 64)}}
}

func TestSnapshotCapabilitiesCannotCarryUnrelatedAuthority(t *testing.T) {
	now := time.Now().UTC()
	claims := snapshotClaimsFixture(now)
	token, err := MintCapabilityToken("server-only-signing-secret", claims, now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCapabilityToken("server-only-signing-secret", token, now)
	if err != nil || parsed.SnapshotScope == nil || parsed.SnapshotScope.NativeKey != claims.SnapshotScope.NativeKey {
		t.Fatal("snapshot scope did not survive signing", err)
	}
	for name, mutate := range map[string]func(*Claims){
		"admin":      func(c *Claims) { c.Capabilities = []Capability{CapabilityAdmin} },
		"extra-read": func(c *Claims) { c.Capabilities = []Capability{CapabilityRead, CapabilityWrite} },
		"unscoped":   func(c *Claims) { c.Integration = "" },
		"retained":   func(c *Claims) { c.Integration = "retained" },
		"future":     func(c *Claims) { c.ExpiresAt = now.Add(SnapshotCapabilityLifetime + time.Second) },
		"github":     func(c *Claims) { c.Subject = "github-id:1" },
		"run":        func(c *Claims) { c.RunID = "self-reported-main" },
		"repository": func(c *Claims) { c.Repository = "acme/widgets" },
		"ref":        func(c *Claims) { c.Ref = "refs/heads/main" },
		"actor":      func(c *Claims) { c.ActionsActorID = "1" },
		"target":     func(c *Claims) { c.Target = "anything" },
		"foreign-key": func(c *Claims) {
			c.SnapshotScope = &SnapshotScope{CredentialID: "trusted-writer", GrantDigest: strings.Repeat("a", 64), NativeKey: "../other"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := snapshotClaimsFixture(now)
			mutate(&changed)
			if _, err := MintCapabilityToken("server-only-signing-secret", changed, now); err == nil {
				t.Fatal("snapshot authority escalation accepted")
			}
		})
	}
}

func TestSnapshotGrantRequiresLiteralNamespaceAndConfiguredWriteAuthority(t *testing.T) {
	now := time.Now().UTC()
	grant := SnapshotCredential{Project: "acme/widgets", Compatibility: "darwin-arm64-xcode26", KeyPrefix: "xcode-v1/", Role: "reader", ExpiresAt: now.Add(time.Hour)}
	for _, key := range []string{"xcode-v1", "xcode-v1/", "xcode-v1-other/run", "other/run", "xcode-v1/a b", "xcode-v1/é"} {
		if grant.Allows(grant.Project, grant.Compatibility, key, "read", now) {
			t.Fatalf("invalid namespace key accepted: %q", key)
		}
	}
	if !grant.Allows(grant.Project, grant.Compatibility, "xcode-v1/run", "read", now) || grant.Allows(grant.Project, grant.Compatibility, "xcode-v1/run", "write", now) {
		t.Fatal("reader authority changed")
	}
	for _, change := range []struct{ project, profile, mode string }{{"foreign", grant.Compatibility, "read"}, {grant.Project, "other-profile", "read"}, {grant.Project, grant.Compatibility, "admin"}} {
		if grant.Allows(change.project, change.profile, "xcode-v1/run", change.mode, now) {
			t.Fatal("foreign scope accepted")
		}
	}
	grant.Role = "writer"
	if !grant.Allows(grant.Project, grant.Compatibility, "xcode-v1/run", "write", now) || grant.Allows(grant.Project, grant.Compatibility, "xcode-v1/run", "write", grant.ExpiresAt) {
		t.Fatal("configured writer expiry changed")
	}
	for _, token := range []string{"lcs1.reader." + strings.Repeat("a", 64), "lcs1.trusted-writer." + strings.Repeat("b", 64)} {
		if _, ok := SnapshotBootstrapID(token); !ok {
			t.Fatal("valid bootstrap rejected")
		}
	}
	for _, token := range []string{"lcs1.Reader." + strings.Repeat("a", 64), "lcs1.reader.short", "lcs1.reader." + strings.Repeat("A", 64), "Bearer lcs1.reader." + strings.Repeat("a", 64), "lcs1.reader." + strings.Repeat("a", 64) + "\n"} {
		if _, ok := SnapshotBootstrapID(token); ok {
			t.Fatal("unsafe bootstrap accepted")
		}
	}
}
