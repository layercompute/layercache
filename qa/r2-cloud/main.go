// Command r2-cloud performs explicit, opt-in qualification against two existing
// private R2 buckets. It never creates buckets or touches an existing project.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/artifact"
	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/retained"
	"github.com/layercache/layercache/internal/server"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const largeBytes int64 = 124078900
const postgresImage = "postgres:16@sha256:081f1bc7bd5e143dbb6e487b710bbc27712cdcfaced4c071b8e47349aa1b4171"

type provider struct {
	Provider        string `json:"provider"`
	PoolID          string `json:"poolId"`
	Endpoint        string `json:"endpoint"`
	Bucket          string `json:"bucket"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	client          *minio.Client
	credentialFile  string
}

type measurement struct {
	Name         string  `json:"name"`
	Status       int     `json:"status,omitempty"`
	Size         int64   `json:"size,omitempty"`
	SHA256       string  `json:"sha256,omitempty"`
	HeadersMS    float64 `json:"headersMs,omitempty"`
	CompleteMS   float64 `json:"completeMs"`
	ServerTiming string  `json:"serverTiming,omitempty"`
	ReadError    bool    `json:"readError,omitempty"`
	Code         string  `json:"providerCode,omitempty"`
}

type receipt struct {
	SchemaVersion string            `json:"schemaVersion"`
	CheckedAt     string            `json:"checkedAt"`
	Source        string            `json:"sourceRevision"`
	SourceDigests map[string]string `json:"sourceDigests"`
	SDK           map[string]string `json:"sdk"`
	Measurements  []measurement     `json:"measurements"`
	Controls      []string          `json:"controls"`
	RetainedID    string            `json:"retainedId"`
	RetainUntil   time.Time         `json:"retainUntil"`
	Limits        []string          `json:"limits"`
	Cleanup       bool              `json:"ownedCleanupComplete"`
}

type fixture struct {
	ctx     context.Context
	root    string
	pg      string
	http    *http.Client
	result  receipt
	cleanup []func(context.Context) error
	journal *cleanupJournal
}

// This protected journal is deliberately separate from public evidence. It
// preserves exact recovery ownership if provider cleanup or the process fails.
type cleanupJournal struct{ file *os.File }

func (journal *cleanupJournal) append(event map[string]any) error {
	event["recordedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err = journal.file.Write(append(data, '\n')); err != nil {
		return err
	}
	return journal.file.Sync()
}

type gateway struct {
	cfg       config.Config
	store     *cloud.Store
	service   *server.Server
	http      *httptest.Server
	dropAck   atomic.Bool
	published atomic.Int64
}

func main() {
	optIn := flag.Bool("allow-provider-writes", false, "explicitly permit writes only to new qualification prefixes")
	primary := flag.String("primary-credentials-file", "", "absolute protected primary credential JSON path")
	secondary := flag.String("secondary-credentials-file", "", "absolute protected secondary credential JSON path")
	output := flag.String("evidence-file", "", "absolute NEW sanitized receipt path")
	flag.Parse()
	if !*optIn || *primary == "" || *secondary == "" || !filepath.IsAbs(*output) {
		fmt.Fprintln(os.Stderr, "requires explicit --allow-provider-writes, two protected credential-file paths and an absolute new --evidence-file")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	// Reserve the new receipt and private recovery journal before any provider
	// operation or container startup. An existing destination fails cheaply.
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reserve new qualification receipt before provider work")
		os.Exit(1)
	}
	journalFile, err := os.OpenFile(*output+".cleanup.jsonl", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(*output)
		fmt.Fprintln(os.Stderr, "reserve new protected cleanup journal before provider work")
		os.Exit(1)
	}
	journal := &cleanupJournal{file: journalFile}
	err = journal.append(map[string]any{"event": "begin", "schemaVersion": "r2-qa-cleanup-v1"})
	if err == nil {
		var directory *os.File
		directory, err = os.Open(filepath.Dir(*output))
		if err == nil {
			err = directory.Sync()
			_ = directory.Close()
		}
	}
	if err != nil {
		_ = journalFile.Close()
		_ = file.Close()
		_ = os.Remove(*output)
		fmt.Fprintln(os.Stderr, "persist protected cleanup journal before provider work")
		os.Exit(1)
	}
	result, err := qualify(ctx, *primary, *secondary, journal)
	if closeErr := journalFile.Close(); closeErr != nil {
		err = errors.Join(err, errors.New("close protected cleanup journal"))
	}
	if err != nil {
		_ = file.Close()
		_ = os.Remove(*output)
		// Operation labels and error classes are deliberately bounded. Provider
		// error strings may contain endpoint, bucket, credentials or signed URLs.
		fmt.Fprintln(os.Stderr, "R2 qualification failed:", err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		_ = file.Close()
		fmt.Fprintln(os.Stderr, "encode sanitized qualification receipt")
		os.Exit(1)
	}
	_, writeErr := file.Write(append(data, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		fmt.Fprintln(os.Stderr, "write qualification receipt")
		os.Exit(1)
	}
	fmt.Println("R2 qualification passed; sanitized receipt written and owned provider prefixes removed")
}

type failure struct{ label string }

func require(ok bool, label string) {
	if !ok {
		panic(failure{label})
	}
}

func check(err error, label string) {
	if err != nil {
		code := minio.ToErrorResponse(err).Code
		if code != "" && len(code) <= 64 && strings.IndexFunc(code, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
		}) < 0 {
			label += " (provider code " + code + ")"
		}
		panic(failure{label})
	}
}

func qualify(ctx context.Context, first, second string, journal *cleanupJournal) (result receipt, err error) {
	f := &fixture{ctx: ctx, http: &http.Client{Timeout: 3 * time.Minute}, journal: journal}
	defer func() {
		if value := recover(); value != nil {
			if failed, ok := value.(failure); ok {
				err = errors.New(failed.label)
			} else {
				err = errors.New("unexpected qualification failure (details suppressed)")
			}
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cleanupFailed := false
		for i := len(f.cleanup) - 1; i >= 0; i-- {
			if failure := f.cleanup[i](cleanup); failure != nil {
				cleanupFailed = true
				err = errors.Join(err, errors.New("owned qualification cleanup incomplete"))
			}
		}
		if f.root != "" && !cleanupFailed {
			if failure := os.RemoveAll(f.root); failure != nil {
				cleanupFailed = true
				err = errors.Join(err, errors.New("owned local scratch cleanup incomplete"))
			}
		}
		if journalErr := f.journal.append(map[string]any{"event": "cleanup-finished", "complete": !cleanupFailed, "qualificationPassed": err == nil}); journalErr != nil {
			err = errors.Join(err, errors.New("persist protected cleanup completion"))
		}
		if err == nil {
			f.result.Cleanup = true
			result = f.result
		}
	}()
	p := loadProvider(first)
	s := loadProvider(second)
	require(p.Bucket != s.Bucket && p.AccessKeyID != s.AccessKeyID, "two distinct bucket-scoped credentials required")
	localRoot := filepath.Join(os.TempDir(), "layercache-r2-qa-"+randomHex(12))
	check(f.journal.append(map[string]any{"event": "own-local-scratch", "path": localRoot}), "persist owned scratch recovery path before creation")
	check(os.Mkdir(localRoot, 0700), "create owned qualification scratch")
	f.root = localRoot
	unique := randomHex(12)
	f.result = receipt{SchemaVersion: "r2-cloud-source-v1", CheckedAt: time.Now().UTC().Format(time.RFC3339),
		SDK: map[string]string{}, SourceDigests: map[string]string{}, Limits: []string{
			"Actual private R2 persistence and synthetic HTTP transport; no public service activation, application build or native-client qualification.",
			"Capabilities are minted locally for isolated qualification; live GitHub OIDC and customer onboarding are not exercised.",
			"Large publish time includes streaming SDK staging, server-side copy, PostgreSQL commit and local HTTP overhead; it is not pure transfer bandwidth.",
			"A separate 10-second receipt exercises elapsed expiry; the 124 MB receipt's one-hour deadline is preserved but not waited out.",
			"Project quota and persistence are checked; portal storage-plan billing and aggregate pool entitlement are qualified separately.",
			"GitHub artifacts remain required until application producer/consumer and consumer-only rerun acceptance passes.",
		}}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dependency := range info.Deps {
			if dependency.Path == "github.com/minio/minio-go/v7" || dependency.Path == "github.com/jackc/pgx/v5" {
				f.result.SDK[dependency.Path] = dependency.Version
			}
		}
	}
	revision, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	check(err, "read exact source revision")
	f.result.Source = strings.TrimSpace(string(revision))
	for _, path := range []string{"qa/r2-cloud/main.go", "internal/cloud/s3.go", "internal/cloud/retained.go", "go.sum"} {
		data, err := os.ReadFile(path)
		check(err, "hash qualification source")
		f.result.SourceDigests[path] = digest(data)
	}
	f.startPostgres(unique)
	for _, candidate := range []*provider{p, s} {
		exists, err := candidate.client.BucketExists(ctx, candidate.Bucket)
		check(err, "inspect owned provider bucket")
		require(exists, "owned provider bucket exists")
	}
	f.bucketDenial(p, s.Bucket, "primary-credential-foreign-bucket-denial")
	f.bucketDenial(s, p.Bucket, "secondary-credential-foreign-bucket-denial")
	f.result.Controls = append(f.result.Controls, "both bucket-scoped credentials reject the other existing private bucket")
	f.sdkProbe(p, "qualification/"+unique+"/")
	a := f.newGateway(p, "layercache-r2-qa-"+unique+"-a", "primary", 256<<20)
	b := f.newGateway(s, "layercache-r2-qa-"+unique+"-b", "secondary", 8<<20)
	c := f.newGateway(p, "layercache-r2-qa-"+unique+"-c", "same-bucket-other-project", 8<<20)
	f.cacheControls(p, a, b, c)
	f.retainedControls(p, a)
	f.result.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	return f.result, nil
}

func loadProvider(path string) *provider {
	require(filepath.IsAbs(path), "provider credential path must be absolute")
	info, err := os.Lstat(path)
	check(err, "read protected provider credential file metadata")
	require(info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 && info.Size() <= 16384, "provider credential file must be protected regular JSON")
	file, err := os.Open(path)
	check(err, "open protected provider credential file")
	defer file.Close()
	var p provider
	p.credentialFile = path
	decoder := json.NewDecoder(io.LimitReader(file, 16385))
	decoder.DisallowUnknownFields()
	check(decoder.Decode(&p), "decode protected provider credentials")
	var trailing any
	require(decoder.Decode(&trailing) == io.EOF, "provider credentials contain one JSON value")
	endpoint, err := url.Parse(p.Endpoint)
	check(err, "parse private provider endpoint")
	require((p.Provider == "r2" || p.Provider == "cloudflare-r2") && endpoint.Scheme == "https" && endpoint.Host != "" && endpoint.User == nil && endpoint.RawQuery == "" && endpoint.Fragment == "" && (endpoint.Path == "" || endpoint.Path == "/"), "protected R2 provider configuration required")
	require(p.Bucket != "" && p.Region != "" && p.AccessKeyID != "" && p.SecretAccessKey != "", "complete provider credentials required")
	p.client, err = minio.New(endpoint.Host, &minio.Options{Creds: credentials.NewStaticV4(p.AccessKeyID, p.SecretAccessKey, ""),
		Secure: true, Region: p.Region, BucketLookup: minio.BucketLookupPath})
	check(err, "construct production pinned MinIO SDK client")
	return &p
}

func (f *fixture) startPostgres(unique string) {
	name := "layercache-r2-qa-" + unique
	check(f.journal.append(map[string]any{"event": "own-postgres-container", "name": name, "image": postgresImage}), "persist owned container recovery name before startup")
	f.cleanup = append(f.cleanup, func(ctx context.Context) error {
		return exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	})
	command := exec.CommandContext(f.ctx, "docker", "run", "--detach", "--rm", "--name", name,
		"--label", "io.layercache.qa=r2-source", "--memory", "512m", "--cpus", "1",
		"--tmpfs", "/var/lib/postgresql/data:rw,size=256m", "--publish", "127.0.0.1::5432",
		"--env", "POSTGRES_HOST_AUTH_METHOD=trust", "--env", "POSTGRES_DB=layercache", postgresImage)
	check(command.Run(), "start isolated bounded PostgreSQL 16 container")
	port, err := exec.CommandContext(f.ctx, "docker", "port", name, "5432/tcp").Output()
	check(err, "locate owned PostgreSQL loopback port")
	address := strings.TrimSpace(string(port))
	require(strings.HasPrefix(address, "127.0.0.1:"), "PostgreSQL remains loopback only")
	f.pg = "postgres://postgres@" + address + "/layercache?sslmode=disable"
	db, err := sql.Open("pgx", f.pg)
	check(err, "open disposable PostgreSQL")
	defer db.Close()
	for i := 0; i < 100; i++ {
		if db.PingContext(f.ctx) == nil {
			return
		}
		select {
		case <-f.ctx.Done():
			panic(failure{"PostgreSQL startup canceled"})
		case <-time.After(100 * time.Millisecond):
		}
	}
	panic(failure{"PostgreSQL startup timeout"})
}

func (f *fixture) ownPrefix(p *provider, prefix string) {
	require(strings.HasSuffix(prefix, "/") && (strings.HasPrefix(prefix, "projects/") || strings.HasPrefix(prefix, "qualification/")), "bounded QA object prefix required")
	check(f.journal.append(map[string]any{"event": "own-r2-prefix", "provider": p.Provider, "endpoint": p.Endpoint, "bucket": p.Bucket, "prefix": prefix, "credentialFile": p.credentialFile}), "persist exact owned provider prefix before writing")
	f.cleanup = append(f.cleanup, func(ctx context.Context) error {
		for upload := range p.client.ListIncompleteUploads(ctx, p.Bucket, prefix, true) {
			if upload.Err != nil || !strings.HasPrefix(upload.Key, prefix) {
				return errors.New("list exact owned multipart QA prefix failed")
			}
			if err := p.client.RemoveIncompleteUpload(ctx, p.Bucket, upload.Key); err != nil {
				return errors.New("remove exact owned multipart QA upload failed")
			}
		}
		for object := range p.client.ListObjects(ctx, p.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			if object.Err != nil || !strings.HasPrefix(object.Key, prefix) {
				return errors.New("list exact owned QA prefix failed")
			}
			if err := p.client.RemoveObject(ctx, p.Bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
				return errors.New("remove exact owned QA object failed")
			}
		}
		return nil
	})
}

func bucketDenialResult(exists bool, err error) (status int, code string, valid bool) {
	if err == nil {
		// Pinned MinIO BucketExists normalizes NoSuchBucket into false,nil.
		// The other credential already confirmed that this exact bucket exists.
		return http.StatusNotFound, "NoSuchBucket", !exists
	}
	response := minio.ToErrorResponse(err)
	valid = !exists && (response.StatusCode == http.StatusForbidden && response.Code == "AccessDenied" ||
		response.StatusCode == http.StatusNotFound && (response.Code == "NoSuchBucket" || response.Code == "NotFound"))
	if !valid {
		return 0, "", false
	}
	return response.StatusCode, response.Code, true
}

func (f *fixture) bucketDenial(p *provider, otherBucket, name string) {
	started := time.Now()
	exists, err := p.client.BucketExists(f.ctx, otherBucket)
	status, code, valid := bucketDenialResult(exists, err)
	require(valid, "foreign-bucket control requires explicit provider denial, not transport failure")
	f.result.Measurements = append(f.result.Measurements, measurement{Name: name, Status: status, Code: code, CompleteMS: milliseconds(started)})
}

func (f *fixture) sdkProbe(p *provider, prefix string) {
	f.ownPrefix(p, prefix)
	body := bytes.Repeat([]byte{0x51}, 4096)
	start := time.Now()
	_, err := p.client.PutObject(f.ctx, p.Bucket, prefix+"staged", bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{UserMetadata: map[string]string{"state": "staged"}})
	check(err, "actual R2 SDK cold PUT")
	f.result.Measurements = append(f.result.Measurements, measurement{Name: "sdk-4k-put", Size: 4096, CompleteMS: milliseconds(start)})
	start = time.Now()
	_, err = p.client.CopyObject(f.ctx, minio.CopyDestOptions{Bucket: p.Bucket, Object: prefix + "committed", ReplaceMetadata: true,
		UserMetadata: map[string]string{"sha256": digest(body), "size": "4096", "state": "committed"}}, minio.CopySrcOptions{Bucket: p.Bucket, Object: prefix + "staged"})
	check(err, "actual R2 SDK copy with replacement metadata")
	f.result.Measurements = append(f.result.Measurements, measurement{Name: "sdk-4k-copy-replace-metadata", Size: 4096, CompleteMS: milliseconds(start)})
	start = time.Now()
	stat, err := p.client.StatObject(f.ctx, p.Bucket, prefix+"committed", minio.StatObjectOptions{})
	check(err, "actual R2 SDK metadata lookup")
	require(stat.Size == 4096 && metadata(stat, "sha256") == digest(body) && metadata(stat, "size") == "4096", "R2 preserves custom complete digest and size metadata")
	f.result.Measurements = append(f.result.Measurements, measurement{Name: "sdk-4k-metadata-lookup", CompleteMS: milliseconds(start)})
	start = time.Now()
	object, err := p.client.GetObject(f.ctx, p.Bucket, prefix+"committed", minio.GetObjectOptions{})
	check(err, "actual R2 SDK GET")
	hasher := sha256.New()
	n, readErr := io.Copy(hasher, object)
	closeErr := object.Close()
	check(readErr, "read complete SDK object")
	check(closeErr, "close SDK object")
	require(n == 4096 && hex.EncodeToString(hasher.Sum(nil)) == digest(body), "actual R2 SDK read verifies exact bytes")
	f.result.Measurements = append(f.result.Measurements, measurement{Name: "sdk-4k-get-full-verify", Size: n, SHA256: digest(body), CompleteMS: milliseconds(start)})
}

func (f *fixture) newGateway(p *provider, project, label string, quota int64) *gateway {
	namespace := digest([]byte(project))[:32]
	f.ownPrefix(p, "projects/"+namespace+"/")
	g := &gateway{cfg: config.Config{Version: 1, Role: "team", DataDir: filepath.Join(f.root, label), Listen: "127.0.0.1:7437",
		MaxBytes: quota, MinFreeBytes: 5 << 30, ProjectID: project, LocalToken: randomHex(32), CompatibilityID: "r2-opaque-qa-v1",
		ActionsRepository: "acme/fixture", ActionsRef: "refs/heads/main", ActionsDefaultRef: "refs/heads/main", TeamMembers: map[string]string{"qa": "writer"},
		RetainedArtifactsEnabled: true, CloudPostgresURL: f.pg, CloudS3Endpoint: p.Endpoint, CloudS3Bucket: p.Bucket, CloudS3Region: p.Region,
		CloudS3AccessKey: p.AccessKeyID, CloudS3SecretKey: p.SecretAccessKey, CloudS3UsePathStyle: true}}
	var err error
	g.store, err = cloud.Open(f.ctx, cloud.Config{PostgresURL: f.pg, S3Endpoint: p.Endpoint, S3Bucket: p.Bucket, S3Region: p.Region,
		S3AccessKey: p.AccessKeyID, S3SecretKey: p.SecretAccessKey, S3PathStyle: true, Project: project, MaxBytes: quota})
	check(err, "open actual PostgreSQL and R2 cloud store")
	f.cleanup = append(f.cleanup, func(context.Context) error { return g.store.Close() })
	f.cleanup = append(f.cleanup, func(context.Context) error { return g.stop() })
	g.start(f.ctx)
	return g
}

func (g *gateway) start(ctx context.Context) {
	var err error
	g.service, err = server.New(ctx, g.cfg)
	check(err, "start actual R2-backed HTTP gateway")
	g.http = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut && strings.HasSuffix(request.URL.Path, "/content") && g.dropAck.CompareAndSwap(true, false) {
			recorded := httptest.NewRecorder()
			g.service.Handler().ServeHTTP(recorded, request)
			require(recorded.Code == 200, "real retained publication commits before forced acknowledgement loss")
			g.published.Add(1)
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		g.service.Handler().ServeHTTP(writer, request)
	}))
}

func (g *gateway) stop() error {
	if g.http != nil {
		g.http.Close()
		g.http = nil
	}
	if g.service != nil {
		err := g.service.Close()
		g.service = nil
		return err
	}
	return nil
}

func capability(g *gateway, integration string, mode access.Capability) string {
	now := time.Now().UTC()
	claims := access.Claims{Subject: "github:qa", Project: g.cfg.ProjectID, Integration: integration,
		Compatibility: g.cfg.CompatibilityID, Capabilities: []access.Capability{mode}, ExpiresAt: now.Add(time.Hour)}
	token, err := access.MintCapabilityToken(g.cfg.LocalToken, claims, now)
	check(err, "mint isolated native capability")
	return token
}

func (f *fixture) request(name, method, endpoint, path, token string, basic bool, body io.Reader, size int64, binary bool) (measurement, []byte) {
	request, err := http.NewRequestWithContext(f.ctx, method, endpoint+path, body)
	check(err, "create qualification HTTP request")
	request.ContentLength = size
	if basic {
		request.SetBasicAuth("layercache", token)
	} else {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	response, err := f.http.Do(request)
	check(err, name+" HTTP request failed")
	defer response.Body.Close()
	row := measurement{Name: name, Status: response.StatusCode, HeadersMS: milliseconds(start), ServerTiming: response.Header.Get("Server-Timing")}
	var data []byte
	if binary && response.StatusCode == 200 {
		hasher := sha256.New()
		row.Size, err = io.Copy(hasher, response.Body)
		row.SHA256 = hex.EncodeToString(hasher.Sum(nil))
		row.ReadError = err != nil
	} else {
		data, err = io.ReadAll(io.LimitReader(response.Body, 65537))
		check(err, name+" complete response read failed")
		require(len(data) <= 65536, "bounded qualification JSON response")
	}
	row.CompleteMS = milliseconds(start)
	f.result.Measurements = append(f.result.Measurements, row)
	return row, data
}

func (f *fixture) cacheControls(p *provider, a, b, c *gateway) {
	fmt.Println("qualifying private cache persistence and access controls")
	first := bytes.Repeat([]byte{0x41}, 4096)
	other := bytes.Repeat([]byte{0x42}, 4096)
	writer := capability(a, "gradle", access.CapabilityWrite)
	reader := capability(a, "gradle", access.CapabilityRead)
	path := "/v1/gradle-cache/opaque-fixture"
	row, _ := f.request("cache-4k-cold-publish", "PUT", a.http.URL, path, writer, true, bytes.NewReader(first), 4096, false)
	require(row.Status == 200, "cold private cache publication accepted")
	row, _ = f.request("cache-4k-read-only-consumer", "GET", a.http.URL, path, reader, true, nil, 0, true)
	require(row.Status == 200 && !row.ReadError && row.Size == 4096 && row.SHA256 == digest(first), "read-only private consumer receives exact bytes")
	check(a.stop(), "stop gateway before persistence check")
	a.start(f.ctx)
	row, _ = f.request("cache-4k-after-gateway-restart", "GET", a.http.URL, path, reader, true, nil, 0, true)
	require(row.Status == 200 && row.SHA256 == digest(first), "PostgreSQL/R2 persistence survives gateway restart")
	row, _ = f.request("read-only-publication-denied", "PUT", a.http.URL, path, reader, true, bytes.NewReader(other), 4096, false)
	require(row.Status == 403, "read-only capability cannot publish")
	row, _ = f.request("foreign-project-capability-denied", "GET", a.http.URL, path, capability(c, "gradle", access.CapabilityRead), true, nil, 0, false)
	require(row.Status == 401, "foreign project credential denied")
	for i, g := range []*gateway{b, c} {
		row, _ = f.request(fmt.Sprintf("isolated-project-%d-cold-miss", i), "GET", g.http.URL, path, capability(g, "gradle", access.CapabilityRead), true, nil, 0, false)
		require(row.Status == 404, "identical native key cannot read another project")
		row, _ = f.request(fmt.Sprintf("isolated-project-%d-publish", i), "PUT", g.http.URL, path, capability(g, "gradle", access.CapabilityWrite), true, bytes.NewReader(other), 4096, false)
		require(row.Status == 200, "independent project can publish its own native key")
	}
	row, _ = f.request("primary-winner-after-other-projects", "GET", a.http.URL, path, reader, true, nil, 0, true)
	require(row.SHA256 == digest(first), "same and different bucket projects preserve first project's bytes")
	key := artifact.Key{Integration: "gradle", Project: a.cfg.ProjectID, Compatibility: a.cfg.CompatibilityID, Native: "opaque-fixture"}
	_, created, err := a.store.Put(f.ctx, key, artifact.Metadata{}, bytes.NewReader(other))
	require(errors.Is(err, artifact.ErrConflict) && !created, "cloud store rejects conflicting immutable bytes")
	row, _ = f.request("gradle-duplicate-retains-winner", "PUT", a.http.URL, path, writer, true, bytes.NewReader(other), 4096, false)
	require(row.Status == 200, "native Gradle equivalent-archive acknowledgement preserves original winner")
	row, _ = f.request("winner-after-native-duplicate", "GET", a.http.URL, path, reader, true, nil, 0, true)
	require(row.SHA256 == digest(first), "native duplicate never replaces immutable committed bytes")
	_, err = c.store.SetQuota(f.ctx, "qualification", 1024)
	check(err, "set isolated project quota")
	row, _ = f.request("project-quota-refusal", "PUT", c.http.URL, "/v1/gradle-cache/over-quota", capability(c, "gradle", access.CapabilityWrite), true, bytes.NewReader(first), 4096, false)
	require(row.Status == 413, "authoritative project quota rejects oversized upload")
	check(a.store.ReconcileMembers(f.ctx, "qualification", nil), "revoke isolated project membership")
	row, _ = f.request("current-membership-revocation", "GET", a.http.URL, path, reader, true, nil, 0, false)
	require(row.Status == 401, "current PostgreSQL membership denies still-valid signed credential")
	check(a.store.UpsertMembers(f.ctx, "qualification", []cloud.Member{{Project: a.cfg.ProjectID, Subject: "github:qa", Role: "writer"}}), "restore owned qualification membership")
	for _, control := range []string{"missing", "corrupt"} {
		payload := bytes.Repeat([]byte(control[:1]), 4096)
		objectPath := "/v1/gradle-cache/" + control
		row, _ = f.request(control+"-control-publish", "PUT", a.http.URL, objectPath, writer, true, bytes.NewReader(payload), 4096, false)
		require(row.Status == 200, "publish owned integrity-control bytes")
		objectKey := blobKey(a.cfg.ProjectID, digest(payload))
		if control == "missing" {
			check(p.client.RemoveObject(f.ctx, p.Bucket, objectKey, minio.RemoveObjectOptions{}), "remove only owned missing-blob control")
		} else {
			_, err := p.client.PutObject(f.ctx, p.Bucket, objectKey, bytes.NewReader(other), 4096, minio.PutObjectOptions{UserMetadata: map[string]string{"sha256": digest(payload), "size": "4096"}})
			check(err, "overwrite only owned corruption-control object")
		}
		row, _ = f.request(control+"-blob-rejected-before-success", "GET", a.http.URL, objectPath, reader, true, nil, 0, true)
		require(row.Status == 404, "missing/corrupt optional bytes are rejected before an accepted response")
	}
	f.result.Controls = append(f.result.Controls, "private cache restart and exact read-only bytes", "same-bucket and two-bucket project isolation", "immutable conflict and native Gradle duplicate-winner semantics", "authoritative project quota and membership revocation", "missing/corrupt optional bytes rejected before acceptance")
}

func (f *fixture) retainedControls(p *provider, g *gateway) {
	fmt.Println("qualifying streamed retained publication and independent consumers")
	now := time.Now().UTC()
	claims := access.Claims{Subject: "github-actions:repo:acme/fixture:pull_request", Project: g.cfg.ProjectID, Integration: "retained", Repository: "acme/fixture", RepositoryID: "42", SourceCommit: strings.Repeat("a", 40), WorkflowRef: "acme/fixture/.github/workflows/qa.yml@refs/pull/7/merge", ActionsRunID: "100", RunAttempt: "1", CheckRunID: "200", ActionsActorID: "300", ActionsActor: "qa", Compatibility: "opaque-retained-v1", Capabilities: []access.Capability{access.CapabilityWrite}, ExpiresAt: now.Add(time.Hour)}
	writer, err := access.MintCapabilityToken(g.cfg.LocalToken, claims, now)
	check(err, "mint isolated retained producer capability")
	declaration := retained.Declaration{Producer: retained.Producer{Project: claims.Project, Repository: claims.Repository, RepositoryID: claims.RepositoryID, SourceCommit: claims.SourceCommit, WorkflowRef: claims.WorkflowRef, RunID: claims.ActionsRunID, RunAttempt: claims.RunAttempt, JobID: claims.CheckRunID, Profile: claims.Compatibility}, Name: "synthetic-required", Digest: patternDigest(largeBytes), Size: largeBytes, RetainUntil: now.Add(time.Hour).Truncate(time.Microsecond)}
	overQuota := declaration
	overQuota.Name, overQuota.Size = "unadmitted-over-quota", g.cfg.MaxBytes+1
	oversizeJSON, _ := json.Marshal(overQuota)
	row, _ := f.request("retained-over-quota-admission-refused", "POST", g.http.URL, "/v1/retained-artifacts", writer, false, bytes.NewReader(oversizeJSON), int64(len(oversizeJSON)), false)
	require(row.Status == 507, "required admission refuses quota before any content upload")
	encoded, _ := json.Marshal(declaration)
	row, data := f.request("retained-admission", "POST", g.http.URL, "/v1/retained-artifacts", writer, false, bytes.NewReader(encoded), int64(len(encoded)), false)
	require(row.Status == 200, "retained declaration admitted before accepting bytes")
	var reserved retained.Receipt
	check(json.Unmarshal(data, &reserved), "decode admitted retained receipt")
	require(reserved.State == retained.Reserved && reserved.Declaration == declaration, "complete immutable retained declaration is preserved")
	g.dropAck.Store(true)
	request, err := http.NewRequestWithContext(f.ctx, "PUT", g.http.URL+"/v1/retained-artifacts/"+reserved.ID+"/content", newPattern(largeBytes))
	check(err, "create streamed retained upload")
	request.ContentLength = largeBytes
	request.Header.Set("Authorization", "Bearer "+writer)
	started := time.Now()
	response, uploadErr := f.http.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	require(uploadErr != nil && g.published.Load() == 1, "actual durable publication succeeds before deliberately lost acknowledgement")
	f.result.Measurements = append(f.result.Measurements, measurement{Name: "retained-124mb-publish-stage-copy-sql-lost-ack", Size: largeBytes, SHA256: declaration.Digest, CompleteMS: milliseconds(started)})
	row, data = f.request("retained-unknown-commit-inspect", "GET", g.http.URL, "/v1/retained-artifacts/"+reserved.ID, writer, false, nil, 0, false)
	require(row.Status == 200, "authoritative inspection resolves unknown publication")
	var published retained.Receipt
	check(json.Unmarshal(data, &published), "decode authoritative retained receipt")
	require(published.State == retained.Published && published.Declaration == declaration && published.ID == reserved.ID, "durable receipt binds exact provenance digest size and deadline")
	row, data = f.request("retained-idempotent-reservation", "POST", g.http.URL, "/v1/retained-artifacts", writer, false, bytes.NewReader(encoded), int64(len(encoded)), false)
	require(row.Status == 200 && g.published.Load() == 1, "unknown outcome reconciles without another content upload")
	started = time.Now()
	info, err := p.client.StatObject(f.ctx, p.Bucket, blobKey(g.cfg.ProjectID, declaration.Digest), minio.StatObjectOptions{})
	check(err, "inspect actual committed R2 metadata")
	require(info.Size == largeBytes && metadata(info, "sha256") == declaration.Digest && metadata(info, "size") == strconv.FormatInt(largeBytes, 10) && metadata(info, "layercache-state") == "committed", "production R2 copy preserves complete custom digest size and committed metadata")
	f.result.Measurements = append(f.result.Measurements, measurement{Name: "retained-124mb-r2-committed-metadata-lookup", CompleteMS: milliseconds(started)})
	changed := declaration
	changed.RetainUntil = changed.RetainUntil.Add(time.Second)
	changedJSON, _ := json.Marshal(changed)
	row, _ = f.request("retained-deadline-immutable", "POST", g.http.URL, "/v1/retained-artifacts", writer, false, bytes.NewReader(changedJSON), int64(len(changedJSON)), false)
	require(row.Status == 409, "retained deadline cannot change after admission")
	_, quotaErr := g.store.SetQuota(f.ctx, "qualification", largeBytes-1)
	require(errors.Is(quotaErr, artifact.ErrQuota), "quota shrink cannot evict a retained obligation")
	require(errors.Is(g.store.GC(f.ctx, 0), artifact.ErrQuota), "full GC cannot evict a required retained obligation")
	check(g.store.GC(f.ctx, largeBytes), "optional cache GC respects retained claims")
	check(g.stop(), "stop actual retained gateway before queued consumer")
	g.start(f.ctx)
	claims.Capabilities = []access.Capability{access.CapabilityRead}
	claims.CheckRunID = "201"
	for _, attempt := range []string{"1", "2"} {
		claims.RunAttempt = attempt
		consumer, err := access.MintCapabilityToken(g.cfg.LocalToken, claims, time.Now().UTC())
		check(err, "mint isolated read-only retained consumer")
		row, _ = f.request("retained-124mb-independent-read-attempt-"+attempt, "GET", g.http.URL, "/v1/retained-artifacts/"+published.ID+"/content", consumer, false, nil, 0, true)
		require(row.Status == 200 && !row.ReadError && row.Size == largeBytes && row.SHA256 == declaration.Digest, "consumer installs no bytes before complete size/digest verification")
		row, _ = f.request("retained-read-only-publication-denied-attempt-"+attempt, "POST", g.http.URL, "/v1/retained-artifacts", consumer, false, bytes.NewReader(encoded), int64(len(encoded)), false)
		require(row.Status == 403, "read-only retained consumers cannot publish")
	}
	claims.ActionsRunID = "101"
	foreign, err := access.MintCapabilityToken(g.cfg.LocalToken, claims, time.Now().UTC())
	check(err, "mint foreign-run retained control")
	row, _ = f.request("retained-foreign-run-hidden", "GET", g.http.URL, "/v1/retained-artifacts/"+published.ID, foreign, false, nil, 0, false)
	require(row.Status == 404, "foreign producing run cannot consume retained receipt")
	small := bytes.Repeat([]byte{0x53}, 4096)
	bad := declaration
	bad.Name, bad.Size, bad.Digest = "bad-content-control", 4096, digest(small)
	badJSON, _ := json.Marshal(bad)
	row, data = f.request("retained-wrong-content-admission", "POST", g.http.URL, "/v1/retained-artifacts", writer, false, bytes.NewReader(badJSON), int64(len(badJSON)), false)
	require(row.Status == 200, "admit only owned retained integrity control")
	var badReceipt retained.Receipt
	check(json.Unmarshal(data, &badReceipt), "decode retained integrity-control receipt")
	row, _ = f.request("retained-wrong-content-rejected", "PUT", g.http.URL, "/v1/retained-artifacts/"+badReceipt.ID+"/content", writer, false, bytes.NewReader(bytes.Repeat([]byte{0x54}, 4096)), 4096, false)
	require(row.Status == 422, "actual R2 staged wrong digest never publishes accepted retained bytes")
	row, data = f.request("retained-wrong-content-not-published", "GET", g.http.URL, "/v1/retained-artifacts/"+badReceipt.ID, writer, false, nil, 0, false)
	require(row.Status == 200, "inspect rejected retained content outcome")
	check(json.Unmarshal(data, &badReceipt), "decode rejected retained outcome")
	require(badReceipt.State != retained.Published, "wrong retained bytes do not acquire publication acknowledgement")
	short := bad
	short.Name, short.RetainUntil = "short-expiry-control", time.Now().UTC().Add(10*time.Second).Truncate(time.Microsecond)
	shortJSON, _ := json.Marshal(short)
	row, data = f.request("retained-short-ttl-admission", "POST", g.http.URL, "/v1/retained-artifacts", writer, false, bytes.NewReader(shortJSON), int64(len(shortJSON)), false)
	require(row.Status == 200, "admit short bounded real expiry control")
	var shortReceipt retained.Receipt
	check(json.Unmarshal(data, &shortReceipt), "decode short expiry receipt")
	row, _ = f.request("retained-short-ttl-publish", "PUT", g.http.URL, "/v1/retained-artifacts/"+shortReceipt.ID+"/content", writer, false, bytes.NewReader(small), 4096, false)
	require(row.Status == 200, "short retained control publishes before its fixed deadline")
	row, _ = f.request("retained-short-ttl-before-expiry", "GET", g.http.URL, "/v1/retained-artifacts/"+shortReceipt.ID+"/content", writer, false, nil, 0, true)
	require(row.Status == 200 && row.SHA256 == digest(small) && !row.ReadError, "short retained content is readable before deadline")
	select {
	case <-f.ctx.Done():
		panic(failure{"elapsed retained expiry control canceled"})
	case <-time.After(max(0, time.Until(short.RetainUntil.Add(100*time.Millisecond)))):
	}
	row, data = f.request("retained-short-ttl-expired-state", "GET", g.http.URL, "/v1/retained-artifacts/"+shortReceipt.ID, writer, false, nil, 0, false)
	require(row.Status == 200, "expired retained tombstone remains inspectable")
	check(json.Unmarshal(data, &shortReceipt), "decode real elapsed expiry state")
	require(shortReceipt.State == retained.Expired, "actual elapsed fixed deadline expires retained receipt")
	row, _ = f.request("retained-short-ttl-content-denied", "GET", g.http.URL, "/v1/retained-artifacts/"+shortReceipt.ID+"/content", writer, false, nil, 0, false)
	require(row.Status == 410, "expired retained content cannot be consumed")
	row, data = f.request("retained-one-hour-receipt-isolated-from-short-expiry", "GET", g.http.URL, "/v1/retained-artifacts/"+published.ID, writer, false, nil, 0, false)
	require(row.Status == 200, "independent one-hour receipt survives other receipt expiry")
	var unaffected retained.Receipt
	check(json.Unmarshal(data, &unaffected), "decode unaffected long receipt")
	require(unaffected.State == retained.Published && unaffected.Declaration == declaration, "receipt expiry never revokes another retained publication")
	f.result.RetainedID, f.result.RetainUntil = published.ID, declaration.RetainUntil
	f.result.Controls = append(f.result.Controls, "124078900-byte streamed retained R2 publication with exact receipt", "lost acknowledgement reconciled without duplicate upload", "fixed deadline survives restart and cannot be extended", "retained admission refuses over-quota content before upload and protects quota shrink/optional GC", "read-only same-run consumer and consumer-only later-attempt rerun verify full bytes", "foreign run hidden and retained read-only publication denied", "actual R2 wrong retained bytes never published", "real elapsed short TTL expires independently of the larger retained publication")
}

type patternReader struct{ remaining, offset int64 }

func newPattern(size int64) *patternReader { return &patternReader{remaining: size} }
func (reader *patternReader) Read(buffer []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(buffer)), reader.remaining)
	for i := int64(0); i < n; i++ {
		buffer[i] = byte((reader.offset + i) % 251)
	}
	reader.offset += n
	reader.remaining -= n
	return int(n), nil
}

func patternDigest(size int64) string {
	hasher := sha256.New()
	_, err := io.Copy(hasher, newPattern(size))
	check(err, "hash deterministic synthetic stream")
	return hex.EncodeToString(hasher.Sum(nil))
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func randomHex(n int) string {
	data := make([]byte, n)
	_, err := rand.Read(data)
	check(err, "generate unique owned qualification identity")
	return hex.EncodeToString(data)
}
func milliseconds(start time.Time) float64 { return float64(time.Since(start).Microseconds()) / 1000 }
func blobKey(project, sum string) string {
	return "projects/" + digest([]byte(project))[:32] + "/blobs/sha256/" + sum
}
func metadata(info minio.ObjectInfo, key string) string {
	if value := info.Metadata.Get("X-Amz-Meta-" + key); value != "" {
		return value
	}
	return info.UserMetadata[key]
}
