package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func taskConfig() TaskConfig {
	return TaskConfig{Title: "Investigate payment", Instructions: "Check the supplied payment and report evidence.", Executor: Executor{Kind: "agent", AgentID: 7}}
}
func newNative(t *testing.T, a *App, actor, key string, c TaskConfig) Task {
	t.Helper()
	s, e := a.createTask("project-a", actor, key, c)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func nativeUpdate(t *testing.T, a *App, s Task, actor string, args map[string]any) Task {
	t.Helper()
	fresh, e := a.task(s.ProjectID, s.ID)
	if e != nil {
		t.Fatal(e)
	}
	args["expected_revision"] = float64(fresh.Revision)
	result, e := a.changeTask(s.ProjectID, actor, s.ID, "task_update", args)
	if e != nil {
		t.Fatal(e)
	}
	return result["task"].(Task)
}
func TestStandaloneTaskReusesDeliveryAndOutcomeWithoutProcedure(t *testing.T) {
	a, f := nativeSetup(t)
	s := newNative(t, a, "operator", "one", taskConfig())
	if s.RunID != "" || s.Origin != "standalone" || s.State != "ready" || len(f.events) != 1 {
		t.Fatalf("task %+v events %d", s, len(f.events))
	}
	if !strings.Contains(f.events[0].Message.(string), "processes_task_get") {
		t.Fatal("wrong agent contract")
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM processes`).Scan(&n)
	if n != 0 {
		t.Fatal("created hidden procedure")
	}
	a.db.QueryRow(`SELECT count(*) FROM process_runs`).Scan(&n)
	if n != 0 {
		t.Fatal("created hidden run")
	}
	again := newNative(t, a, "operator", "one", taskConfig())
	if again.ID != s.ID || len(f.events) != 1 {
		t.Fatal("duplicate dispatch")
	}
	bad := taskConfig()
	bad.Title = "different"
	if _, e := a.createTask(s.ProjectID, "operator", "one", bad); e == nil {
		t.Fatal("key reuse changed input")
	}
	if _, e := a.task("other", s.ID); !errors.Is(e, errNotFound) {
		t.Fatal("scope leak", e)
	}
	if _, e := a.changeTask(s.ProjectID, "agent:8:t", s.ID, "task_update", map[string]any{"expected_revision": float64(s.Revision), "state": "completed", "output": "wrong agent"}); e == nil {
		t.Fatal("wrong executor completed")
	}
	s = nativeUpdate(t, a, s, "agent:7:t", map[string]any{"state": "completed", "output": "Payment was settled; receipt ref 123."})
	if s.State != "completed" || s.Progress != 100 {
		t.Fatal(s)
	}
	if e := a.tickDirect(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	if len(f.events) != 1 {
		t.Fatal("terminal task redispatched")
	}
}
func TestTaskOfflineRetryAndReassignmentGuard(t *testing.T) {
	a, f := nativeSetup(t)
	f.lose = true
	s := newNative(t, a, "operator", "retry", taskConfig())
	if s.DeliveredAt != "" || s.Attempts != 1 || s.DeliveryWarning == "" {
		t.Fatal(s)
	}
	_, e := a.changeTask(s.ProjectID, "operator", s.ID, "task_update", map[string]any{"expected_revision": float64(s.Revision), "executor": map[string]any{"kind": "agent", "agent_id": 8.0}})
	if e == nil {
		t.Fatal("reassigned after possibly accepted delivery")
	}
	f.lose = false
	a.db.Exec(`UPDATE process_step_runs SET next_attempt_at='' WHERE id=?`, s.ID)
	if e = a.tickDirect(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	fresh, _ := a.task(s.ProjectID, s.ID)
	if fresh.DeliveredAt == "" {
		t.Fatal("retry lost task")
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM process_step_runs`).Scan(&n)
	if n != 1 {
		t.Fatal("retry created more work")
	}
}
func TestNativeTaskSettingsRevisionHistoryAndFilters(t *testing.T) {
	a, _ := nativeSetup(t)
	c := taskConfig()
	c.Executor = Executor{Kind: "human"}
	c.DueAt = "2020-01-01T00:00:00Z"
	s := newNative(t, a, "operator", "human", c)
	old := s.Revision
	s = nativeUpdate(t, a, s, "operator", map[string]any{"title": "Review payment evidence", "due_at": "2021-01-01T00:00:00Z"})
	if _, e := a.changeTask(s.ProjectID, "operator", s.ID, "task_update", map[string]any{"expected_revision": float64(old), "due_at": ""}); !errors.Is(e, errConflict) {
		t.Fatal("stale revision accepted", e)
	}
	r, e := a.listNativeTasks(s.ProjectID, "operator", map[string]any{"assignee": "human", "overdue": true})
	if e != nil || len(r.(map[string]any)["tasks"].([]Task)) != 1 {
		t.Fatal(r, e)
	}
	r, e = a.listNativeTasks(s.ProjectID, "agent:7:t", nil)
	if e != nil || r.(map[string]any)["total"] != 0 {
		t.Fatal("mine filter", r, e)
	}
	s = nativeUpdate(t, a, s, "operator", map[string]any{"executor": map[string]any{"kind": "agent", "agent_id": 8.0}})
	if s.Executor.AgentID != 8 || s.DeliveredAt == "" {
		t.Fatal("reassign did not dispatch", s)
	}
	details, e := a.taskDetails(s.ProjectID, "operator", s.ID)
	if e != nil || len(details["history"].([]map[string]any)) < 3 {
		t.Fatal("audit missing", e)
	}
	if details["can_reassign"] != false {
		t.Fatal("dispatched work editable")
	}
}
func TestAttachedRequiredGateOptionalSurvivalAndFrozenProcedure(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	c := taskConfig()
	c.RunID = r.ID
	c.Executor = Executor{Kind: "human"}
	c.Required = true
	c.DependsOn = []string{"research"}
	required := newNative(t, a, "operator", "required", c)
	if required.State != "pending" {
		t.Fatal("dependency ignored")
	}
	if _, e := a.changeTask(p.ProjectID, "operator", required.ID, "task_update", map[string]any{"expected_revision": float64(required.Revision), "state": "completed", "output": "too early"}); e == nil {
		t.Fatal("dependency bypass")
	}
	c.Required = false
	c.DependsOn = nil
	optional := newNative(t, a, "operator", "optional", c)
	if _, e := a.createTask(p.ProjectID, "agent:8:t", "unapproved", c); e == nil {
		t.Fatal("noncoordinator added run work")
	}
	finishStep(t, a, p, r, "research", "agent:8:t", "Research", "")
	finishStep(t, a, p, r, "write", "agent:7:t", "Draft", "")
	finishStep(t, a, p, r, "review", "operator", "Approved", "approved")
	finishStep(t, a, p, r, "publish", "agent:8:t", "Receipt", "")
	fresh, _ := a.getRun(p.ProjectID, p.ID, r.ID)
	if terminal(fresh.State) {
		t.Fatal("required attachment did not gate completion", fresh)
	}
	required = nativeUpdate(t, a, required, "operator", map[string]any{"state": "completed", "output": "Additional verification done"})
	fresh, _ = a.getRun(p.ProjectID, p.ID, r.ID)
	if fresh.State != "completed" {
		t.Fatal("gate did not release", fresh)
	}
	optional = nativeUpdate(t, a, optional, "operator", map[string]any{"state": "completed", "output": "Follow-up done after run completion"})
	if optional.State != "completed" {
		t.Fatal(optional)
	}
	d, e := a.definition(p.ID, p.Version)
	if e != nil || len(d.Steps) != 4 {
		t.Fatal("modified reusable procedure", e)
	}
	// Creation retry remains idempotent even after the parent run completes.
	c.Required = true
	c.DependsOn = []string{"research"}
	again := newNative(t, a, "operator", "required", c)
	if again.ID != required.ID {
		t.Fatal("creation retry duplicated")
	}
}
func TestTaskCanAttachToUnstructuredNativeRun(t *testing.T) {
	a, _ := nativeSetup(t)
	d := def()
	d.ExecutionMode = "agent"
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	raw, e := a.start(p.ProjectID, p.ID, "one", "")
	if e != nil {
		t.Fatal(e)
	}
	r := raw.(map[string]any)["run"].(Run)
	c := taskConfig()
	c.RunID = r.ID
	c.Required = true
	c.Executor = Executor{Kind: "human"}
	s := newNative(t, a, "operator", "gate", c)
	if _, e = a.directRun(p.ProjectID, "agent:7:t", p.ID, r.ID, "run_update", map[string]any{"state": "completed", "result": "done"}); e == nil {
		t.Fatal("direct completion bypassed attached task")
	}
	nativeUpdate(t, a, s, "operator", map[string]any{"state": "completed", "output": "verified"})
	if _, e = a.directRun(p.ProjectID, "agent:7:t", p.ID, r.ID, "run_update", map[string]any{"state": "completed", "result": "done"}); e != nil {
		t.Fatal(e)
	}
}
func TestTaskHTTPIdentityAndCancellation(t *testing.T) {
	a, _ := nativeSetup(t)
	c := taskConfig()
	c.Executor = Executor{Kind: "human"}
	s := newNative(t, a, "operator", "cancel", c)
	body := fmtTaskJSON(map[string]any{"task_id": "different", "project_id": "other", "expected_revision": s.Revision, "reason": "No longer needed"})
	req := httptest.NewRequest("POST", "/processes/tasks/"+s.ID+"/cancel?project_id=project-a", strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.handleHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	fresh, _ := a.task(s.ProjectID, s.ID)
	if fresh.State != "cancelled" {
		t.Fatal("path identity overridden")
	}
	req = httptest.NewRequest("GET", "/processes/tasks/"+s.ID+"?project_id=other", nil)
	rec = httptest.NewRecorder()
	a.handleHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal("cross project", rec.Code)
	}
}
func fmtTaskJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func nativeSetup(t *testing.T) (*App, *directPlatform) {
	t.Helper()
	f := &directPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	a := &App{}
	if e := a.OnMount(ctx); e != nil {
		t.Fatal(e)
	}
	return a, f
}

func TestNativeTaskMigrationPreservesLegacyWorkAndForeignKeys(t *testing.T) {
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(`PRAGMA foreign_keys=ON`); e != nil {
		t.Fatal(e)
	}
	for _, file := range []string{"001_init.sql", "002_direct_execution.sql", "003_assignments.sql", "004_workflow_steps.sql", "005_event_triggers.sql"} {
		raw, e := os.ReadFile("migrations/" + file)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			t.Fatal(file, e)
		}
	}
	for _, q := range []string{
		`INSERT INTO processes(id,project_id,status,created_at,updated_at) VALUES('p','project-a','active','now','now')`,
		`INSERT INTO process_versions(process_id,version,body_json,created_by,created_at) VALUES('p',1,'{}','operator','now')`,
		`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at) VALUES('r','p',1,'manual','one','now')`,
		`INSERT INTO process_step_runs(id,run_id,step_key,position,definition_json,executor_json,state,output,updated_at,execution_id,delivered_at) VALUES('old-step','r','write',0,'{"name":"Write","kind":"work"}','{"kind":"agent","agent_id":7}','completed','Preserved result','now','exec-old','then')`,
		`INSERT INTO process_step_events(step_id,actor,state,output,created_at) VALUES('old-step','agent:7:t','completed','Preserved result','now')`,
	} {
		if _, e = db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	raw, e := os.ReadFile("migrations/006_native_tasks.sql")
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(string(raw)); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	a := &App{db: db}
	s, e := a.task("project-a", "old-step")
	if e != nil || s.ID != "old-step" || s.Output != "Preserved result" || s.ExecutionID != "exec-old" || !s.Required || s.Origin != "process_step" {
		t.Fatal(s, e)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM process_step_events WHERE step_id='old-step'`).Scan(&n)
	if n != 1 {
		t.Fatal("lost audit history")
	}
	if _, e = db.Exec(`INSERT INTO process_step_events(step_id,actor,state,created_at) VALUES('missing','operator','running','now')`); e == nil {
		t.Fatal("history foreign key was lost")
	}
	rows, e := db.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke a foreign key")
	}
}
func TestNativeTaskCreationRollbackAndConcurrentRetries(t *testing.T) {
	a, f := nativeSetup(t)
	if _, e := a.db.Exec(`CREATE TRIGGER reject_task_history BEFORE INSERT ON process_step_events BEGIN SELECT RAISE(ABORT,'test failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := a.createTask("project-a", "operator", "rollback", taskConfig()); e == nil {
		t.Fatal("audit failure did not roll back creation")
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM process_step_runs`).Scan(&n)
	if n != 0 || len(f.events) != 0 {
		t.Fatal("partial creation or premature dispatch")
	}
	a.db.Exec(`DROP TRIGGER reject_task_history`)
	args := map[string]any{"title": "One task", "instructions": "Do once", "executor": map[string]any{"kind": "agent", "agent_id": 7.0}, "idempotency_key": "same"}
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, e := a.execute("project-a", "agent:7:t", "task_create", args); done <- e }()
	}
	for i := 0; i < 8; i++ {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	a.db.QueryRow(`SELECT count(*) FROM process_step_runs`).Scan(&n)
	if n != 1 || len(f.events) != 1 {
		t.Fatal("duplicate work", n, len(f.events))
	}
}
func TestStandaloneTaskLifecycleIsScopedAndDoesNotCompleteWork(t *testing.T) {
	a, _ := nativeSetup(t)
	s := newNative(t, a, "operator", "lifecycle", taskConfig())
	event := sdk.Event{DeliveryID: "lifecycle-delivery", Event: sdk.AgentEventLifecycleEvent, SourceApp: "apteva-server", InstanceID: 7, ProjectID: s.ProjectID, Data: map[string]any{"source_event_id": "process-step:" + s.ID, "type": sdk.AgentEventSettled, "sequence": 2, "execution_id": "exec-finished"}}
	handler := a.EventHandlers()[0].Handler
	if e := handler(a.ctx, event); e != nil {
		t.Fatal(e)
	}
	fresh, _ := a.task(s.ProjectID, s.ID)
	if terminal(fresh.State) || fresh.ExecutionState != sdk.AgentEventSettled {
		t.Fatal("lifecycle was treated as evidence", fresh)
	}
	event.ProjectID = "other"
	if e := handler(a.ctx, event); e == nil {
		t.Fatal("cross-project lifecycle accepted")
	}
}
func TestTaskOptionalFailureDoesNotFailRun(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	c := taskConfig()
	c.RunID = r.ID
	c.Executor = Executor{Kind: "human"}
	s := newNative(t, a, "operator", "optional-failure", c)
	nativeUpdate(t, a, s, "operator", map[string]any{"state": "failed", "error": "Optional research unavailable"})
	fresh, _ := a.getRun(p.ProjectID, p.ID, r.ID)
	if terminal(fresh.State) {
		t.Fatal("optional task failed the run")
	}
	pending := stepBy(t, a, r, "write")
	_, e := a.changeTask(p.ProjectID, "operator", pending.ID, "task_cancel", map[string]any{"expected_revision": float64(pending.Revision), "reason": "Stop required work"})
	if e != nil {
		t.Fatal(e)
	}
	fresh, _ = a.getRun(p.ProjectID, p.ID, r.ID)
	if fresh.State != "failed" {
		t.Fatal("required cancellation did not stop run")
	}
}
