// Package retainedqa qualifies the actual private retained transport with the
// production server, PostgreSQL, MinIO SDK/backend and direct JavaScript client.
package retainedqa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/retained"
	"github.com/layercache/layercache/internal/server"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const artifactBytes int64 = 124078900

type nodeResult struct {
	Receipt  retained.Receipt `json:"receipt"`
	Evidence json.RawMessage  `json:"evidence"`
}

func TestActualRetainedServerAndClient(t *testing.T) {
	pgURL := os.Getenv("LAYER_CACHE_QA_POSTGRES_URL")
	endpoint := os.Getenv("LAYER_CACHE_QA_S3_ENDPOINT")
	if pgURL == "" || endpoint == "" {
		t.Skip("set disposable loopback LAYER_CACHE_QA_POSTGRES_URL and LAYER_CACHE_QA_S3_ENDPOINT for retained integration qualification")
	}
	pg := requireLoopback(t, pgURL, "postgres")
	s3URL := requireLoopback(t, endpoint, "http")
	if os.Getenv("LAYER_CACHE_QA_S3_ACCESS_KEY") == "" || os.Getenv("LAYER_CACHE_QA_S3_SECRET_KEY") == "" {
		t.Fatal("disposable S3 fixture credentials required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	unique := fmt.Sprintf("%d", time.Now().UnixNano())
	schema := "retained_sdk_" + unique
	bucket := "retained-qa-" + unique
	admin, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanup, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	query := pg.Query()
	query.Set("search_path", schema)
	pg.RawQuery = query.Encode()
	region := os.Getenv("LAYER_CACHE_QA_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	s3, err := minio.New(s3URL.Host, &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("LAYER_CACHE_QA_S3_ACCESS_KEY"), os.Getenv("LAYER_CACHE_QA_S3_SECRET_KEY"), ""), Region: region, Secure: false, BucketLookup: minio.BucketLookupPath})
	if err != nil {
		t.Fatal(err)
	}
	if err = s3.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: region}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for object := range s3.ListObjects(cleanup, bucket, minio.ListObjectsOptions{Recursive: true}) {
			if object.Err != nil {
				t.Error(object.Err)
				continue
			}
			if err := s3.RemoveObject(cleanup, bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
				t.Error(err)
			}
		}
		if err := s3.RemoveBucket(cleanup, bucket); err != nil {
			t.Error(err)
		}
	})
	scoped, err := sql.Open("pgx", pg.String())
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	root := t.TempDir()
	source := filepath.Join(root, "synthetic-required.bin")
	digest := writeSynthetic(t, source, artifactBytes)
	cfg := config.Config{Version: 1, Role: "team", DataDir: filepath.Join(root, "server"), Listen: "127.0.0.1:7437", MaxBytes: 256 << 20, ProjectID: "retained-integration-qa", LocalToken: "disposable-retained-local-signing-secret", CompatibilityID: "server-host-v1", ActionsRepository: "acme/fixture", ActionsRef: "refs/heads/main", ActionsDefaultRef: "refs/heads/main", TeamMembers: map[string]string{"alice": "writer"}, RetainedArtifactsEnabled: true, CloudPostgresURL: pg.String(), CloudS3Endpoint: endpoint, CloudS3Bucket: bucket, CloudS3Region: region, CloudS3AccessKey: os.Getenv("LAYER_CACHE_QA_S3_ACCESS_KEY"), CloudS3SecretKey: os.Getenv("LAYER_CACHE_QA_S3_SECRET_KEY"), CloudS3UsePathStyle: true}
	var service *server.Server
	var dropPublicationAck atomic.Bool
	dropPublicationAck.Store(true)
	var httpServer *httptest.Server
	start := func() {
		var err error
		service, err = server.New(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		httpServer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodPut && strings.HasSuffix(request.URL.Path, "/content") && dropPublicationAck.CompareAndSwap(true, false) {
				// Commit the real SQL/S3 publication, then lose only its HTTP response.
				recorded := httptest.NewRecorder()
				service.Handler().ServeHTTP(recorded, request)
				if recorded.Code != http.StatusOK {
					for k, values := range recorded.Header() {
						writer.Header()[k] = values
					}
					writer.WriteHeader(recorded.Code)
					_, _ = writer.Write(recorded.Body.Bytes())
					return
				}
				connection, _, err := writer.(http.Hijacker).Hijack()
				if err != nil {
					panic(err)
				}
				_ = connection.Close()
				return
			}
			service.Handler().ServeHTTP(writer, request)
		}))
	}
	stop := func() {
		if httpServer != nil {
			httpServer.Close()
			httpServer = nil
		}
		if service != nil {
			if err := service.Close(); err != nil {
				t.Error(err)
			}
			service = nil
		}
	}
	defer stop()
	start()
	claims := access.Claims{Subject: "github-actions:repo:acme/fixture:pull_request", Project: cfg.ProjectID, Integration: "retained", Repository: "acme/fixture", RepositoryID: "42", SourceCommit: strings.Repeat("a", 40), WorkflowRef: "acme/fixture/.github/workflows/qa.yml@refs/pull/7/merge", ActionsRunID: "100", RunAttempt: "1", CheckRunID: "200", ActionsActorID: "300", ActionsActor: "alice", Compatibility: "qa-opaque-v1", Capabilities: []access.Capability{access.CapabilityWrite}, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	producer := retained.Producer{Project: claims.Project, Repository: claims.Repository, RepositoryID: claims.RepositoryID, SourceCommit: claims.SourceCommit, WorkflowRef: claims.WorkflowRef, RunID: claims.ActionsRunID, RunAttempt: claims.RunAttempt, JobID: claims.CheckRunID, Profile: claims.Compatibility}
	writer := mint(t, cfg.LocalToken, claims)
	retainUntil := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	options := map[string]any{"endpoint": httpServer.URL, "token": writer, "path": source, "producer": producer, "name": "required-opaque", "retainUntil": retainUntil, "bodyTimeoutMs": 120000, "idleTimeoutMs": 30000}
	published := runNode(t, ctx, root, "publish", options)
	if published.Receipt.State != retained.Published || published.Receipt.Declaration.Digest != digest || published.Receipt.Declaration.Size != artifactBytes || !published.Receipt.Declaration.RetainUntil.Equal(retainUntil) {
		t.Fatalf("incorrect published declaration %+v", published.Receipt)
	}
	var publicationEvidence struct {
		Attempts []struct {
			Operation  string `json:"operation"`
			Reconciled bool   `json:"reconciled"`
		} `json:"attempts"`
	}
	if err = json.Unmarshal(published.Evidence, &publicationEvidence); err != nil {
		t.Fatal(err)
	}
	reconciled := false
	for _, attempt := range publicationEvidence.Attempts {
		if attempt.Operation == "upload" && attempt.Reconciled {
			reconciled = true
		}
	}
	if !reconciled {
		t.Fatal("lost real publication acknowledgement was not resolved through Inspect")
	}
	inspected := inspect(t, httpServer.URL, writer, published.Receipt.ID)
	if inspected.State != retained.Published || inspected.ID != published.Receipt.ID {
		t.Fatal("authoritative inspection did not resolve committed identity")
	}
	assertAccounting(t, ctx, scoped, cfg.ProjectID, published.Receipt.ID, artifactBytes)
	// A duplicate call must reconcile the one durable publication without PUT.
	duplicate := runNode(t, ctx, root, "publish", options)
	var duplicateEvidence struct {
		Attempts []struct {
			Operation string `json:"operation"`
		} `json:"attempts"`
	}
	if err = json.Unmarshal(duplicate.Evidence, &duplicateEvidence); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range duplicateEvidence.Attempts {
		if attempt.Operation == "upload" {
			t.Fatal("committed retry duplicated upload")
		}
	}
	// Reader authority belongs to another job in the producing workflow run.
	readerClaims := claims
	readerClaims.CheckRunID = "201"
	readerClaims.Capabilities = []access.Capability{access.CapabilityRead}
	reader := mint(t, cfg.LocalToken, readerClaims)
	declarationJSON, _ := json.Marshal(published.Receipt.Declaration)
	requireStatus(t, ctx, httpServer.URL, "POST", "/v1/retained-artifacts", reader, declarationJSON, http.StatusForbidden)
	foreignClaims := readerClaims
	foreignClaims.ActionsRunID = "101"
	foreign := mint(t, cfg.LocalToken, foreignClaims)
	requireStatus(t, ctx, httpServer.URL, "GET", "/v1/retained-artifacts/"+published.Receipt.ID+"/content", foreign, nil, http.StatusNotFound)
	oversized := published.Receipt.Declaration
	oversized.Name = "oversized"
	oversized.Size = cfg.MaxBytes + 1
	oversizedJSON, _ := json.Marshal(oversized)
	requireStatus(t, ctx, httpServer.URL, "POST", "/v1/retained-artifacts", writer, oversizedJSON, http.StatusInsufficientStorage)
	// Close every server/storage handle, then reconstruct solely from SQL/S3.
	stop()
	start()
	readOptions := map[string]any{"endpoint": httpServer.URL, "token": reader, "path": filepath.Join(root, "reader-one.bin"), "receipt": published.Receipt, "expected": map[string]any{"producer": producer, "name": "required-opaque"}, "bodyTimeoutMs": 120000, "idleTimeoutMs": 30000}
	first := runNode(t, ctx, root, "read", readOptions)
	verifyFile(t, readOptions["path"].(string), artifactBytes, digest)
	rerunClaims := readerClaims
	rerunClaims.CheckRunID = "202"
	rerunClaims.RunAttempt = "2"
	rerun := mint(t, cfg.LocalToken, rerunClaims)
	readOptions["token"] = rerun
	readOptions["path"] = filepath.Join(root, "reader-rerun.bin")
	second := runNode(t, ctx, root, "read", readOptions)
	verifyFile(t, readOptions["path"].(string), artifactBytes, digest)
	assertAccounting(t, ctx, scoped, cfg.ProjectID, published.Receipt.ID, artifactBytes)
	// Corrupt canonical bytes directly through the owned fixture backend. The
	// client must fail visibly and remove any prior output at its destination.
	var objectKey string
	if err = scoped.QueryRowContext(ctx, `SELECT object_key FROM layercache_artifacts_v1 WHERE project_id=$1 AND digest=$2`, cfg.ProjectID, digest).Scan(&objectKey); err != nil {
		t.Fatal(err)
	}
	if _, err = s3.PutObject(ctx, bucket, objectKey, strings.NewReader("corrupt"), 7, minio.PutObjectOptions{}); err != nil {
		t.Fatal(err)
	}
	readOptions["path"] = filepath.Join(root, "must-not-survive.bin")
	if err = os.WriteFile(readOptions["path"].(string), []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	runNodeFailure(t, ctx, root, "read", readOptions)
	if _, err = os.Lstat(readOptions["path"].(string)); !os.IsNotExist(err) {
		t.Fatalf("failed integrity retained destination: %v", err)
	}
	current := inspect(t, httpServer.URL, rerun, published.Receipt.ID)
	if current.State != retained.Unavailable {
		t.Fatalf("corruption state=%s", current.State)
	}
	report := map[string]any{"schemaVersion": "retained-integration-qualification-v1", "scope": "disposable local production server + PostgreSQL + real MinIO + direct JavaScript client; locally minted capabilities bypass GitHub issuer/exchange; no hosted or production proof", "payloadBytes": artifactBytes, "payloadSha256": digest, "receipt": published.Receipt, "publisher": json.RawMessage(published.Evidence), "readerAfterRestart": json.RawMessage(first.Evidence), "consumerOnlyAttempt2": json.RawMessage(second.Evidence), "qualified": []string{"immutable publication", "authoritative inspect", "duplicate upload avoided", "lost publication response reconciled through authoritative Inspect", "read-only and foreign-run denial", "quota admission rejection", "server restart recovery", "another-job read", "consumer-only later attempt", "complete size and SHA256", "SQL receipt/claim/pin/accounting", "corrupt-object rejection and no stale output"}}
	data, _ := json.Marshal(report)
	t.Log(string(data))
	if path := os.Getenv("LAYER_CACHE_QA_RETAINED_REPORT"); path != "" {
		if err = os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func requireLoopback(t *testing.T, raw, scheme string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != scheme || u.User != nil && scheme == "http" || u.Fragment != "" || u.Path != "" && scheme == "http" {
		t.Fatal("qualification requires explicit disposable loopback endpoints")
	}
	host := u.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			t.Fatal("qualification refuses non-loopback endpoints")
		}
	}
	return u
}
func mint(t *testing.T, secret string, claims access.Claims) string {
	t.Helper()
	token, err := access.MintCapabilityToken(secret, claims, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func inspect(t *testing.T, endpoint, token, id string) retained.Receipt {
	t.Helper()
	request, _ := http.NewRequest("GET", endpoint+"/v1/retained-artifacts/"+id, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("inspect HTTP%d", response.StatusCode)
	}
	var result retained.Receipt
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
func requireStatus(t *testing.T, ctx context.Context, endpoint, method, path, token string, body []byte, want int) {
	t.Helper()
	request, _ := http.NewRequestWithContext(ctx, method, endpoint+path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s expectedHTTP%d got%d", method, want, response.StatusCode)
	}
}
func writeSynthetic(t *testing.T, path string, size int64) string {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	chunk := bytes.Repeat([]byte("retained-opaque-fixture\x00\xff"), 4096)
	remaining := size
	for remaining > 0 {
		part := chunk
		if int64(len(part)) > remaining {
			part = part[:remaining]
		}
		if _, err = file.Write(part); err != nil {
			t.Fatal(err)
		}
		hash.Write(part)
		remaining -= int64(len(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
func verifyFile(t *testing.T, path string, size int64, digest string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil || n != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		t.Fatalf("complete consumer identity mismatch size=%d err=%v", n, err)
	}
}
func assertAccounting(t *testing.T, ctx context.Context, db *sql.DB, project, id string, size int64) {
	t.Helper()
	var receipts, claims, pins int
	var used, reserved int64
	err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM layercache_retained_receipts_v1 WHERE project_id=$1),(SELECT COUNT(*) FROM layercache_retained_claims_v1 WHERE project_id=$1 AND receipt_id=$2),(SELECT COUNT(*) FROM layercache_entry_pins_v1 WHERE project_id=$1 AND namespace='retained-publication-v1' AND owner=$2),(SELECT used_bytes FROM layercache_projects_v1 WHERE project_id=$1),(SELECT reserved_bytes FROM layercache_retained_receipts_v1 WHERE project_id=$1 AND receipt_id=$2)`, project, id).Scan(&receipts, &claims, &pins, &used, &reserved)
	if err != nil || receipts != 1 || claims != 1 || pins != 1 || used != size || reserved != 0 {
		t.Fatalf("SQL publication/accounting receipts=%d claims=%d pins=%d used=%d reserved=%d err=%v", receipts, claims, pins, used, reserved, err)
	}
}

const nodeScript = `import {readFile} from 'node:fs/promises';
import {pathToFileURL} from 'node:url';
const input=JSON.parse(await readFile(process.env.LAYER_CACHE_RETAINED_QA_INPUT,'utf8'));
const client=await import(pathToFileURL(process.env.LAYER_CACHE_RETAINED_QA_CLIENT));
const report=client.evidence();
try { const receipt=await (input.operation==='publish'?client.publishFile:client.downloadFile)({...input.options,evidence:report});process.stdout.write(JSON.stringify({receipt,evidence:report.json()})); }
catch(error){process.stderr.write(JSON.stringify({failed:true,name:error.name,status:error.status,evidence:report.json()}));process.exitCode=1;}
`

func nodeCommand(t *testing.T, ctx context.Context, root, operation string, options map[string]any) *exec.Cmd {
	t.Helper()
	client, err := filepath.Abs(filepath.Join("..", "..", "action", "retained", "client.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(root, "node-input-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = input.Chmod(0600); err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(input).Encode(map[string]any{"operation": operation, "options": options}); err != nil {
		t.Fatal(err)
	}
	input.Close()
	cmd := exec.CommandContext(ctx, "node", "--input-type=module")
	cmd.Stdin = strings.NewReader(nodeScript)
	cmd.Env = append(os.Environ(), "LAYER_CACHE_RETAINED_QA_INPUT="+input.Name(), "LAYER_CACHE_RETAINED_QA_CLIENT="+client)
	return cmd
}
func runNode(t *testing.T, ctx context.Context, root, operation string, options map[string]any) nodeResult {
	t.Helper()
	cmd := nodeCommand(t, ctx, root, operation, options)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("direct client %s failed: %v; bounded diagnostic %s", operation, err, truncate(stderr.String(), 8192))
	}
	var result nodeResult
	if err = json.Unmarshal(out, &result); err != nil {
		t.Fatalf("invalid direct client result: %v", err)
	}
	return result
}
func runNodeFailure(t *testing.T, ctx context.Context, root, operation string, options map[string]any) {
	t.Helper()
	cmd := nodeCommand(t, ctx, root, operation, options)
	if err := cmd.Run(); err == nil {
		t.Fatal("direct client accepted corrupt content")
	}
}
func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}
