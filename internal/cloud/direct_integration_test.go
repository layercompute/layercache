package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
)

type directFixture struct {
	*actionsHTTPFixture
	key artifact.Key
}

func newDirectFixture(t *testing.T, quota int64) *directFixture {
	t.Helper()
	if os.Getenv("LAYER_CACHE_QA_POSTGRES_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	fixture := &directFixture{actionsHTTPFixture: newActionsHTTPFixture(t, quota)}
	fixture.store.config.DirectTransfer = DirectTransferPolicy{Read: true, Write: true, MaxURL: 2 * time.Minute}
	fixture.key = artifact.Key{
		Integration: "turbo", Project: fixture.store.config.Project, Compatibility: "linux-x64-node24", Native: "task-hash",
	}
	return fixture
}

func (fixture *directFixture) clock() time.Time { return time.Unix(0, fixture.now.Load()).UTC() }

func (fixture *directFixture) as(subject string, expires time.Duration) context.Context {
	return access.WithClaims(context.Background(), access.Claims{
		Subject: subject, Project: fixture.store.config.Project, ExpiresAt: fixture.clock().Add(expires),
		Capabilities: []access.Capability{access.CapabilityWrite},
	})
}

func (fixture *directFixture) put(t *testing.T, key artifact.Key, metadata artifact.Metadata, payload []byte) artifact.Entry {
	t.Helper()
	entry, _, err := fixture.store.Put(context.Background(), key, metadata, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func fetchURL(t *testing.T, method, location string, header http.Header, body []byte) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, location, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name := range header {
		request.Header.Set(name, header.Get(name))
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, data
}

func TestDirectReadSignsOnlyTheCommittedObjectAndCompletesOnce(t *testing.T) {
	fixture := newDirectFixture(t, 1<<20)
	payload := bytes.Repeat([]byte("direct read"), 512)
	entry := fixture.put(t, fixture.key, artifact.Metadata{DurationMS: 7}, payload)
	ctx := fixture.as("github:reader", time.Hour)

	read, err := fixture.store.IssueDirectRead(ctx, fixture.key, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if read.URL.Path != "/cache/"+fixture.store.blobKey(entry.Digest) || !strings.HasPrefix(fixture.store.blobKey(entry.Digest), "projects/"+fixture.store.namespace+"/blobs/") {
		t.Fatalf("descriptor signs %q, want the committed content-addressed object", read.URL.Path)
	}
	if read.URL.Query().Get("X-Amz-Signature") == "" || read.URL.Query().Get("X-Amz-Expires") != "120" {
		t.Fatalf("descriptor URL is not a 120 second presigned GET: %v", read.URL.Query())
	}
	if !read.ExpiresAt.Equal(fixture.clock().Add(2*time.Minute)) || read.Entry.Digest != entry.Digest || read.Entry.Size != entry.Size {
		t.Fatalf("descriptor expires %v, entry %+v", read.ExpiresAt, read.Entry)
	}
	var principal string
	var leaseExpires time.Time
	if err := fixture.store.database.QueryRow(`SELECT principal, expires_at FROM layercache_blob_read_leases_v1 WHERE lease_id = $1`, read.Lease).
		Scan(&principal, &leaseExpires); err != nil {
		t.Fatal(err)
	}
	if principal != "github:reader" || !leaseExpires.Equal(read.ExpiresAt.Add(readLeaseTTL)) {
		t.Fatalf("lease principal %q expires %v", principal, leaseExpires)
	}
	if status, data := fetchURL(t, http.MethodGet, read.URL.String(), nil, nil); status != http.StatusOK || !bytes.Equal(data, payload) {
		t.Fatalf("presigned GET = %d, %d bytes", status, len(data))
	}

	other := fixture.key
	other.Native = "other-task"
	fixture.put(t, other, artifact.Metadata{}, []byte("other"))
	for name, attempt := range map[string]func() error{
		"other principal": func() error {
			return fixture.store.CompleteDirectRead(fixture.as("github:intruder", time.Hour), fixture.key, read.Lease, entry.Digest, entry.Size)
		},
		"other entry": func() error {
			return fixture.store.CompleteDirectRead(ctx, other, read.Lease, entry.Digest, entry.Size)
		},
		"wrong digest": func() error {
			return fixture.store.CompleteDirectRead(ctx, fixture.key, read.Lease, strings.Repeat("0", 64), entry.Size)
		},
		"wrong size": func() error {
			return fixture.store.CompleteDirectRead(ctx, fixture.key, read.Lease, entry.Digest, entry.Size+1)
		},
		"unknown lease": func() error {
			return fixture.store.CompleteDirectRead(ctx, fixture.key, "read-unknown", entry.Digest, entry.Size)
		},
		"anonymous": func() error {
			return fixture.store.CompleteDirectRead(context.Background(), fixture.key, read.Lease, entry.Digest, entry.Size)
		},
	} {
		if err := attempt(); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s completion = %v, want not found", name, err)
		}
	}
	readCount := func() int {
		var count int
		_ = fixture.store.database.QueryRow(`SELECT COALESCE((SELECT read_count FROM layercache_artifact_access_v1 WHERE project_id = $1 AND digest = $2), 0)`,
			fixture.store.config.Project, entry.Digest).Scan(&count)
		return count
	}
	if readCount() != 0 {
		t.Fatal("issuing or rejected completions counted a verified read")
	}
	if err := fixture.store.CompleteDirectRead(ctx, fixture.key, read.Lease, "sha256:"+entry.Digest, entry.Size); err != nil {
		t.Fatal(err)
	}
	if readCount() != 1 {
		t.Fatal("completion did not record the verified read")
	}
	if err := fixture.store.CompleteDirectRead(ctx, fixture.key, read.Lease, entry.Digest, entry.Size); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replayed completion = %v", err)
	}
	if readCount() != 1 {
		t.Fatal("a replayed completion counted a second read")
	}
}

func TestDirectReadExpiryAndFallbackConditions(t *testing.T) {
	fixture := newDirectFixture(t, 1<<20)
	entry := fixture.put(t, fixture.key, artifact.Metadata{}, []byte("expiry"))
	expires := func(ctx context.Context, notAfter time.Time) (time.Duration, error) {
		read, err := fixture.store.IssueDirectRead(ctx, fixture.key, notAfter)
		if err != nil {
			return 0, err
		}
		ttl := read.ExpiresAt.Sub(fixture.clock())
		if read.URL.Query().Get("X-Amz-Expires") != strconv.Itoa(int(ttl/time.Second)) {
			t.Fatalf("signed expiry %s does not match descriptor %v", read.URL.Query().Get("X-Amz-Expires"), ttl)
		}
		return ttl, nil
	}
	if got, err := expires(fixture.as("github:reader", 30*time.Second), time.Time{}); err != nil || got != 30*time.Second {
		t.Fatalf("token-capped expiry = %v, %v", got, err)
	}
	if got, err := expires(fixture.as("github:reader", time.Hour), fixture.clock().Add(10500*time.Millisecond)); err != nil || got != 10*time.Second {
		t.Fatalf("archive-capped expiry = %v, %v", got, err)
	}
	fixture.store.config.DirectTransfer.MaxURL = 45 * time.Second
	if got, err := expires(fixture.as("github:reader", time.Hour), time.Time{}); err != nil || got != 45*time.Second {
		t.Fatalf("policy-capped expiry = %v, %v", got, err)
	}
	if _, err := expires(fixture.as("github:reader", 500*time.Millisecond), time.Time{}); !errors.Is(err, ErrDirectTransferUnavailable) {
		t.Fatalf("an almost expired token got a descriptor: %v", err)
	}
	if _, err := expires(context.Background(), time.Time{}); !errors.Is(err, ErrDirectTransferUnavailable) {
		t.Fatalf("an anonymous read got a descriptor: %v", err)
	}

	ctx := fixture.as("github:reader", time.Hour)
	leases := func() int {
		var count int
		_ = fixture.store.database.QueryRow(`SELECT COUNT(*) FROM layercache_blob_read_leases_v1 WHERE project_id = $1 AND digest = $2 AND principal IS NOT NULL AND expires_at > $3`,
			fixture.store.config.Project, entry.Digest, fixture.clock().Add(time.Hour)).Scan(&count)
		return count
	}
	before := leases()
	fixture.store.config.DirectTransfer.Read = false
	if _, err := fixture.store.IssueDirectRead(ctx, fixture.key, time.Time{}); !errors.Is(err, ErrDirectTransferUnavailable) {
		t.Fatalf("switch off = %v", err)
	}
	fixture.store.config.DirectTransfer.Read = true
	blobs := fixture.store.blobs.(*s3BlobStore)
	blobs.presign = false
	if _, err := fixture.store.IssueDirectRead(ctx, fixture.key, time.Time{}); !errors.Is(err, ErrDirectTransferUnavailable) {
		t.Fatalf("no static credentials = %v", err)
	}
	blobs.presign = true
	public := fixture.key
	public.Native = "public-origin"
	fixture.put(t, public, artifact.Metadata{Values: map[string]string{"origin": "public"}}, []byte("public bytes"))
	if _, err := fixture.store.IssueDirectRead(ctx, public, time.Time{}); !errors.Is(err, ErrDirectTransferUnavailable) {
		t.Fatalf("Public-origin entry = %v", err)
	}
	if leases() != before {
		t.Fatal("a refused descriptor left its lease behind")
	}
	missing := fixture.key
	missing.Native = "missing"
	if _, err := fixture.store.IssueDirectRead(ctx, missing, time.Time{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing entry = %v", err)
	}
	foreign := fixture.key
	foreign.Project = "github.com/other/project"
	if _, err := fixture.store.IssueDirectRead(ctx, foreign, time.Time{}); err == nil {
		t.Fatal("a key outside the configured project got a descriptor")
	}
}

func TestDirectReadLeaseProtectsTheObjectPastBlobGrace(t *testing.T) {
	fixture := newDirectFixture(t, 1<<20)
	fixture.store.config.BlobGrace = time.Minute
	payload := bytes.Repeat([]byte{0x5a}, 4096)
	entry := fixture.put(t, fixture.key, artifact.Metadata{}, payload)
	read, err := fixture.store.IssueDirectRead(fixture.as("github:reader", time.Hour), fixture.key, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Delete(context.Background(), fixture.key); err != nil {
		t.Fatal(err)
	}
	// Past the blob grace and the URL expiry, but within the lease allowance
	// for a transfer that started before the URL expired.
	fixture.now.Add(int64(10 * time.Minute))
	if _, err := fixture.store.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.blobs.Stat(context.Background(), fixture.store.blobKey(entry.Digest)); err != nil {
		t.Fatalf("an object with a live direct lease was collected: %v", err)
	}
	fixture.now.Add(int64(read.ExpiresAt.Add(readLeaseTTL).Sub(fixture.clock()) + time.Second))
	if _, err := fixture.store.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.blobs.Stat(context.Background(), fixture.store.blobKey(entry.Digest)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the object outlived its expired lease: %v", err)
	}
}

func TestDirectUploadAdmitsCommitsAndRejects(t *testing.T) {
	fixture := newDirectFixture(t, 64<<10)
	writer := fixture.as("github:writer", time.Hour)
	payload := bytes.Repeat([]byte("direct write"), 1024)
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	if _, err := fixture.store.BeginDirectUpload(writer, fixture.key, digest, 64<<10+1); !errors.Is(err, ErrQuota) {
		t.Fatalf("over-quota declaration = %v", err)
	}
	if _, err := fixture.store.BeginDirectUpload(writer, fixture.key, "not-a-digest", 1); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("invalid declaration = %v", err)
	}
	fixture.store.config.DirectTransfer.Write = false
	if _, err := fixture.store.BeginDirectUpload(writer, fixture.key, digest, int64(len(payload))); !errors.Is(err, ErrDirectTransferUnavailable) {
		t.Fatalf("write switch off = %v", err)
	}
	fixture.store.config.DirectTransfer.Write = true

	upload, err := fixture.store.BeginDirectUpload(writer, fixture.key, digest, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(upload.URL.Path, "/projects/"+fixture.store.namespace+"/staging/") || upload.Header.Get("x-amz-checksum-sha256") == "" {
		t.Fatalf("upload signs %q with headers %v", upload.URL.Path, upload.Header)
	}
	tampered := append([]byte(nil), payload...)
	tampered[0] ^= 1
	if status, _ := fetchURL(t, http.MethodPut, upload.URL.String(), upload.Header, tampered); status != http.StatusBadRequest {
		t.Fatalf("tampered PUT = %d, want BadDigest", status)
	}
	if status, _ := fetchURL(t, http.MethodPut, upload.URL.String(), nil, payload); status != http.StatusForbidden {
		t.Fatalf("PUT without the signed checksum = %d", status)
	}
	if status, _ := fetchURL(t, http.MethodPut, upload.URL.String(), upload.Header, payload); status != http.StatusOK {
		t.Fatalf("matching PUT = %d", status)
	}
	if _, _, err := fixture.store.CommitDirectUpload(fixture.as("github:intruder", time.Hour), fixture.key, artifact.Metadata{}, upload.Upload); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-principal commit = %v", err)
	}
	other := fixture.key
	other.Native = "other-task"
	if _, _, err := fixture.store.CommitDirectUpload(writer, other, artifact.Metadata{}, upload.Upload); !errors.Is(err, ErrNotFound) {
		t.Fatalf("commit against another entry = %v", err)
	}
	entry, created, err := fixture.store.CommitDirectUpload(writer, fixture.key, artifact.Metadata{DurationMS: 9}, upload.Upload)
	if err != nil || !created || entry.Digest != digest || entry.Size != int64(len(payload)) {
		t.Fatalf("commit = %+v, %v, %v", entry, created, err)
	}
	if _, _, err := fixture.store.CommitDirectUpload(writer, fixture.key, artifact.Metadata{}, upload.Upload); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replayed commit = %v", err)
	}
	stored, body, err := fixture.store.Get(context.Background(), fixture.key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	body.Close()
	if err != nil || !bytes.Equal(data, payload) || stored.Metadata.DurationMS != 9 {
		t.Fatalf("committed direct upload reads %d bytes, %v", len(data), err)
	}
	if _, err := fixture.store.blobs.Stat(context.Background(), strings.TrimPrefix(upload.URL.Path, "/cache/")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("staging object survived commit: %v", err)
	}

	// A declared size that does not match the staged object fails commit and
	// discards the staging object.
	mismatch := fixture.key
	mismatch.Native = "size-mismatch"
	upload, err = fixture.store.BeginDirectUpload(writer, mismatch, digest, int64(len(payload))+1)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := fetchURL(t, http.MethodPut, upload.URL.String(), upload.Header, payload); status != http.StatusOK {
		t.Fatalf("PUT = %d", status)
	}
	if _, _, err := fixture.store.CommitDirectUpload(writer, mismatch, artifact.Metadata{}, upload.Upload); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("size mismatch commit = %v", err)
	}
	if _, err := fixture.store.blobs.Stat(context.Background(), strings.TrimPrefix(upload.URL.Path, "/cache/")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatched staging object was kept: %v", err)
	}
	if _, err := fixture.store.Head(context.Background(), mismatch); !errors.Is(err, ErrNotFound) {
		t.Fatalf("size mismatch published an entry: %v", err)
	}

	// First writer wins: different bytes for a committed entry conflict.
	different := bytes.Repeat([]byte("different"), 64)
	differentSum := sha256.Sum256(different)
	upload, err = fixture.store.BeginDirectUpload(writer, fixture.key, hex.EncodeToString(differentSum[:]), int64(len(different)))
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := fetchURL(t, http.MethodPut, upload.URL.String(), upload.Header, different); status != http.StatusOK {
		t.Fatalf("PUT = %d", status)
	}
	if _, _, err := fixture.store.CommitDirectUpload(writer, fixture.key, artifact.Metadata{}, upload.Upload); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting commit = %v", err)
	}

	// An abandoned upload is reconciled by the staging TTL like any other.
	abandoned := fixture.key
	abandoned.Native = "abandoned"
	upload, err = fixture.store.BeginDirectUpload(writer, abandoned, digest, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := fetchURL(t, http.MethodPut, upload.URL.String(), upload.Header, payload); status != http.StatusOK {
		t.Fatalf("PUT = %d", status)
	}
	fixture.now.Add(int64(fixture.store.config.StageTTL + time.Minute))
	result, err := fixture.store.Maintain(context.Background())
	if err != nil || result.ExpiredUploads == 0 {
		t.Fatalf("maintenance = %+v, %v", result, err)
	}
	if _, err := fixture.store.blobs.Stat(context.Background(), strings.TrimPrefix(upload.URL.Path, "/cache/")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("abandoned staging object survived its TTL: %v", err)
	}
}
