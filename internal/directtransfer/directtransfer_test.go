package directtransfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestAcceptsOnlyTheTransferMediaType(t *testing.T) {
	for value, want := range map[string]bool{
		MediaType:                                     true,
		"application/json, " + MediaType:              true,
		MediaType + "; q=0.9":                         true,
		"application/json":                            false,
		"application/vnd.layercache.transfer.v2+json": false,
		"": false,
	} {
		header := http.Header{}
		if value != "" {
			header.Set("Accept", value)
		}
		if got := Accepts(header); got != want {
			t.Errorf("Accepts(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestVerifierAcceptsOnlyTheCompleteDeclaredObject(t *testing.T) {
	payload := []byte("verified direct transfer payload")
	digest := digestOf(payload)
	for name, test := range map[string]struct {
		body   []byte
		size   int64
		digest string
		ok     bool
	}{
		"exact":     {payload, int64(len(payload)), digest, true},
		"truncated": {payload[:10], int64(len(payload)), digest, false},
		"oversized": {append(append([]byte(nil), payload...), 'x'), int64(len(payload)), digest, false},
		"mismatch":  {bytes.Repeat([]byte{'z'}, len(payload)), int64(len(payload)), digest, false},
		"empty":     {nil, 0, digestOf(nil), true},
	} {
		t.Run(name, func(t *testing.T) {
			verifier := NewVerifier(io.NopCloser(bytes.NewReader(test.body)), test.digest, test.size)
			read, err := io.ReadAll(verifier)
			if test.ok {
				if err != nil || !verifier.Verified() || !bytes.Equal(read, test.body) {
					t.Fatalf("read %d bytes, err %v, verified %v", len(read), err, verifier.Verified())
				}
				return
			}
			if !errors.Is(err, ErrFailed) || verifier.Verified() || !verifier.Failed() {
				t.Fatalf("err %v, verified %v, failed %v", err, verifier.Verified(), verifier.Failed())
			}
			// The failure is sticky: io.ReadFull can discard an error that
			// arrives with a full buffer.
			if _, again := verifier.Read(make([]byte, 8)); !errors.Is(again, ErrFailed) {
				t.Fatalf("repeat read error = %v", again)
			}
		})
	}
}

func TestRedactRemovesPresignedQueries(t *testing.T) {
	secret := "X-Amz-Signature=deadbeef&X-Amz-Credential=key"
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	descriptor := Descriptor{
		Transfer: Kind, Method: http.MethodGet, URL: server.URL + "/bucket/object?" + secret,
		Digest: Digest(digestOf(nil)), Size: 0, ExpiresAt: time.Now().Add(time.Minute), Lease: "read-1",
	}
	location, err := descriptor.Validate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Get(context.Background(), NewClient(time.Second), descriptor, location)
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("Get against a closed server = %v", err)
	}
	if strings.Contains(err.Error(), "deadbeef") || strings.Contains(err.Error(), "X-Amz") || strings.Contains(err.Error(), "?") {
		t.Fatalf("error leaks the presigned query: %v", err)
	}
	if !strings.Contains(err.Error(), "/bucket/object") {
		t.Fatalf("error lost the object path: %v", err)
	}
	plain := errors.New("dial https://storage.example/object?" + secret + " failed")
	if redacted := Redact(plain); strings.Contains(redacted.Error(), "deadbeef") {
		t.Fatalf("Redact kept a query: %v", redacted)
	}
}

func TestClientSendsNoCredentialsAndFollowsNoRedirects(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	var headers http.Header
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers = request.Header.Clone()
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	descriptor := Descriptor{
		Transfer: Kind, Method: http.MethodGet, URL: origin.URL + "/object?X-Amz-Signature=s",
		Digest: Digest(digestOf(nil)), ExpiresAt: time.Now().Add(time.Minute), Lease: "read-1",
	}
	location, err := descriptor.Validate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Get(context.Background(), NewClient(time.Second), descriptor, location); !errors.Is(err, ErrFailed) {
		t.Fatalf("redirected Get = %v", err)
	}
	if redirected {
		t.Fatal("the object client followed a redirect")
	}
	for _, name := range []string{"Authorization", "Cookie", "X-Layercache-Compatibility"} {
		if headers.Get(name) != "" {
			t.Fatalf("object request carried %s", name)
		}
	}
}

func TestDescriptorValidation(t *testing.T) {
	now := time.Now()
	valid := Descriptor{
		Transfer: Kind, Method: http.MethodGet, URL: "https://storage.example/object?sig=1",
		Digest: Digest(digestOf(nil)), ExpiresAt: now.Add(time.Minute), Lease: "read-1",
	}
	if _, err := valid.Validate(now); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Descriptor){
		"expired":       func(d *Descriptor) { d.ExpiresAt = now },
		"plain http":    func(d *Descriptor) { d.URL = "http://storage.example/object" },
		"credentials":   func(d *Descriptor) { d.URL = "https://user:pass@storage.example/object" },
		"wrong kind":    func(d *Descriptor) { d.Transfer = "direct-v2" },
		"wrong method":  func(d *Descriptor) { d.Method = http.MethodPut },
		"bare digest":   func(d *Descriptor) { d.Digest = digestOf(nil) },
		"negative size": func(d *Descriptor) { d.Size = -1 },
		"no lease":      func(d *Descriptor) { d.Lease = "" },
	} {
		descriptor := valid
		mutate(&descriptor)
		if _, err := descriptor.Validate(now); !errors.Is(err, ErrFailed) {
			t.Errorf("%s: Validate = %v", name, err)
		}
	}
}

func TestBreakerOpensAfterThreeConsecutiveFailures(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	breaker := NewBreaker(3, 10*time.Minute, func() time.Time { return now })
	breaker.Fail()
	breaker.Fail()
	breaker.Succeed()
	breaker.Fail()
	breaker.Fail()
	if !breaker.Allow() {
		t.Fatal("a success must reset the consecutive failure count")
	}
	breaker.Fail()
	if breaker.Allow() {
		t.Fatal("three consecutive failures must stop opting in")
	}
	now = now.Add(10*time.Minute - time.Second)
	if breaker.Allow() {
		t.Fatal("the breaker reopened before its cooldown")
	}
	now = now.Add(time.Second)
	if !breaker.Allow() {
		t.Fatal("the breaker stayed open after its cooldown")
	}
}
