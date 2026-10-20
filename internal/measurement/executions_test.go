package measurement

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/execution"
)

func TestExecutionActivityMatchesWorkspaceAndCheckScopedTransport(t *testing.T) {
	r, err := OpenSQLiteRepository(filepath.Join(t.TempDir(), "measurements.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	project := "widgets"
	client := execution.Authority{Context: *execution.ClientContext("github-id:42", "client-one"), Project: project, RunID: "github-local:github:person:run-one", WorkspaceID: "workspace-one"}
	job := execution.Authority{Context: execution.Context{Kind: "githubActions", OwnerID: "github-id:42", ExecutionID: "github-actions:1:2:3:4", RunnerEnvironment: "self-hosted"}, Project: project, RunID: "github-actions:acme/widgets:2:3:check:4", WorkspaceID: "github-actions-check:acme/widgets:4"}
	sibling := job
	sibling.ExecutionID = "github-actions:1:2:3:5"
	sibling.RunID = "github-actions:acme/widgets:2:3"
	sibling.WorkspaceID = "github-actions-check:acme/widgets:5"
	if err = r.Record(FinalOutcome{RunID: client.RunID, WorkspaceID: client.WorkspaceID, Integration: IntegrationTurbo, WorkID: "compile", Result: ResultMiss, Source: SourceNone, StartedAt: now, FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []string{job.RunID, sibling.RunID} {
		if err = r.ObserveTurbo(TurboObservation{RunID: run, ArtifactID: "opaque", Result: ResultMiss, Source: SourceNone, StartedAt: now, FinishedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	foreignWorkspace := client
	foreignWorkspace.ExecutionID = "foreign-execution"
	foreignWorkspace.WorkspaceID = "workspace-other"
	known, err := executionActivity(ctx, r.database, "sqlite", project, []execution.Authority{client, job, sibling, foreignWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	if !known[client.ExecutionID] || !known[job.ExecutionID] || known[sibling.ExecutionID] || known[foreignWorkspace.ExecutionID] {
		t.Fatalf("unsafe association %+v", known)
	}
	// More than SQLite's expression depth limit worth of distinct authorities
	// stays bounded through lookup chunks, while preserving the real match.
	var many []execution.Authority
	for i := range 1100 {
		a := client
		a.RunID = fmt.Sprintf("run-%d", i)
		a.WorkspaceID = fmt.Sprintf("workspace-%d", i)
		a.ExecutionID = fmt.Sprintf("client-%d", i)
		many = append(many, a)
	}
	many = append(many, client)
	known, err = executionActivity(ctx, r.database, "sqlite", project, many)
	if err != nil || !known[client.ExecutionID] || len(known) != 1 {
		t.Fatalf("chunked association %v %v", known, err)
	}
}

func TestExecutionProjectionNeverInventsPhysicalHostsOrResourceDefaults(t *testing.T) {
	r, err := OpenSQLiteRepository(filepath.Join(t.TempDir(), "measurements.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	repo, err := r.ExecutionRepository(ctx, "widgets")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	report := execution.ObservationRequest{Version: 1, Phase: "start", InstallationID: strings.Repeat("a", 64), Sample: execution.Sample{OS: "linux", Arch: "amd64", CPU: execution.CPU{LimitKnowledge: "unknown"}, Memory: execution.Memory{LimitKnowledge: "unknown"}, Filesystems: []execution.Filesystem{}}}
	for _, nonce := range []string{"worktree-one", "worktree-two"} {
		a := execution.Authority{Context: *execution.ClientContext("github-id:42", nonce), Project: "widgets", RunID: "run-" + nonce, WorkspaceID: "workspace-" + nonce}
		if err = repo.Record(ctx, a, report, now); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := repo.Inventory(ctx, "widgets", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil || len(inv.Installations) != 1 || len(inv.Jobs) != 0 || inv.Installations[0].Sample.CPU.VisibleLogical != nil || inv.Installations[0].Sample.Memory.EffectiveLimitBytes != nil || inv.Installations[0].CacheActivity != "unconfirmed" {
		t.Fatalf("projection %+v %v", inv, err)
	}
}
