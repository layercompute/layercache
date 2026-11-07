// Package directtransfer holds the wire format and client mechanics shared by
// LayerCache clients that move hosted object bytes directly with object
// storage. The gateway keeps all authority: it issues a short-lived presigned
// URL for one committed object, and the client trusts the bytes only after it
// verifies their complete size and SHA-256. Any direct failure is repeated once
// through the proxied path; the URL itself is never retried.
//
// Presigned URLs are bearer credentials. Nothing in this package logs them,
// and every error it returns has the URL's query removed.
package directtransfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MediaType is the opt-in a client sends in Accept, and the Content-Type of
	// a read descriptor.
	MediaType = "application/vnd.layercache.transfer.v1+json"
	// Kind identifies this descriptor version.
	Kind = "direct-v1"

	maximumDescriptorBytes = 64 << 10
)

// ErrFailed marks a failed direct transfer. Callers repeat the operation once
// without the opt-in.
var ErrFailed = errors.New("direct object transfer failed")

// Descriptor is a read descriptor. URL is a presigned GET for one immutable,
// content-addressed object.
type Descriptor struct {
	Transfer  string    `json:"transfer"`
	Method    string    `json:"method"`
	URL       string    `json:"url"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	ExpiresAt time.Time `json:"expiresAt"`
	Lease     string    `json:"lease"`
}

// Upload is a write descriptor: a presigned PUT for a fresh staging key. Every
// header must be sent unchanged; storage rejects any other body.
type Upload struct {
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Upload    string            `json:"upload"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// Completion reports a verified direct read, and Declaration declares an
// object before a direct write.
type Completion struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type Declaration = Completion

// Accepts reports whether a request opted in to direct transfer.
func Accepts(header http.Header) bool {
	for _, value := range header.Values("Accept") {
		for _, part := range strings.Split(value, ",") {
			mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err == nil && mediaType == MediaType {
				return true
			}
		}
	}
	return false
}

// IsDescriptor reports whether a response carries a read descriptor rather
// than the proxied bytes.
func IsDescriptor(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && mediaType == MediaType
}

// Digest formats a lowercase hexadecimal SHA-256 as a descriptor digest.
func Digest(hexDigest string) string {
	return "sha256:" + strings.TrimPrefix(hexDigest, "sha256:")
}

// ParseDigest returns the lowercase hexadecimal form of a descriptor digest.
func ParseDigest(value string) (string, bool) {
	digest, found := strings.CutPrefix(value, "sha256:")
	if !found || len(digest) != sha256.Size*2 {
		return "", false
	}
	for _, character := range digest {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", false
		}
	}
	return digest, true
}

// Decode reads and validates one read descriptor.
func Decode(body io.Reader, now time.Time) (Descriptor, *url.URL, error) {
	data, err := io.ReadAll(io.LimitReader(body, maximumDescriptorBytes+1))
	if err != nil {
		return Descriptor{}, nil, fmt.Errorf("%w: read descriptor: %w", ErrFailed, Redact(err))
	}
	if len(data) > maximumDescriptorBytes {
		return Descriptor{}, nil, fmt.Errorf("%w: descriptor is too large", ErrFailed)
	}
	var descriptor Descriptor
	if err := decodeStrict(data, &descriptor); err != nil {
		return Descriptor{}, nil, fmt.Errorf("%w: descriptor is invalid", ErrFailed)
	}
	location, err := descriptor.Validate(now)
	if err != nil {
		return Descriptor{}, nil, err
	}
	return descriptor, location, nil
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing JSON")
	}
	return nil
}

// Validate checks a descriptor and returns its object URL.
func (descriptor Descriptor) Validate(now time.Time) (*url.URL, error) {
	if descriptor.Transfer != Kind || descriptor.Method != http.MethodGet || descriptor.Size < 0 ||
		descriptor.Lease == "" || len(descriptor.Lease) > 128 {
		return nil, fmt.Errorf("%w: descriptor is invalid", ErrFailed)
	}
	if _, ok := ParseDigest(descriptor.Digest); !ok {
		return nil, fmt.Errorf("%w: descriptor digest is invalid", ErrFailed)
	}
	if !descriptor.ExpiresAt.After(now) {
		return nil, fmt.Errorf("%w: descriptor has expired", ErrFailed)
	}
	return ObjectURL(descriptor.URL)
}

// ObjectURL validates a presigned URL. It must be HTTPS, except on loopback,
// and carry no user information or fragment.
func ObjectURL(raw string) (*url.URL, error) {
	location, err := url.Parse(raw)
	if err != nil || location.Host == "" || location.User != nil || location.Fragment != "" ||
		(location.Scheme != "https" && !(location.Scheme == "http" && loopbackHost(location.Hostname()))) {
		return nil, fmt.Errorf("%w: object URL is invalid", ErrFailed)
	}
	return location, nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// NewClient returns the client used for presigned URLs. It sends no
// LayerCache credentials, keeps no cookies and follows no redirects.
func NewClient(responseHeaderTimeout time.Duration) *http.Client {
	if responseHeaderTimeout <= 0 {
		responseHeaderTimeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: responseHeaderTimeout, KeepAlive: 30 * time.Second}
	transport.DialContext = dialer.DialContext
	transport.TLSHandshakeTimeout = responseHeaderTimeout
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	transport.Proxy = http.ProxyFromEnvironment
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("object storage redirect refused")
		},
	}
}

// Get opens a descriptor's object. The returned body verifies the complete
// size and digest: its final Read reports ErrFailed instead of io.EOF when the
// bytes do not match, and every later Read repeats that error.
func Get(ctx context.Context, client *http.Client, descriptor Descriptor, location *url.URL) (*Verifier, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFailed, Redact(err))
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: read object: %w", ErrFailed, Redact(err))
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%w: object storage returned HTTP %d", ErrFailed, response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != descriptor.Size {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%w: object size does not match the descriptor", ErrFailed)
	}
	digest, _ := ParseDigest(descriptor.Digest)
	return NewVerifier(response.Body, digest, descriptor.Size), nil
}

// Put uploads exactly size bytes to a write descriptor.
func Put(ctx context.Context, client *http.Client, upload Upload, body io.Reader, size int64) error {
	location, err := ObjectURL(upload.URL)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, location.String(), io.LimitReader(body, size))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFailed, Redact(err))
	}
	request.ContentLength = size
	if size == 0 {
		request.Body = http.NoBody
	}
	for name, value := range upload.Headers {
		if http.CanonicalHeaderKey(name) == "Content-Length" {
			// Signed by the gateway; the transport sends it from ContentLength.
			if value != strconv.FormatInt(size, 10) {
				return fmt.Errorf("%w: upload length does not match the declaration", ErrFailed)
			}
			continue
		}
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: write object: %w", ErrFailed, Redact(err))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("%w: object storage returned HTTP %d", ErrFailed, response.StatusCode)
	}
	return nil
}

// Verifier checks a body's complete size and SHA-256 as it is read.
type Verifier struct {
	body     io.ReadCloser
	hash     hash.Hash
	digest   string
	size     int64
	read     int64
	err      error
	verified bool
}

func NewVerifier(body io.ReadCloser, digest string, size int64) *Verifier {
	return &Verifier{body: body, hash: sha256.New(), digest: digest, size: size}
}

func (verifier *Verifier) Read(buffer []byte) (int, error) {
	if verifier.err != nil {
		return 0, verifier.err
	}
	if verifier.verified {
		return 0, io.EOF
	}
	if remaining := verifier.size - verifier.read + 1; int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	count, err := verifier.body.Read(buffer)
	verifier.read += int64(count)
	_, _ = verifier.hash.Write(buffer[:count])
	switch {
	case verifier.read > verifier.size:
		verifier.err = fmt.Errorf("%w: object is larger than the descriptor", ErrFailed)
	case err == io.EOF && verifier.read != verifier.size:
		verifier.err = fmt.Errorf("%w: object is truncated", ErrFailed)
	case err == io.EOF && hex.EncodeToString(verifier.hash.Sum(nil)) != verifier.digest:
		verifier.err = fmt.Errorf("%w: object digest does not match the descriptor", ErrFailed)
	case err == io.EOF:
		verifier.verified = true
		return count, io.EOF
	case err != nil:
		verifier.err = fmt.Errorf("%w: read object: %w", ErrFailed, Redact(err))
	default:
		return count, nil
	}
	// Bytes delivered with a failure are never trusted.
	return 0, verifier.err
}

func (verifier *Verifier) Close() error { return verifier.body.Close() }

// Verified reports whether the complete body matched the descriptor.
func (verifier *Verifier) Verified() bool { return verifier.verified }

// Failed reports whether the body was truncated, oversized, mismatched, or
// failed in transport.
func (verifier *Verifier) Failed() bool { return verifier.err != nil }

// Redact removes URL queries from an error. Go's *url.Error embeds the full
// request URL, which for a presigned URL is a credential.
func Redact(err error) error {
	if err == nil {
		return nil
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		location := urlError.URL
		if parsed, parseErr := url.Parse(location); parseErr == nil {
			parsed.RawQuery, parsed.ForceQuery, parsed.Fragment, parsed.User = "", false, "", nil
			location = parsed.String()
		} else {
			location = "[redacted]"
		}
		return &redactedError{
			message: fmt.Sprintf("%s %q: %s", urlError.Op, location, scrub(urlError.Err)),
			cause:   urlError.Err,
		}
	}
	message := scrub(err)
	if message == err.Error() {
		return err
	}
	return &redactedError{message: message, cause: errors.Unwrap(err)}
}

// scrub drops anything after a "?" in a URL-looking token as a last resort.
func scrub(err error) string {
	if err == nil {
		return ""
	}
	fields := strings.Fields(err.Error())
	for index, field := range fields {
		if strings.Contains(field, "://") {
			if before, _, found := strings.Cut(field, "?"); found {
				fields[index] = before
			}
		}
	}
	return strings.Join(fields, " ")
}

type redactedError struct {
	message string
	cause   error
}

func (err *redactedError) Error() string { return err.message }
func (err *redactedError) Unwrap() error { return err.cause }

// Breaker stops opting in after consecutive direct failures, so a broken
// storage path costs one extra round trip instead of a failed transfer per
// read. A success resets it.
type Breaker struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	threshold int
	cooldown  time.Duration
	now       func() time.Time
}

func NewBreaker(threshold int, cooldown time.Duration, now func() time.Time) *Breaker {
	if now == nil {
		now = time.Now
	}
	return &Breaker{threshold: threshold, cooldown: cooldown, now: now}
}

var processBreaker = NewBreaker(3, 10*time.Minute, nil)

// ProcessBreaker is shared by every client in this process: a broken object
// storage path is a property of the host, not of one client.
func ProcessBreaker() *Breaker { return processBreaker }

// Allow reports whether a client may opt in now.
func (breaker *Breaker) Allow() bool {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	return !breaker.now().Before(breaker.openUntil)
}

func (breaker *Breaker) Fail() {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	breaker.failures++
	if breaker.failures >= breaker.threshold {
		breaker.failures = 0
		breaker.openUntil = breaker.now().Add(breaker.cooldown)
	}
}

func (breaker *Breaker) Succeed() {
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	breaker.failures = 0
}
