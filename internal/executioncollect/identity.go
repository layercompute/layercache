package executioncollect

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Identity is a random client installation, not a hardware identity. The seed
// stays in the normal user's protected configuration and is never reported.
type Identity struct {
	seed   [32]byte
	origin string
	id     string
}

// LoadIdentity is independent of project configuration and workspace paths.
// Different service origins receive unrelated opaque identifiers.
func LoadIdentity(ctx context.Context, origin string) (Identity, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return Identity{}, errors.New("client configuration directory unavailable")
	}
	return loadIdentityAt(ctx, directory, origin)
}

func loadIdentityAt(ctx context.Context, directory, origin string) (Identity, error) {
	normalized, err := serviceOrigin(origin)
	if err != nil {
		return Identity{}, err
	}
	seed, err := loadIdentitySeed(ctx, directory)
	if err != nil {
		return Identity{}, err
	}
	identity := Identity{seed: seed, origin: normalized}
	identity.id = identity.digest("installation", "")
	return identity, nil
}

func (identity Identity) InstallationID() string { return identity.id }

// FilesystemID consumes only a local logical filesystem key. The key and any
// filesystem paths never leave the collector.
func (identity Identity) FilesystemID(localKey string) string {
	return identity.digest("filesystem", localKey)
}

func (identity Identity) digest(domain, localKey string) string {
	mac := hmac.New(sha256.New, identity.seed[:])
	_, _ = mac.Write([]byte("layercache-execution-v1\x00" + domain + "\x00" + identity.origin + "\x00" + localKey))
	return hex.EncodeToString(mac.Sum(nil))
}

func serviceOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("invalid observation service origin")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", errors.New("invalid observation service origin")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", errors.New("invalid observation service origin")
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	port := parsed.Port()
	if port != "" {
		number, parseErr := strconv.Atoi(port)
		if parseErr != nil || number < 1 || number > 65535 {
			return "", errors.New("invalid observation service origin")
		}
		port = strconv.Itoa(number)
	}
	if scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}
