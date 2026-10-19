package portal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/server"
)

// TestStore prefix keeps this real PostgreSQL/protocol control in the existing
// PostgreSQL CI lane. No hosted service or provider credentials are used.
func TestStoreHostedRuntimeUsesAssignedS3PoolAndOperatorQuota(t *testing.T) {
	postgresURL := os.Getenv("LAYERCACHE_PORTAL_TEST_POSTGRES_URL")
	if postgresURL == "" {
		t.Skip("requires the disposable portal PostgreSQL fixture")
	}
	metadata := testStore(t) // Owns an isolated schema in the dedicated QA database.
	var schema string
	if err := metadata.db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(postgresURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	first, second := newPortalS3(t), newPortalS3(t)
	storage := storageTestConfig(t)
	for id, endpoint := range map[string]string{"r2-primary": first.server.URL, "r2-secondary": second.server.URL} {
		pool := storage.HostedStoragePools[id]
		pool.CloudPostgresURL, pool.CloudS3Endpoint, pool.CloudS3UsePathStyle = parsed.String(), endpoint, true
		storage.HostedStoragePools[id] = pool
	}
	q := newPortalQA(t, func(c *Config) {
		c.HostedStoragePools, c.StoragePlans, c.DefaultStoragePlan = storage.HostedStoragePools, storage.StoragePlans, storage.DefaultStoragePlan
		c.TeamCreatorIDs = []string{"1", "2"}
		c.TeamCreatorStoragePlans = map[string]string{"2": "larger"}
	})
	alice, bob := q.login("alice"), q.login("bob")
	requireStatus(t, q.request("POST", "/api/teams", `{"name":"Untrusted plan","storagePlanId":"larger"}`, alice, nil), 400)
	team := q.team(alice, "Hosted Team")
	otherTeam := q.team(bob, "Other hosted Team")
	requireStatus(t, q.request("POST", "/api/teams/"+team.ID+"/projects", `{"name":"Untrusted pool","repository":"acme/alpha","poolId":"r2-secondary"}`, alice, nil), 400)
	project, other := q.project(alice, team, "acme/alpha"), q.project(bob, otherTeam, "acme/alpha")
	privateResponse := q.request("POST", "/api/teams", `{"kind":"personal"}`, alice, nil)
	requireStatus(t, privateResponse, 201)
	private := q.project(alice, decodeQA[Team](t, privateResponse), "acme/alpha")
	writer, otherWriter, privateWriter := q.capability(project, "alice"), q.capability(other, "bob"), q.capability(private, "alice")
	headers := func(token string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + token, "X-LayerCache-Compatibility": "linux-amd64-schema1"}
	}
	for _, item := range []struct{ token, data string }{{writer, "primary-bytes"}, {otherWriter, "secondary-bytes"}, {privateWriter, "personal-bytes"}} {
		requireStatus(t, q.request("PUT", "/v8/artifacts/shared-key", item.data, nil, headers(item.token)), 200)
	}
	if !first.contains([]byte("primary-bytes")) || first.contains([]byte("secondary-bytes")) || !second.contains([]byte("secondary-bytes")) || second.contains([]byte("primary-bytes")) {
		t.Fatal("project bytes reached the wrong S3 pool")
	}
	quota := q.request("GET", "/v1/cache/quota", "", nil, headers(writer))
	requireStatus(t, quota, 200)
	if got := decodeQA[struct {
		MaxBytes int64 `json:"maxBytes"`
	}](t, quota).MaxBytes; got != 1024 {
		t.Fatalf("native SQL quota = %d, want assigned 1024", got)
	}
	requireStatus(t, q.request("PUT", "/v1/cache/quota", `{"maxBytes":9999999}`, nil, headers(writer)), 403)
	quota = q.request("GET", "/v1/cache/quota", "", nil, headers(writer))
	requireStatus(t, quota, 200)
	if got := decodeQA[struct {
		MaxBytes int64 `json:"maxBytes"`
	}](t, quota).MaxBytes; got != 1024 {
		t.Fatal("managed quota changed despite refusal")
	}
	requireStatus(t, q.request("PUT", "/v8/artifacts/too-large", strings.Repeat("x", 1025), nil, headers(writer)), 507)
	standalone := storage.HostedStoragePools["r2-primary"]
	standalone.ProjectID, standalone.DataDir = "operator-cache-qa", t.TempDir()
	operator, err := server.New(context.Background(), standalone)
	if err != nil {
		t.Fatal(err)
	}
	defer operator.Close()
	request := httptest.NewRequest("PUT", "https://operator.example/v1/cache/quota", strings.NewReader(`{"maxBytes":2048}`))
	request.Header.Set("Authorization", "Bearer "+standalone.LocalToken)
	response := httptest.NewRecorder()
	operator.Handler().ServeHTTP(response, request)
	requireStatus(t, response, 200)
	if got := decodeQA[struct {
		MaxBytes int64 `json:"maxBytes"`
	}](t, response).MaxBytes; got != 2048 {
		t.Fatal("standalone administrator lost quota control")
	}
	// Restart restores authority, pool binding, SQL quotas and exact cache bytes.
	if err := q.p.Close(); err != nil {
		t.Fatal(err)
	}
	q.p, err = New(context.Background(), q.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ token, data string }{{writer, "primary-bytes"}, {otherWriter, "secondary-bytes"}, {privateWriter, "personal-bytes"}} {
		response := q.request("GET", "/v8/artifacts/shared-key", "", nil, headers(item.token))
		requireStatus(t, response, 200)
		if response.Body.String() != item.data {
			t.Fatal("cross-project bytes after restart")
		}
	}
	requireStatus(t, q.request("GET", "/api/projects/"+private.ID+"/snapshot", "", bob, nil), 403)
	mustJoin(t, q.p.store, "1", team.ID, "2", "reader")
	reader := q.capability(project, "bob")
	requireStatus(t, q.request("GET", "/v8/artifacts/shared-key", "", nil, headers(reader)), 200)
	requireStatus(t, q.request("PUT", "/v8/artifacts/forbidden", "reader", nil, headers(reader)), 401)
	if err := q.p.store.ChangeMember(context.Background(), "1", team.ID, "2", ""); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, q.request("GET", "/v8/artifacts/shared-key", "", nil, headers(reader)), 404)
	// A backend edit must be rejected before any existing runtime is opened.
	if err := q.p.Close(); err != nil {
		t.Fatal(err)
	}
	changed := q.cfg
	changed.HostedStoragePools = map[string]config.Config{}
	for id, pool := range q.cfg.HostedStoragePools {
		changed.HostedStoragePools[id] = pool
	}
	pool := changed.HostedStoragePools["r2-primary"]
	pool.CloudS3Bucket = "changed-backend"
	changed.HostedStoragePools["r2-primary"] = pool
	if candidate, err := New(context.Background(), changed); err == nil {
		candidate.Close()
		t.Fatal("restart accepted a changed storage backend")
	}
}

type portalS3Object struct {
	data     []byte
	metadata http.Header
}

type portalS3 struct {
	server  *httptest.Server
	mu      sync.Mutex
	objects map[string]portalS3Object
	parts   map[string]map[int][]byte
	next    int
}

func newPortalS3(t *testing.T) *portalS3 {
	t.Helper()
	s := &portalS3{objects: map[string]portalS3Object{}, parts: map[string]map[int][]byte{}}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	return s
}

func (s *portalS3) contains(data []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, object := range s.objects {
		if bytes.Equal(object.data, data) {
			return true
		}
	}
	return false
}

func (s *portalS3) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	objectKey := bucket + "/" + key
	writeXML := func(body string) { w.Header().Set("Content-Type", "application/xml"); _, _ = io.WriteString(w, body) }
	if key == "" && r.Method == "HEAD" {
		return
	}
	query := r.URL.Query()
	if r.Method == "POST" && query.Has("uploads") {
		s.next++
		id := strconv.Itoa(s.next)
		s.parts[id] = map[int][]byte{}
		writeXML("<InitiateMultipartUploadResult><Bucket>" + bucket + "</Bucket><Key>" + key + "</Key><UploadId>" + id + "</UploadId></InitiateMultipartUploadResult>")
		return
	}
	if id := query.Get("uploadId"); id != "" {
		switch r.Method {
		case "PUT":
			data, err := readPortalS3Body(r)
			if err != nil {
				http.Error(w, "fixture input", 400)
				return
			}
			number, _ := strconv.Atoi(query.Get("partNumber"))
			s.parts[id][number] = data
			w.Header().Set("ETag", `"fixture"`)
		case "POST":
			numbers := make([]int, 0, len(s.parts[id]))
			for number := range s.parts[id] {
				numbers = append(numbers, number)
			}
			sort.Ints(numbers)
			var data []byte
			for _, number := range numbers {
				data = append(data, s.parts[id][number]...)
			}
			s.objects[objectKey] = portalS3Object{data, r.Header.Clone()}
			delete(s.parts, id)
			writeXML("<CompleteMultipartUploadResult><Bucket>" + bucket + "</Bucket><Key>" + key + "</Key><ETag>\"fixture\"</ETag></CompleteMultipartUploadResult>")
		case "DELETE":
			delete(s.parts, id)
			w.WriteHeader(204)
		default:
			http.Error(w, "fixture method", 400)
		}
		return
	}
	if source := r.Header.Get("X-Amz-Copy-Source"); source != "" && r.Method == "PUT" {
		source, _ = url.PathUnescape(source)
		object, ok := s.objects[strings.TrimPrefix(source, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		object.metadata = r.Header.Clone()
		s.objects[objectKey] = object
		writeXML(`<CopyObjectResult><LastModified>2026-10-19T00:00:00.000Z</LastModified><ETag>"fixture"</ETag></CopyObjectResult>`)
		return
	}
	if key == "" && r.Method == "GET" {
		type entry struct {
			Key          string `xml:"Key"`
			Size         int    `xml:"Size"`
			LastModified string `xml:"LastModified"`
		}
		list := struct {
			XMLName  xml.Name `xml:"ListBucketResult"`
			Name     string
			Contents []entry
		}{Name: bucket}
		for name, object := range s.objects {
			b, k, _ := strings.Cut(name, "/")
			if b == bucket && strings.HasPrefix(k, query.Get("prefix")) {
				list.Contents = append(list.Contents, entry{k, len(object.data), "2026-10-19T00:00:00.000Z"})
			}
		}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(list)
		return
	}
	if r.Method == "DELETE" {
		delete(s.objects, objectKey)
		w.WriteHeader(204)
		return
	}
	object, ok := s.objects[objectKey]
	if !ok {
		http.NotFound(w, r)
		return
	}
	for name, values := range object.metadata {
		if strings.HasPrefix(strings.ToLower(name), "x-amz-meta-") {
			w.Header()[name] = values
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(object.data)))
	w.Header().Set("Last-Modified", "Mon, 19 Oct 2026 00:00:00 GMT")
	w.Header().Set("ETag", `"fixture"`)
	if r.Method == "GET" {
		_, _ = w.Write(object.data)
	} else if r.Method != "HEAD" {
		http.Error(w, "fixture method", 400)
	}
}

func readPortalS3Body(r *http.Request) ([]byte, error) {
	if !strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") && r.Header.Get("X-Amz-Decoded-Content-Length") == "" {
		return io.ReadAll(r.Body)
	}
	reader := bufio.NewReader(r.Body)
	var data []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeText, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		size, err := strconv.ParseInt(sizeText, 16, 64)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			return data, nil
		}
		start := len(data)
		data = append(data, make([]byte, size)...)
		if _, err := io.ReadFull(reader, data[start:]); err != nil {
			return nil, err
		}
		if _, err := reader.ReadString('\n'); err != nil {
			return nil, err
		}
	}
}
