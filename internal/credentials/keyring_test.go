package credentials

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestDarwinStoreCommandKeepsRawSecretOutOfCommandAndRoundTrips(t *testing.T) {
	secret := `{"access_token":"marker + spaces / newline\\n"}`
	command, err := darwinStoreCommand("github-account", secret)
	if err != nil {
		t.Fatalf("darwinStoreCommand: %v", err)
	}
	if strings.Contains(command, secret) {
		t.Fatal("interactive Keychain command contains the raw secret")
	}
	prefix := " -w " + darwinEncodedSecretPrefix
	start := strings.Index(command, prefix)
	if start < 0 || !strings.HasSuffix(command, "\n") {
		t.Fatalf("unexpected interactive Keychain command %q", command)
	}
	encoded := strings.TrimSuffix(command[start+len(" -w "):], "\n")
	decoded, err := decodeDarwinSecret(encoded)
	if err != nil {
		t.Fatalf("decodeDarwinSecret: %v", err)
	}
	if decoded != secret {
		t.Fatalf("decoded secret = %q, want %q", decoded, secret)
	}
}

func TestDarwinStoreCommandRejectsUnsafeAccountAndOversizedInput(t *testing.T) {
	if validAccount("account\n-w injected") {
		t.Fatal("account with command separators was accepted")
	}
	secret := strings.Repeat("x", maximumSecurityInputLine)
	if _, err := darwinStoreCommand("account", secret); err == nil {
		t.Fatal("oversized Keychain command was accepted")
	}
}

func TestDecodeDarwinSecretSupportsLegacyValuesAndRejectsBadEncoding(t *testing.T) {
	const legacy = `{"access_token":"legacy"}`
	if got, err := decodeDarwinSecret(legacy); err != nil || got != legacy {
		t.Fatalf("legacy value = %q, %v", got, err)
	}
	encoded := darwinEncodedSecretPrefix + base64.RawStdEncoding.EncodeToString([]byte("current"))
	if got, err := decodeDarwinSecret(encoded); err != nil || got != "current" {
		t.Fatalf("encoded value = %q, %v", got, err)
	}
	if _, err := decodeDarwinSecret(darwinEncodedSecretPrefix + "%%%"); err == nil {
		t.Fatal("invalid encoded value was accepted")
	}
}

func TestCredentialDeleteOnlyAcceptsNativeMissingItemResults(t *testing.T) {
	for _, test := range []struct {
		name, system, output string
		code                 int
		absent               bool
	}{
		{name: "Linux missing item", system: "linux", code: 1, absent: true},
		{name: "Linux bus unavailable", system: "linux", code: 1, output: "secret-tool: could not connect to Secret Service"},
		{name: "Linux cancelled", system: "linux", code: 137},
		{name: "Linux invalid arguments", system: "linux", code: 2},
		{name: "macOS missing item", system: "darwin", code: 44, output: "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.", absent: true},
		{name: "macOS search failed before missing status", system: "darwin", code: 44, output: "security: SecKeychainSearchCreateFromAttributes: User interaction is not allowed.\nsecurity: SecKeychainSearchCopyNext: The specified item could not be found in the keychain."},
		{name: "macOS status without diagnostic", system: "darwin", code: 44},
		{name: "macOS denied", system: "darwin", code: 51, output: "security: User interaction is not allowed."},
		{name: "macOS unspecified error", system: "darwin", code: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestCredentialDeleteStatusChild$")
			command.Env = append(os.Environ(), "LAYERCACHE_DELETE_STATUS="+strconv.Itoa(test.code), "LAYERCACHE_DELETE_OUTPUT="+test.output)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatal("fixture did not return its native failure status")
			}
			if got := credentialAlreadyAbsent(test.system, err, output); got != test.absent {
				t.Fatalf("missing-item classification %v, want %v", got, test.absent)
			}
		})
	}
	if credentialAlreadyAbsent("linux", errors.New("could not launch credential manager"), nil) {
		t.Fatal("launch failure was treated as an absent credential")
	}
}

func TestCredentialDeleteStatusChild(t *testing.T) {
	value := os.Getenv("LAYERCACHE_DELETE_STATUS")
	if value == "" {
		return
	}
	code, err := strconv.Atoi(value)
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprint(os.Stderr, os.Getenv("LAYERCACHE_DELETE_OUTPUT"))
	os.Exit(code)
}
