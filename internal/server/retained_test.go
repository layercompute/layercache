package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/retained"
)

type retainedAdapterFixture struct {
	receipt                    retained.Receipt
	content                    []byte
	reserveCalls, publishCalls int
	err                        error
}

func (f *retainedAdapterFixture) ReserveRetained(_ context.Context, d retained.Declaration) (retained.Receipt, error) {
	f.reserveCalls++
	f.receipt.Declaration = d
	f.receipt.ID = retained.ID(d)
	return f.receipt, f.err
}
func (f *retainedAdapterFixture) InspectRetained(_ context.Context, id string) (retained.Receipt, error) {
	if id != f.receipt.ID {
		return retained.Receipt{}, artifact.ErrNotFound
	}
	return f.receipt, f.err
}
func (f *retainedAdapterFixture) PublishRetained(_ context.Context, _ string, r io.Reader) (retained.Receipt, error) {
	f.publishCalls++
	data, err := io.ReadAll(r)
	if err != nil {
		return retained.Receipt{}, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != f.receipt.Declaration.Size || hex.EncodeToString(sum[:]) != f.receipt.Declaration.Digest {
		return retained.Receipt{}, artifact.ErrCorrupt
	}
	f.content = data
	f.receipt.State = retained.Published
	return f.receipt, f.err
}
func (f *retainedAdapterFixture) OpenRetained(_ context.Context, id string) (retained.Receipt, io.ReadCloser, error) {
	if f.err != nil {
		return retained.Receipt{}, nil, f.err
	}
	return f.receipt, io.NopCloser(bytes.NewReader(f.content)), nil
}

func retainedTestClaims() access.Claims {
	return access.Claims{Subject: "github-actions:repo:acme/widget:pull_request", Project: "widget", Integration: "retained", Repository: "acme/widget", RepositoryID: "123", SourceCommit: strings.Repeat("a", 40),
		WorkflowRef: "acme/widget/.github/workflows/qa.yml@refs/pull/7/merge", ActionsRunID: "234", RunAttempt: "2", CheckRunID: "345", ActionsActorID: "456", ActionsActor: "alice", Compatibility: "android-x86-qa-v1", Capabilities: []access.Capability{access.CapabilityWrite}, ExpiresAt: time.Now().UTC().Add(15 * time.Minute)}
}
func retainedTestServer(t *testing.T) (*Server, *retainedAdapterFixture) {
	t.Helper()
	cfg := config.Config{Version: 1, Role: "team", DataDir: t.TempDir(), Listen: "127.0.0.1:7437", MaxBytes: 1 << 20, ProjectID: "widget", LocalToken: "secret", CompatibilityID: "server-host-v1", ActionsRepository: "acme/widget", ActionsRef: "refs/heads/main", ActionsDefaultRef: "refs/heads/main", TeamMembers: map[string]string{"alice": "writer"}}
	s, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.config.RetainedArtifactsEnabled = true
	content := []byte("verified required transfer")
	sum := sha256.Sum256(content)
	d := retained.Declaration{Producer: retainedProducer(retainedTestClaims()), Name: "qa-apk", Digest: hex.EncodeToString(sum[:]), Size: int64(len(content)), RetainUntil: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)}
	f := &retainedAdapterFixture{receipt: retained.Receipt{SchemaVersion: retained.SchemaVersion, ID: retained.ID(d), State: retained.Published, Declaration: d, ReservedUntil: time.Now().UTC().Add(15 * time.Minute)}, content: content}
	s.retainedStore = f
	return s, f
}
func retainedRequest(t *testing.T, s *Server, method, path string, claims access.Claims, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	token, err := access.MintCapabilityToken(s.config.LocalToken, claims, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestRetainedAdapterAuthorityAndRequiredContent(t *testing.T) {
	s, f := retainedTestServer(t)
	producer := retainedTestClaims()
	reader := producer
	reader.CheckRunID = "999"
	reader.Capabilities = []access.Capability{access.CapabilityRead}
	base := "/v1/retained-artifacts/" + f.receipt.ID
	for _, path := range []string{base, base + "/content"} {
		if r := retainedRequest(t, s, "GET", path, reader, nil); r.Code != 200 {
			t.Fatalf("reader %s: %d %s", path, r.Code, r.Body.String())
		}
	}
	rerunReader := reader
	rerunReader.RunAttempt = "3"
	for _, path := range []string{base, base + "/content"} {
		if w := retainedRequest(t, s, "GET", path, rerunReader, nil); w.Code != 200 {
			t.Fatalf("consumer-only rerun of exact receipt=%d %s", w.Code, w.Body.String())
		}
	}
	for _, change := range []struct {
		name string
		edit func(*access.Claims)
	}{
		{"project", func(c *access.Claims) { c.Project = "other" }}, {"repository", func(c *access.Claims) { c.Repository = "acme/foreign" }}, {"repository identity", func(c *access.Claims) { c.RepositoryID = "999" }},
		{"source", func(c *access.Claims) { c.SourceCommit = strings.Repeat("b", 40) }}, {"workflow", func(c *access.Claims) { c.WorkflowRef = "acme/widget/.github/workflows/other.yml@refs/pull/7/merge" }},
		{"run", func(c *access.Claims) { c.ActionsRunID = "999" }}, {"future producer attempt", func(c *access.Claims) { c.RunAttempt = "1" }}, {"profile", func(c *access.Claims) { c.Compatibility = "another-profile" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			c := reader
			change.edit(&c)
			w := retainedRequest(t, s, "GET", base+"/content", c, nil)
			if w.Code != 404 && w.Code != 401 {
				t.Fatalf("foreign content accepted: %d", w.Code)
			}
		})
	}
	if w := retainedRequest(t, s, "PUT", base+"/content", reader, f.content); w.Code != 403 {
		t.Fatalf("reader publication=%d", w.Code)
	}
	wrongJob := producer
	wrongJob.CheckRunID = "777"
	if w := retainedRequest(t, s, "PUT", base+"/content", wrongJob, f.content); w.Code != 404 {
		t.Fatalf("foreign producer job=%d", w.Code)
	}
	f.receipt.State = retained.Reserved
	rerunProducer := producer
	rerunProducer.RunAttempt = "3"
	if w := retainedRequest(t, s, "PUT", base+"/content", rerunProducer, f.content); w.Code != 404 {
		t.Fatalf("later attempt mutated prior producer receipt: %d", w.Code)
	}
	if w := retainedRequest(t, s, "PUT", base+"/content", producer, bytes.Repeat([]byte("x"), len(f.content))); w.Code != 422 {
		t.Fatalf("corrupt publication=%d %s", w.Code, w.Body.String())
	}
	if w := retainedRequest(t, s, "PUT", base+"/content", producer, f.content[:3]); w.Code != 400 {
		t.Fatalf("partial publication=%d", w.Code)
	}
	if w := retainedRequest(t, s, "PUT", base+"/content", producer, f.content); w.Code != 200 {
		t.Fatalf("publication=%d %s", w.Code, w.Body.String())
	}
	if f.publishCalls != 2 {
		t.Fatalf("publication body reached storage %d times", f.publishCalls)
	}
	if w := retainedRequest(t, s, "PUT", base+"/content", producer, f.content); w.Code != 200 || f.publishCalls != 2 {
		t.Fatalf("published receipt duplicated content: HTTP%d publications=%d", w.Code, f.publishCalls)
	}
	s.config.TeamMembers["alice"] = "reader"
	if w := retainedRequest(t, s, "PUT", base+"/content", producer, f.content); w.Code != 403 {
		t.Fatalf("removed write role=%d", w.Code)
	}
	delete(s.config.TeamMembers, "alice")
	if w := retainedRequest(t, s, "GET", base+"/content", reader, nil); w.Code != 401 {
		t.Fatalf("removed membership=%d", w.Code)
	}
}

func TestRetainedPublisherReadsOnlyOwnReceipt(t *testing.T) {
	s, f := retainedTestServer(t)
	producer := retainedTestClaims()
	otherJob := producer
	otherJob.CheckRunID = "999"
	laterAttempt := producer
	laterAttempt.RunAttempt = "3"
	reader := otherJob
	reader.Capabilities = []access.Capability{access.CapabilityRead}
	rerunReader := reader
	rerunReader.RunAttempt = "3"
	for _, test := range []struct {
		name   string
		claims access.Claims
		status int
	}{
		{"publisher inspects own publication", producer, http.StatusOK},
		{"publisher cannot inspect another job", otherJob, http.StatusNotFound},
		{"publisher cannot inspect an earlier attempt", laterAttempt, http.StatusNotFound},
		{"read-only consumer reads another job", reader, http.StatusOK},
		{"read-only rerun reads explicit earlier receipt", rerunReader, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, suffix := range []string{"", "/content"} {
				w := retainedRequest(t, s, http.MethodGet, "/v1/retained-artifacts/"+f.receipt.ID+suffix, test.claims, nil)
				if w.Code != test.status {
					t.Fatalf("GET %s: HTTP %d, want %d", suffix, w.Code, test.status)
				}
			}
		})
	}
}

func TestRetainedAdapterReservationAdmissionAndProtocolIsolation(t *testing.T) {
	s, f := retainedTestServer(t)
	claims := retainedTestClaims()
	d := f.receipt.Declaration
	body, _ := json.Marshal(d)
	f.err = artifact.ErrQuota
	if w := retainedRequest(t, s, "POST", "/v1/retained-artifacts", claims, body); w.Code != 507 {
		t.Fatalf("capacity rejection=%d %s", w.Code, w.Body.String())
	}
	f.err = nil
	if w := retainedRequest(t, s, "POST", "/v1/retained-artifacts", claims, body); w.Code != 200 {
		t.Fatalf("reserve=%d %s", w.Code, w.Body.String())
	}
	s.storagePool = StoragePoolConfig{Path: filepath.Join(t.TempDir(), "missing")}
	if w := retainedRequest(t, s, "POST", "/v1/retained-artifacts", claims, body); w.Code != 507 {
		t.Fatalf("physical admission=%d", w.Code)
	}
	s.storagePool = StoragePoolConfig{}
	oversize := d
	oversize.Size = math.MaxInt64
	oversizeBody, _ := json.Marshal(oversize)
	if w := retainedRequest(t, s, "POST", "/v1/retained-artifacts", claims, oversizeBody); w.Code != 413 {
		t.Fatalf("overflow admission=%d", w.Code)
	}
	d.Producer.JobID = "777"
	foreign, _ := json.Marshal(d)
	if w := retainedRequest(t, s, "POST", "/v1/retained-artifacts", claims, foreign); w.Code != 403 {
		t.Fatalf("spoofed signed producer=%d", w.Code)
	}
	if f.reserveCalls != 2 {
		t.Fatalf("rejected producer reached admission: %d", f.reserveCalls)
	}
	if w := retainedRequest(t, s, "PUT", "/v8/artifacts/foreign", claims, []byte("x")); w.Code != 401 {
		t.Fatalf("retained credential escaped protocol=%d", w.Code)
	}
	claims.Integration = "turbo"
	if w := retainedRequest(t, s, "GET", "/v1/retained-artifacts/"+f.receipt.ID, claims, nil); w.Code != 401 {
		t.Fatalf("cache credential escaped protocol=%d", w.Code)
	}
	r := httptest.NewRequest("GET", "/v1/retained-artifacts/"+f.receipt.ID, nil)
	r.Header.Set("Authorization", "Bearer "+s.config.LocalToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("unscoped administrator obtained producer authority: %d", w.Code)
	}
	s.retainedStore = nil
	claims = retainedTestClaims()
	if w := retainedRequest(t, s, "POST", "/v1/retained-artifacts", claims, body); w.Code != 404 {
		t.Fatalf("local acknowledgement=%d", w.Code)
	}
}

type stalledRetainedBody struct {
	entered, closed chan struct{}
	once, closeOnce sync.Once
}

func (body *stalledRetainedBody) Read([]byte) (int, error) {
	body.once.Do(func() { close(body.entered) })
	<-body.closed
	return 0, io.ErrClosedPipe
}
func (body *stalledRetainedBody) Close() error {
	body.closeOnce.Do(func() { close(body.closed) })
	return nil
}

func TestRetainedUploadCancelsStalledBodyAtReservationDeadline(t *testing.T) {
	s, f := retainedTestServer(t)
	f.receipt.State = retained.Reserved
	f.receipt.ReservedUntil = time.Now().UTC().Add(100 * time.Millisecond)
	claims := retainedTestClaims()
	token, err := access.MintCapabilityToken(s.config.LocalToken, claims, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body := &stalledRetainedBody{entered: make(chan struct{}), closed: make(chan struct{})}
	defer body.Close()
	r := httptest.NewRequest("PUT", "/v1/retained-artifacts/"+f.receipt.ID+"/content", body)
	r.ContentLength = f.receipt.Declaration.Size
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.Handler().ServeHTTP(w, r) }()
	select {
	case <-body.entered:
	case <-time.After(time.Second):
		t.Fatal("upload body was not read")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled body ignored reservation deadline")
	}
	if w.Code != 503 || len(f.content) != len("verified required transfer") || f.receipt.State != retained.Reserved {
		t.Fatalf("canceled upload acknowledged or committed: HTTP%d state%s", w.Code, f.receipt.State)
	}
}

func TestRetainedOIDCExchangeEligibility(t *testing.T) {
	s, _ := retainedTestServer(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"keys": []map[string]string{{"kid": "retained", "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	defer issuer.Close()
	s.config.GitHubOIDCIssuer = issuer.URL
	var foreignHead, wrongAttempt bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widget/actions/runs/234" || r.Header.Get("Authorization") != "Bearer ephemeral-job-token" {
			t.Errorf("unexpected metadata request: %s", r.URL.Path)
			http.Error(w, "forbidden", 403)
			return
		}
		head, attempt := 123, 2
		if foreignHead {
			head = 999
		}
		if wrongAttempt {
			attempt = 3
		}
		writeJSON(w, 200, map[string]any{"id": 234, "run_attempt": attempt, "event": "pull_request", "repository": map[string]any{"id": 123}, "head_repository": map[string]any{"id": head}, "head_sha": strings.Repeat("b", 40)})
	}))
	defer api.Close()
	s.config.GitHubAPIURL = api.URL
	call := func(event, mode string, expired bool) *httptest.ResponseRecorder {
		now := time.Now().UTC()
		expiry := now.Add(5 * time.Minute)
		if expired {
			expiry = now.Add(-time.Minute)
		}
		ref := "refs/pull/7/merge"
		if event == "push" || event == "workflow_dispatch" {
			ref = "refs/heads/main"
		}
		id := signOIDCTestToken(t, key, "retained", map[string]any{"iss": issuer.URL, "aud": "layercache:widget", "iat": now.Add(-time.Minute).Unix(), "exp": expiry.Unix(), "sub": "repo:acme/widget:pull_request", "repository": "acme/widget", "repository_id": "123", "actor": "alice", "actor_id": "456", "ref": ref, "sha": strings.Repeat("a", 40), "workflow_ref": "acme/widget/.github/workflows/qa.yml@" + ref, "run_id": "234", "run_attempt": "2", "check_run_id": "345", "event_name": event})
		body, _ := json.Marshal(map[string]any{"project": "widget", "integration": "retained", "mode": mode, "compatibility": "android-x86-qa-v1", "idToken": id, "githubToken": "ephemeral-job-token"})
		r := httptest.NewRequest("POST", "/v1/auth/github-oidc/exchange", bytes.NewReader(body))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	for _, event := range []string{"push", "workflow_dispatch"} {
		if w := call(event, "publish", false); w.Code != 200 {
			t.Fatalf("eligible %s publisher=%d %s", event, w.Code, w.Body.String())
		}
	}
	for _, mode := range []string{"publish", "read"} {
		w := call("pull_request", mode, false)
		if w.Code != 200 {
			t.Fatalf("same repo %s=%d %s", mode, w.Code, w.Body.String())
		}
		var out struct {
			Token    string            `json:"token"`
			Producer retained.Producer `json:"producer"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		claims, err := access.ParseCapabilityToken(s.config.LocalToken, out.Token, time.Now())
		if err != nil || claims.Integration != "retained" || claims.Allows(access.CapabilityWrite) != (mode == "publish") || out.Producer.JobID != "345" || out.Producer.SourceCommit != strings.Repeat("a", 40) {
			t.Fatalf("wrong signed scope: %+v %v", claims, err)
		}
	}
	foreignHead = true
	if w := call("pull_request", "publish", false); w.Code != 401 {
		t.Fatalf("fork=%d", w.Code)
	}
	foreignHead = false
	wrongAttempt = true
	if w := call("pull_request", "read", false); w.Code != 401 {
		t.Fatalf("old attempt=%d", w.Code)
	}
	wrongAttempt = false
	for _, event := range []string{"pull_request_target", "workflow_run", "schedule", "unknown"} {
		if w := call(event, "publish", false); w.Code != 401 {
			t.Fatalf("event %s=%d", event, w.Code)
		}
	}
	if w := call("pull_request", "publish", true); w.Code != 401 {
		t.Fatalf("expired token=%d", w.Code)
	}
	s.config.RetainedArtifactsEnabled = false
	if w := call("pull_request", "publish", false); w.Code != 404 {
		t.Fatalf("disabled retained exchange=%d", w.Code)
	}
	s.config.RetainedArtifactsEnabled = true
	s.config.TeamMembers["alice"] = "reader"
	if w := call("pull_request", "publish", false); w.Code != 401 {
		t.Fatalf("reader publisher=%d", w.Code)
	}
	if w := call("pull_request", "read", false); w.Code != 200 {
		t.Fatalf("member reader=%d %s", w.Code, w.Body.String())
	}
}

func TestRetainedErrorRedactsBackendDetails(t *testing.T) {
	w := httptest.NewRecorder()
	writeRetainedError(w, errors.New("s3://secret:credential/private-bucket"))
	if w.Code != 503 || strings.Contains(w.Body.String(), "credential") {
		t.Fatalf("leaked backend error: %s", w.Body.String())
	}
}
