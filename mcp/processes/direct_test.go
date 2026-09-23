package main

import (
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"strings"
	"testing"
	"time"
)

type directPlatform struct {
	fakeTasks
	events []sdk.AgentEventRequest
	lose   bool
}

func (f *directPlatform) SpawnThread(req sdk.ThreadSpawnRequest) (*sdk.ThreadSpawnResult, error) {
	f.threads = append(f.threads, req)
	for _, event := range req.Events {
		f.events = append(f.events, sdk.AgentEventRequest{AgentID: req.AgentID, ThreadID: req.ThreadID, SourceEventID: event.ID, Message: event.Message})
	}
	if f.lose {
		f.lose = false
		return nil, errors.New("response lost")
	}
	accepted := make([]string, 0, len(req.Events))
	for _, event := range req.Events {
		accepted = append(accepted, event.ID)
	}
	return &sdk.ThreadSpawnResult{Status: "created", Thread: sdk.ThreadRef{AgentID: req.AgentID, ThreadID: req.ThreadID}, Events: sdk.ThreadEventReceipt{Accepted: accepted}}, nil
}

func (f *directPlatform) SendTrackedAgentEvent(r sdk.AgentEventRequest) (*sdk.AgentEventReceipt, error) {
	f.events = append(f.events, r)
	if f.lose {
		f.lose = false
		return nil, errors.New("response lost")
	}
	return &sdk.AgentEventReceipt{Accepted: true, ExecutionID: "exec-1", ThreadID: r.ThreadID}, nil
}
func directSetup(t *testing.T) (*App, *directPlatform, *Process) {
	f := &directPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	a := &App{}
	if e := a.OnMount(ctx); e != nil {
		t.Fatal(e)
	}
	d := def()
	d.ExecutionMode = ""
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	if p.ExecutionMode != "agent" || p.SyncPending {
		t.Fatalf("%+v", p)
	}
	return a, f, p
}
func TestDirectNoTasksAndOutcome(t *testing.T) {
	a, f, p := directSetup(t)
	raw, e := a.start("project-a", p.ID, "one", "input")
	if e != nil {
		t.Fatal(e)
	}
	r := raw.(map[string]any)["run"].(Run)
	if len(f.calls) != 0 || len(f.events) != 1 || !strings.Contains(f.events[0].Message.(string), "run_update") {
		t.Fatal("incorrect delivery")
	}
	a.start("project-a", p.ID, "one", "input")
	if len(f.events) != 1 {
		t.Fatal("duplicate delivery")
	}
	args := map[string]any{"state": "completed", "result": "Approved report https://example.com/report"}
	if _, e = a.directRun("project-a", "agent:8:x", p.ID, r.ID, "run_update", args); e == nil {
		t.Fatal("nonowner allowed")
	}
	if _, e = a.directRun("other", "agent:7:x", p.ID, r.ID, "run_get", nil); e == nil {
		t.Fatal("cross project allowed")
	}
	if _, e = a.directRun("project-a", "agent:7:x", p.ID, r.ID, "run_update", map[string]any{"state": "completed"}); e == nil {
		t.Fatal("no evidence allowed")
	}
	if _, e = a.directRun("project-a", "agent:7:x", p.ID, r.ID, "run_update", args); e != nil {
		t.Fatal(e)
	}
	if _, e = a.directRun("project-a", "agent:7:x", p.ID, r.ID, "run_update", args); e != nil {
		t.Fatal("idempotent completion", e)
	}
	if _, e = a.directRun("project-a", "agent:7:x", p.ID, r.ID, "run_update", map[string]any{"state": "running"}); e == nil {
		t.Fatal("terminal reverted")
	}
	history, e := a.runs("project-a", p.ID)
	if e != nil || len(history.(map[string]any)["direct_runs"].([]Run)) != 1 || len(f.calls) != 0 {
		t.Fatal("history depends on tasks", e)
	}
}

func TestProjectRunsAreScopedAndIncludeProcessIdentity(t *testing.T) {
	a, _, p := directSetup(t)
	if _, err := a.start("project-a", p.ID, "project-history", "input"); err != nil {
		t.Fatal(err)
	}
	other, err := a.save("other", "", "operator", 0, def())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,backend,state,assignment_json,overrides_json) VALUES('hidden-run',?,1,'manual','hidden','2026-09-23T09:00:00Z','agent','completed','{}','{}')`, other.ID); err != nil {
		t.Fatal(err)
	}
	history, err := a.projectRuns("project-a")
	if err != nil {
		t.Fatal(err)
	}
	runs := history.(map[string]any)["runs"].([]projectRun)
	if len(runs) != 1 || runs[0].ProcessID != p.ID || runs[0].ProcessName != p.Name {
		t.Fatalf("unexpected project history: %+v", runs)
	}
	response := overviewGET(t, a, "/processes/runs?project_id=project-a", "project-a")
	if response.Code != 200 {
		t.Fatalf("project runs route: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Runs []projectRun `json:"runs"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Runs) != 1 || payload.Runs[0].ProcessName != p.Name {
		t.Fatalf("project runs response: %+v err=%v", payload, err)
	}
}
func TestDirectRetryPinnedAndLifecycle(t *testing.T) {
	a, f, p := directSetup(t)
	f.lose = true
	if _, e := a.start("project-a", p.ID, "one", "input"); e == nil {
		t.Fatal("expected lost response")
	}
	runs, _ := a.dispatches(p.ID)
	r := runs[0]
	a.db.Exec(`UPDATE process_runs SET next_attempt_at='' WHERE id=?`, r.ID)
	restarted := &App{ctx: a.ctx, db: a.db}
	if e := restarted.tickDirect(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	if len(f.events) != 2 || f.events[0].SourceEventID != f.events[1].SourceEventID || f.events[0].ThreadID != f.events[1].ThreadID {
		t.Fatal("retry identity changed")
	}
	ev := sdk.Event{DeliveryID: "transition", Event: sdk.AgentEventLifecycleEvent, SourceApp: "apteva-server", InstanceID: 7, ProjectID: "project-a", Data: map[string]any{"type": sdk.AgentEventSettled, "source_event_id": "processes:" + r.ID, "execution_id": "exec-1", "sequence": 2}}
	handler := a.EventHandlers()[0].Handler
	if e := handler(a.ctx, ev); e != nil {
		t.Fatal(e)
	}
	ev.Data["sequence"] = 1
	ev.Data["type"] = sdk.AgentEventActive
	handler(a.ctx, ev)
	r, _ = a.getRun("project-a", p.ID, r.ID)
	if terminal(r.State) || r.ExecutionState != sdk.AgentEventSettled {
		t.Fatal("settlement completed or reordered run", r)
	}
}
func TestDirectScheduleAndModeSwitch(t *testing.T) {
	a, f, p := directSetup(t)
	p = status(t, a, p.ID, "paused")
	x := p.Assignments[0]
	c := x.AssignmentConfig
	c.Schedule = &Schedule{Kind: "interval", Every: "1m"}
	_, e := a.saveAssignment("project-a", p.ID, x.ID, x.Revision, c)
	if e != nil {
		t.Fatal(e)
	}
	p = status(t, a, p.ID, "active")
	now := time.Now().Add(2 * time.Minute)
	if e = a.tickDirect(context.Background(), now); e != nil {
		t.Fatal(e)
	}
	if e = a.tickDirect(context.Background(), now.Add(2*time.Minute)); e != nil {
		t.Fatal(e)
	}
	runs, _ := a.dispatches(p.ID)
	if len(runs) != 1 || runs[0].ScheduledFor == "" || len(f.calls) != 0 {
		t.Fatal("overlap or tasks call")
	}
	p = status(t, a, p.ID, "paused")
	if p.NextRunAt != "" {
		t.Fatal("deadline not cleared")
	}
	a.tickDirect(context.Background(), now.Add(time.Hour))
	runs, _ = a.dispatches(p.ID)
	if len(runs) != 1 {
		t.Fatal("paused schedule ran")
	}
}
func TestTasksToDirectAfterConfirmedPause(t *testing.T) {
	t.Skip("legacy Tasks backend removed in Processes 0.14")
	a, f := setup(t)
	d := def()
	d.Schedule = &Schedule{Kind: "interval", Every: "1m"}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	p = status(t, a, p.ID, "paused")
	x := p.Assignments[0]
	c := x.AssignmentConfig
	c.ExecutionMode = "agent"
	_, e := a.saveAssignment("project-a", p.ID, x.ID, x.Revision, c)
	if e != nil {
		t.Fatal(e)
	}
	f.fail = "pause"
	before := len(f.calls)
	p = status(t, a, p.ID, "active")
	if p.SyncPending || len(f.calls) != before {
		t.Fatal("confirmed old pause requires Tasks")
	}
	f.fail = "list"
	history, e := a.runs("project-a", p.ID)
	if e != nil || history.(map[string]any)["tasks_error"] == nil {
		t.Fatal("missing history warning")
	}
}
func TestLegacyDefinitionKeepsTasks(t *testing.T) {
	t.Skip("legacy Tasks definition mode removed in Processes 0.14")
	a, _ := setup(t)
	p := create(t, a, def())
	_, e := a.db.Exec(`UPDATE process_versions SET body_json=json_remove(body_json,'$.execution_mode') WHERE process_id=?`, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	p, e = a.get("project-a", p.ID)
	if e != nil || p.ExecutionMode != "tasks" {
		t.Fatal("legacy mode changed", e)
	}
	d, _ := a.definition(p.ID, 1)
	versions, _ := a.versions(p.ID)
	if d.ExecutionMode != "tasks" || versions[0].Definition.ExecutionMode != "tasks" {
		t.Fatal("legacy version changed")
	}
}

func TestUncertainResumeInvalidatesPauseConfirmation(t *testing.T) {
	t.Skip("legacy Tasks schedule resume removed in Processes 0.14")
	a, f := setup(t)
	d := def()
	d.Schedule = &Schedule{Kind: "interval", Every: "1m"}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	p = status(t, a, p.ID, "paused")
	f.fail = "resume"
	p = status(t, a, p.ID, "active")
	if !p.SyncPending {
		t.Fatal("resume should be uncertain")
	}
	f.fail = "pause"
	p = status(t, a, p.ID, "paused")
	if !p.SyncPending {
		t.Fatal("stale pause confirmation reused after uncertain resume")
	}
	d.ExecutionMode = "agent"
	if _, e := a.save("project-a", p.ID, "operator", p.Version, d); e == nil {
		t.Fatal("mode switched before pause confirmed")
	}
}
