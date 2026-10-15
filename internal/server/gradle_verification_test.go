package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/config"
)

func TestCloudGradleReadVerifiesCompleteArchiveBeforeResponse(t *testing.T) {
	t.Parallel()
	good := []byte("verified opaque Gradle archive")
	digest := sha256.Sum256(good)
	key := artifact.Key{Integration: "gradle", Project: "github.com/acme/widgets", Compatibility: "linux-amd64-jdk21-schema1", Native: "native-hash"}
	entry := artifact.Entry{Key: key, Digest: hex.EncodeToString(digest[:]), Size: int64(len(good))}
	for _, test := range []struct {
		name    string
		open    func() io.ReadCloser
		minimum int64
		want    int
		deleted bool
	}{
		{name: "valid", open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(good)) }, want: 200},
		{name: "wrong digest", open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), len(good)))) }, want: 404, deleted: true},
		{name: "late checksum failure", open: func() io.ReadCloser { return &lateCorruptReadCloser{reader: bytes.NewReader(good)} }, want: 404, deleted: true},
		{name: "short stream", open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(good[:len(good)-1])) }, want: 404, deleted: true},
		{name: "trailing bytes", open: func() io.ReadCloser { return io.NopCloser(strings.NewReader(string(good) + "extra")) }, want: 404, deleted: true},
		{name: "staging reserve exhausted", open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(good)) }, minimum: math.MaxInt64, want: 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			local, err := artifact.Open(context.Background(), root, 1<<20, test.minimum)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = local.Close() })
			closed := false
			store := &turboStreamingStore{entry: entry, open: func() io.ReadCloser { return &gradleClosingBody{ReadCloser: test.open(), closed: &closed} }}
			instance := &Server{config: config.Config{Role: "team", DataDir: root, MaxBytes: 1 << 20}, store: store, localStore: local, cloudStore: &cloud.Store{}}
			w := httptest.NewRecorder()
			instance.getGradle(w, httptest.NewRequest("GET", gradleCachePrefix+key.Native, nil), key)
			if w.Code != test.want || !closed {
				t.Fatalf("HTTP %d, want %d; body closed=%v", w.Code, test.want, closed)
			}
			if test.want == 200 && !bytes.Equal(w.Body.Bytes(), good) {
				t.Fatalf("restored bytes = %q", w.Body.Bytes())
			}
			if test.want != 200 && (bytes.Contains(w.Body.Bytes(), good) || w.Header().Get("x-layercache-digest") != "") {
				t.Fatal("unverified archive escaped the HTTP response")
			}
			if store.deleted != test.deleted {
				t.Fatalf("deleted=%v, want %v", store.deleted, test.deleted)
			}
			assertGradleStagingEmpty(t, root)
		})
	}
}

type gradleClosingBody struct {
	io.ReadCloser
	closed *bool
}

func (body *gradleClosingBody) Close() error {
	*body.closed = true
	return body.ReadCloser.Close()
}
