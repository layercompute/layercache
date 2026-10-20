package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/measurement"
)

func TestActionsReportsAttributeSelectedRuntimeSource(t *testing.T) {
	for _, backend := range []string{"local", "team", "cloud-team"} {
		t.Run(backend, func(t *testing.T) {
			const archive = "source-report-archive"
			cfg, objectsRead := sourceAttributionConfig(t, backend, archive)
			instance, err := New(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = instance.Close() })
			endpoint := httptest.NewServer(instance.Handler())
			t.Cleanup(endpoint.Close)
			claims := sourceAttributionClaims(cfg, "actions")
			request := sourceAttributionRequest(t, endpoint.URL, cfg, claims)
			reservation := request(http.MethodPost, "/_apis/artifactcache/caches", `{"key":"source-report","version":"v1","cacheSize":21}`, http.StatusCreated, nil)
			var result struct {
				ID int64 `json:"cacheId"`
			}
			if err := json.Unmarshal(reservation, &result); err != nil || result.ID == 0 {
				t.Fatalf("reserve archive: %s, error=%v", reservation, err)
			}
			archivePath := fmt.Sprintf("/_apis/artifactcache/caches/%d", result.ID)
			request(http.MethodPatch, archivePath, archive, http.StatusNoContent, map[string]string{"Content-Range": "bytes 0-20/*"})
			request(http.MethodPost, archivePath, `{"size":21}`, http.StatusNoContent, nil)
			lookup := request(http.MethodGet, "/_apis/artifactcache/cache?keys=source-report&version=v1", "", http.StatusOK, nil)
			var location struct {
				URL string `json:"archiveLocation"`
			}
			if err := json.Unmarshal(lookup, &location); err != nil || location.URL == "" {
				t.Fatalf("lookup archive: %s, error=%v", lookup, err)
			}
			if got := string(request(http.MethodGet, location.URL, "", http.StatusOK, nil)); got != archive {
				t.Fatalf("downloaded archive = %q", got)
			}
			report := sourceAttributionReport(t, request, claims.RunID)
			want := measurement.SourceTeamCache
			if backend == "local" {
				want = measurement.SourceLocalCache
			}
			if report.Hits != 1 || len(report.Outcomes) != 1 || report.Outcomes[0].Source != want || len(report.Sources) != 1 || report.Sources[0].Source != want || report.Bytes.Downloaded != int64(len(archive)) {
				t.Fatalf("%s archive source report = %+v; want one %s hit", backend, report, want)
			}
			if backend == "cloud-team" {
				if _, ok := instance.store.(*cloud.Store); !ok || objectsRead.Load() == 0 {
					t.Fatal("requires the real cloud store and successful S3 SDK reads")
				}
			}
		})
	}
}

func TestTurboReportsAttributeSelectedTeamRuntimeSource(t *testing.T) {
	for _, backend := range []string{"team", "cloud-team"} {
		t.Run(backend, func(t *testing.T) {
			const archive = "source-report-archive"
			cfg, objectsRead := sourceAttributionConfig(t, backend, archive)
			instance, err := New(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = instance.Close() })
			endpoint := httptest.NewServer(instance.Handler())
			t.Cleanup(endpoint.Close)
			claims := sourceAttributionClaims(cfg, "turbo")
			request := sourceAttributionRequest(t, endpoint.URL, cfg, claims)
			request(http.MethodPut, "/v8/artifacts/build-hash", archive, http.StatusOK, nil)
			if got := string(request(http.MethodGet, "/v8/artifacts/build-hash", "", http.StatusOK, nil)); got != archive {
				t.Fatalf("downloaded archive = %q", got)
			}
			response := postTurboReport(t, instance, claims, reportSummary(t, "source-summary", "HIT", time.Now().UTC()))
			if response.Code != http.StatusOK {
				t.Fatalf("reconcile: HTTP %d %s", response.Code, response.Body.String())
			}
			report := sourceAttributionReport(t, request, claims.RunID)
			if report.Hits != 1 || len(report.Outcomes) != 1 || report.Outcomes[0].Source != measurement.SourceTeamCache || len(report.Sources) != 1 || report.Sources[0].Source != measurement.SourceTeamCache || report.Bytes.Downloaded != int64(len(archive)) {
				t.Fatalf("%s native Turbo source report = %+v", backend, report)
			}
			if backend == "cloud-team" {
				if _, ok := instance.store.(*cloud.Store); !ok || objectsRead.Load() == 0 {
					t.Fatal("requires the real cloud store and successful S3 SDK reads")
				}
			}
		})
	}
}

func sourceAttributionClaims(cfg config.Config, integration string) access.Claims {
	return access.Claims{
		Subject: "github:alice", Project: cfg.ProjectID, Integration: integration,
		Compatibility: cfg.CompatibilityID, Repository: cfg.ActionsRepository,
		Ref: cfg.ActionsRef, DefaultRef: cfg.ActionsDefaultRef,
		RunID:        "github-local:github:alice:run-source-attribution-" + integration,
		WorkspaceID:  "github-local:github:alice:sha256:" + strings.Repeat("a", 64),
		Capabilities: []access.Capability{access.CapabilityWrite}, ExpiresAt: time.Now().Add(time.Hour),
	}
}

type sourceRequest func(string, string, string, int, map[string]string) []byte

func sourceAttributionRequest(t *testing.T, endpoint string, cfg config.Config, claims access.Claims) sourceRequest {
	t.Helper()
	token, err := access.MintCapabilityToken(cfg.LocalToken, claims, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return func(method, target, body string, want int, headers map[string]string) []byte {
		t.Helper()
		if strings.HasPrefix(target, "/") {
			target = endpoint + target
		}
		request, err := http.NewRequest(method, target, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		authorization := token
		if strings.Contains(target, "/v1/reports/") {
			authorization = cfg.LocalToken
		}
		request.Header.Set("Authorization", "Bearer "+authorization)
		request.Header.Set("X-LayerCache-Compatibility", cfg.CompatibilityID)
		request.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("%s request: HTTP %d, body=%q, error=%v", method, response.StatusCode, data, err)
		}
		return data
	}
}

func sourceAttributionReport(t *testing.T, request sourceRequest, run string) measurement.RunReport {
	t.Helper()
	data := request(http.MethodGet, "/v1/reports/"+url.PathEscape(run), "", http.StatusOK, nil)
	var report measurement.RunReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func sourceAttributionConfig(t *testing.T, backend, archive string) (config.Config, *atomic.Int64) {
	t.Helper()
	cfg := gatewayTestConfig(t, "source-attribution")
	cfg.CompatibilityID = "linux-amd64-schema1"
	cfg.ActionsRef, cfg.ActionsDefaultRef = "refs/heads/main", "refs/heads/main"
	reads := &atomic.Int64{}
	if backend == "local" {
		cfg.Role = "local"
	}
	if backend != "cloud-team" {
		return cfg, reads
	}
	postgresURL := os.Getenv("LAYER_CACHE_QA_POSTGRES_URL")
	if postgresURL == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	admin, err := sql.Open("pgx", postgresURL)
	if err != nil {
		t.Fatal("open disposable PostgreSQL")
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("source_report_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal("create isolated source reporting schema")
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error("drop isolated source reporting schema")
		}
	})
	scoped, err := url.Parse(postgresURL)
	if err != nil {
		t.Fatal("parse disposable PostgreSQL URL")
	}
	query := scoped.Query()
	query.Set("search_path", schema)
	scoped.RawQuery = query.Encode()
	cfg.CloudPostgresURL = scoped.String()
	// This fixed archive endpoint exercises the production SDK and metadata
	// store. It is deliberately not a general S3 implementation or an integrity
	// fixture: both native protocols publish and read this exact payload.
	digest := sha256.Sum256([]byte(archive))
	objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/cache/":
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
			_, _ = io.WriteString(w, "<ListBucketResult><Name>cache</Name><IsTruncated>false</IsTruncated></ListBucketResult>")
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			_, _ = io.WriteString(w, "<InitiateMultipartUploadResult><Bucket>cache</Bucket><Key>fixture</Key><UploadId>fixture</UploadId></InitiateMultipartUploadResult>")
		case r.Method == http.MethodPut && r.URL.Query().Get("partNumber") != "":
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("ETag", `"fixture"`)
		case r.Method == http.MethodPost && r.URL.Query().Get("uploadId") != "":
			_, _ = io.WriteString(w, "<CompleteMultipartUploadResult><Location>/cache/fixture</Location><Bucket>cache</Bucket><Key>fixture</Key><ETag>\"fixture\"</ETag></CompleteMultipartUploadResult>")
		case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
			_, _ = io.WriteString(w, "<CopyObjectResult><LastModified>2026-10-20T00:00:00.000Z</LastModified><ETag>\"fixture\"</ETag></CopyObjectResult>")
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodHead || r.Method == http.MethodGet:
			w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
			w.Header().Set("Last-Modified", "Tue, 20 Oct 2026 00:00:00 GMT")
			w.Header().Set("ETag", `"fixture"`)
			w.Header().Set("X-Amz-Meta-Sha256", hex.EncodeToString(digest[:]))
			if r.Method == http.MethodGet {
				reads.Add(1)
				_, _ = io.WriteString(w, archive)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(objects.Close)
	cfg.CloudS3Endpoint, cfg.CloudS3Bucket, cfg.CloudS3Region = objects.URL, "cache", "us-east-1"
	cfg.CloudS3UsePathStyle = true
	cfg.CloudS3AccessKey, cfg.CloudS3SecretKey = "fixture-access", "fixture-secret"
	return cfg, reads
}
