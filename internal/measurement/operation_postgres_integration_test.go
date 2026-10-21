package measurement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

// The supplied PostgreSQL fixture is opt-in. Every case owns a fresh schema;
// these tests never discover credentials, start services or touch existing rows.
func operationPostgresTestURL(t *testing.T) string {
	t.Helper()
	connection := os.Getenv("LAYERCACHE_MEASUREMENT_POSTGRES_URL")
	if connection == "" {
		t.Skip("set LAYERCACHE_MEASUREMENT_POSTGRES_URL to run isolated operation PostgreSQL controls")
	}
	parsed, err := url.Parse(connection)
	if err != nil {
		t.Fatal("invalid PostgreSQL test fixture URL")
	}
	admin, err := sql.Open("pgx", connection)
	if err != nil {
		t.Fatal("open PostgreSQL operation test fixture")
	}
	schema := fmt.Sprintf("layercache_operation_qa_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = admin.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)); err != nil {
		_ = admin.Close()
		t.Fatal("create isolated PostgreSQL operation test schema")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.ExecContext(ctx, fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema))
		_ = admin.Close()
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestOperationPostgresContract(t *testing.T) {
	if os.Getenv("LAYERCACHE_MEASUREMENT_POSTGRES_URL") == "" {
		t.Skip("set LAYERCACHE_MEASUREMENT_POSTGRES_URL to run isolated operation PostgreSQL controls")
	}
	runOperationSQLContract(t, func(t *testing.T) *operationSQLRepository {
		owner, err := OpenPostgresRepository(context.Background(), operationPostgresTestURL(t), operationTestAuthority().Project)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = owner.Close() })
		if _, err := owner.OperationRepository(context.Background(), "foreign/project"); !errors.Is(err, ErrOperationInvalid) {
			t.Fatalf("PostgreSQL project boundary not enforced: %v", err)
		}
		r, err := owner.OperationRepository(context.Background(), operationTestAuthority().Project)
		if err != nil {
			t.Fatal(err)
		}
		return r.(*operationSQLRepository)
	})
}

func TestOperationPostgresConcurrentInitializationAdmissionAndRestart(t *testing.T) {
	connection := operationPostgresTestURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const replicas = 8
	a := operationTestAuthority()
	start := make(chan struct{})
	owners := make([]*PostgresRepository, replicas)
	repositories := make([]OperationRepository, replicas)
	results := make(chan error, replicas)
	var wg sync.WaitGroup
	for i := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var err error
			owners[i], err = OpenPostgresRepository(ctx, connection, a.Project)
			if err == nil {
				repositories[i], err = owners[i].OperationRepository(ctx, a.Project)
			}
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for _, owner := range owners {
		if owner != nil {
			t.Cleanup(func() { _ = owner.Close() })
		}
	}
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent PostgreSQL operation initialization: %v", err)
		}
	}
	base := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	first := operationTestReport("postgres-restart-0001", base)
	firstReceipt, err := repositories[0].RecordOperation(ctx, a, first, base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = owners[0].Close(); err != nil {
		t.Fatal(err)
	}
	owners[0], err = OpenPostgresRepository(ctx, connection, a.Project)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owners[0].Close() })
	repositories[0], err = owners[0].OperationRepository(ctx, a.Project)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := repositories[0].RecordOperation(ctx, a, first, base.Add(2*time.Minute)); err != nil || !retry.Repeated || retry.ReceiptID != firstReceipt.ReceiptID {
		t.Fatalf("PostgreSQL restart changed immutable receipt: %+v %v", retry, err)
	}
	for i := 1; i < 31; i++ {
		if _, err := repositories[0].RecordOperation(ctx, a, operationTestReport(fmt.Sprintf("postgres-seed-%04d", i), base), base.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	start = make(chan struct{})
	results = make(chan error, replicas)
	for i := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repositories[i].RecordOperation(ctx, a, operationTestReport(fmt.Sprintf("postgres-race-%04d", i), base), base.Add(time.Minute))
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted, refused := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrOperationQuota) {
			refused++
		} else {
			t.Fatalf("PostgreSQL operation admission: %v", err)
		}
	}
	if accepted != 1 || refused != replicas-1 {
		t.Fatalf("PostgreSQL quota race admitted %d refused %d", accepted, refused)
	}
	period, err := repositories[0].OperationPeriod(ctx, base, base.Add(time.Minute))
	if err != nil || period.StoredObservations != 32 {
		t.Fatalf("PostgreSQL final receipt set: %+v %v", period, err)
	}
}
