package main

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestRestoredTerminalWorkerReopensOnlyOlderCompletedCleanup(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	s := stepBy(t, a, r, "research")
	if _, err := a.cancelWorkflow(p.ProjectID, "operator", p.ID, r.ID, "fixture finished"); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	before := len(f.kills)
	event := sdk.TelemetryStreamEvent{ProjectID: p.ProjectID, Type: "thread.spawn", AgentID: s.Executor.AgentID, ThreadID: s.ThreadID, Time: time.Now()}
	for _, bad := range []sdk.TelemetryStreamEvent{
		{ProjectID: "another-project", Type: event.Type, AgentID: event.AgentID, ThreadID: event.ThreadID, Time: event.Time},
		{ProjectID: p.ProjectID, Type: event.Type, AgentID: event.AgentID, ThreadID: "main", Time: event.Time},
		{ProjectID: p.ProjectID, Type: "thread.message", AgentID: event.AgentID, ThreadID: event.ThreadID, Time: event.Time},
	} {
		if err := a.requeueRestoredTerminalWorker(context.Background(), p.ProjectID, bad); err != nil {
			t.Fatal(err)
		}
	}
	var pending int
	if err := a.db.QueryRow(`SELECT count(*) FROM process_worker_cleanup WHERE completed_at=''`).Scan(&pending); err != nil || pending != 0 {
		t.Fatal("unscoped metadata reopened cleanup", err)
	}
	if err := a.requeueRestoredTerminalWorker(context.Background(), p.ProjectID, event); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(f.kills) != before+1 || f.kills[len(f.kills)-1].ThreadID != s.ThreadID {
		t.Fatal("restored completed worker not retired")
	}
	before = len(f.kills)
	// Replay must not reopen the newer acknowledgement, including after restart.
	a = &App{ctx: a.ctx, db: a.db}
	if err := a.requeueRestoredTerminalWorker(context.Background(), p.ProjectID, event); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil || len(f.kills) != before {
		t.Fatal("replay repeated acknowledged kill", err)
	}
	after, err := a.getRun(p.ProjectID, p.ID, r.ID)
	if err != nil || after.State != "cancelled" || after.Error != "fixture finished" {
		t.Fatal("repair changed terminal outcome", after, err)
	}
}

func TestRestoredWorkerMetadataCannotRetireApprovalOrResetPendingBackoff(t *testing.T) {
	a, f, p, steps := legacyWaitBatch(t, []int64{7})
	s := steps[0]
	event := sdk.TelemetryStreamEvent{ProjectID: p.ProjectID, Type: "thread.spawn", AgentID: 7, ThreadID: s.ThreadID, Time: time.Now()}
	r, err := a.getRun(p.ProjectID, p.ID, s.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.stepAction(p.ProjectID, "agent:7:"+s.ThreadID, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "waiting", "error": "pending approval", "wait": map[string]any{"kind": "approval", "reference": "approval-123"}}); err != nil {
		t.Fatal(err)
	}
	if err = a.requeueRestoredTerminalWorker(context.Background(), p.ProjectID, event); err != nil {
		t.Fatal(err)
	}
	if err = a.cleanupWorkers(context.Background(), time.Now()); err != nil || len(f.kills) != 0 {
		t.Fatal("metadata retired approval owner", err)
	}
	if _, err = a.cancelWorkflow(p.ProjectID, "operator", p.ID, r.ID, "fixture cancelled"); err != nil {
		t.Fatal(err)
	}
	f.killError = errors.New("temporary cleanup failure")
	if err = a.cleanupWorkers(context.Background(), time.Now()); err == nil {
		t.Fatal("expected cleanup failure")
	}
	var deadline string
	if err = a.db.QueryRow(`SELECT next_attempt_at FROM process_worker_cleanup WHERE run_id=?`, r.ID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	event.Time = time.Now().Add(time.Minute)
	if err = a.requeueRestoredTerminalWorker(context.Background(), p.ProjectID, event); err != nil {
		t.Fatal(err)
	}
	var got string
	if err = a.db.QueryRow(`SELECT next_attempt_at FROM process_worker_cleanup WHERE run_id=?`, r.ID).Scan(&got); err != nil || got != deadline {
		t.Fatal("repair reset existing cleanup retry order", got, deadline, err)
	}
}

func TestStoppedCleanupOwnerIsDeferredUntilLiveAcknowledgement(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	s := stepBy(t, a, r, "research")
	if _, err := a.cancelWorkflow(p.ProjectID, "operator", p.ID, r.ID, "fixture done"); err != nil {
		t.Fatal(err)
	}
	f.instanceStatuses = map[int64]string{s.Executor.AgentID: "stopped"}
	now := time.Now()
	if err := a.cleanupWorkers(context.Background(), now); err == nil {
		t.Fatal("stopped cleanup falsely acknowledged")
	}
	if len(f.kills) != 0 {
		t.Fatal("cleanup called stopped owner")
	}
	f.instanceStatuses[s.Executor.AgentID] = "running"
	if err := a.cleanupWorkers(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(f.kills) != 1 {
		t.Fatal("live retirement not retried")
	}
}

type cleanupRepairPlatform struct {
	directPlatform
	sdk.TelemetryClient
	queries []sdk.TelemetryQuery
	pages   []*sdk.TelemetryPage
}

func (f *cleanupRepairPlatform) QueryTelemetry(_ context.Context, q sdk.TelemetryQuery) (*sdk.TelemetryPage, error) {
	f.queries = append(f.queries, q)
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, nil
}
func TestWorkerStartRepairPagesSurviveRestartAndUseOnlyMetadata(t *testing.T) {
	f := &cleanupRepairPlatform{pages: []*sdk.TelemetryPage{{NextCursor: "older-page"}, {}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := a.db.Exec(`INSERT INTO process_worker_start_agent_scans(project_id,agent_id,since_at) VALUES('project-a',7,?)`, now.Add(-24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := a.repairRestoredWorkerCleanup(context.Background(), "project-a", now); err != nil {
		t.Fatal(err)
	}
	a = &App{ctx: a.ctx, db: a.db}
	if err := a.repairRestoredWorkerCleanup(context.Background(), "project-a", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(f.queries) != 1 {
		t.Fatal("page retried before saved deadline")
	}
	if err := a.repairRestoredWorkerCleanup(context.Background(), "project-a", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(f.queries) != 2 || f.queries[1].Cursor != "older-page" || !f.queries[0].Since.Equal(f.queries[1].Since) || !f.queries[0].Until.Equal(f.queries[1].Until) {
		t.Fatal("restart changed cursor filters", f.queries)
	}
	for _, q := range f.queries {
		if q.Projection != "metadata" || q.Limit != 25 || q.AgentID != 7 || q.ProjectID != "project-a" || q.ThreadPrefix != "process-run-" || len(q.Events) != 1 || q.Events[0] != "thread.spawn" {
			t.Fatal("repair read broad telemetry", q)
		}
	}
	at, err := a.nextWorkerStartScan(context.Background(), "project-a")
	if err != nil || !at.Equal(now.Add(62*time.Second)) {
		t.Fatal("scan did not return to minute cadence", at, err)
	}
}

func TestWorkerStartRepairDefersStoppedOwnerAndContinuesAnotherAgent(t *testing.T) {
	f := &cleanupRepairPlatform{pages: []*sdk.TelemetryPage{{}}}
	f.instanceStatuses = map[int64]string{7: "stopped"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, agent := range []int64{7, 8} {
		if _, err := a.db.Exec(`INSERT INTO process_worker_start_agent_scans(project_id,agent_id,since_at) VALUES('project-a',?,?)`, agent, now.Add(-24*time.Hour).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.repairRestoredWorkerCleanup(context.Background(), "project-a", now); err != nil {
		t.Fatal(err)
	}
	if len(f.queries) != 0 {
		t.Fatal("queried or woke stopped owner")
	}
	a = &App{ctx: a.ctx, db: a.db}
	if err := a.repairRestoredWorkerCleanup(context.Background(), "project-a", now); err != nil {
		t.Fatal(err)
	}
	if len(f.queries) != 1 || f.queries[0].AgentID != 8 || f.queries[0].Limit != 25 {
		t.Fatal("healthy metadata owner was blocked", f.queries)
	}
}
