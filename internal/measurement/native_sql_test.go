package measurement_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/execution"
	"github.com/layercache/layercache/internal/measurement"
)

// Keep the fake report clock in retained history even as the real receive
// clock advances. Individual fixtures use fixed offsets from this UTC hour.
var nativeSQLBase = time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)

const nativeSQLKey = "a8e040e29aac448693d23ccdf5a81af9"

type nativeSQLTestRepository struct {
	measurement.NativeRepository
	database *sql.DB
	dialect  string
}

func TestNativeSQLSQLite(t *testing.T) {
	t.Parallel()
	runNativeSQLContract(t, func(t *testing.T) measurement.NativeRepository {
		path := filepath.Join(t.TempDir(), "measurement.sqlite")
		repository, err := measurement.OpenSQLiteRepository(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repository.Close() })
		native, err := repository.NativeRepository(context.Background(), "project")
		if err != nil {
			t.Fatal(err)
		}
		database, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		database.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = database.Close() })
		return &nativeSQLTestRepository{NativeRepository: native, database: database, dialect: "sqlite"}
	})
}

func TestNativeSQLPostgres(t *testing.T) {
	postgresURL := os.Getenv("LAYERCACHE_MEASUREMENT_POSTGRES_URL")
	if postgresURL == "" {
		t.Skip("set LAYERCACHE_MEASUREMENT_POSTGRES_URL to run the isolated PostgreSQL native-report contract")
	}
	parsed, err := url.Parse(postgresURL)
	if err != nil {
		t.Fatal(err)
	}
	runNativeSQLContract(t, func(t *testing.T) measurement.NativeRepository {
		// Every case owns a new schema. Never delete rows from an existing schema.
		schema := fmt.Sprintf("layercache_native_qa_%d", time.Now().UnixNano())
		admin, err := sql.Open("pgx", postgresURL)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)); err != nil {
			_ = admin.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := admin.ExecContext(ctx, fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema)); err != nil {
				t.Errorf("drop owned native-report test schema: %v", err)
			}
			_ = admin.Close()
		})
		isolated := *parsed
		query := isolated.Query()
		query.Set("search_path", schema)
		isolated.RawQuery = query.Encode()
		repository, err := measurement.OpenPostgresRepository(context.Background(), isolated.String(), "project")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repository.Close() })
		native, err := repository.NativeRepository(context.Background(), "project")
		if err != nil {
			t.Fatal(err)
		}
		database, err := sql.Open("pgx", isolated.String())
		if err != nil {
			t.Fatal(err)
		}
		database.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = database.Close() })
		return &nativeSQLTestRepository{NativeRepository: native, database: database, dialect: "postgres"}
	})
}

func runNativeSQLContract(t *testing.T, open func(*testing.T) measurement.NativeRepository) {
	t.Run("atomic replay", func(t *testing.T) {
		repository := open(t)
		authority := nativeSQLAuthority("producer", "job-one")
		report := nativeSQLReport("run-atomic-replay-0001", nativeSQLBase,
			nativeSQLTask("compile", nativeSQLKey, "executed", nativeSQLBase, 8000))
		const callers = 12
		start := make(chan struct{})
		results := make(chan measurement.NativeReceipt, callers)
		failures := make(chan error, callers)
		var wait sync.WaitGroup
		for range callers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				receipt, err := repository.RecordReport(context.Background(), authority, report, nativeSQLBase.Add(time.Hour))
				if err != nil {
					failures <- err
					return
				}
				results <- receipt
			}()
		}
		close(start)
		wait.Wait()
		close(results)
		close(failures)
		for err := range failures {
			t.Errorf("concurrent identical report: %v", err)
		}
		first, repeated := 0, 0
		for receipt := range results {
			if receipt.Version != measurement.NativeVersion || receipt.InvocationID != report.InvocationID || receipt.Tasks != 1 {
				t.Errorf("unexpected immutable receipt: %+v", receipt)
			}
			if receipt.Repeated {
				repeated++
			} else if receipt.Recorded {
				first++
			}
		}
		if first != 1 || repeated != callers-1 {
			t.Fatalf("first=%d repeated=%d, want one accepted invocation and %d replays", first, repeated, callers-1)
		}
		projection := nativeSQLProjection(t, repository)
		if projection.ReportedInvocations != 1 || projection.ReportedTasks != 1 || projection.Outcomes.Executed != 1 || projection.HistoryTruncated {
			t.Fatalf("replays changed native totals: %+v", projection)
		}
	})

	t.Run("full authority conflict and matrix isolation", func(t *testing.T) {
		repository := open(t)
		authority := nativeSQLAuthority("workflow-one", "matrix-linux")
		report := nativeSQLReport("run-authority-bound-0001", nativeSQLBase,
			nativeSQLTask("compile", nativeSQLKey, "executed", nativeSQLBase, 8000))
		nativeSQLRecord(t, repository, authority, report)
		for _, change := range []struct {
			name   string
			modify func(*measurement.NativeAuthority)
		}{
			{"source", func(a *measurement.NativeAuthority) { a.SourceCommit = strings.Repeat("b", 40) }},
			{"subject", func(a *measurement.NativeAuthority) { a.Subject = "repo:someone/else:ref:refs/heads/main" }},
			{"profile", func(a *measurement.NativeAuthority) { a.CompatibilityID = "gradle-profile-other" }},
		} {
			t.Run(change.name, func(t *testing.T) {
				changed := authority
				change.modify(&changed)
				_, err := repository.RecordReport(context.Background(), changed, report, nativeSQLBase.Add(time.Hour))
				if !errors.Is(err, measurement.ErrNativeConflict) {
					t.Fatalf("changed %s authority accepted or returned wrong error: %v", change.name, err)
				}
			})
		}
		changedReport := report
		changedReport.Status = "failed"
		if _, err := repository.RecordReport(context.Background(), authority, changedReport, nativeSQLBase.Add(time.Hour)); !errors.Is(err, measurement.ErrNativeConflict) {
			t.Fatalf("changed final report = %v, want invocation conflict", err)
		}
		otherJob := nativeSQLAuthority("workflow-one", "matrix-macos")
		otherJob.Execution.ExecutionID = "github-actions:1:10:1:21"
		nativeSQLRecord(t, repository, otherJob, report)
		projection := nativeSQLProjection(t, repository)
		if projection.ReportedInvocations != 2 || projection.ReportedTasks != 2 || projection.HistoryTruncated {
			t.Fatalf("matrix jobs collided or rejected mutations changed totals: %+v", projection)
		}
	})

	t.Run("invalid batch never partially commits", func(t *testing.T) {
		repository := open(t)
		task := nativeSQLTask("compile", nativeSQLKey, "executed", nativeSQLBase, 8000)
		report := nativeSQLReport("run-invalid-batch-0001", nativeSQLBase, task, task)
		authority := nativeSQLAuthority("workflow-one", "job-one")
		if _, err := repository.RecordReport(context.Background(), authority, report, nativeSQLBase.Add(time.Hour)); !errors.Is(err, measurement.ErrNativeInvalid) {
			t.Fatalf("duplicate native work in one batch = %v, want invalid report", err)
		}
		reports, err := repository.PeriodReport(context.Background(), nativeSQLBase.Add(-time.Hour), nativeSQLBase.Add(2*time.Hour))
		if err != nil || len(reports) != 0 {
			t.Fatalf("invalid batch persisted a partial projection: reports=%+v error=%v", reports, err)
		}
		report.Tasks = report.Tasks[:1]
		nativeSQLRecord(t, repository, authority, report)
		if projection := nativeSQLProjection(t, repository); projection.ReportedInvocations != 1 || projection.ReportedTasks != 1 {
			t.Fatalf("invalid report claimed the invocation: %+v", projection)
		}
	})

	t.Run("unknown eligibility is not a zero denominator", func(t *testing.T) {
		repository := open(t)
		report := nativeSQLReport("run-eligibility-0001", nativeSQLBase,
			nativeSQLTask("known-miss", nativeSQLKey, "executed", nativeSQLBase, 8000),
			nativeSQLTask("known-hit", strings.Repeat("b", 32), "fromCache", nativeSQLBase, -1),
			nativeSQLTask("unknown-miss", "", "executed", nativeSQLBase, 8000),
			nativeSQLTask("unknown-hit", "", "fromCache", nativeSQLBase, -1))
		nativeSQLRecord(t, repository, nativeSQLAuthority("workflow-one", "job-one"), report)
		projection := nativeSQLProjection(t, repository)
		if projection.KnownEligible != 3 || projection.EligibilityUnknown != 1 || projection.Hits != 2 || projection.Misses != 1 || projection.Eligible != nil || projection.HitRate != nil {
			t.Fatalf("unknown eligibility became complete native hit-rate coverage: %+v", projection)
		}
		if projection.ProducerTiming.Total != 2 || projection.ProducerResources.Total != 2 || nativeSQLSourceCount(projection, measurement.SourceUnattributed) != 2 {
			t.Fatalf("keyless native FROM-CACHE result disappeared from hit attribution coverage: %+v", projection)
		}
		if projection.Outcomes.Executed != 2 || projection.Outcomes.FromCache != 2 {
			t.Fatalf("native outcomes lost keyless observations: %+v", projection.Outcomes)
		}
		assertNativeSQLUnknownSavings(t, projection)
	})

	t.Run("explicit ineligible execution is excluded from the hit rate", func(t *testing.T) {
		repository := open(t)
		ineligible := nativeSQLTask("uncacheable", "", "executed", nativeSQLBase, 8000)
		ineligible.CacheEligibility = "ineligible"
		report := nativeSQLReport("run-explicit-eligibility-0001", nativeSQLBase,
			ineligible,
			nativeSQLTask("known-miss", nativeSQLKey, "executed", nativeSQLBase, 8000),
			nativeSQLTask("keyless-hit", "", "fromCache", nativeSQLBase, -1))
		authority := nativeSQLAuthority("workflow-one", "job-one")
		nativeSQLRecord(t, repository, authority, report)
		projection := nativeSQLProjection(t, repository)
		if projection.EligibilityUnknown != 0 || projection.KnownEligible != 2 || projection.Eligible == nil || *projection.Eligible != 2 || projection.Hits != 1 || projection.Misses != 1 || projection.HitRate == nil || *projection.HitRate != 0.5 {
			t.Fatalf("explicit ineligible execution changed the native eligible denominator: %+v", projection)
		}
		if projection.Outcomes.Executed != 2 || projection.Outcomes.FromCache != 1 || projection.ReportedTasks != 3 {
			t.Fatalf("ineligible task disappeared from observed work: %+v", projection)
		}
		invalid := nativeSQLReport("run-invalid-eligibility-0001", nativeSQLBase,
			nativeSQLTask("contradiction", nativeSQLKey, "executed", nativeSQLBase, 8000))
		invalid.Tasks[0].CacheEligibility = "ineligible"
		if _, err := repository.RecordReport(context.Background(), authority, invalid, nativeSQLBase.Add(time.Hour)); !errors.Is(err, measurement.ErrNativeInvalid) {
			t.Fatalf("keyed cacheable task contradicted by ineligible claim was accepted: %v", err)
		}
		assertNativeSQLUnknownSavings(t, projection)
	})

	t.Run("keyless FROM-CACHE outcomes have known eligibility", func(t *testing.T) {
		repository := open(t)
		nativeSQLRecord(t, repository, nativeSQLAuthority("workflow-one", "job-one"), nativeSQLReport("run-keyless-cache-hits-0001", nativeSQLBase,
			nativeSQLTask("first-hit", "", "fromCache", nativeSQLBase, -1),
			nativeSQLTask("second-hit", "", "fromCache", nativeSQLBase, -1)))
		projection := nativeSQLProjection(t, repository)
		if projection.KnownEligible != 2 || projection.EligibilityUnknown != 0 || projection.Eligible == nil || *projection.Eligible != 2 || projection.Hits != 2 || projection.Misses != 0 || projection.HitRate == nil || *projection.HitRate != 1 {
			t.Fatalf("explicit native cache hits required a key to establish eligibility: %+v", projection)
		}
		if projection.ProducerTiming.Known != 0 || projection.ProducerTiming.Total != 2 || projection.GrossAvoidedTaskTime.Milliseconds != nil || projection.NetAvoidedTaskTime.Milliseconds != nil || nativeSQLSourceCount(projection, measurement.SourceUnattributed) != 2 {
			t.Fatalf("keyless native cache hits invented source or producer timing: %+v", projection)
		}
		assertNativeSQLUnknownSavings(t, projection)
	})

	t.Run("first publication supplies timing only for exact delivered digest", func(t *testing.T) {
		repository := open(t)
		first := nativeSQLAuthority("first-producer", "job-one")
		duplicate := nativeSQLAuthority("duplicate-producer", "job-two")
		consumer := nativeSQLAuthority("consumer", "job-three")
		digest := strings.Repeat("c", 64)
		nativeSQLTransport(t, repository, first, "published", nativeSQLKey, digest, nativeSQLBase.Add(9*time.Second))
		nativeSQLRecord(t, repository, first, nativeSQLReport("run-first-producer-0001", nativeSQLBase,
			nativeSQLTask("compile", nativeSQLKey, "executed", nativeSQLBase, 8000)))
		later := nativeSQLBase.Add(5 * time.Minute)
		nativeSQLTransport(t, repository, duplicate, "published", nativeSQLKey, digest, later.Add(9*time.Second))
		nativeSQLRecord(t, repository, duplicate, nativeSQLReport("run-duplicate-producer-0001", later,
			nativeSQLTask("compile", nativeSQLKey, "executed", later, 2000)))
		read := nativeSQLBase.Add(10 * time.Minute)
		nativeSQLTransport(t, repository, consumer, "delivered", nativeSQLKey, digest, read.Add(time.Second))
		nativeSQLRecord(t, repository, consumer, nativeSQLReport("run-first-consumer-0001", read,
			nativeSQLTask("compile", nativeSQLKey, "fromCache", read, -1)))
		projection := nativeSQLProjection(t, repository)
		if projection.ProducerTiming.Known != 1 || projection.ProducerTiming.Total != 1 || projection.GrossAvoidedTaskTime.Milliseconds == nil || *projection.GrossAvoidedTaskTime.Milliseconds != 8000 {
			t.Fatalf("duplicate publication replaced original producer timing, or exact delivered bytes were not reconciled: %+v", projection)
		}
		if nativeSQLSourceCount(projection, measurement.SourceTeamCache) != 1 {
			t.Fatalf("verified delivery did not attribute Team source: %+v", projection.Sources)
		}
		if projection.NetAvoidedTaskTime.Milliseconds == nil || *projection.NetAvoidedTaskTime.Milliseconds != -2000 || projection.NetAvoidedTaskTime.Known != 1 || projection.NetAvoidedTaskTime.Total != 1 || projection.NetAvoidedTaskTime.Confidence != measurement.ConfidenceMedium {
			t.Fatalf("observed consumer task window did not retain negative net avoided task time: %+v", projection.NetAvoidedTaskTime)
		}
		assertNativeSQLUnknownSavings(t, projection)
		onlyConsumer, err := repository.PeriodReport(context.Background(), read.Add(-time.Second), read.Add(40*time.Second))
		if err != nil || len(onlyConsumer) != 1 || onlyConsumer[0].ReportedInvocations != 1 || onlyConsumer[0].GrossAvoidedTaskTime.Milliseconds == nil || *onlyConsumer[0].GrossAvoidedTaskTime.Milliseconds != 8000 {
			t.Fatalf("in-period hit lost retained original timing outside the reporting period: reports=%+v error=%v", onlyConsumer, err)
		}
		otherRead := read.Add(time.Minute)
		nativeSQLTransport(t, repository, consumer, "delivered", nativeSQLKey, strings.Repeat("d", 64), otherRead.Add(time.Second))
		nativeSQLRecord(t, repository, consumer, nativeSQLReport("run-other-digest-consumer-0001", otherRead,
			nativeSQLTask("compile", nativeSQLKey, "fromCache", otherRead, -1)))
		projection = nativeSQLProjection(t, repository)
		if projection.ProducerTiming.Known != 1 || projection.ProducerTiming.Total != 2 || projection.GrossAvoidedTaskTime.Known != 1 || projection.GrossAvoidedTaskTime.Total != 2 {
			t.Fatalf("different delivered bytes inherited producer timing: %+v", projection)
		}
		if projection.GrossAvoidedTaskTime.Milliseconds == nil || *projection.GrossAvoidedTaskTime.Milliseconds != 8000 || projection.GrossAvoidedTaskTime.Confidence != measurement.ConfidenceLow {
			t.Fatalf("partial producer coverage must preserve a bounded known subtotal with low confidence: %+v", projection.GrossAvoidedTaskTime)
		}
		if projection.NetAvoidedTaskTime.Milliseconds == nil || *projection.NetAvoidedTaskTime.Milliseconds != -2000 || projection.NetAvoidedTaskTime.Known != 1 || projection.NetAvoidedTaskTime.Total != 2 || projection.NetAvoidedTaskTime.Confidence != measurement.ConfidenceLow {
			t.Fatalf("partial net avoided task time invented coverage or discarded the known negative subtotal: %+v", projection.NetAvoidedTaskTime)
		}
	})

	t.Run("net avoided task time preserves positive zero and negative results", func(t *testing.T) {
		for _, consumerMS := range []int64{2000, 8000, 10000} {
			t.Run(fmt.Sprintf("consumer %dms", consumerMS), func(t *testing.T) {
				repository := open(t)
				producer := nativeSQLAuthority("producer", "job-one")
				consumer := nativeSQLAuthority("consumer", "job-two")
				digest := strings.Repeat("c", 64)
				nativeSQLTransport(t, repository, producer, "published", nativeSQLKey, digest, nativeSQLBase.Add(9*time.Second))
				nativeSQLRecord(t, repository, producer, nativeSQLReport("run-net-task-producer-0001", nativeSQLBase,
					nativeSQLTask("compile", nativeSQLKey, "executed", nativeSQLBase, 8000)))
				read := nativeSQLBase.Add(10 * time.Minute)
				nativeSQLTransport(t, repository, consumer, "delivered", nativeSQLKey, digest, read.Add(time.Second))
				task := nativeSQLTask("compile", nativeSQLKey, "fromCache", read, -1)
				task.FinishedAt = task.StartedAt.Add(time.Duration(consumerMS) * time.Millisecond)
				nativeSQLRecord(t, repository, consumer, nativeSQLReport("run-net-task-consumer-0001", read, task))
				projection := nativeSQLProjection(t, repository)
				if projection.NetAvoidedTaskTime.Milliseconds == nil || *projection.NetAvoidedTaskTime.Milliseconds != 8000-consumerMS || projection.NetAvoidedTaskTime.Known != 1 || projection.NetAvoidedTaskTime.Total != 1 || projection.GrossAvoidedTaskTime.Milliseconds == nil || *projection.GrossAvoidedTaskTime.Milliseconds != 8000 {
					t.Fatalf("net avoided task time lost sign or changed gross producer time: %+v", projection)
				}
				assertNativeSQLUnknownSavings(t, projection)
			})
		}
	})

	t.Run("transport cannot cross report authority", func(t *testing.T) {
		for _, change := range []struct {
			name   string
			modify func(*measurement.NativeAuthority)
		}{
			{"source", func(a *measurement.NativeAuthority) { a.SourceCommit = strings.Repeat("b", 40) }},
			{"subject", func(a *measurement.NativeAuthority) { a.Subject = "repo:other/project:ref:refs/heads/main" }},
			{"profile", func(a *measurement.NativeAuthority) { a.CompatibilityID = "gradle-profile-other" }},
			{"run", func(a *measurement.NativeAuthority) { a.RunID = "other-workflow" }},
			{"workspace", func(a *measurement.NativeAuthority) { a.WorkspaceID = "other-job" }},
		} {
			t.Run(change.name, func(t *testing.T) {
				repository := open(t)
				authority := nativeSQLAuthority("workflow-one", "job-one")
				foreign := authority
				change.modify(&foreign)
				nativeSQLTransport(t, repository, foreign, "delivered", nativeSQLKey, strings.Repeat("c", 64), nativeSQLBase.Add(time.Second))
				nativeSQLRecord(t, repository, authority, nativeSQLReport("run-authority-consumer-0001", nativeSQLBase,
					nativeSQLTask("compile", nativeSQLKey, "fromCache", nativeSQLBase, -1)))
				projection := nativeSQLProjection(t, repository)
				if nativeSQLSourceCount(projection, measurement.SourceTeamCache) != 0 || nativeSQLSourceCount(projection, measurement.SourceUnattributed) != 1 || projection.ProducerTiming.Known != 0 {
					t.Fatalf("delivery under different %s authority attributed a report: %+v", change.name, projection)
				}
			})
		}
	})

	t.Run("resource coverage belongs to the original publisher", func(t *testing.T) {
		for _, test := range []struct {
			name          string
			modify        func(*execution.ResourceSnapshot) *execution.ResourceSnapshot
			withoutSigner bool
			wantKnown     int
		}{
			{name: "original two cores", wantKnown: 1},
			{name: "missing original", modify: func(*execution.ResourceSnapshot) *execution.ResourceSnapshot { return nil }},
			{name: "partial original CPU", wantKnown: 1, modify: func(resource *execution.ResourceSnapshot) *execution.ResourceSnapshot {
				resource.Sample.CPU.EffectiveMilliCPU = nil
				resource.Sample.CPU.LimitKnowledge = "partial"
				return resource
			}},
			{name: "future original", modify: func(resource *execution.ResourceSnapshot) *execution.ResourceSnapshot {
				resource.ObservedAt = nativeSQLBase.Add(10 * time.Second)
				return resource
			}},
			{name: "stale original", modify: func(resource *execution.ResourceSnapshot) *execution.ResourceSnapshot {
				resource.ObservedAt = nativeSQLBase.Add(-24 * time.Hour)
				return resource
			}},
			{name: "invalid original", modify: func(resource *execution.ResourceSnapshot) *execution.ResourceSnapshot {
				resource.Sample.Arch = "not-an-architecture"
				return resource
			}},
			{name: "no signed execution", withoutSigner: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				repository := open(t)
				first := nativeSQLAuthority("first-producer", "job-one")
				first.Execution.ExecutionID = "github-actions:42:100:1:200"
				if test.withoutSigner {
					first.Execution = nil
				}
				duplicate := nativeSQLAuthority("duplicate-producer", "job-two")
				duplicate.Execution.ExecutionID = "github-actions:42:101:1:201"
				consumer := nativeSQLAuthority("consumer", "job-three")
				consumer.Execution.ExecutionID = "github-actions:42:102:1:202"
				originalResource := nativeSQLResource(nativeSQLBase.Add(-time.Second), 2)
				if test.modify != nil {
					originalResource = test.modify(originalResource)
				}
				digest := strings.Repeat("c", 64)
				nativeSQLTransport(t, repository, first, "published", nativeSQLKey, digest, nativeSQLBase.Add(9*time.Second), originalResource)
				nativeSQLRecord(t, repository, first, nativeSQLReport("run-resource-producer-0001", nativeSQLBase,
					nativeSQLTask("compile", nativeSQLKey, "executed", nativeSQLBase, 8000)))
				later := nativeSQLBase.Add(5 * time.Minute)
				nativeSQLTransport(t, repository, duplicate, "published", nativeSQLKey, digest, later.Add(9*time.Second), nativeSQLResource(later.Add(-time.Second), 8))
				nativeSQLRecord(t, repository, duplicate, nativeSQLReport("run-resource-duplicate-0001", later,
					nativeSQLTask("compile", nativeSQLKey, "executed", later, 2000)))
				read := nativeSQLBase.Add(10 * time.Minute)
				nativeSQLTransport(t, repository, consumer, "delivered", nativeSQLKey, digest, read.Add(time.Second), nativeSQLResource(read.Add(-time.Second), 64))
				nativeSQLRecord(t, repository, consumer, nativeSQLReport("run-resource-consumer-0001", read,
					nativeSQLTask("compile", nativeSQLKey, "fromCache", read, -1)))
				projection := nativeSQLProjection(t, repository)
				if projection.ProducerResources.Known != test.wantKnown || projection.ProducerResources.Total != 1 {
					t.Fatalf("original publisher resource coverage was lost or filled by a later execution: got=%+v want known=%d total=1", projection.ProducerResources, test.wantKnown)
				}
				if projection.GrossAvoidedTaskTime.Milliseconds == nil || *projection.GrossAvoidedTaskTime.Milliseconds != 8000 {
					t.Fatalf("resource observations changed original producer task timing: %+v", projection.GrossAvoidedTaskTime)
				}
				if projection.NetEstimatedBuildTimeSaved.Milliseconds != nil || projection.AllocatedCPUSecondsSaved != nil || projection.MeasuredCPUSecondsSaved != nil || projection.CostSaved != nil {
					t.Fatalf("point-in-time client resource observations invented net or allocated compute savings: %+v", projection)
				}
			})
		}
	})

	t.Run("ambiguous task windows do not establish a source", func(t *testing.T) {
		for _, mode := range []string{"one delivery for two tasks", "two deliveries for one task", "outside task window"} {
			t.Run(mode, func(t *testing.T) {
				repository := open(t)
				authority := nativeSQLAuthority("workflow-one", "job-one")
				tasks := []measurement.NativeTask{nativeSQLTask("compile-one", nativeSQLKey, "fromCache", nativeSQLBase, -1)}
				deliveryAt := nativeSQLBase.Add(time.Second)
				if mode == "one delivery for two tasks" {
					tasks = append(tasks, nativeSQLTask("compile-two", nativeSQLKey, "fromCache", nativeSQLBase, -1))
				}
				if mode == "outside task window" {
					deliveryAt = nativeSQLBase.Add(time.Minute)
				}
				nativeSQLTransport(t, repository, authority, "delivered", nativeSQLKey, strings.Repeat("c", 64), deliveryAt)
				if mode == "two deliveries for one task" {
					nativeSQLTransport(t, repository, authority, "delivered", nativeSQLKey, strings.Repeat("c", 64), deliveryAt.Add(time.Second))
				}
				nativeSQLRecord(t, repository, authority, nativeSQLReport("run-ambiguous-consumer-0001", nativeSQLBase, tasks...))
				projection := nativeSQLProjection(t, repository)
				if nativeSQLSourceCount(projection, measurement.SourceTeamCache) != 0 || nativeSQLSourceCount(projection, measurement.SourceUnattributed) != len(tasks) || projection.ProducerTiming.Known != 0 || projection.ProducerTiming.Total != len(tasks) {
					t.Fatalf("ambiguous or unrelated gateway traffic became native attribution: %+v", projection)
				}
				if projection.GrossAvoidedTaskTime.Milliseconds != nil {
					t.Fatalf("unattributed native hit acquired producer timing: %+v", projection.GrossAvoidedTaskTime)
				}
			})
		}
	})

	t.Run("partial phase and counter coverage stays unknown", func(t *testing.T) {
		repository := open(t)
		authority := nativeSQLAuthority("workflow-one", "job-one")
		first := nativeSQLReport("run-partial-coverage-0001", nativeSQLBase)
		first.Counters = &measurement.NativeCounters{RPC: &measurement.NativeRPCCounters{ActionReads: nativeSQLNumber(4), ActionHits: nativeSQLNumber(0)}, Compiler: &measurement.NativeCompilerCounters{Scope: "clang-pch", Coverage: "partial", Reason: "missing", Hits: nativeSQLNumber(0)}}
		first.TimingMS = &measurement.NativePhaseTiming{Operation: nativeSQLNumber(30000), Compile: nativeSQLNumber(1000)}
		nativeSQLRecord(t, repository, authority, first)
		second := nativeSQLReport("run-partial-coverage-0002", nativeSQLBase.Add(time.Minute))
		second.TimingMS = &measurement.NativePhaseTiming{Operation: nativeSQLNumber(30000), Restore: nativeSQLNumber(0)}
		nativeSQLRecord(t, repository, authority, second)
		projection := nativeSQLProjection(t, repository)
		if projection.ReportedInvocations != 2 || projection.ReportedTasks != 0 || projection.Eligible != nil || projection.HitRate != nil {
			t.Fatalf("counter-only reports invented task coverage: %+v", projection)
		}
		if projection.TimingMS == nil || projection.TimingMS.Operation == nil || *projection.TimingMS.Operation != 60000 || projection.TimingMS.Compile != nil || projection.TimingMS.Restore != nil || projection.TimingMS.Load != nil || projection.TimingMS.Close != nil || projection.TimingMS.Snapshot != nil {
			t.Fatalf("missing per-invocation phase timing became a partial total or zero: %+v", projection.TimingMS)
		}
		if projection.Counters != nil && projection.Counters.RPC != nil && (projection.Counters.RPC.ActionReads != nil || projection.Counters.RPC.ActionHits != nil || projection.Counters.RPC.CacheErrors != nil) {
			t.Fatalf("missing protocol counters in the second report became complete totals: %+v", projection.Counters.RPC)
		}
		if projection.Counters != nil && projection.Counters.Compiler != nil && (projection.Counters.Compiler.Hits != nil || projection.Counters.Compiler.Misses != nil || projection.Counters.Compiler.Skipped != nil || projection.Counters.Compiler.Unknown != nil) {
			t.Fatalf("missing compiler counters in the second report became complete totals: %+v", projection.Counters.Compiler)
		}
		assertNativeSQLUnknownSavings(t, projection)
	})

	t.Run("known zero counters survive complete aggregation", func(t *testing.T) {
		repository := open(t)
		authority := nativeSQLAuthority("workflow-one", "job-one")
		for invocation := range 2 {
			report := nativeSQLReport(fmt.Sprintf("run-complete-counters-%04d", invocation), nativeSQLBase.Add(time.Duration(invocation)*time.Minute))
			report.Counters = &measurement.NativeCounters{
				RPC:      &measurement.NativeRPCCounters{ActionReads: nativeSQLNumber(3), ActionHits: nativeSQLNumber(0), ActionWrites: nativeSQLNumber(1), CASReads: nativeSQLNumber(2), CASHits: nativeSQLNumber(0), CASWrites: nativeSQLNumber(1), CacheErrors: nativeSQLNumber(0)},
				Compiler: &measurement.NativeCompilerCounters{Scope: "clang-pch", Coverage: "complete", Hits: nativeSQLNumber(0), Misses: nativeSQLNumber(3), Skipped: nativeSQLNumber(0), Unknown: nativeSQLNumber(0)},
			}
			nativeSQLRecord(t, repository, authority, report)
		}
		projection := nativeSQLProjection(t, repository)
		if projection.Counters == nil || projection.Counters.RPC == nil || projection.Counters.Compiler == nil {
			t.Fatalf("complete native counters disappeared: %+v", projection.Counters)
		}
		rpc, compiler := projection.Counters.RPC, projection.Counters.Compiler
		if rpc.ActionReads == nil || *rpc.ActionReads != 6 || rpc.ActionHits == nil || *rpc.ActionHits != 0 || rpc.CacheErrors == nil || *rpc.CacheErrors != 0 || compiler.Hits == nil || *compiler.Hits != 0 || compiler.Misses == nil || *compiler.Misses != 6 || compiler.Unknown == nil || *compiler.Unknown != 0 {
			t.Fatalf("known zeros became unknown or counters failed to aggregate: rpc=%+v compiler=%+v", rpc, compiler)
		}
		if projection.Eligible != nil || projection.HitRate != nil || projection.ReportedTasks != 0 {
			t.Fatalf("compiler/protocol counters were conflated with native task eligibility: %+v", projection)
		}
	})

	t.Run("compiler limitation aggregation does not depend on report order", func(t *testing.T) {
		for _, test := range []struct {
			name         string
			first        measurement.NativeCompilerCounters
			second       measurement.NativeCompilerCounters
			wantReason   string
			permutations int
		}{
			{name: "complete empty and truncated", first: nativeSQLCompiler("complete", ""), second: nativeSQLCompiler("partial", "truncated"), wantReason: "truncated", permutations: 2},
			{name: "complete none and truncated", first: nativeSQLCompiler("complete", "none"), second: nativeSQLCompiler("partial", "truncated"), wantReason: "truncated", permutations: 2},
			{name: "different limitations", first: nativeSQLCompiler("partial", "missing"), second: nativeSQLCompiler("partial", "truncated"), wantReason: "ambiguous", permutations: 2},
			{name: "identical limitations", first: nativeSQLCompiler("partial", "truncated"), second: nativeSQLCompiler("partial", "truncated"), wantReason: "truncated", permutations: 1},
		} {
			t.Run(test.name, func(t *testing.T) {
				for order := range test.permutations {
					t.Run(fmt.Sprintf("order %d", order), func(t *testing.T) {
						repository := open(t)
						authority := nativeSQLAuthority("workflow-one", "job-one")
						values := []measurement.NativeCompilerCounters{test.first, test.second}
						if order == 1 {
							values[0], values[1] = values[1], values[0]
						}
						for invocation, compiler := range values {
							report := nativeSQLReport(fmt.Sprintf("run-compiler-limits-%04d", invocation), nativeSQLBase.Add(time.Duration(invocation)*time.Minute))
							report.Counters = &measurement.NativeCounters{Compiler: &compiler}
							nativeSQLRecord(t, repository, authority, report)
						}
						projection := nativeSQLProjection(t, repository)
						if projection.Counters == nil || projection.Counters.Compiler == nil {
							t.Fatalf("compiler limitation projection disappeared: %+v", projection)
						}
						compiler := projection.Counters.Compiler
						if compiler.Scope != "clang-pch" || compiler.Coverage != "partial" || compiler.Reason != test.wantReason {
							t.Fatalf("compiler limitations changed with invocation order: got=%+v want partial/%s", compiler, test.wantReason)
						}
					})
				}
			})
		}
	})

	t.Run("complete compiler coverage cannot carry a limitation", func(t *testing.T) {
		repository := open(t)
		report := nativeSQLReport("run-compiler-invalid-0001", nativeSQLBase)
		compiler := nativeSQLCompiler("complete", "truncated")
		report.Counters = &measurement.NativeCounters{Compiler: &compiler}
		if _, err := repository.RecordReport(context.Background(), nativeSQLAuthority("workflow-one", "job-one"), report, nativeSQLBase.Add(time.Hour)); !errors.Is(err, measurement.ErrNativeInvalid) {
			t.Fatalf("complete compiler counters with an explicit limitation were accepted: %v", err)
		}
	})

	t.Run("xcode task claims are unsupported", func(t *testing.T) {
		repository := open(t)
		authority := nativeSQLAuthority("workflow-one", "job-one")
		authority.Integration = measurement.IntegrationXcode
		report := nativeSQLReport("run-xcode-task-reject-0001", nativeSQLBase,
			nativeSQLTask("compile", nativeSQLKey, "fromCache", nativeSQLBase, -1))
		if _, err := repository.RecordReport(context.Background(), authority, report, nativeSQLBase.Add(time.Hour)); !errors.Is(err, measurement.ErrNativeInvalid) {
			t.Fatalf("Xcode v1 accepted task-level claims outside its qualified protocol/compiler report: %v", err)
		}
		report.Tasks = []measurement.NativeTask{}
		report.TimingMS = &measurement.NativePhaseTiming{Compile: nativeSQLNumber(1000)}
		nativeSQLRecord(t, repository, authority, report)
		reports, err := repository.PeriodReport(context.Background(), nativeSQLBase.Add(-time.Hour), nativeSQLBase.Add(2*time.Hour))
		if err != nil || len(reports) != 1 || reports[0].Integration != measurement.IntegrationXcode || reports[0].ReportedTasks != 0 || reports[0].Eligible != nil || reports[0].HitRate != nil {
			t.Fatalf("Xcode counters/phases were conflated with task eligibility: reports=%+v error=%v", reports, err)
		}
	})

	t.Run("invocation capacity is bounded per job and replays remain accepted", func(t *testing.T) {
		repository := open(t)
		authority := nativeSQLAuthority("workflow-one", "matrix-one")
		var first measurement.NativeReport
		for invocation := range measurement.NativeMaximumInvocations {
			report := nativeSQLReport(fmt.Sprintf("run-invocation-%04d", invocation), nativeSQLBase,
				nativeSQLTask("compile", nativeSQLKey, "executed", nativeSQLBase, 8000))
			if invocation == 0 {
				first = report
			}
			nativeSQLRecord(t, repository, authority, report)
		}
		if projection := nativeSQLProjection(t, repository); projection.HistoryTruncated {
			t.Fatalf("full but lossless job incorrectly marked history truncated: %+v", projection)
		}
		beforeLoss, err := repository.RecordReport(context.Background(), authority, first, nativeSQLBase.Add(time.Hour))
		if err != nil || !beforeLoss.Repeated {
			t.Fatalf("full lossless job rejected an exact replay: receipt=%+v error=%v", beforeLoss, err)
		}
		if projection := nativeSQLProjection(t, repository); projection.HistoryTruncated {
			t.Fatalf("exact replay invented a reporting loss: %+v", projection)
		}
		full := nativeSQLReport("run-invocation-overflow", nativeSQLBase,
			nativeSQLTask("compile", nativeSQLKey, "executed", nativeSQLBase, 8000))
		if _, err := repository.RecordReport(context.Background(), authority, full, nativeSQLBase.Add(time.Hour)); !errors.Is(err, measurement.ErrNativeQuota) {
			t.Fatalf("invocation beyond per-job capacity = %v, want quota error", err)
		}
		receipt, err := repository.RecordReport(context.Background(), authority, first, nativeSQLBase.Add(time.Hour))
		if err != nil || !receipt.Repeated {
			t.Fatalf("full job rejected exact replay: receipt=%+v error=%v", receipt, err)
		}
		matrix := nativeSQLAuthority("workflow-one", "matrix-two")
		matrix.Execution.ExecutionID = "github-actions:1:10:1:21"
		nativeSQLRecord(t, repository, matrix, full)
		projection := nativeSQLProjection(t, repository)
		if projection.ReportedInvocations != measurement.NativeMaximumInvocations+1 || !projection.HistoryTruncated {
			t.Fatalf("job capacity leaked across matrix jobs or rejected report persisted: %+v", projection)
		}
	})

	t.Run("coverage loss follows integration and half-open workload intervals", func(t *testing.T) {
		repository := open(t)
		firstAt := time.Now().UTC().Truncate(24 * time.Hour).Add(-72*time.Hour + 23*time.Hour + 59*time.Minute + 30*time.Second)
		authority := nativeSQLAuthority("coverage-limited-workflow", "job-one")
		// The refused report starts before midnight and is submitted after it.
		nativeSQLRefuseCappedInvocation(t, repository, authority, firstAt, firstAt.Add(2*time.Minute), true)
		probeNumber := 0
		probe := func(integration measurement.Integration, at time.Time) {
			t.Helper()
			probeNumber++
			a := nativeSQLAuthority("coverage-probes", "job-one")
			a.Integration = integration
			r := nativeSQLCounterReport(fmt.Sprintf("run-coverage-probe-%04d", probeNumber), at)
			nativeSQLRecordAt(t, repository, a, r, at.Add(time.Minute))
		}
		check := func(integration measurement.Integration, from, to time.Time, want bool) {
			t.Helper()
			reports, err := repository.PeriodReport(context.Background(), from, to)
			if err != nil {
				t.Fatal(err)
			}
			for _, report := range reports {
				if report.Integration == integration {
					if report.HistoryTruncated != want {
						t.Fatalf("%s loss in [%s,%s): truncated=%v want=%v", integration, from, to, report.HistoryTruncated, want)
					}
					return
				}
			}
			t.Fatalf("missing %s probe in [%s,%s): %+v", integration, from, to, reports)
		}
		before, after := firstAt.Add(-time.Nanosecond), firstAt.Add(time.Nanosecond)
		probe(measurement.IntegrationGradle, before)
		probe(measurement.IntegrationGradle, after)
		probe(measurement.IntegrationXcode, firstAt)
		check(measurement.IntegrationGradle, before, firstAt, false)
		check(measurement.IntegrationGradle, firstAt, after, true)
		check(measurement.IntegrationGradle, after, after.Add(time.Nanosecond), false)
		check(measurement.IntegrationXcode, firstAt, after, false)
		earlierSameDay := firstAt.Add(-12 * time.Hour)
		probe(measurement.IntegrationGradle, earlierSameDay)
		check(measurement.IntegrationGradle, earlierSameDay, earlierSameDay.Add(time.Nanosecond), false)
		laterAt := firstAt.Truncate(24 * time.Hour).Add(24 * time.Hour)
		nativeSQLRefuseCappedInvocation(t, repository, authority, laterAt, laterAt.Add(3*time.Minute), false)
		probe(measurement.IntegrationGradle, laterAt.Add(-time.Nanosecond))
		check(measurement.IntegrationGradle, laterAt.Add(-time.Nanosecond), laterAt, false)
		probe(measurement.IntegrationGradle, laterAt)
		check(measurement.IntegrationGradle, laterAt, laterAt.Add(time.Nanosecond), true)
		// A later day's loss must not replace the first day's evidence.
		check(measurement.IntegrationGradle, firstAt, after, true)
		lastAt := laterAt.Add(4 * time.Hour)
		nativeSQLRefuseCappedInvocation(t, repository, authority, lastAt, lastAt.Add(time.Minute), false)
		gap := laterAt.Add(2 * time.Hour)
		probe(measurement.IntegrationGradle, gap)
		check(measurement.IntegrationGradle, gap, gap.Add(time.Nanosecond), true)
		probe(measurement.IntegrationGradle, lastAt.Add(time.Nanosecond))
		check(measurement.IntegrationGradle, lastAt.Add(time.Nanosecond), lastAt.Add(2*time.Nanosecond), false)
		unrelatedDay := firstAt.Add(48 * time.Hour)
		probe(measurement.IntegrationGradle, unrelatedDay)
		check(measurement.IntegrationGradle, unrelatedDay, unrelatedDay.Add(time.Nanosecond), false)
	})

	t.Run("transport loss follows the selected invocation window", func(t *testing.T) {
		for _, test := range []struct {
			name          string
			losses        []time.Duration
			beforeReceipt bool
			reportEnd     time.Duration
			taskStart     time.Duration
			taskEnd       time.Duration
			integration   measurement.Integration
			want          bool
		}{
			{name: "loss beyond query end before report receipt", losses: []time.Duration{8 * time.Minute}, beforeReceipt: true, reportEnd: 10 * time.Minute, taskEnd: 10 * time.Minute, want: true},
			{name: "loss beyond query end after report receipt", losses: []time.Duration{8 * time.Minute}, reportEnd: 10 * time.Minute, taskEnd: 10 * time.Minute, want: true},
			{name: "earlier job already finished", losses: []time.Duration{8 * time.Minute}, reportEnd: time.Minute, taskEnd: time.Minute},
			{name: "upper tolerance endpoint", losses: []time.Duration{10*time.Minute + 5*time.Second}, reportEnd: 10 * time.Minute, taskEnd: 10 * time.Minute, want: true},
			{name: "outside upper tolerance", losses: []time.Duration{10*time.Minute + 5*time.Second + time.Nanosecond}, reportEnd: 10 * time.Minute, taskEnd: 10 * time.Minute},
			{name: "lower tolerance endpoint", losses: []time.Duration{-5 * time.Second}, reportEnd: 10 * time.Minute, taskEnd: 10 * time.Minute, want: true},
			{name: "outside lower tolerance", losses: []time.Duration{-5*time.Second - time.Nanosecond}, reportEnd: 10 * time.Minute, taskEnd: 10 * time.Minute},
			{name: "task extends upper invocation envelope", losses: []time.Duration{10*time.Minute + 10*time.Second}, reportEnd: 10 * time.Minute, taskEnd: 10*time.Minute + 5*time.Second, want: true},
			{name: "outside extended task envelope", losses: []time.Duration{10*time.Minute + 10*time.Second + time.Nanosecond}, reportEnd: 10 * time.Minute, taskEnd: 10*time.Minute + 5*time.Second},
			{name: "task extends lower invocation envelope", losses: []time.Duration{-10 * time.Second}, reportEnd: 10 * time.Minute, taskStart: -5 * time.Second, taskEnd: 10 * time.Minute, want: true},
			{name: "other integration is isolated", losses: []time.Duration{8 * time.Minute}, reportEnd: 10 * time.Minute, taskEnd: 10 * time.Minute, integration: measurement.IntegrationXcode},
			{name: "daily envelope gap is conservative", losses: []time.Duration{-time.Hour, time.Hour}, reportEnd: 10 * time.Minute, taskEnd: 10 * time.Minute, want: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := open(t).(*nativeSQLTestRepository)
				at := time.Now().UTC().Truncate(24 * time.Hour).Add(-24*time.Hour + 12*time.Hour)
				authority := nativeSQLAuthority("cohort-workflow", "job-one")
				task := nativeSQLTask("compile", nativeSQLKey, "fromCache", at.Add(test.taskStart), -1)
				task.FinishedAt = at.Add(test.taskEnd)
				report := nativeSQLReport("run-transport-cohort-0001", at, task)
				report.FinishedAt = at.Add(test.reportEnd)
				lossIntegration := test.integration
				if lossIntegration == "" {
					lossIntegration = measurement.IntegrationGradle
				}
				seedLoss := func() {
					t.Helper()
					first, last := at.Add(test.losses[0]), at.Add(test.losses[len(test.losses)-1])
					query := `INSERT INTO layercache_native_loss_days_v1(project_id,integration,loss_kind,day_start,first_lost_at,last_lost_at) VALUES(?,?,?,?,?,?)`
					if fixture.dialect == "postgres" {
						query = `INSERT INTO layercache_native_loss_days_v1(project_id,integration,loss_kind,day_start,first_lost_at,last_lost_at) VALUES($1,$2,$3,$4,$5,$6)`
					}
					if _, err := fixture.database.Exec(query, "project", string(lossIntegration), "transport", first.Truncate(24*time.Hour).UnixNano(), first.UnixNano(), last.UnixNano()); err != nil {
						t.Fatal(err)
					}
				}
				if test.beforeReceipt {
					seedLoss()
				}
				nativeSQLRecordAt(t, fixture, authority, report, report.FinishedAt.Add(time.Minute))
				if !test.beforeReceipt {
					before, err := fixture.PeriodReport(context.Background(), at, at.Add(5*time.Minute))
					if err != nil || len(before) != 1 || before[0].HistoryTruncated {
						t.Fatalf("lossless invocation already marked truncated: reports=%+v error=%v", before, err)
					}
					seedLoss()
				}
				reports, err := fixture.PeriodReport(context.Background(), at, at.Add(5*time.Minute))
				if err != nil || len(reports) != 1 || reports[0].HistoryTruncated != test.want {
					t.Fatalf("transport loss ignored actual selected invocation window or leaked into another cohort: reports=%+v want truncated=%v error=%v", reports, test.want, err)
				}
			})
		}
	})
}

func TestNativeSQLiteReopenKeepsReceiptsAndDoesNotPersistNativeKeys(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "measurement.sqlite")
	repository, err := measurement.OpenSQLiteRepository(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	native, err := repository.NativeRepository(context.Background(), "project")
	if err != nil {
		_ = repository.Close()
		t.Fatal(err)
	}
	authority := nativeSQLAuthority("workflow-one", "job-one")
	report := nativeSQLReport("run-reopen-receipt-0001", nativeSQLBase,
		nativeSQLTask("compile", nativeSQLKey, "executed", nativeSQLBase, 8000))
	nativeSQLRecord(t, native, authority, report)
	if report.Tasks[0].NativeKey != nativeSQLKey {
		t.Fatal("persistence sanitized the caller's report in place")
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(databasePath + suffix)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(nativeSQLKey)) {
			t.Fatalf("raw native cache key survived in persisted measurement database %q", suffix)
		}
	}
	repository, err = measurement.OpenSQLiteRepository(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	native, err = repository.NativeRepository(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := native.RecordReport(context.Background(), authority, report, nativeSQLBase.Add(time.Hour))
	if err != nil || !receipt.Repeated {
		t.Fatalf("reopen lost immutable receipt: receipt=%+v error=%v", receipt, err)
	}
	if projection := nativeSQLProjection(t, native); projection.ReportedInvocations != 1 || projection.ReportedTasks != 1 {
		t.Fatalf("reopen duplicated or lost reports: %+v", projection)
	}
	foreign, err := repository.NativeRepository(context.Background(), "other-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.RecordReport(context.Background(), authority, report, nativeSQLBase.Add(time.Hour)); !errors.Is(err, measurement.ErrNativeInvalid) {
		t.Fatalf("bound native repository accepted foreign project authority: %v", err)
	}
	foreignReports, err := foreign.PeriodReport(context.Background(), nativeSQLBase.Add(-time.Hour), nativeSQLBase.Add(2*time.Hour))
	if err != nil || len(foreignReports) != 0 {
		t.Fatalf("project-scoped projection leaked native reports: reports=%+v error=%v", foreignReports, err)
	}
}

func TestNativeSQLiteRollsBackReceiptAndEarlierTasksAfterSQLFailure(t *testing.T) {
	t.Parallel()
	native, database := nativeSQLSQLiteDatabase(t)
	first := nativeSQLTask("first-task", nativeSQLKey, "executed", nativeSQLBase, 8000)
	second := nativeSQLTask("second-task", strings.Repeat("b", 32), "executed", nativeSQLBase, 8000)
	// Fail the second INSERT after the receipt and first task have been written.
	// The work reference contains only its fixed sha256 prefix and hex digest.
	trigger := fmt.Sprintf(`CREATE TRIGGER native_test_reject_task
		BEFORE INSERT ON layercache_native_tasks_v1 WHEN NEW.work_ref = '%s'
		BEGIN SELECT RAISE(ABORT, 'native test task failure'); END`, second.WorkRef)
	if _, err := database.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	authority := nativeSQLAuthority("workflow-one", "job-one")
	report := nativeSQLReport("run-sql-rollback-0001", nativeSQLBase, first, second)
	if _, err := native.RecordReport(context.Background(), authority, report, nativeSQLBase.Add(time.Hour)); err == nil {
		t.Fatal("task insertion failure was accepted")
	}
	for _, table := range []string{"layercache_native_reports_v1", "layercache_native_tasks_v1"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed transaction retained %d rows in %s", count, table)
		}
	}
	if _, err := database.Exec(`DROP TRIGGER native_test_reject_task`); err != nil {
		t.Fatal(err)
	}
	nativeSQLRecord(t, native, authority, report)
	if projection := nativeSQLProjection(t, native); projection.ReportedInvocations != 1 || projection.ReportedTasks != 2 || projection.Outcomes.Executed != 2 {
		t.Fatalf("transaction rollback prevented exact retry or retained partial work: %+v", projection)
	}
}

func TestNativeSQLiteExpiredOriginalPublicationCannotUseDuplicateExecutionTiming(t *testing.T) {
	t.Parallel()
	native, database := nativeSQLSQLiteDatabase(t)
	producer := nativeSQLAuthority("original-producer", "job-one")
	duplicate := nativeSQLAuthority("duplicate-producer", "job-two")
	consumer := nativeSQLAuthority("consumer", "job-three")
	digest := strings.Repeat("c", 64)
	publication := nativeSQLTransport(t, native, producer, "published", nativeSQLKey, digest, nativeSQLBase.Add(9*time.Second))
	nativeSQLRecord(t, native, producer, nativeSQLReport("run-original-retention-0001", nativeSQLBase,
		nativeSQLTask("compile", nativeSQLKey, "executed", nativeSQLBase, 8000)))
	later := nativeSQLBase.Add(5 * time.Minute)
	// An already-present duplicate PUT supplies no new publication evidence.
	nativeSQLRecord(t, native, duplicate, nativeSQLReport("run-duplicate-retention-0001", later,
		nativeSQLTask("compile", nativeSQLKey, "executed", later, 2000)))
	read := nativeSQLBase.Add(10 * time.Minute)
	nativeSQLTransport(t, native, consumer, "delivered", nativeSQLKey, digest, read.Add(time.Second))
	nativeSQLRecord(t, native, consumer, nativeSQLReport("run-consumer-retention-0001", read,
		nativeSQLTask("compile", nativeSQLKey, "fromCache", read, -1)))
	before := nativeSQLProjection(t, native)
	if before.ProducerTiming.Known != 1 || before.GrossAvoidedTaskTime.Milliseconds == nil || *before.GrossAvoidedTaskTime.Milliseconds != 8000 {
		t.Fatalf("unpublished duplicate execution replaced original timing before expiry: %+v", before)
	}
	encoded, err := json.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	id := sha256.Sum256(encoded)
	result, err := database.Exec(`UPDATE layercache_native_transport_v1 SET received_at=? WHERE project_id='project' AND transport_id=?`,
		time.Now().UTC().Add(-31*24*time.Hour).UnixNano(), hex.EncodeToString(id[:]))
	if err != nil {
		t.Fatal(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("age exact original publication: rows=%d error=%v", affected, err)
	}
	// Reading must disregard expired proof without requiring another write.
	after := nativeSQLProjection(t, native)
	if after.Hits != 1 || nativeSQLSourceCount(after, measurement.SourceTeamCache) != 1 || after.ProducerTiming.Known != 0 || after.ProducerTiming.Total != 1 || after.GrossAvoidedTaskTime.Milliseconds != nil || after.NetAvoidedTaskTime.Milliseconds != nil {
		t.Fatalf("expired original publication inherited unpublished duplicate timing or lost the current native hit: %+v", after)
	}
	assertNativeSQLUnknownSavings(t, after)
}

func TestNativeSQLiteTransportByteBudgetRejectsGrowthButAllowsExactReplay(t *testing.T) {
	t.Parallel()
	native, database := nativeSQLSQLiteDatabase(t)
	authority := nativeSQLAuthority("workflow-one", "job-one")
	existing := measurement.NativeTransport{
		Authority: authority, Kind: "delivered",
		ArtifactID: measurement.NativeArtifactIdentity(authority.Project, authority.CompatibilityID, authority.Integration, nativeSQLKey),
		Digest:     strings.Repeat("c", 64), StartedAt: nativeSQLBase,
		FinishedAt: nativeSQLBase.Add(100 * time.Millisecond), Source: measurement.SourceTeamCache, Bytes: 4096,
	}
	if err := native.RecordTransport(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	encode := func(event measurement.NativeTransport) []byte {
		t.Helper()
		document, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		return document
	}
	const limit int64 = 32 << 20
	remaining := limit - int64(len(encode(existing)))
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.Prepare(`INSERT INTO layercache_native_transport_v1(project_id,transport_id,received_at,document_json) VALUES(?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	seed := func(document []byte) {
		t.Helper()
		id := sha256.Sum256(document)
		if _, err := statement.Exec("project", hex.EncodeToString(id[:]), time.Now().UTC().UnixNano(), string(document)); err != nil {
			t.Fatal(err)
		}
		remaining -= int64(len(document))
	}
	// Seed valid, distinct transport documents directly. Exercising every
	// admission on the way to 32 MiB would repeatedly scan the same fixture.
	// Larger bounded documents reduce the number of fixture INSERTs.
	seedBase := existing
	seedBase.Authority.RunID = strings.Repeat("r", 256)
	seedBase.Authority.WorkspaceID = strings.Repeat("w", 512)
	seedBase.Authority.Subject = strings.Repeat("s", 512)
	seedBase.Authority.Repository = strings.Repeat("r", 256)
	seedBase.Authority.CompatibilityID = strings.Repeat("p", 512)
	seedBase.ArtifactID = measurement.NativeArtifactIdentity(authority.Project, seedBase.Authority.CompatibilityID, authority.Integration, nativeSQLKey)
	for sequence := int64(1); ; sequence++ {
		event := seedBase
		event.StartedAt = nativeSQLBase.Add(time.Duration(sequence) * time.Nanosecond)
		event.FinishedAt = event.StartedAt.Add(100 * time.Millisecond)
		document := encode(event)
		if int64(len(document)) > remaining {
			break
		}
		seed(document)
	}
	newEvent := existing
	boundary := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	newEvent.StartedAt = boundary.Add(-100 * time.Millisecond)
	newEvent.FinishedAt = boundary.Add(100 * time.Millisecond)
	newDocument := encode(newEvent)
	for sequence := int64(2); remaining >= int64(len(newDocument)); sequence++ {
		// Smaller valid documents fill the remaining gap below one new event.
		tail := newEvent
		tail.StartedAt = nativeSQLBase.Add(time.Duration(sequence) * time.Second)
		tail.FinishedAt = tail.StartedAt.Add(100 * time.Millisecond)
		seed(encode(tail))
	}
	// Direct row seeding must reproduce production's transactional usage update.
	if _, err := tx.Exec(`UPDATE layercache_native_transport_usage_v1
		SET row_count=(SELECT COUNT(*) FROM layercache_native_transport_v1 WHERE project_id='project'),
		document_bytes=(SELECT COALESCE(SUM(length(CAST(document_json AS BLOB))),0) FROM layercache_native_transport_v1 WHERE project_id='project')
		WHERE project_id='project'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count, total int64
	if err := database.QueryRow(`SELECT COUNT(*),SUM(length(CAST(document_json AS BLOB))) FROM layercache_native_transport_v1 WHERE project_id='project'`).Scan(&count, &total); err != nil {
		t.Fatal(err)
	}
	if count >= 65536 || total > limit || total+int64(len(newDocument)) <= limit {
		t.Fatalf("fixture did not reach byte admission boundary independently of row cap: rows=%d bytes=%d next=%d", count, total, len(newDocument))
	}
	assertNativeSQLQueryPlan(t, database,
		`SELECT row_count,document_bytes FROM layercache_native_transport_usage_v1 WHERE project_id=?`,
		[]any{"project"}, "layercache_native_transport_usage_v1", "SEARCH")
	assertNativeSQLQueryPlan(t, database,
		`SELECT COUNT(*),COALESCE(SUM(length(CAST(document_json AS BLOB))),0) FROM layercache_native_transport_v1 WHERE project_id=? AND received_at<?`,
		[]any{"project", time.Now().UTC().Add(-30 * 24 * time.Hour).UnixNano()}, "layercache_native_transport_age_v1", "SEARCH")
	if err := native.RecordTransport(context.Background(), newEvent); !errors.Is(err, measurement.ErrNativeQuota) {
		t.Fatalf("transport beyond aggregate byte budget = %v, want quota error", err)
	}
	if err := native.RecordTransport(context.Background(), existing); err != nil {
		t.Fatalf("full transport budget rejected exact existing retry: %v", err)
	}
	var afterCount, afterBytes int64
	if err := database.QueryRow(`SELECT COUNT(*),SUM(length(CAST(document_json AS BLOB))) FROM layercache_native_transport_v1 WHERE project_id='project'`).Scan(&afterCount, &afterBytes); err != nil {
		t.Fatal(err)
	}
	if afterCount != count || afterBytes != total {
		t.Fatalf("rejected growth or exact replay changed transport storage: rows %d to %d, bytes %d to %d", count, afterCount, total, afterBytes)
	}
	var day, firstLoss, lastLoss int64
	if err := database.QueryRow(`SELECT day_start,first_lost_at,last_lost_at FROM layercache_native_loss_days_v1 WHERE project_id='project' AND integration='gradle' AND loss_kind='transport'`).Scan(&day, &firstLoss, &lastLoss); err != nil {
		t.Fatal(err)
	}
	if day != boundary.UnixNano() || firstLoss != newEvent.FinishedAt.UnixNano() || lastLoss != newEvent.FinishedAt.UnixNano() {
		t.Fatalf("transport refusal used submission/start time instead of its completed workload interval: day=%d first=%d last=%d", day, firstLoss, lastLoss)
	}
}

func TestNativeSQLiteCoverageLossDaysSurviveRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "measurement.sqlite")
	repository, err := measurement.OpenSQLiteRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	native, err := repository.NativeRepository(context.Background(), "project")
	if err != nil {
		_ = repository.Close()
		t.Fatal(err)
	}
	firstAt := time.Now().UTC().Truncate(24 * time.Hour).Add(-72*time.Hour + 23*time.Hour + 59*time.Minute + 30*time.Second)
	authority := nativeSQLAuthority("coverage-limited-workflow", "job-one")
	nativeSQLRefuseCappedInvocation(t, native, authority, firstAt, firstAt.Add(2*time.Minute), true)
	secondAt := firstAt.Truncate(24 * time.Hour).Add(24 * time.Hour)
	nativeSQLRefuseCappedInvocation(t, native, authority, secondAt, secondAt.Add(3*time.Minute), false)
	probe := nativeSQLCounterReport("run-restart-loss-probe-0001", secondAt)
	nativeSQLRecordAt(t, native, nativeSQLAuthority("coverage-probes", "job-one"), probe, secondAt.Add(time.Minute))
	transportAt := secondAt.Add(12 * time.Hour)
	task := nativeSQLTask("compile", nativeSQLKey, "fromCache", transportAt, -1)
	task.FinishedAt = transportAt.Add(10 * time.Minute)
	transportReport := nativeSQLReport("run-restart-transport-0001", transportAt, task)
	transportReport.FinishedAt = task.FinishedAt
	nativeSQLRecordAt(t, native, nativeSQLAuthority("transport-restart-probe", "job-one"), transportReport, task.FinishedAt.Add(time.Minute))
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	lostTransport := transportAt.Add(8 * time.Minute)
	if _, err := database.Exec(`INSERT INTO layercache_native_loss_days_v1(project_id,integration,loss_kind,day_start,first_lost_at,last_lost_at) VALUES(?,?,?,?,?,?)`,
		"project", "gradle", "transport", lostTransport.Truncate(24*time.Hour).UnixNano(), lostTransport.UnixNano(), lostTransport.UnixNano()); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err = measurement.OpenSQLiteRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	native, err = repository.NativeRepository(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{firstAt, secondAt} {
		reports, err := native.PeriodReport(context.Background(), at, at.Add(time.Nanosecond))
		if err != nil || len(reports) != 1 || !reports[0].HistoryTruncated {
			t.Fatalf("restart lost retained loss day at %s: reports=%+v error=%v", at, reports, err)
		}
	}
	transportReports, err := native.PeriodReport(context.Background(), transportAt, transportAt.Add(5*time.Minute))
	if err != nil || len(transportReports) != 1 || !transportReports[0].HistoryTruncated {
		t.Fatalf("restart lost transport coverage beyond the query end within the selected invocation: reports=%+v error=%v", transportReports, err)
	}
}

func TestNativeSQLiteCoverageLossDaysStayBoundedPerIntegration(t *testing.T) {
	t.Parallel()
	native, database := nativeSQLSQLiteDatabase(t)
	newestDay := time.Now().UTC().Truncate(24 * time.Hour)
	newestEvent := newestDay.Add(time.Minute)
	// Existing transport losses must keep their independent daily allowance
	// while report refusals grow and prune their own bounded history.
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, integration := range []measurement.Integration{measurement.IntegrationGradle, measurement.IntegrationXcode} {
		for day := range 31 {
			at := newestEvent.Add(-time.Duration(day) * 24 * time.Hour)
			if _, err := tx.Exec(`INSERT INTO layercache_native_loss_days_v1(project_id,integration,loss_kind,day_start,first_lost_at,last_lost_at) VALUES(?,?,?,?,?,?)`,
				"project", string(integration), "transport", at.Truncate(24*time.Hour).UnixNano(), at.UnixNano(), at.UnixNano()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, integration := range []measurement.Integration{measurement.IntegrationGradle, measurement.IntegrationXcode} {
		authority := nativeSQLAuthority("coverage-limited-workflow", "job-one")
		authority.Integration = integration
		// Moving the trusted test clock backwards retains the same saturated
		// job. Only coverage rows grow, rather than 32 new reports per day.
		for day := range 36 {
			at := newestEvent.Add(-time.Duration(day) * 24 * time.Hour)
			nativeSQLRefuseCappedInvocation(t, native, authority, at, at.Add(time.Minute), day == 0)
		}
	}
	rows, err := database.Query(`SELECT integration,loss_kind,COUNT(*),MIN(day_start),MAX(day_start)
		FROM layercache_native_loss_days_v1 WHERE project_id='project' GROUP BY integration,loss_kind ORDER BY integration,loss_kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	groups := 0
	for rows.Next() {
		var integration, kind string
		var count, oldest, newest int64
		if err := rows.Scan(&integration, &kind, &count, &oldest, &newest); err != nil {
			t.Fatal(err)
		}
		if count != 31 || oldest != newestDay.Add(-30*24*time.Hour).UnixNano() || newest != newestDay.UnixNano() {
			t.Fatalf("%s/%s loss-day bound changed: count=%d oldest=%d newest=%d", integration, kind, count, oldest, newest)
		}
		groups++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if groups != 4 {
		t.Fatalf("loss-day limits leaked between integrations or report/transport kinds: groups=%d", groups)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var reports int
	if err := database.QueryRow(`SELECT COUNT(*) FROM layercache_native_reports_v1 WHERE project_id='project'`).Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if reports != 2*measurement.NativeMaximumInvocations {
		t.Fatalf("loss-day fixture grew or expired saturated jobs: retained reports=%d", reports)
	}
}

func TestNativeSQLiteTransportUsageSurvivesConcurrentRetriesRestartAndExpiry(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "measurement.sqlite")
	open := func() (*measurement.SQLiteRepository, measurement.NativeRepository) {
		t.Helper()
		repository, err := measurement.OpenSQLiteRepository(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repository.Close() })
		native, err := repository.NativeRepository(context.Background(), "project")
		if err != nil {
			t.Fatal(err)
		}
		return repository, native
	}
	firstRepository, first := open()
	secondRepository, second := open()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	check := func(wantCount int64) {
		t.Helper()
		var count, charged, actualCount, actualBytes int64
		if err := database.QueryRow(`SELECT row_count,document_bytes FROM layercache_native_transport_usage_v1 WHERE project_id='project'`).Scan(&count, &charged); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(CAST(document_json AS BLOB))),0) FROM layercache_native_transport_v1 WHERE project_id='project'`).Scan(&actualCount, &actualBytes); err != nil {
			t.Fatal(err)
		}
		if count != wantCount || actualCount != count || actualBytes != charged {
			t.Fatalf("transport usage diverged: charged rows=%d bytes=%d, actual rows=%d bytes=%d, want rows=%d", count, charged, actualCount, actualBytes, wantCount)
		}
	}
	check(0)
	const distinct = 16
	events := make([]measurement.NativeTransport, distinct)
	for invocation := range distinct {
		authority := nativeSQLAuthority("workflow-one", "job-one")
		start := nativeSQLBase.Add(time.Duration(invocation) * time.Second)
		events[invocation] = measurement.NativeTransport{
			Authority: authority, Kind: "delivered",
			ArtifactID: measurement.NativeArtifactIdentity(authority.Project, authority.CompatibilityID, authority.Integration, nativeSQLKey),
			Digest:     strings.Repeat("c", 64), StartedAt: start, FinishedAt: start.Add(100 * time.Millisecond),
			Source: measurement.SourceTeamCache, Bytes: 4096,
		}
	}
	start := make(chan struct{})
	failures := make(chan error, distinct*2)
	var wait sync.WaitGroup
	for _, repository := range []measurement.NativeRepository{first, second} {
		for _, event := range events {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				if err := repository.RecordTransport(context.Background(), event); err != nil {
					failures <- err
				}
			}()
		}
	}
	close(start)
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("concurrent exact transport retry: %v", err)
	}
	check(distinct)
	if err := firstRepository.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secondRepository.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := open()
	check(distinct)
	for _, event := range events[:2] {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		id := sha256.Sum256(encoded)
		if _, err := database.Exec(`UPDATE layercache_native_transport_v1 SET received_at=? WHERE project_id='project' AND transport_id=?`,
			time.Now().UTC().Add(-31*24*time.Hour).UnixNano(), hex.EncodeToString(id[:])); err != nil {
			t.Fatal(err)
		}
	}
	// Aging does not delete or uncharge rows before the expiry transaction runs.
	check(distinct)
	newEvent := events[0]
	newEvent.StartedAt = nativeSQLBase.Add(time.Minute)
	newEvent.FinishedAt = newEvent.StartedAt.Add(100 * time.Millisecond)
	if err := reopened.RecordTransport(context.Background(), newEvent); err != nil {
		t.Fatal(err)
	}
	check(distinct - 2 + 1)
	if err := reopened.RecordTransport(context.Background(), newEvent); err != nil {
		t.Fatal(err)
	}
	check(distinct - 2 + 1)
}

func assertNativeSQLQueryPlan(t *testing.T, database *sql.DB, query string, args []any, fragments ...string) {
	t.Helper()
	rows, err := database.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, "\n")
	for _, fragment := range fragments {
		if !strings.Contains(plan, fragment) {
			t.Fatalf("bounded transport lookup did not use its expected index: missing %q in %s", fragment, plan)
		}
	}
	if strings.Contains(plan, "SCAN ") {
		t.Fatalf("bounded transport lookup scans history: %s", plan)
	}
}

func nativeSQLCompiler(coverage, reason string) measurement.NativeCompilerCounters {
	return measurement.NativeCompilerCounters{Scope: "clang-pch", Coverage: coverage, Reason: reason,
		Hits: nativeSQLNumber(0), Misses: nativeSQLNumber(3), Skipped: nativeSQLNumber(0), Unknown: nativeSQLNumber(0)}
}

func nativeSQLSQLiteDatabase(t *testing.T) (measurement.NativeRepository, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "measurement.sqlite")
	repository, err := measurement.OpenSQLiteRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	native, err := repository.NativeRepository(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	return native, database
}

func nativeSQLAuthority(run, workspace string) measurement.NativeAuthority {
	return measurement.NativeAuthority{
		Project: "project", RunID: run, WorkspaceID: workspace,
		Integration: measurement.IntegrationGradle, CompatibilityID: "gradle-profile-one",
		Subject: "repo:example/project:ref:refs/heads/main", Repository: "example/project",
		SourceCommit: strings.Repeat("a", 40), Kind: "githubActions",
		Execution: &execution.Context{Kind: "githubActions", OwnerID: "github-id:1", ExecutionID: "github-actions:1:10:1:20", RunnerEnvironment: "self-hosted"},
	}
}

func nativeSQLTask(work, key, outcome string, start time.Time, executionMS int64) measurement.NativeTask {
	var duration *int64
	if executionMS >= 0 {
		duration = nativeSQLNumber(executionMS)
	}
	return measurement.NativeTask{
		WorkRef:   measurement.NativeArtifactIdentity("project", "work", measurement.IntegrationGradle, work),
		NativeKey: key, Outcome: outcome, StartedAt: start, FinishedAt: start.Add(10 * time.Second), ExecutionMS: duration,
	}
}

func nativeSQLReport(invocation string, start time.Time, tasks ...measurement.NativeTask) measurement.NativeReport {
	if tasks == nil {
		tasks = []measurement.NativeTask{}
	}
	return measurement.NativeReport{Version: measurement.NativeVersion, InvocationID: invocation, StartedAt: start, FinishedAt: start.Add(30 * time.Second), Status: "success", Tasks: tasks}
}

func nativeSQLNumber(value int64) *int64 { return &value }

func nativeSQLRecord(t *testing.T, repository measurement.NativeRepository, authority measurement.NativeAuthority, report measurement.NativeReport) {
	t.Helper()
	nativeSQLRecordAt(t, repository, authority, report, nativeSQLBase.Add(time.Hour))
}

func nativeSQLRecordAt(t *testing.T, repository measurement.NativeRepository, authority measurement.NativeAuthority, report measurement.NativeReport, now time.Time) {
	t.Helper()
	receipt, err := repository.RecordReport(context.Background(), authority, report, now)
	if err != nil {
		t.Fatalf("record %s: %v", report.InvocationID, err)
	}
	if !receipt.Recorded || receipt.Repeated || receipt.InvocationID != report.InvocationID || receipt.Tasks != len(report.Tasks) {
		t.Fatalf("new native report returned unexpected receipt: %+v", receipt)
	}
}

func nativeSQLCounterReport(invocation string, at time.Time) measurement.NativeReport {
	report := nativeSQLReport(invocation, at)
	report.TimingMS = &measurement.NativePhaseTiming{Operation: nativeSQLNumber(1)}
	return report
}

func nativeSQLRefuseCappedInvocation(t *testing.T, repository measurement.NativeRepository, authority measurement.NativeAuthority, at, submitted time.Time, seed bool) {
	t.Helper()
	if seed {
		for invocation := range measurement.NativeMaximumInvocations {
			report := nativeSQLCounterReport(fmt.Sprintf("run-coverage-seed-%04d", invocation), at)
			nativeSQLRecordAt(t, repository, authority, report, submitted)
		}
	}
	report := nativeSQLCounterReport(fmt.Sprintf("run-coverage-refused-%d", at.UnixNano()), at)
	if _, err := repository.RecordReport(context.Background(), authority, report, submitted); !errors.Is(err, measurement.ErrNativeQuota) {
		t.Fatalf("coverage fixture did not reach invocation quota: %v", err)
	}
}

func nativeSQLTransport(t *testing.T, repository measurement.NativeRepository, authority measurement.NativeAuthority, kind, key, digest string, at time.Time, resource ...*execution.ResourceSnapshot) measurement.NativeTransport {
	t.Helper()
	source := measurement.SourceNone
	if kind == "delivered" {
		source = measurement.SourceTeamCache
	}
	event := measurement.NativeTransport{
		Authority: authority, Kind: kind,
		ArtifactID: measurement.NativeArtifactIdentity(authority.Project, authority.CompatibilityID, authority.Integration, key),
		Digest:     digest, StartedAt: at, FinishedAt: at.Add(100 * time.Millisecond), Source: source, Bytes: 4096,
	}
	if len(resource) != 0 {
		event.Resource = resource[0]
	}
	if err := repository.RecordTransport(context.Background(), event); err != nil {
		t.Fatalf("record native %s: %v", kind, err)
	}
	return event
}

func nativeSQLResource(at time.Time, cores int64) *execution.ResourceSnapshot {
	return &execution.ResourceSnapshot{
		ObservedAt: at, Phase: "start", Provider: "ubicloud", ProviderEvidence: "declared", Basis: "client-reported",
		Sample: execution.Sample{
			OS: "linux", Arch: "amd64",
			CPU:    execution.CPU{VisibleLogical: nativeSQLNumber(cores), AffinityLogical: nativeSQLNumber(cores), QuotaMilliCPU: nativeSQLNumber(cores * 1000), EffectiveMilliCPU: nativeSQLNumber(cores * 1000), LimitKnowledge: "complete"},
			Memory: execution.Memory{LimitKnowledge: "unknown"}, Filesystems: []execution.Filesystem{},
		},
	}
}

func nativeSQLProjection(t *testing.T, repository measurement.NativeRepository) measurement.NativeIntegrationReport {
	t.Helper()
	reports, err := repository.PeriodReport(context.Background(), nativeSQLBase.Add(-time.Hour), nativeSQLBase.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Integration != measurement.IntegrationGradle {
		t.Fatalf("native Gradle projection = %+v, want exactly one Gradle integration", reports)
	}
	return reports[0]
}

func nativeSQLSourceCount(report measurement.NativeIntegrationReport, source measurement.Source) int {
	total := 0
	for _, item := range report.Sources {
		if item.Source == source {
			total += item.Hits
		}
	}
	return total
}

func assertNativeSQLUnknownSavings(t *testing.T, report measurement.NativeIntegrationReport) {
	t.Helper()
	if report.NetEstimatedBuildTimeSaved.Milliseconds != nil || report.AllocatedCPUSecondsSaved != nil || report.MeasuredCPUSecondsSaved != nil || report.CostSaved != nil || report.ProducerResources.Known != 0 {
		t.Fatalf("native task timing invented net build, CPU, resource or cost savings: %+v", report)
	}
}
