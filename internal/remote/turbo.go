package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/compatibility"
	"github.com/layercache/layercache/internal/directtransfer"
)

var (
	ErrMiss         = errors.New("remote cache miss")
	ErrUnauthorized = errors.New("remote cache authorization failed")
	ErrMetadata     = errors.New("remote cache metadata is invalid")
	ErrConflict     = errors.New("remote cache artifact conflicts with immutable bytes")
)

type TurboClient struct {
	baseURL       string
	compatibility string
	http          *http.Client
	direct        *http.Client
	breaker       *directtransfer.Breaker
	transferIdle  time.Duration
	tokenMu       sync.RWMutex
	token         string
}

type TurboDownload struct {
	Body           io.ReadCloser
	ExpectedDigest string
	Size           int64
	Metadata       artifact.Metadata
}

type TurboMetadata struct {
	Digest string
	Size   int64
}

// Head reports Team Cache artifact metadata without downloading its archive.
// A successful status alone is insufficient: both immutable digest and size must
// be present. Hosted Team servers answer HEAD from the committed entry and
// object metadata only, so it does not prove the stored bytes are intact; use
// Confirm when a caller must establish a verified remote copy.
func (client *TurboClient) Head(ctx context.Context, hash string) (TurboMetadata, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, client.baseURL+"/v8/artifacts/"+url.PathEscape(hash), nil)
	if err != nil {
		return TurboMetadata{}, err
	}
	client.authorize(request)
	response, err := client.http.Do(request)
	if err != nil {
		return TurboMetadata{}, fmt.Errorf("check remote cache: %w", err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNotFound:
		return TurboMetadata{}, ErrMiss
	case http.StatusUnauthorized, http.StatusForbidden:
		return TurboMetadata{}, ErrUnauthorized
	case http.StatusOK:
	default:
		return TurboMetadata{}, fmt.Errorf("remote cache check returned HTTP %d", response.StatusCode)
	}
	digests := response.Header.Values("x-layercache-digest")
	if len(digests) != 1 || response.ContentLength < 0 {
		return TurboMetadata{}, ErrMetadata
	}
	digest, ok := turboDigest(digests[0])
	if !ok {
		return TurboMetadata{}, ErrMetadata
	}
	return TurboMetadata{Digest: digest, Size: response.ContentLength}, nil
}

// Confirm reads the complete Team Cache artifact, directly or proxied, and
// verifies its bytes against the advertised digest and size. Hosted Team
// servers verify before their 200 and remove a corrupt entry, which then
// reports ErrMiss. Bytes that disagree with their own metadata report
// artifact.ErrCorrupt.
func (client *TurboClient) Confirm(ctx context.Context, hash string) (TurboMetadata, error) {
	var confirmed TurboMetadata
	err := client.Fetch(ctx, hash, func(download TurboDownload) error {
		digest, ok := turboDigest(download.ExpectedDigest)
		if !ok || download.Size < 0 {
			return ErrMetadata
		}
		hasher := sha256.New()
		written, err := io.Copy(hasher, io.LimitReader(download.Body, download.Size+1))
		if err != nil {
			return fmt.Errorf("confirm remote cache: %w", err)
		}
		if written != download.Size || hex.EncodeToString(hasher.Sum(nil)) != digest {
			return artifact.ErrCorrupt
		}
		confirmed = TurboMetadata{Digest: digest, Size: download.Size}
		return nil
	})
	return confirmed, err
}

func turboDigest(value string) (string, bool) {
	digest := strings.TrimPrefix(value, "sha256:")
	if len(digest) != 64 {
		return "", false
	}
	for _, character := range digest {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return "", false
			}
		}
	}
	return digest, true
}

// NewTurboClient preserves the original constructor with an OS/architecture
// default. Callers whose outputs depend on more ABI facts should use
// NewTurboClientForCompatibility.
func NewTurboClient(baseURL, token string) (*TurboClient, error) {
	return NewTurboClientForCompatibilityWithTimeouts(baseURL, token, runtime.GOOS+"-"+runtime.GOARCH+"-schema1", Timeouts{})
}

// NewTurboClientForCompatibility creates a Team Cache client that selects the
// caller's compatibility namespace after bearer authentication. The Team
// server must never substitute its own host identity for this value.
func NewTurboClientForCompatibility(baseURL, token, compatibilityID string) (*TurboClient, error) {
	return NewTurboClientForCompatibilityWithTimeouts(baseURL, token, compatibilityID, Timeouts{})
}

func NewTurboClientForCompatibilityWithTimeouts(baseURL, token, compatibilityID string, timeouts Timeouts) (*TurboClient, error) {
	if err := compatibility.Validate(compatibilityID); err != nil {
		return nil, fmt.Errorf("invalid remote cache compatibility: %w", err)
	}
	return newTurboClient(baseURL, token, compatibilityID, timeouts)
}

func newTurboClient(baseURL, token, compatibilityID string, timeouts Timeouts) (*TurboClient, error) {
	timeouts, err := normalizeTimeouts(timeouts)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid remote cache URL %q", baseURL)
	}
	if parsed.Scheme == "http" && !publicLoopbackHost(parsed.Hostname()) {
		return nil, errors.New("remote cache URL must use HTTPS except on loopback")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("remote cache URL cannot contain credentials, a query, or a fragment")
	}
	if token == "" {
		return nil, errors.New("remote cache token is required")
	}
	return &TurboClient{
		baseURL:       parsed.String(),
		token:         token,
		compatibility: compatibilityID,
		http: &http.Client{
			Transport: transportWithMetadataTimeout(timeouts.Metadata),
			CheckRedirect: func(request *http.Request, _ []*http.Request) error {
				if request.URL.Scheme != parsed.Scheme || request.URL.Host != parsed.Host {
					return errors.New("remote cache redirect changed origin")
				}
				return nil
			},
		},
		// Presigned object URLs get a client that sends no credentials and
		// follows no redirects.
		direct:       directtransfer.NewClient(30 * time.Second),
		breaker:      directtransfer.ProcessBreaker(),
		transferIdle: timeouts.TransferIdle,
	}, nil
}

// Get reads one artifact through the gateway. It never opts in to direct
// transfer; see Fetch.
func (client *TurboClient) Get(ctx context.Context, hash string) (TurboDownload, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.artifactURL(hash), nil)
	if err != nil {
		return TurboDownload{}, err
	}
	client.authorize(request)
	response, err := client.http.Do(request)
	if err != nil {
		return TurboDownload{}, fmt.Errorf("read remote cache: %w", err)
	}
	return turboResponse(response, client.transferIdle)
}

func (client *TurboClient) Put(ctx context.Context, hash string, entry artifact.Entry, body io.Reader) error {
	requestContext, cancel := context.WithCancel(ctx)
	progress := newProgressReader(body, client.transferIdle, cancel)
	defer func() {
		progress.stop()
		cancel()
	}()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPut, client.baseURL+"/v8/artifacts/"+url.PathEscape(hash), progress)
	if err != nil {
		return err
	}
	client.authorize(request)
	request.ContentLength = entry.Size
	request.Header.Set("x-artifact-duration", strconv.FormatInt(entry.Metadata.DurationMS, 10))
	if entry.Metadata.Tag != "" {
		request.Header.Set("x-artifact-tag", entry.Metadata.Tag)
	}
	response, err := client.http.Do(request)
	if err != nil {
		if progress.stop() {
			return ErrTransferIdle
		}
		return fmt.Errorf("publish remote cache: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if response.StatusCode == http.StatusConflict {
		return ErrConflict
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return fmt.Errorf("remote cache publication returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (client *TurboClient) authorize(request *http.Request) {
	client.tokenMu.RLock()
	token := client.token
	client.tokenMu.RUnlock()
	request.Header.Set("Authorization", "Bearer "+token)
	if client.compatibility != "" {
		request.Header.Set(compatibility.Header, client.compatibility)
	}
}

// SetToken atomically rotates the bearer credential used by subsequent Team
// Cache requests. In-flight requests retain the credential with which they
// started.
func (client *TurboClient) SetToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("remote cache token is required")
	}
	client.tokenMu.Lock()
	client.token = token
	client.tokenMu.Unlock()
	return nil
}
