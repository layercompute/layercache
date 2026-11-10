package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/actionscache"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/directtransfer"
	"github.com/layercache/layercache/internal/remote"
	"github.com/layercache/layercache/internal/server"
)

// directGateway is a hosted Team gateway on PostgreSQL and S3 with direct
// transfer enabled. Presigned URLs need the S3 fixture's static key.
type directGateway struct {
	cfg      config.Config
	endpoint *httptest.Server
	mu       sync.Mutex
	requests []string
}

func newDirectGateway(t *testing.T, direct config.DirectTransfer) *directGateway {
	t.Helper()
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" || os.Getenv("LAYER_CACHE_QA_S3_ENDPOINT") == "" ||
		os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "" {
		t.Skip("set PostgreSQL and S3 QA endpoints with a static S3 key")
	}
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Role, cfg.DataDir, cfg.MaxBytes = "team", t.TempDir(), 4<<20
	cfg.ProjectID = "direct-" + time.Now().Format("150405.000000000")
	cfg.ActionsRepository = "acme/widgets"
	cfg.CompatibilityID = "linux-amd64-schema1"
	cfg.CloudPostgresURL, cfg.CloudS3Endpoint = os.Getenv("LAYER_CACHE_QA_POSTGRES_URL"), os.Getenv("LAYER_CACHE_QA_S3_ENDPOINT")
	cfg.CloudS3Bucket, cfg.CloudS3Region, cfg.CloudS3UsePathStyle = os.Getenv("LAYER_CACHE_QA_S3_BUCKET"), "us-east-1", true
	cfg.CloudS3AccessKey, cfg.CloudS3SecretKey = os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
	cfg.TeamMembers = map[string]string{"alice": "reader"}
	cfg.DirectTransfer = direct
	instance, err := server.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	gateway := &directGateway{cfg: cfg}
	gateway.endpoint = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		label := request.Method + " " + request.URL.Path
		if directtransfer.Accepts(request.Header) {
			label += " direct"
		}
		gateway.mu.Lock()
		gateway.requests = append(gateway.requests, label)
		gateway.mu.Unlock()
		instance.Handler().ServeHTTP(writer, request)
	}))
	t.Cleanup(gateway.endpoint.Close)
	return gateway
}

func (gateway *directGateway) token(t *testing.T, subject string, capability access.Capability, expires time.Duration) string {
	t.Helper()
	token, err := access.MintCapabilityToken(gateway.cfg.LocalToken, access.Claims{
		Subject: subject, Project: gateway.cfg.ProjectID, Capabilities: []access.Capability{capability},
		ExpiresAt: time.Now().Add(expires),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (gateway *directGateway) do(t *testing.T, method, path, token string, body []byte, header http.Header) *http.Response {
	t.Helper()
	request := authenticatedRequest(t, token, method, gateway.endpoint.URL+path, bytes.NewReader(body))
	request.Header.Set("X-LayerCache-Compatibility", gateway.cfg.CompatibilityID)
	for name := range header {
		request.Header.Set(name, header.Get(name))
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func (gateway *directGateway) seen(label string) int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	count := 0
	for _, request := range gateway.requests {
		if request == label {
			count++
		}
	}
	return count
}

var optIn = http.Header{"Accept": {directtransfer.MediaType}}

func decodeDescriptor(t *testing.T, response *http.Response) directtransfer.Descriptor {
	t.Helper()
	if response.StatusCode != http.StatusOK || !directtransfer.IsDescriptor(response.Header) {
		t.Fatalf("opted-in GET = %d %q, want a descriptor", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if response.Header.Get("Cache-Control") != "no-store" || !strings.Contains(strings.Join(response.Header.Values("Vary"), ","), "Accept") {
		t.Fatalf("descriptor caching headers = %q, Vary %q", response.Header.Get("Cache-Control"), response.Header.Values("Vary"))
	}
	var descriptor directtransfer.Descriptor
	if err := json.NewDecoder(response.Body).Decode(&descriptor); err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestHostedGatewayIssuesDirectReadsAndAcceptsOneCompletion(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	gateway := newDirectGateway(t, config.DirectTransfer{Read: true, Write: true, MaxURLSeconds: 60})
	writer := gateway.token(t, "writer", access.CapabilityWrite, time.Hour)
	reader := gateway.token(t, "reader", access.CapabilityRead, time.Hour)
	payload := bytes.Repeat([]byte("hosted direct read"), 2048)
	if response := gateway.do(t, http.MethodPut, "/v8/artifacts/seeded", writer, payload, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("proxied PUT = %d", response.StatusCode)
	}

	proxied := gateway.do(t, http.MethodGet, "/v8/artifacts/seeded", reader, nil, nil)
	body, _ := io.ReadAll(proxied.Body)
	if proxied.StatusCode != http.StatusOK || !bytes.Equal(body, payload) || !strings.Contains(strings.Join(proxied.Header.Values("Vary"), ","), "Accept") {
		t.Fatalf("GET without opt-in = %d, %d bytes, Vary %q", proxied.StatusCode, len(body), proxied.Header.Values("Vary"))
	}

	descriptor := decodeDescriptor(t, gateway.do(t, http.MethodGet, "/v8/artifacts/seeded", reader, nil, optIn))
	sum := sha256.Sum256(payload)
	if descriptor.Transfer != directtransfer.Kind || descriptor.Method != http.MethodGet || descriptor.Size != int64(len(payload)) ||
		descriptor.Digest != "sha256:"+hex.EncodeToString(sum[:]) || descriptor.Lease == "" {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	if remaining := time.Until(descriptor.ExpiresAt); remaining > time.Minute || remaining < 50*time.Second {
		t.Fatalf("descriptor expires in %v, want the 60 second policy cap", remaining)
	}
	object, err := http.Get(descriptor.URL)
	if err != nil {
		t.Fatal(directtransfer.Redact(err))
	}
	objectBody, _ := io.ReadAll(object.Body)
	object.Body.Close()
	if object.StatusCode != http.StatusOK || !bytes.Equal(objectBody, payload) {
		t.Fatalf("presigned GET = %d, %d bytes", object.StatusCode, len(objectBody))
	}
	tampered, _ := url.Parse(descriptor.URL)
	tampered.Path = strings.Replace(tampered.Path, "/blobs/", "/staging/", 1)
	if response, err := http.Get(tampered.String()); err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("a presigned URL moved to another key = %v, %v", response.StatusCode, err)
	}

	completion, _ := json.Marshal(directtransfer.Completion{Digest: descriptor.Digest, Size: descriptor.Size})
	completePath := "/v8/artifacts/seeded/transfers/" + descriptor.Lease + "/complete"
	intruder := gateway.token(t, "intruder", access.CapabilityRead, time.Hour)
	if response := gateway.do(t, http.MethodPost, completePath, intruder, completion, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-principal completion = %d", response.StatusCode)
	}
	if response := gateway.do(t, http.MethodPost, completePath, reader, completion, nil); response.StatusCode != http.StatusNoContent {
		t.Fatalf("completion = %d", response.StatusCode)
	}
	if response := gateway.do(t, http.MethodPost, completePath, reader, completion, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("replayed completion = %d", response.StatusCode)
	}
	if response := gateway.do(t, http.MethodGet, "/v8/artifacts/missing", reader, nil, optIn); response.StatusCode != http.StatusNotFound {
		t.Fatalf("opted-in miss = %d", response.StatusCode)
	}

	// The capability token caps the URL lifetime.
	short := gateway.token(t, "reader", access.CapabilityRead, 20*time.Second)
	if remaining := time.Until(decodeDescriptor(t, gateway.do(t, http.MethodGet, "/v8/artifacts/seeded", short, nil, optIn)).ExpiresAt); remaining > 20*time.Second {
		t.Fatalf("descriptor outlives its token by %v", remaining-20*time.Second)
	}

	// Membership is re-checked when a descriptor is issued.
	alice := gateway.token(t, "github:alice", access.CapabilityRead, time.Hour)
	aliceDescriptor := decodeDescriptor(t, gateway.do(t, http.MethodGet, "/v8/artifacts/seeded", alice, nil, optIn))
	if response := gateway.do(t, http.MethodDelete, "/v1/members/github:alice", gateway.cfg.LocalToken, nil, nil); response.StatusCode != http.StatusNoContent {
		t.Fatalf("remove member = %d", response.StatusCode)
	}
	if response := gateway.do(t, http.MethodGet, "/v8/artifacts/seeded", alice, nil, optIn); response.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked member opted-in GET = %d", response.StatusCode)
	}
	aliceCompletion, _ := json.Marshal(directtransfer.Completion{Digest: aliceDescriptor.Digest, Size: aliceDescriptor.Size})
	if response := gateway.do(t, http.MethodPost, "/v8/artifacts/seeded/transfers/"+aliceDescriptor.Lease+"/complete", alice, aliceCompletion, nil); response.StatusCode == http.StatusNoContent {
		t.Fatal("a revoked member completed a read")
	}

	for _, leaked := range []string{"X-Amz-Signature", "X-Amz-Credential", gateway.cfg.CloudS3SecretKey} {
		if strings.Contains(logs.String(), leaked) {
			t.Fatalf("gateway logs contain presigned URL material %q", leaked)
		}
	}
}

func TestHostedGatewayKeepsProxiedReadsWhenDirectTransferIsOff(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T) config.DirectTransfer{
		"policy off": func(*testing.T) config.DirectTransfer { return config.DirectTransfer{} },
		"environment override": func(t *testing.T) config.DirectTransfer {
			t.Setenv("LAYER_CACHE_DIRECT_TRANSFER", "off")
			return config.DirectTransfer{Read: true, Write: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			gateway := newDirectGateway(t, setup(t))
			writer := gateway.token(t, "writer", access.CapabilityWrite, time.Hour)
			payload := []byte("stays proxied")
			gateway.do(t, http.MethodPut, "/v8/artifacts/proxied", writer, payload, nil)
			response := gateway.do(t, http.MethodGet, "/v8/artifacts/proxied", writer, nil, optIn)
			body, _ := io.ReadAll(response.Body)
			if strings.Contains(strings.Join(response.Header.Values("Vary"), ","), "Accept") {
				t.Fatal("a proxied response with direct read off varies on Accept")
			}
			if response.StatusCode != http.StatusOK || directtransfer.IsDescriptor(response.Header) || !bytes.Equal(body, payload) {
				t.Fatalf("opted-in GET = %d %q, %d bytes", response.StatusCode, response.Header.Get("Content-Type"), len(body))
			}
			declaration, _ := json.Marshal(directtransfer.Declaration{Digest: directtransfer.Digest(strings.Repeat("a", 64)), Size: 1})
			if response := gateway.do(t, http.MethodPost, "/v8/artifacts/proxied/transfers/upload", writer, declaration, nil); response.StatusCode != http.StatusNotFound {
				t.Fatalf("direct upload with the switch off = %d", response.StatusCode)
			}
		})
	}
}

func TestTurboClientTransfersDirectlyThroughTheHostedGateway(t *testing.T) {
	gateway := newDirectGateway(t, config.DirectTransfer{Read: true, Write: true})
	writer := gateway.token(t, "writer", access.CapabilityWrite, time.Hour)
	client, err := remote.NewTurboClientForCompatibility(gateway.endpoint.URL, writer, gateway.cfg.CompatibilityID)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("daemon direct write"), 4096)
	sum := sha256.Sum256(payload)
	entry := artifact.Entry{Digest: hex.EncodeToString(sum[:]), Size: int64(len(payload)), Metadata: artifact.Metadata{DurationMS: 1234}}
	opens := 0
	open := func() (io.ReadCloser, error) {
		opens++
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	if err := client.PutFrom(context.Background(), "daemon-task", entry, open); err != nil {
		t.Fatal(err)
	}
	if opens != 1 || gateway.seen("POST /v8/artifacts/daemon-task/transfers/upload") != 1 || gateway.seen("PUT /v8/artifacts/daemon-task") != 0 {
		t.Fatalf("opens %d, requests %v", opens, gateway.requests)
	}
	// The same bytes again are an idempotent direct publication.
	if err := client.PutFrom(context.Background(), "daemon-task", entry, open); err != nil {
		t.Fatal(err)
	}
	other := bytes.Repeat([]byte("conflicting bytes"), 64)
	otherSum := sha256.Sum256(other)
	conflict := artifact.Entry{Digest: hex.EncodeToString(otherSum[:]), Size: int64(len(other))}
	if err := client.PutFrom(context.Background(), "daemon-task", conflict, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(other)), nil
	}); err != remote.ErrConflict {
		t.Fatalf("conflicting direct publication = %v", err)
	}

	// Storage enforces the signed checksum, and commit rejects a staging
	// object that never received the declared bytes.
	declaration, _ := json.Marshal(directtransfer.Declaration{Digest: directtransfer.Digest(entry.Digest), Size: entry.Size})
	response := gateway.do(t, http.MethodPost, "/v8/artifacts/tampered/transfers/upload", writer, declaration, nil)
	var upload directtransfer.Upload
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&upload) != nil {
		t.Fatalf("direct upload admission = %d", response.StatusCode)
	}
	if upload.Headers["Content-Length"] != strconv.Itoa(len(payload)) {
		t.Fatalf("upload does not sign the declared length: %v", upload.Headers)
	}
	tampered := append([]byte(nil), payload...)
	tampered[0] ^= 1
	longer := append(append([]byte(nil), payload...), 'x')
	for name, attempt := range map[string]struct {
		body    []byte
		headers map[string]string
	}{
		"tampered body":     {tampered, upload.Headers},
		"unsigned checksum": {tampered, nil},
		"unsigned length":   {longer, upload.Headers},
	} {
		request, _ := http.NewRequest(http.MethodPut, upload.URL, bytes.NewReader(attempt.body))
		for header, value := range attempt.headers {
			if header != "Content-Length" {
				request.Header.Set(header, value)
			}
		}
		stored, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(directtransfer.Redact(err))
		}
		stored.Body.Close()
		if stored.StatusCode != http.StatusBadRequest && stored.StatusCode != http.StatusForbidden {
			t.Fatalf("%s PUT = %d, want storage to reject it", name, stored.StatusCode)
		}
	}
	if response := gateway.do(t, http.MethodPost, "/v8/artifacts/tampered/transfers/"+upload.Upload+"/commit", writer, nil, nil); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("commit of a rejected upload = %d", response.StatusCode)
	}
	reader := gateway.token(t, "reader", access.CapabilityRead, time.Hour)
	if response := gateway.do(t, http.MethodPost, "/v8/artifacts/tampered/transfers/upload", reader, declaration, nil); response.StatusCode == http.StatusOK {
		t.Fatal("a read-only token was admitted to a direct write")
	}

	var installed []byte
	var duration int64
	if err := client.Fetch(context.Background(), "daemon-task", func(download remote.TurboDownload) error {
		duration = download.Metadata.DurationMS
		installed, err = io.ReadAll(download.Body)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(installed, payload) || duration != 1234 {
		t.Fatalf("direct Fetch installed %d bytes, duration %d", len(installed), duration)
	}
	if gateway.seen("GET /v8/artifacts/daemon-task direct") != 1 || gateway.seen("GET /v8/artifacts/daemon-task") != 0 {
		t.Fatalf("requests %v", gateway.requests)
	}
	completions := 0
	for _, request := range gateway.requests {
		if strings.HasPrefix(request, "POST /v8/artifacts/daemon-task/transfers/read-") && strings.HasSuffix(request, "/complete") {
			completions++
		}
	}
	if completions != 1 {
		t.Fatalf("completions %d, requests %v", completions, gateway.requests)
	}
	if _, err := client.Confirm(context.Background(), "daemon-task"); err != nil {
		t.Fatalf("Confirm through a direct read = %v", err)
	}
}

func TestRemoteActionsStorageReadsDirectlyThroughTheHostedGateway(t *testing.T) {
	gateway := newDirectGateway(t, config.DirectTransfer{Read: true})
	token, err := access.MintCapabilityToken(gateway.cfg.LocalToken, access.Claims{
		Subject: "actions-writer", Project: gateway.cfg.ProjectID, Integration: "actions",
		Compatibility: "linux-x64-node24", Repository: "acme/widgets", Ref: "refs/heads/main", DefaultRef: "refs/heads/main",
		Capabilities: []access.Capability{access.CapabilityWrite}, ExpiresAt: time.Now().Add(time.Hour),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	client, err := actionscache.NewRemoteStorage(actionscache.RemoteStorageConfig{Endpoint: gateway.endpoint.URL, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	scope := actionscache.Scope{Repository: "acme/widgets", Ref: "refs/heads/main", DefaultRef: "refs/heads/main", Compatibility: "linux-x64-node24"}
	payload := bytes.Repeat([]byte("actions direct read"), 4096)
	size := int64(len(payload))
	reservation, err := client.Reserve(context.Background(), actionscache.ReserveRequest{Scope: scope, Key: "direct", Version: "v1", CacheSize: &size})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Upload(context.Background(), actionscache.UploadRequest{ReservationID: reservation.ID, Start: 0, End: size - 1, Body: bytes.NewReader(payload)}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Commit(context.Background(), actionscache.CommitRequest{ReservationID: reservation.ID, Size: size}); err != nil {
		t.Fatal(err)
	}
	result, err := client.Lookup(context.Background(), actionscache.LookupRequest{Scope: scope, Keys: []string{"direct"}, Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := client.Open(context.Background(), actionscache.OpenRequest{Scope: scope, ID: result.Entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(archive.Body)
	_ = archive.Body.Close()
	if err != nil || !bytes.Equal(contents, payload) {
		t.Fatalf("direct archive: %d bytes, %v", len(contents), err)
	}
	downloads, completions := 0, 0
	for _, request := range gateway.requests {
		if strings.HasPrefix(request, "GET ") && strings.HasSuffix(request, "/archive") {
			downloads++
		}
		if strings.HasPrefix(request, "POST ") && strings.HasSuffix(request, "/archive") {
			completions++
		}
	}
	if downloads != 0 || completions != 1 {
		t.Fatalf("proxied downloads %d, completions %d: %v", downloads, completions, gateway.requests)
	}
}

func TestLocalDaemonPublishesAndRestoresDirectlyThroughTheHostedGateway(t *testing.T) {
	gateway := newDirectGateway(t, config.DirectTransfer{Read: true, Write: true})
	writer := gateway.token(t, "writer", access.CapabilityWrite, time.Hour)
	daemon := func() (config.Config, http.Handler) {
		cfg, err := config.Defaults()
		if err != nil {
			t.Fatal(err)
		}
		cfg.DataDir, cfg.MinFreeBytes = t.TempDir(), 0
		cfg.ProjectID, cfg.ActionsRepository = gateway.cfg.ProjectID, gateway.cfg.ProjectID
		cfg.CompatibilityID = gateway.cfg.CompatibilityID
		cfg.TeamURL, cfg.TeamToken = gateway.endpoint.URL, writer
		instance, err := server.New(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = instance.Close() })
		return cfg, instance.Handler()
	}
	serve := func(cfg config.Config, handler http.Handler, method string, body []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/v8/artifacts/daemon-restore", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+cfg.LocalToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	payload := bytes.Repeat([]byte("daemon read-through"), 4096)
	producer, producerHandler := daemon()
	if response := serve(producer, producerHandler, http.MethodPut, payload); response.Code != http.StatusOK ||
		response.Header().Get("x-layercache-team-upload") != "complete" {
		t.Fatalf("producer PUT = %d %v", response.Code, response.Header())
	}
	if gateway.seen("POST /v8/artifacts/daemon-restore/transfers/upload") != 1 || gateway.seen("PUT /v8/artifacts/daemon-restore") != 0 {
		t.Fatalf("producer did not publish directly: %v", gateway.requests)
	}
	consumer, consumerHandler := daemon()
	response := serve(consumer, consumerHandler, http.MethodGet, nil)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), payload) || response.Header().Get("x-layercache-source") != "team" {
		t.Fatalf("consumer GET = %d, %d bytes, source %q", response.Code, response.Body.Len(), response.Header().Get("x-layercache-source"))
	}
	if gateway.seen("GET /v8/artifacts/daemon-restore direct") != 1 || gateway.seen("GET /v8/artifacts/daemon-restore") != 0 {
		t.Fatalf("consumer did not restore directly: %v", gateway.requests)
	}
	// The verified local copy now serves repeat reads without the gateway.
	gateway.mu.Lock()
	before := len(gateway.requests)
	gateway.mu.Unlock()
	if response := serve(consumer, consumerHandler, http.MethodGet, nil); response.Code != http.StatusOK || gateway.seen("GET /v8/artifacts/daemon-restore direct") != 1 {
		t.Fatalf("repeat local read = %d after %d gateway requests", response.Code, before)
	}
}

var stockTurbo = http.Header{"User-Agent": {"turbo 2.10.12 1.97.0-nightly linux x86_64"}}

// getWithoutFollowing shows the gateway's own answer, as a client would see it
// before following a redirect.
func (gateway *directGateway) getWithoutFollowing(t *testing.T, method, path, token string, header http.Header) *http.Response {
	t.Helper()
	request := authenticatedRequest(t, token, method, gateway.endpoint.URL+path, nil)
	request.Header.Set("X-LayerCache-Compatibility", gateway.cfg.CompatibilityID)
	for name := range header {
		request.Header.Set(name, header.Get(name))
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(directtransfer.Redact(err))
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestHostedGatewayRedirectsStockTurboReadsToObjectStorage(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	gateway := newDirectGateway(t, config.DirectTransfer{Read: true, TurboRedirect: true, MaxURLSeconds: 60})
	writer := gateway.token(t, "writer", access.CapabilityWrite, time.Hour)
	reader := gateway.token(t, "reader", access.CapabilityRead, time.Hour)
	payload := bytes.Repeat([]byte("stock turbo read"), 2048)
	if response := gateway.do(t, http.MethodPut, "/v8/artifacts/seeded", writer, payload, stockTurbo); response.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d, want the proxied upload", response.StatusCode)
	}

	redirect := gateway.getWithoutFollowing(t, http.MethodGet, "/v8/artifacts/seeded", reader, stockTurbo)
	location := redirect.Header.Get("Location")
	if redirect.StatusCode != http.StatusFound || !strings.Contains(location, "X-Amz-Signature") {
		t.Fatalf("stock Turbo GET = %d, redirected: %v", redirect.StatusCode, location != "")
	}
	if redirect.Header.Get("Cache-Control") != "no-store" || !strings.Contains(strings.Join(redirect.Header.Values("Vary"), ","), "User-Agent") {
		t.Fatalf("redirect caching headers = %q, Vary %q", redirect.Header.Get("Cache-Control"), redirect.Header.Values("Vary"))
	}
	object, err := http.Get(location)
	if err != nil {
		t.Fatal(directtransfer.Redact(err))
	}
	body, _ := io.ReadAll(object.Body)
	object.Body.Close()
	if object.StatusCode != http.StatusOK || !bytes.Equal(body, payload) {
		t.Fatalf("presigned GET = %d, %d bytes", object.StatusCode, len(body))
	}

	for name, header := range map[string]http.Header{
		"no user agent":     {"User-Agent": {""}},
		"another client":    {"User-Agent": {"Go-http-client/1.1"}},
		"untested Turbo":    {"User-Agent": {"turbo 2.9.13 1.95.0-nightly linux x86_64"}},
		"pre-release Turbo": {"User-Agent": {"turbo 2.10.12-canary.1 1.97.0 linux x86_64"}},
	} {
		response := gateway.getWithoutFollowing(t, http.MethodGet, "/v8/artifacts/seeded", reader, header)
		proxied, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK || !bytes.Equal(proxied, payload) {
			t.Fatalf("%s GET = %d, %d bytes, want the proxied read", name, response.StatusCode, len(proxied))
		}
	}
	descriptor := decodeDescriptor(t, gateway.do(t, http.MethodGet, "/v8/artifacts/seeded", reader, nil, http.Header{
		"Accept": {directtransfer.MediaType}, "User-Agent": {stockTurbo.Get("User-Agent")},
	}))
	if descriptor.Lease == "" {
		t.Fatal("an opted-in client lost its descriptor")
	}

	probe := gateway.getWithoutFollowing(t, http.MethodHead, "/v8/artifacts/seeded", reader, stockTurbo)
	if probe.StatusCode != http.StatusOK || probe.Header.Get("Location") != "" ||
		probe.Header.Get("Content-Length") != strconv.Itoa(len(payload)) {
		t.Fatalf("stock Turbo HEAD = %d, Location set: %v", probe.StatusCode, probe.Header.Get("Location") != "")
	}
	if miss := gateway.getWithoutFollowing(t, http.MethodGet, "/v8/artifacts/absent", reader, stockTurbo); miss.StatusCode != http.StatusNotFound {
		t.Fatalf("stock Turbo miss = %d", miss.StatusCode)
	}

	for _, leaked := range []string{"X-Amz-Signature", "X-Amz-Credential", gateway.cfg.CloudS3SecretKey} {
		if strings.Contains(logs.String(), leaked) {
			t.Fatalf("gateway logs contain presigned URL material %q", leaked)
		}
	}
}

func TestSignedTurboArtifactsStayProxied(t *testing.T) {
	gateway := newDirectGateway(t, config.DirectTransfer{Read: true, TurboRedirect: true})
	writer := gateway.token(t, "writer", access.CapabilityWrite, time.Hour)
	payload := []byte("signed artifact")
	upload := http.Header{"X-Artifact-Tag": {"signature"}, "User-Agent": {stockTurbo.Get("User-Agent")}}
	if response := gateway.do(t, http.MethodPut, "/v8/artifacts/signed", writer, payload, upload); response.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d", response.StatusCode)
	}
	response := gateway.getWithoutFollowing(t, http.MethodGet, "/v8/artifacts/signed", writer, stockTurbo)
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !bytes.Equal(body, payload) || response.Header.Get("x-artifact-tag") != "signature" {
		t.Fatalf("signed GET = %d, %d bytes, tag %q", response.StatusCode, len(body), response.Header.Get("x-artifact-tag"))
	}
}

func TestUploadAfterATurboRedirectProxiesTheNextRead(t *testing.T) {
	gateway := newDirectGateway(t, config.DirectTransfer{Read: true, TurboRedirect: true})
	writer := gateway.token(t, "writer", access.CapabilityWrite, time.Hour)
	payload := []byte("failed restore")
	upload := func(step string) {
		t.Helper()
		response := gateway.do(t, http.MethodPut, "/v8/artifacts/failed", writer, payload, stockTurbo)
		if response.StatusCode != http.StatusOK {
			message, _ := io.ReadAll(response.Body)
			t.Fatalf("%s PUT = %d: %s", step, response.StatusCode, message)
		}
	}
	upload("seeding")
	if first := gateway.getWithoutFollowing(t, http.MethodGet, "/v8/artifacts/failed", writer, stockTurbo); first.StatusCode != http.StatusFound {
		message, _ := io.ReadAll(first.Body)
		t.Fatalf("first GET = %d: %s", first.StatusCode, message)
	}
	upload("failure-signalling")
	second := gateway.getWithoutFollowing(t, http.MethodGet, "/v8/artifacts/failed", writer, stockTurbo)
	body, _ := io.ReadAll(second.Body)
	if second.StatusCode != http.StatusOK || !bytes.Equal(body, payload) {
		t.Fatalf("GET after a suspected failure = %d, %d bytes (%q), want the verified proxied read", second.StatusCode, len(body), body)
	}
}

func TestTurboRedirectStaysOffWithoutItsSwitch(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T) config.DirectTransfer{
		"read only": func(*testing.T) config.DirectTransfer { return config.DirectTransfer{Read: true} },
		"environment override": func(t *testing.T) config.DirectTransfer {
			t.Setenv("LAYER_CACHE_TURBO_REDIRECT", "off")
			return config.DirectTransfer{Read: true, TurboRedirect: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			gateway := newDirectGateway(t, setup(t))
			writer := gateway.token(t, "writer", access.CapabilityWrite, time.Hour)
			payload := []byte("stays proxied")
			gateway.do(t, http.MethodPut, "/v8/artifacts/proxied", writer, payload, stockTurbo)
			response := gateway.getWithoutFollowing(t, http.MethodGet, "/v8/artifacts/proxied", writer, stockTurbo)
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != http.StatusOK || !bytes.Equal(body, payload) ||
				strings.Contains(strings.Join(response.Header.Values("Vary"), ","), "User-Agent") {
				t.Fatalf("GET = %d, %d bytes, Vary %q", response.StatusCode, len(body), response.Header.Values("Vary"))
			}
		})
	}
}
