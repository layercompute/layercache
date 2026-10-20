package artifact

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type integrationStatsQuery struct {
	connection *sql.Conn
	statement  string
}

func (query *integrationStatsQuery) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	query.statement = statement
	return query.connection.QueryContext(ctx, statement, args...)
}

func integrationStatsPlan(t testing.TB, connection *sql.Conn) string {
	t.Helper()
	ctx := context.Background()
	query := &integrationStatsQuery{connection: connection}
	if _, err := readArtifactStats(ctx, query); err != nil {
		t.Fatal(err)
	}
	rows, err := connection.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query.statement)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(plan, "\n")
}

func TestIntegrationStatsUsesPersistentCoveringDigestLookup(t *testing.T) {
	store := integrationStatsSQLFixture(t, 64, true)
	connection, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	// Per-connection disabling catches a missing persistent index without
	// relying on machine speed or allowing a temporary automatic index to hide it.
	if _, err := connection.ExecContext(context.Background(), "PRAGMA automatic_index=OFF"); err != nil {
		t.Fatal(err)
	}
	plan := integrationStatsPlan(t, connection)
	if !strings.Contains(plan, "SEARCH e USING COVERING INDEX cache_entries_digest") || strings.Contains(plan, "AUTOMATIC") {
		t.Fatalf("statistics must probe a persistent digest/integration index, got:\n%s", plan)
	}
}

func TestIntegrationStatsLegacyIndexUpgradeKeepsOfflineReadsReadOnly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, root, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	for index, integration := range []string{"gradle", "gradle", "turbo", "nx"} {
		payload := "shared payload"
		if integration == "nx" {
			payload = "separate payload"
		}
		key := Key{Integration: integration, Project: "project", Compatibility: "profile", Native: fmt.Sprintf("entry-%d", index)}
		if _, _, err := store.Put(ctx, key, Metadata{}, strings.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
	}
	want, err := readArtifactStats(ctx, store.db)
	if err != nil {
		t.Fatal(err)
	}
	// Model an existing database created before the digest index existed.
	if _, err := store.db.ExecContext(ctx, "DROP INDEX IF EXISTS cache_entries_digest"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(root, "metadata.db")
	before, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	offline, err := ReadStats(ctx, root)
	if err != nil || !reflect.DeepEqual(offline, want) {
		t.Fatalf("legacy offline statistics = %+v, want %+v, error %v", offline, want, err)
	}
	after, err := os.ReadFile(metadata)
	if err != nil || string(before) != string(after) {
		t.Fatalf("offline statistics mutated legacy metadata: %v", err)
	}
	store, err = Open(ctx, root, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := readArtifactStats(ctx, store.db)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("opening legacy store changed its partition: %+v, want %+v, error %v", got, want, err)
	}
	connection, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "PRAGMA automatic_index=OFF"); err != nil {
		t.Fatal(err)
	}
	if plan := integrationStatsPlan(t, connection); !strings.Contains(plan, "SEARCH e USING COVERING INDEX cache_entries_digest") {
		t.Fatalf("normal opening did not migrate the digest lookup:\n%s", plan)
	}
}

// These fixtures measure SQL reporting only. They insert synthetic metadata in
// one transaction, without building artifacts or claiming upload/admission cost.
func integrationStatsSQLFixture(t testing.TB, references int, shared bool) *Store {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), 1<<30, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	blobs, err := tx.PrepareContext(ctx, "INSERT INTO artifacts(digest,size,created_at,last_accessed_at) VALUES(?,128,0,0)")
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	entries, err := tx.PrepareContext(ctx, `INSERT INTO cache_entries(integration,project,compatibility,native_key,version,ref_scope,digest,size,metadata_json,created_at,last_accessed_at) VALUES(?,'project','profile',?,'','',?,128,'{}',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	defer entries.Close()
	for index := range references {
		blob := index
		integration := []string{"gradle", "turbo"}[index%2]
		if shared {
			blob = index / 4
			integration = []string{"gradle", "gradle", "turbo", "nx"}[index%4]
		}
		digest := fmt.Sprintf("%064x", blob+1)
		if !shared || index%4 == 0 {
			if _, err := blobs.ExecContext(ctx, digest); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := entries.ExecContext(ctx, integration, fmt.Sprintf("entry-%d", index), digest); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return store
}

func BenchmarkIntegrationStatsDigestLookup(b *testing.B) {
	for _, references := range []int{10000, 50000} {
		for _, shared := range []bool{false, true} {
			b.Run(fmt.Sprintf("references-%d/shared-%t", references, shared), func(b *testing.B) {
				store := integrationStatsSQLFixture(b, references, shared)
				for _, variant := range []struct{ name, statement string }{
					{"automatic", ""},
					{"digest", "CREATE INDEX cache_entries_digest ON cache_entries(digest)"},
					{"covering", "CREATE INDEX cache_entries_digest ON cache_entries(digest,integration)"},
				} {
					b.Run(variant.name, func(b *testing.B) {
						ctx := context.Background()
						connection, err := store.db.Conn(ctx)
						if err != nil {
							b.Fatal(err)
						}
						defer connection.Close()
						for _, statement := range []string{"DROP INDEX IF EXISTS cache_entries_digest", "PRAGMA automatic_index=ON"} {
							if _, err := connection.ExecContext(ctx, statement); err != nil {
								b.Fatal(err)
							}
						}
						pages := func() int64 {
							var count, free, size int64
							for statement, value := range map[string]*int64{"PRAGMA page_count": &count, "PRAGMA freelist_count": &free, "PRAGMA page_size": &size} {
								if err := connection.QueryRowContext(ctx, statement).Scan(value); err != nil {
									b.Fatal(err)
								}
							}
							return (count - free) * size
						}
						before := pages()
						started := time.Now()
						if variant.statement != "" {
							if _, err := connection.ExecContext(ctx, variant.statement); err != nil {
								b.Fatal(err)
							}
						}
						build := time.Since(started)
						indexBytes := pages() - before
						query := &integrationStatsQuery{connection: connection}
						wantBlobs := references
						if shared {
							wantBlobs /= 4
						}
						check := func(stats Stats, err error) {
							if err != nil || stats.Artifacts != int64(wantBlobs) || stats.Entries != int64(references) || stats.UsageBytes != int64(wantBlobs*128) {
								b.Fatalf("wrong measured partition: %+v, %v", stats, err)
							}
						}
						started = time.Now()
						check(readArtifactStats(ctx, query))
						first := time.Since(started)
						b.Logf("actual query plan:\n%s", integrationStatsPlan(b, connection))
						b.ReportAllocs()
						samples := make([]time.Duration, 0, b.N)
						b.ResetTimer()
						for range b.N {
							started := time.Now()
							check(readArtifactStats(ctx, query))
							samples = append(samples, time.Since(started))
						}
						b.StopTimer()
						sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
						b.ReportMetric(float64(build.Nanoseconds())/1e6, "build-ms")
						b.ReportMetric(float64(first.Nanoseconds())/1e6, "first-ms")
						b.ReportMetric(float64(indexBytes), "index-B")
						b.ReportMetric(float64(samples[len(samples)/2].Nanoseconds())/1e6, "median-ms")
					})
				}
			})
		}
	}
}
