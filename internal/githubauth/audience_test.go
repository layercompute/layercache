package githubauth

import "testing"

func TestProjectAudienceProtocolVectors(t *testing.T) {
	for _, tc := range []struct {
		project, digest string
	}{
		{"github.com/acme/widget", "8ac5bc08a41714b439de011cd921e0dddce459b45787709cfa4e295aa228f879"},
		{"github.com/acme/widget ", "faf28a8dd0a3cc6d88a5649a6dfa435d4988ffb8e6cabf0a25e70c4eac027488"},
		{"github.com/acme/WIDGET", "8aacf668968b3b41bca7a8a32d246dd3b370a5a0fbf426dbb02feb714243fdbc"},
	} {
		if got, want := ProjectAudience(tc.project), "https://layercache.dev/oidc/projects/"+tc.digest; got != want {
			t.Fatalf("ProjectAudience(%q) = %q, want %q", tc.project, got, want)
		}
	}
}
