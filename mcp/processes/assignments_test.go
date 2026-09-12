package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func addAssignment(t *testing.T, a *App, p *Process, name string, owner int64, mode string, schedule *Schedule, values map[string]any) *Assignment {
	t.Helper()
	x, e := a.saveAssignment(p.ProjectID, p.ID, "", 0, AssignmentConfig{Name: name, Target: name, OwnerAgentID: owner, ExecutionMode: mode, ProcedureVersion: p.Version, FollowLatest: true, Schedule: schedule, Parameters: values})
	if e != nil {
		t.Fatal(e)
	}
	return x
}
func enableAssignment(t *testing.T, a *App, p *Process, x *Assignment) *Assignment {
	t.Helper()
	x, e := a.assignmentStatus(p.ProjectID, p.ID, x.ID, "active")
	if e != nil || x.SyncPending {
		t.Fatalf("enable %+v: %v", x, e)
	}
	return x
}
func startOn(t *testing.T, a *App, p *Process, x *Assignment, key string, overrides map[string]any) Run {
	t.Helper()
	raw, e := a.startAssignment(p.ProjectID, p.ID, x.ID, key, "", overrides)
	if e != nil {
		t.Fatal(e)
	}
	return raw.(map[string]any)["run"].(Run)
}
func TestAssignmentsDifferentAgentsSameProcedure(t *testing.T) {
	a, f, p := directSetup(t)
	p = status(t, a, p.ID, "paused")
	d := p.Definition
	d.Parameters = []Parameter{{Key: "page", Type: "string", Required: true}, {Key: "language", Type: "string", Default: "en"}, {Key: "paid", Type: "boolean", Default: false}}
	p, e := a.save(p.ProjectID, p.ID, "operator", p.Version, d)
	if e != nil {
		t.Fatal(e)
	}
	// Retire the empty convenience assignment, then configure the actual targets.
	if _, e = a.assignmentStatus(p.ProjectID, p.ID, "assignment-"+p.ID, "archived"); e != nil {
		t.Fatal(e)
	}
	x := addAssignment(t, a, p, "Photo", 7, "agent", nil, map[string]any{"page": "photo"})
	y := addAssignment(t, a, p, "Cooking", 8, "agent", nil, map[string]any{"page": "cooking"})
	p = status(t, a, p.ID, "active")
	x = enableAssignment(t, a, p, x)
	y = enableAssignment(t, a, p, y)
	r1 := startOn(t, a, p, x, "today", nil)
	r2 := startOn(t, a, p, y, "today", map[string]any{"language": "fr"})
	if r1.ID == r2.ID || r1.Version != r2.Version || len(f.events) != 2 || f.events[0].AgentID != 7 || f.events[1].AgentID != 8 {
		t.Fatal("assignments not independent")
	}
	if r1.Binding.Parameters["page"] != "photo" || r2.Binding.Parameters["language"] != "fr" || r1.Binding.Parameters["paid"] != false {
		t.Fatal("resolved inputs missing")
	}
	if !strings.Contains(f.events[1].Message.(string), `"page":"cooking"`) {
		t.Fatal("snapshot missing parameters")
	}
	if _, e = a.execute(p.ProjectID, "agent:8:thread", "start", map[string]any{"process_id": p.ID, "idempotency_key": "ambiguous"}); e == nil {
		t.Fatal("ambiguous default selected")
	}
	if _, e = a.directRun(p.ProjectID, "agent:7:thread", p.ID, r2.ID, "run_update", map[string]any{"state": "completed", "result": "done"}); e == nil {
		t.Fatal("procedure's original owner updated another assignment")
	}
	if _, e = a.directRun(p.ProjectID, "agent:8:thread", p.ID, r2.ID, "run_update", map[string]any{"state": "completed", "result": "Published URL"}); e != nil {
		t.Fatal(e)
	}
	a.assignmentStatus(p.ProjectID, p.ID, x.ID, "paused")
	before := len(f.events)
	again := startOn(t, a, p, x, "today", nil)
	if again.ID != r1.ID || len(f.events) != before {
		t.Fatal("retry after pause duplicated")
	}
	startOn(t, a, p, y, "tomorrow", nil)
	if _, e = a.startAssignment(p.ProjectID, p.ID, x.ID, "new", "", nil); e == nil {
		t.Fatal("paused assignment started")
	}
	if len(f.calls) != 0 {
		t.Fatal("direct used Tasks")
	}
}
func TestAssignmentSnapshotSurvivesEditsAndRetry(t *testing.T) {
	a, f, p := directSetup(t)
	x := addAssignment(t, a, p, "Page A", 8, "agent", nil, nil)
	x = enableAssignment(t, a, p, x)
	f.lose = true
	if _, e := a.startAssignment(p.ProjectID, p.ID, x.ID, "one", "", nil); e == nil {
		t.Fatal("expected lost response")
	}
	x, e := a.assignmentStatus(p.ProjectID, p.ID, x.ID, "paused")
	if e != nil {
		t.Fatal(e)
	}
	c := x.AssignmentConfig
	c.OwnerAgentID = 7
	c.Target = "Page B"
	if _, e = a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); e != nil {
		t.Fatal(e)
	}
	runs, _ := a.dispatches(p.ID)
	r := runs[0]
	a.db.Exec(`UPDATE process_runs SET next_attempt_at='' WHERE id=?`, r.ID)
	if e = a.tickDirect(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	if len(f.events) != 2 || f.events[1].AgentID != 8 || f.events[0].SourceEventID != f.events[1].SourceEventID || !strings.Contains(f.events[1].Message.(string), "Page A") {
		t.Fatal("retry picked up new config")
	}
	if _, e = a.directRun(p.ProjectID, "agent:8:thread", p.ID, r.ID, "run_update", map[string]any{"state": "completed", "result": "Page A posted"}); e != nil {
		t.Fatal(e)
	}
}
func TestSchedulesOverlapPerAssignmentAndPauseAll(t *testing.T) {
	a, f, p := directSetup(t)
	s := &Schedule{Kind: "interval", Every: "1m"}
	x := enableAssignment(t, a, p, addAssignment(t, a, p, "A", 7, "agent", s, nil))
	y := enableAssignment(t, a, p, addAssignment(t, a, p, "B", 8, "agent", s, nil))
	now := time.Now().Add(2 * time.Minute)
	if e := a.tickDirect(context.Background(), now); e != nil {
		t.Fatal(e)
	}
	if len(f.events) != 2 {
		t.Fatal("one assignment blocked another")
	}
	a.tickDirect(context.Background(), now.Add(2*time.Minute))
	if len(f.events) != 2 {
		t.Fatal("overlapping runs")
	}
	a.assignmentStatus(p.ProjectID, p.ID, x.ID, "paused")
	rows, _ := a.dispatches(p.ID)
	for _, r := range rows {
		if r.AssignmentID == y.ID {
			a.directRun(p.ProjectID, "agent:8:t", p.ID, r.ID, "run_update", map[string]any{"state": "completed", "result": "done"})
		}
	}
	a.tickDirect(context.Background(), now.Add(4*time.Minute))
	if len(f.events) != 3 {
		t.Fatal("other assignment stopped")
	}
	status(t, a, p.ID, "paused")
	a.tickDirect(context.Background(), now.Add(24*time.Hour))
	if len(f.events) != 3 {
		t.Fatal("parent pause ignored")
	}
}
func TestTasksSchedulesAreIsolatedAndPinned(t *testing.T) {
	a, f := setup(t)
	p := create(t, a, def())
	p = status(t, a, p.ID, "active")
	s := &Schedule{Kind: "interval", Every: "1m"}
	x := enableAssignment(t, a, p, addAssignment(t, a, p, "A", 7, "tasks", s, nil))
	y := enableAssignment(t, a, p, addAssignment(t, a, p, "B", 8, "tasks", s, nil))
	if f.creates != 2 {
		t.Fatal("schedules missing")
	}
	a.assignmentStatus(p.ProjectID, p.ID, x.ID, "paused")
	if f.tasks["task-1"]["schedule_enabled"] != false || f.tasks["task-2"]["schedule_enabled"] != true {
		t.Fatal("pause crossed assignment boundary")
	}
	y, e := a.assignmentStatus(p.ProjectID, p.ID, y.ID, "paused")
	if e != nil {
		t.Fatal(e)
	}
	c := y.AssignmentConfig
	c.FollowLatest = false
	y, e = a.saveAssignment(p.ProjectID, p.ID, y.ID, y.Revision, c)
	if e != nil {
		t.Fatal(e)
	}
	p = status(t, a, p.ID, "paused")
	d := p.Definition
	d.Instructions = "New shared procedure"
	p, e = a.save(p.ProjectID, p.ID, "operator", p.Version, d)
	if e != nil {
		t.Fatal(e)
	}
	xs, _ := a.assignments(p.ID)
	for _, item := range xs {
		if item.ID == x.ID && item.ProcedureVersion != 2 {
			t.Fatal("following assignment did not advance")
		}
		if item.ID == y.ID && item.ProcedureVersion != 1 {
			t.Fatal("pinned assignment advanced")
		}
	}
}
func TestParameterValidationAndAssignmentScope(t *testing.T) {
	a, _, p := directSetup(t)
	d := p.Definition
	d.Parameters = []Parameter{{Key: "page", Type: "string", Required: true}, {Key: "count", Type: "number"}, {Key: "tier", Type: "string", Options: []string{"free", "paid"}}}
	p = status(t, a, p.ID, "paused")
	p, e := a.save(p.ProjectID, p.ID, "operator", p.Version, d)
	if e != nil {
		t.Fatal(e)
	}
	c := AssignmentConfig{Name: "A", OwnerAgentID: 8, ExecutionMode: "agent", ProcedureVersion: p.Version, Parameters: map[string]any{"count": "wrong"}}
	if _, e = a.saveAssignment(p.ProjectID, p.ID, "", 0, c); e == nil {
		t.Fatal("wrong type accepted")
	}
	c.Parameters = map[string]any{"tier": "other"}
	if _, e = a.saveAssignment(p.ProjectID, p.ID, "", 0, c); e == nil {
		t.Fatal("enum accepted")
	}
	c.Parameters = map[string]any{"unknown": true}
	if _, e = a.saveAssignment(p.ProjectID, p.ID, "", 0, c); e == nil {
		t.Fatal("unknown accepted")
	}
	c.Parameters = nil
	c.OwnerAgentID = 9
	if _, e = a.saveAssignment(p.ProjectID, p.ID, "", 0, c); e == nil {
		t.Fatal("cross-project owner accepted")
	}
	x := p.Assignments[0]
	if _, e = a.assignment("other", p.ID, x.ID); e == nil {
		t.Fatal("cross-project read")
	}
	if _, e = a.changeStatus(p.ProjectID, p.ID, "active"); e == nil {
		t.Fatal("missing required input activation")
	}
	c = x.AssignmentConfig
	c.Parameters = map[string]any{"page": "photo"}
	if _, e = a.saveAssignment(p.ProjectID, p.ID, x.ID, 999, c); e == nil {
		t.Fatal("stale revision accepted")
	}
}
func TestV02MigrationPreservesSchedulesAndRuns(t *testing.T) {
	a, _ := setup(t)
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, file := range []string{"001_init.sql", "002_direct_execution.sql"} {
		raw, e := os.ReadFile("migrations/" + file)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	d := def()
	d.Schedule = &Schedule{Kind: "interval", Every: "24h"}
	deadline := "2026-10-01T09:00:00Z"
	db.Exec(`INSERT INTO processes(id,project_id,status,created_at,updated_at,next_run_at,scheduled_version) VALUES('p','project-a','active','now','now',?,1)`, deadline)
	db.Exec(`INSERT INTO process_versions(process_id,version,body_json,created_by,created_at) VALUES('p',1,?,'operator','now')`, jsonText(d))
	db.Exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,task_id,created_at) VALUES('r','p',1,'schedule','schedule:v1','task-existing','now')`)
	raw, e := os.ReadFile("migrations/003_assignments.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(raw)); e != nil {
		t.Fatal(e)
	}
	migrated := &App{ctx: a.ctx, db: db}
	p, e := migrated.get("project-a", "p")
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Assignments) != 1 || p.Assignments[0].NextRunAt != deadline || p.Assignments[0].ExecutionMode != "tasks" || !p.Assignments[0].FollowLatest {
		t.Fatal("migration changed assignment", p)
	}
	r, e := migrated.getRun("project-a", "p", "r")
	if e != nil {
		t.Fatal(e)
	}
	if r.TaskID != "task-existing" || r.Binding.OwnerAgentID != 7 || r.AssignmentID != p.Assignments[0].ID || r.RequestKey != "schedule:v1" {
		t.Fatal("migration changed run", r)
	}
	var original Definition
	json.Unmarshal([]byte(jsonText(d)), &original)
	if r.Binding.Schedule.Every != original.Schedule.Every {
		t.Fatal("schedule snapshot missing")
	}
}
