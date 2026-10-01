package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type reliabilityPlatform struct {
	directPlatform
	a                     *App
	sendError, spawnError error
	loseSend              bool
	ledger                map[string]string
}

func (f *reliabilityPlatform) SendTrackedAgentEvent(req sdk.AgentEventRequest) (*sdk.AgentEventReceipt, error) {
	// Verify the whole request is durable before even the first send.
	var saved string
	if err := f.a.db.QueryRow(`SELECT request_json FROM process_delivery_envelopes WHERE event_id=?`, req.SourceEventID).Scan(&saved); err != nil {
		return nil, err
	}
	if saved != jsonText(req) {
		return nil, errors.New("request was not persisted before send")
	}
	f.events = append(f.events, req)
	if f.sendError != nil {
		return nil, f.sendError
	}
	if f.ledger == nil {
		f.ledger = map[string]string{}
	}
	previous, duplicate := f.ledger[req.SourceEventID]
	if duplicate && previous != jsonText(req) {
		return nil, errors.New("HTTP 409: source event id already exists with different content")
	}
	f.ledger[req.SourceEventID] = jsonText(req)
	if f.loseSend {
		f.loseSend = false
		return nil, errors.New("response lost after acceptance")
	}
	return &sdk.AgentEventReceipt{Accepted: true, Duplicate: duplicate, ExecutionID: "exec-durable", ThreadID: req.ThreadID}, nil
}

func (f *reliabilityPlatform) SpawnThread(req sdk.ThreadSpawnRequest) (*sdk.ThreadSpawnResult, error) {
	if f.spawnError != nil {
		f.threads = append(f.threads, req)
		return nil, f.spawnError
	}
	return f.directPlatform.SpawnThread(req)
}

func reliabilitySetup(t *testing.T, d Definition) (*App, *reliabilityPlatform, *Process) {
	t.Helper()
	f := &reliabilityPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	f.a = a
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	return a, f, p
}

func savedRun(t *testing.T, a *App, p *Process) Run {
	t.Helper()
	runs, err := a.dispatches(p.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("saved run: %v %d", err, len(runs))
	}
	return runs[0]
}

func totalChanges(t *testing.T, a *App) int {
	t.Helper()
	var n int
	if err := a.db.QueryRow(`SELECT total_changes()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGlobalWorkersOnlyTouchTheirDispatchedProject(t *testing.T) {
	a, _, _ := directSetup(t)
	workers := a.Workers()
	var runWorker sdk.Worker
	for _, w := range workers {
		if w.Name == "process-runs" {
			runWorker = w
		}
	}
	// Ten project callbacks must initialize exactly ten runs, not scan all ten
	// projects on each callback. Human work is intentionally idle after init.
	targets := make([]*Process, 10)
	for i := range targets {
		project := fmt.Sprintf("project-%d", i)
		d := workflowDefinition()
		d.Steps = d.Steps[:1]
		p, err := a.save(project, "", "operator", 0, d)
		if err != nil {
			t.Fatal(err)
		}
		r := Run{ID: fmt.Sprintf("scoped-run-%d", i), ProcessID: p.ID, Version: p.Version, Kind: "manual", RequestKey: "scoped", CreatedAt: timestamp(), Backend: "agent", Workflow: true, Binding: AssignmentConfig{Roles: map[string]Executor{"researcher": {Kind: "human"}}}, Overrides: map[string]any{}}
		if err = insertProcessRun(a.db, r); err != nil {
			t.Fatal(err)
		}
		targets[i] = p
	}
	t.Setenv("APTEVA_PROJECT_ID", "") // actual global install dispatch
	for i, p := range targets {
		if err := runWorker.Run(context.Background(), a.ctx.WithProject(p.ProjectID)); err != nil {
			t.Fatal(err)
		}
		for j, target := range targets {
			r, err := a.getRun(target.ProjectID, target.ID, fmt.Sprintf("scoped-run-%d", j))
			if err != nil {
				t.Fatal(err)
			}
			if (r.DeliveredAt != "") != (j <= i) {
				t.Fatalf("callback %s touched another project %s", p.ProjectID, target.ProjectID)
			}
		}
	}
	before := totalChanges(t, a)
	for _, p := range targets {
		if err := runWorker.Run(context.Background(), a.ctx.WithProject(p.ProjectID)); err != nil {
			t.Fatal(err)
		}
	}
	if n := totalChanges(t, a) - before; n != 0 {
		t.Fatalf("idle scoped tick made %d row changes", n)
	}
	// An unscoped global callback must do nothing, not fall back to all data.
	if err := runWorker.Run(context.Background(), a.ctx.WithProject("")); err != nil {
		t.Fatal(err)
	}
	if n := totalChanges(t, a) - before; n != 0 {
		t.Fatalf("empty scope wrote %d rows", n)
	}
}

func TestUnchangedBlockedWorkflowHasZeroWrites(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	s := stepBy(t, a, r, "research")
	if _, err := a.stepAction(p.ProjectID, fmt.Sprintf("agent:%d:%s", s.Executor.AgentID, s.ThreadID), p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "blocked", "error": "waiting for upstream service"}); err != nil {
		t.Fatal(err)
	}
	before := totalChanges(t, a)
	for i := 0; i < 100; i++ {
		if err := a.reconcileWorkflowAt(p, &r, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := a.tickDirect(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if n := totalChanges(t, a) - before; n != 0 {
		t.Fatalf("unchanged blocked run wrote %d rows", n)
	}
	pending, err := a.pendingRuns(p.ID, time.Now())
	if err != nil || len(pending) != 0 {
		t.Fatalf("idle blocked run selected: %v %+v", err, pending)
	}
}

func TestIdleReconciliationDoesNotGrowSQLiteWAL(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	s := stepBy(t, a, r, "research")
	if _, err := a.stepAction(p.ProjectID, fmt.Sprintf("agent:%d:%s", s.Executor.AgentID, s.ThreadID), p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "blocked", "error": "waiting for service"}); err != nil {
		t.Fatal(err)
	}
	// Copy the fixture to a real file-backed WAL database. Disable automatic
	// checkpointing so any accidental write remains measurable in the WAL.
	path := filepath.Join(t.TempDir(), "idle.db")
	if _, err := a.db.Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	disk, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	disk.SetMaxOpenConns(1)
	if _, err = disk.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	a = &App{ctx: a.ctx, db: disk}
	r = savedRun(t, a, p)
	before := totalChanges(t, a)
	started := time.Now()
	for i := 0; i < 1000; i++ {
		if err = a.reconcileWorkflow(p, &r); err != nil {
			t.Fatal(err)
		}
		if err = a.tickDirect(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if totalChanges(t, a) != before {
		t.Fatal("idle WAL fixture mutated rows")
	}
	wal, err := os.Stat(path + "-wal")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err == nil && wal.Size() != 0 {
		t.Fatalf("idle reconciliation wrote %d WAL bytes", wal.Size())
	}
	t.Logf("1000 reconciliations + 1000 worker ticks: 0 changed rows, 0 WAL bytes, %s", time.Since(started))
}

func TestImmutableIndependentDeliverySurvivesLostAckAndChangedContext(t *testing.T) {
	d := workflowDefinition()
	d.Steps = d.Steps[:1] // independent worker
	a, f, p := reliabilitySetup(t, d)
	f.loseSend = true
	if _, err := a.start(p.ProjectID, p.ID, "immutable", ""); err == nil {
		t.Fatal("expected lost acknowledgement")
	}
	r := savedRun(t, a, p)
	s := stepBy(t, a, r, "research")
	original := jsonText(f.events[0])
	if s.DeliveryWarning == "" || s.Attempts != 1 {
		t.Fatalf("missing durable error: %+v", s)
	}
	before := totalChanges(t, a)
	for i := 0; i < 20; i++ {
		if err := a.reconcileWorkflow(p, &r); err != nil {
			t.Fatal(err)
		}
	}
	if n := totalChanges(t, a) - before; n != 0 {
		t.Fatalf("backoff wrote %d rows", n)
	}
	if fresh := savedRun(t, a, p); fresh.DeliveryWarning != r.DeliveryWarning || fresh.DeliveryWarning == "" {
		t.Fatal("warning disappeared in backoff")
	}
	if _, err := a.db.Exec(`UPDATE process_step_runs SET due_at='2030-01-01T00:00:00Z',next_attempt_at='' WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	p.Instructions = "Changed application context after a deployment"
	a = &App{ctx: a.ctx, db: a.db}
	f.a = a // sidecar restart
	if err := a.reconcileWorkflow(p, &r); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 2 || jsonText(f.events[1]) != original {
		t.Fatal("retry changed immutable content")
	}
	if len(f.threads) != 1 {
		t.Fatal("retry recreated the worker")
	}
	fresh := stepBy(t, a, r, "research")
	if fresh.DeliveredAt == "" || fresh.DeliveryWarning != "" || fresh.ExecutionID != "exec-durable" || fresh.Attempts != 2 {
		t.Fatalf("duplicate receipt not recovered: %+v", fresh)
	}
	if savedRun(t, a, p).DeliveryWarning != "" {
		t.Fatal("resolved run warning not cleared")
	}
}

func TestImmutableDirectDeliverySurvivesLostAck(t *testing.T) {
	a, f, p := reliabilitySetup(t, def())
	f.loseSend = true
	if _, err := a.start(p.ProjectID, p.ID, "direct", ""); err == nil {
		t.Fatal("expected lost acknowledgement")
	}
	r := savedRun(t, a, p)
	original := jsonText(f.events[0])
	if _, err := a.db.Exec(`UPDATE process_runs SET next_attempt_at='',inputs='changed retry context' WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	a = &App{ctx: a.ctx, db: a.db}
	f.a = a
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 2 || jsonText(f.events[1]) != original {
		t.Fatal("direct retry changed content")
	}
	if r = savedRun(t, a, p); r.DeliveredAt == "" || r.DeliveryWarning != "" {
		t.Fatalf("lost direct receipt not recovered: %+v", r)
	}
}

func TestPermanentConflictIsSuspendedWithoutWritesOrDuplicateExecution(t *testing.T) {
	d := workflowDefinition()
	d.Steps = d.Steps[:1]
	a, f, p := reliabilitySetup(t, d)
	f.sendError = errors.New("platform /events: http 409: source event id already exists with different content")
	if _, err := a.start(p.ProjectID, p.ID, "conflict", ""); err == nil {
		t.Fatal("expected conflict")
	}
	r := savedRun(t, a, p)
	s := stepBy(t, a, r, "research")
	if !s.DeliverySuspended || s.NextAttemptAt != "" || s.Attempts != 1 {
		t.Fatalf("permanent error not suspended: %+v", s)
	}
	before := totalChanges(t, a)
	var suspendedEvents int
	if err := a.db.QueryRow(`SELECT count(*) FROM process_event_outbox WHERE topic='delivery.state_changed' AND json_extract(payload_json,'$.to_state')='suspended'`).Scan(&suspendedEvents); err != nil || suspendedEvents != 2 {
		t.Fatalf("step/run suspension events missing: %d %v", suspendedEvents, err)
	}
	for i := 0; i < 100; i++ {
		if err := a.tickDirect(context.Background(), time.Now().Add(time.Duration(i+1)*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := a.reconcileWorkflow(p, &r); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.events) != 1 || totalChanges(t, a) != before {
		t.Fatal("permanent conflict retried/wrote during suspension")
	}
	if fresh := savedRun(t, a, p); fresh.State != "blocked" || fresh.DeliveryWarning == "" {
		t.Fatal("suspended warning disappeared")
	}
	if _, err := a.assignStep(p.ProjectID, "agent:7:main", p.ID, r.ID, s.ID, "replacement-worker"); err == nil {
		t.Fatal("unsafe reassignment allowed")
	}
	// Only an acknowledgement for the same executor AND target can resolve
	// an ambiguous conflict; another thread's event must not clear suspension.
	lifecycle := &sdk.AgentEventLifecycle{SourceEventID: s.DeliveryEventID, ExecutionID: "original-execution", ThreadID: "different-worker", Type: sdk.AgentEventActive, Sequence: 1}
	event := sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: s.Executor.AgentID}
	if err := a.stepLifecycle(event, lifecycle); err == nil {
		t.Fatal("mismatched target resolved suspension")
	}
	if !stepBy(t, a, r, "research").DeliverySuspended {
		t.Fatal("mismatched acknowledgement cleared suspension")
	}
	lifecycle.ThreadID = s.ThreadID
	if err := a.stepLifecycle(event, lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 1 || stepBy(t, a, r, "research").DeliverySuspended || savedRun(t, a, p).DeliveryWarning != "" {
		t.Fatal("confirmed original event not recovered safely")
	}
}

func TestDirectPermanentConflictDoesNotRetry(t *testing.T) {
	a, f, p := reliabilitySetup(t, def())
	f.sendError = errors.New("HTTP 409: source event id already exists with different content")
	if _, err := a.start(p.ProjectID, p.ID, "direct-conflict", ""); err == nil {
		t.Fatal("expected conflict")
	}
	r := savedRun(t, a, p)
	if !r.DeliverySuspended || r.NextAttemptAt != "" {
		t.Fatal("direct conflict not suspended")
	}
	before := totalChanges(t, a)
	for i := 0; i < 100; i++ {
		if err := a.tickDirect(context.Background(), time.Now().Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.events) != 1 || totalChanges(t, a) != before {
		t.Fatal("direct conflict retried or wrote")
	}
}

func TestUnavailableAgentDeliveryResumesWithoutReplacingWorker(t *testing.T) {
	a, f, p := reliabilitySetup(t, workflowDefinition()) // persistent sequential worker
	f.spawnError = errors.New("agent is not running")
	if _, err := a.start(p.ProjectID, p.ID, "offline", ""); err == nil {
		t.Fatal("expected unavailable agent")
	}
	r := savedRun(t, a, p)
	s := stepBy(t, a, r, "research")
	if s.DeliverySuspended || s.NextAttemptAt == "" || s.DeliveryWarning == "" {
		t.Fatalf("unavailable agent not retryable: %+v", s)
	}
	before := totalChanges(t, a)
	for i := 0; i < 10; i++ {
		if err := a.reconcileWorkflow(p, &r); err != nil {
			t.Fatal(err)
		}
	}
	if totalChanges(t, a) != before || len(f.threads) != 1 {
		t.Fatal("backoff retried provisioning or wrote")
	}
	f.spawnError = nil
	if _, err := a.db.Exec(`UPDATE process_step_runs SET next_attempt_at='' WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	a = &App{ctx: a.ctx, db: a.db}
	f.a = a
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(f.threads) != 2 || jsonText(f.threads[0]) != jsonText(f.threads[1]) || len(f.events) != 1 {
		t.Fatal("recovery replaced worker or changed spawn")
	}
	if fresh := stepBy(t, a, r, "research"); fresh.DeliveredAt == "" || fresh.DeliveryWarning != "" {
		t.Fatalf("recovery failed: %+v", fresh)
	}
}

func TestDeliveryMigrationSuspendsKnownLegacyConflicts(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	files, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() == "013_delivery_reliability.sql" {
			continue
		}
		raw, err := os.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO processes(id,project_id,current_version,created_at,updated_at) VALUES('p','project-a',1,'now','now'); INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,delivery_warning,delivery_attempts,next_attempt_at,backend) VALUES('r','p',1,'manual','legacy','now','HTTP 409: source event id already exists with different content',552,'later','agent'); INSERT INTO process_step_runs(id,run_id,step_key,position,definition_json,executor_json,updated_at,created_at,project_id,delivery_warning,delivery_attempts,next_attempt_at) VALUES('s','r','work',0,'{}','{}','now','now','project-a','HTTP 409: source event id already exists with different content',552,'later')`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/013_delivery_reliability.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"process_runs", "process_step_runs"} {
		var suspended bool
		var warning, next string
		var attempts int
		if err = db.QueryRow(`SELECT delivery_suspended,delivery_warning,next_attempt_at,delivery_attempts FROM `+table).Scan(&suspended, &warning, &next, &attempts); err != nil {
			t.Fatal(err)
		}
		if !suspended || next != "" || attempts != 552 || !strings.Contains(warning, "409") {
			t.Fatal("migration lost diagnostics or left conflict retrying")
		}
	}
}
