package remote_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/directtransfer"
	"github.com/layercache/layercache/internal/remote"
)

// directFixture is a Team gateway that offers direct transfer, and an object
// store that serves and accepts presigned requests.
type directFixture struct {
	t         *testing.T
	gateway   *httptest.Server
	objects   *httptest.Server
	payload   []byte
	served    []byte // what the object store returns for the descriptor URL
	objectErr int    // object store status override
	expired   bool
	stored    []byte
	commit    int

	mu             sync.Mutex
	optedIn        int
	proxiedGets    int
	objectGets     int
	objectPuts     int
	proxiedPuts    int
	completions    []directtransfer.Completion
	objectHeaders  []http.Header
	commitRequests int
}

func newDirectFixture(t *testing.T, payload []byte) *directFixture {
	t.Helper()
	fixture := &directFixture{t: t, payload: payload, served: payload, commit: http.StatusOK}
	fixture.objects = httptest.NewServer(http.HandlerFunc(fixture.serveObject))
	t.Cleanup(fixture.objects.Close)
	fixture.gateway = httptest.NewServer(http.HandlerFunc(fixture.serveGateway))
	t.Cleanup(fixture.gateway.Close)
	return fixture
}

func (fixture *directFixture) digest() string {
	sum := sha256.Sum256(fixture.payload)
	return hex.EncodeToString(sum[:])
}

func (fixture *directFixture) serveObject(writer http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	fixture.objectHeaders = append(fixture.objectHeaders, request.Header.Clone())
	fixture.mu.Unlock()
	if request.URL.Query().Get("X-Amz-Signature") != "secret-signature" {
		writer.WriteHeader(http.StatusForbidden)
		return
	}
	if fixture.objectErr != 0 {
		writer.WriteHeader(fixture.objectErr)
		return
	}
	switch request.Method {
	case http.MethodGet:
		fixture.mu.Lock()
		fixture.objectGets++
		fixture.mu.Unlock()
		writer.Header().Set("Content-Length", strconv.Itoa(len(fixture.served)))
		_, _ = writer.Write(fixture.served)
	case http.MethodPut:
		body, _ := io.ReadAll(request.Body)
		sum := sha256.Sum256(body)
		fixture.mu.Lock()
		fixture.objectPuts++
		fixture.mu.Unlock()
		if request.Header.Get("x-amz-checksum-sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		fixture.stored = body
	}
}

func (fixture *directFixture) serveGateway(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer team-token" {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := request.URL.Path
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	switch {
	case request.Method == http.MethodGet && path == "/v8/artifacts/task":
		if directtransfer.Accepts(request.Header) {
			fixture.optedIn++
			expires := time.Now().Add(time.Minute)
			if fixture.expired {
				expires = time.Now().Add(-time.Second)
			}
			writer.Header().Set("Content-Type", directtransfer.MediaType)
			writer.Header().Set("x-artifact-duration", "42")
			_ = json.NewEncoder(writer).Encode(directtransfer.Descriptor{
				Transfer: directtransfer.Kind, Method: http.MethodGet,
				URL:    fixture.objects.URL + "/blobs/object?X-Amz-Signature=secret-signature",
				Digest: directtransfer.Digest(fixture.digest()), Size: int64(len(fixture.payload)),
				ExpiresAt: expires, Lease: "read-lease",
			})
			return
		}
		fixture.proxiedGets++
		writer.Header().Set("x-layercache-digest", "sha256:"+fixture.digest())
		writer.Header().Set("Content-Length", strconv.Itoa(len(fixture.payload)))
		_, _ = writer.Write(fixture.payload)
	case request.Method == http.MethodPost && path == "/v8/artifacts/task/transfers/read-lease/complete":
		var completion directtransfer.Completion
		_ = json.NewDecoder(request.Body).Decode(&completion)
		fixture.completions = append(fixture.completions, completion)
		writer.WriteHeader(http.StatusNoContent)
	case request.Method == http.MethodPost && path == "/v8/artifacts/task/transfers/upload":
		var declaration directtransfer.Declaration
		_ = json.NewDecoder(request.Body).Decode(&declaration)
		sum, _ := hex.DecodeString(strings.TrimPrefix(declaration.Digest, "sha256:"))
		_ = json.NewEncoder(writer).Encode(directtransfer.Upload{
			URL:       fixture.objects.URL + "/staging/upload-1?X-Amz-Signature=secret-signature",
			Headers:   map[string]string{"x-amz-checksum-sha256": base64.StdEncoding.EncodeToString(sum)},
			Upload:    "upload-1",
			ExpiresAt: time.Now().Add(time.Minute),
		})
	case request.Method == http.MethodPost && path == "/v8/artifacts/task/transfers/upload-1/commit":
		fixture.commitRequests++
		writer.WriteHeader(fixture.commit)
	case request.Method == http.MethodPut && path == "/v8/artifacts/task":
		fixture.proxiedPuts++
		body, _ := io.ReadAll(request.Body)
		fixture.stored = body
	default:
		http.NotFound(writer, request)
	}
}

func (fixture *directFixture) client(breaker *directtransfer.Breaker) *remote.TurboClient {
	fixture.t.Helper()
	client, err := remote.NewTurboClientForCompatibility(fixture.gateway.URL, "team-token", "linux-amd64-schema1")
	if err != nil {
		fixture.t.Fatal(err)
	}
	if breaker == nil {
		breaker = directtransfer.NewBreaker(3, 10*time.Minute, nil)
	}
	remote.SetDirectBreaker(client, breaker)
	return client
}

func installAll(installs *[][]byte) func(remote.TurboDownload) error {
	return func(download remote.TurboDownload) error {
		data, err := io.ReadAll(download.Body)
		if err != nil {
			return err
		}
		*installs = append(*installs, data)
		return nil
	}
}

func TestTurboFetchReadsDirectlyWithoutCredentialsAndCompletesOnce(t *testing.T) {
	payload := bytes.Repeat([]byte("direct"), 4096)
	fixture := newDirectFixture(t, payload)
	var installs [][]byte
	var metadata artifact.Metadata
	err := fixture.client(nil).Fetch(context.Background(), "task", func(download remote.TurboDownload) error {
		metadata = download.Metadata
		if download.Size != int64(len(payload)) || download.ExpectedDigest != "sha256:"+fixture.digest() {
			t.Fatalf("download metadata %d %q", download.Size, download.ExpectedDigest)
		}
		return installAll(&installs)(download)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(installs) != 1 || !bytes.Equal(installs[0], payload) || metadata.DurationMS != 42 {
		t.Fatalf("installs %d, duration %d", len(installs), metadata.DurationMS)
	}
	if fixture.optedIn != 1 || fixture.objectGets != 1 || fixture.proxiedGets != 0 {
		t.Fatalf("opted in %d, object GETs %d, proxied GETs %d", fixture.optedIn, fixture.objectGets, fixture.proxiedGets)
	}
	for _, header := range fixture.objectHeaders {
		if header.Get("Authorization") != "" || header.Get("X-LayerCache-Compatibility") != "" {
			t.Fatal("the object request carried LayerCache credentials")
		}
	}
	if len(fixture.completions) != 1 || fixture.completions[0].Size != int64(len(payload)) ||
		fixture.completions[0].Digest != "sha256:"+fixture.digest() {
		t.Fatalf("completions = %+v", fixture.completions)
	}
}

func TestTurboFetchFallsBackExactlyOnceOnDirectFailure(t *testing.T) {
	payload := bytes.Repeat([]byte("fallback"), 2048)
	for name, configure := range map[string]func(*directFixture){
		"digest mismatch": func(fixture *directFixture) {
			fixture.served = bytes.Repeat([]byte("x"), len(payload))
		},
		"truncation": func(fixture *directFixture) { fixture.served = payload[:len(payload)-1] },
		"oversized":  func(fixture *directFixture) { fixture.served = append(append([]byte(nil), payload...), 'x') },
		"forbidden":  func(fixture *directFixture) { fixture.objectErr = http.StatusForbidden },
		"expired":    func(fixture *directFixture) { fixture.expired = true },
		"transport":  func(fixture *directFixture) { fixture.objects.Close() },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newDirectFixture(t, payload)
			configure(fixture)
			var installs [][]byte
			attempts := 0
			err := fixture.client(nil).Fetch(context.Background(), "task", func(download remote.TurboDownload) error {
				attempts++
				return installAll(&installs)(download)
			})
			if err != nil {
				t.Fatal(err)
			}
			if fixture.optedIn != 1 || fixture.proxiedGets != 1 || fixture.objectGets > 1 {
				t.Fatalf("opted in %d, proxied %d, object GETs %d", fixture.optedIn, fixture.proxiedGets, fixture.objectGets)
			}
			if len(installs) != 1 || !bytes.Equal(installs[0], payload) || attempts > 2 {
				t.Fatalf("installs %d, attempts %d", len(installs), attempts)
			}
			if len(fixture.completions) != 0 {
				t.Fatal("an unverified direct read was completed")
			}
		})
	}
}

func TestTurboFetchStopsOptingInAfterThreeConsecutiveFailures(t *testing.T) {
	payload := []byte("breaker payload")
	fixture := newDirectFixture(t, payload)
	fixture.objectErr = http.StatusForbidden
	client := fixture.client(directtransfer.NewBreaker(3, 10*time.Minute, nil))
	for range 5 {
		var installs [][]byte
		if err := client.Fetch(context.Background(), "task", installAll(&installs)); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.optedIn != 3 || fixture.proxiedGets != 5 {
		t.Fatalf("opted in %d times, proxied %d", fixture.optedIn, fixture.proxiedGets)
	}
}

func TestTurboFetchDoesNotRepeatLocalInstallFailures(t *testing.T) {
	fixture := newDirectFixture(t, []byte("local failure"))
	local := errors.New("local store is full")
	attempts := 0
	err := fixture.client(nil).Fetch(context.Background(), "task", func(download remote.TurboDownload) error {
		attempts++
		_, _ = io.Copy(io.Discard, download.Body)
		return local
	})
	if !errors.Is(err, local) || attempts != 1 || fixture.proxiedGets != 0 {
		t.Fatalf("err %v, attempts %d, proxied %d", err, attempts, fixture.proxiedGets)
	}
}

func TestTurboPutFromWritesDirectlyAndFallsBackOnce(t *testing.T) {
	payload := bytes.Repeat([]byte("upload"), 1024)
	sum := sha256.Sum256(payload)
	entry := artifact.Entry{Digest: hex.EncodeToString(sum[:]), Size: int64(len(payload))}
	open := func(data []byte) (func() (io.ReadCloser, error), *int) {
		opens := 0
		return func() (io.ReadCloser, error) {
			opens++
			return io.NopCloser(bytes.NewReader(data)), nil
		}, &opens
	}

	fixture := newDirectFixture(t, payload)
	opener, opens := open(payload)
	if err := fixture.client(nil).PutFrom(context.Background(), "task", entry, opener); err != nil {
		t.Fatal(err)
	}
	if *opens != 1 || fixture.objectPuts != 1 || fixture.commitRequests != 1 || fixture.proxiedPuts != 0 || !bytes.Equal(fixture.stored, payload) {
		t.Fatalf("opens %d, object PUTs %d, commits %d, proxied %d", *opens, fixture.objectPuts, fixture.commitRequests, fixture.proxiedPuts)
	}
	for _, header := range fixture.objectHeaders {
		if header.Get("Authorization") != "" {
			t.Fatal("the object upload carried LayerCache credentials")
		}
	}

	// Storage rejects a body that does not match the signed checksum.
	fixture = newDirectFixture(t, payload)
	tampered := append([]byte(nil), payload...)
	tampered[0] ^= 1
	calls := 0
	err := fixture.client(nil).PutFrom(context.Background(), "task", entry, func() (io.ReadCloser, error) {
		calls++
		if calls == 1 {
			return io.NopCloser(bytes.NewReader(tampered)), nil
		}
		return io.NopCloser(bytes.NewReader(payload)), nil
	})
	if err != nil || calls != 2 || fixture.commitRequests != 0 || fixture.proxiedPuts != 1 || !bytes.Equal(fixture.stored, payload) {
		t.Fatalf("err %v, opens %d, commits %d, proxied %d", err, calls, fixture.commitRequests, fixture.proxiedPuts)
	}

	// A first-writer conflict at commit is authoritative.
	fixture = newDirectFixture(t, payload)
	fixture.commit = http.StatusConflict
	opener, opens = open(payload)
	if err := fixture.client(nil).PutFrom(context.Background(), "task", entry, opener); !errors.Is(err, remote.ErrConflict) || fixture.proxiedPuts != 0 {
		t.Fatalf("conflict err %v, proxied %d", err, fixture.proxiedPuts)
	}

	// A rejected commit, such as a size mismatch, repeats once proxied.
	fixture = newDirectFixture(t, payload)
	fixture.commit = http.StatusBadRequest
	opener, opens = open(payload)
	if err := fixture.client(nil).PutFrom(context.Background(), "task", entry, opener); err != nil || *opens != 2 || fixture.proxiedPuts != 1 {
		t.Fatalf("rejected commit err %v, opens %d, proxied %d", err, *opens, fixture.proxiedPuts)
	}
}
