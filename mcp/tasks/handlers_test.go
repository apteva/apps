package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type threadEventCall struct {
	Target  sdk.ThreadRef
	Message any
}

type recordingPlatform struct {
	tk.BasePlatformClient
	mu      sync.Mutex
	events  []threadEventCall
	agents  map[int64]*sdk.PlatformInstance
	sendErr error
}

func (p *recordingPlatform) SendThreadEvent(target sdk.ThreadRef, message any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, threadEventCall{Target: target, Message: message})
	return p.sendErr
}

func (p *recordingPlatform) SendTrackedAgentEvent(request sdk.AgentEventRequest) (*sdk.AgentEventReceipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, threadEventCall{
		Target:  sdk.ThreadRef{AgentID: request.AgentID, ThreadID: request.ThreadID},
		Message: request.Message,
	})
	if p.sendErr != nil {
		return nil, p.sendErr
	}
	return &sdk.AgentEventReceipt{SourceEventID: request.SourceEventID,
		ExecutionID: "execution:" + request.SourceEventID, Accepted: true, ThreadID: request.ThreadID}, nil
}

func (p *recordingPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	if agent := p.agents[id]; agent != nil {
		copy := *agent
		return &copy, nil
	}
	return nil, errTaskNotFound
}

func newTestApp(t *testing.T) (*App, *sdk.AppCtx, *recordingPlatform) {
	t.Helper()
	platform := &recordingPlatform{agents: map[int64]*sdk.PlatformInstance{
		7: {ID: 7, ProjectID: "project-a", DefaultThreadID: "opaque-default"},
	}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	return app, ctx, platform
}

func callerContext(agentID int64, threadID, projectID string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: agentID, ThreadID: threadID, ProjectID: projectID})
}

func TestTaskLifecycleUsesOpaqueThreadOwnership(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	creator := callerContext(7, "thread-a7f3", "project-a")

	createdRaw, err := app.toolCreate(creator, appCtx, map[string]any{
		"title": "Review CRM health", "description": "Check active opportunities",
	})
	if err != nil {
		t.Fatal(err)
	}
	created := createdRaw.(map[string]any)["task"].(*Task)
	if created.CreatedByThreadID != "thread-a7f3" || created.AssignedThreadID != "thread-a7f3" {
		t.Fatalf("thread provenance not preserved: %+v", created)
	}
	if strings.Contains(created.AssignedThreadID, "main") || strings.Contains(created.AssignedThreadID, "worker") {
		t.Fatalf("task app classified an opaque thread: %q", created.AssignedThreadID)
	}

	running, progress, step := stateRunning, 25, "Reviewing records"
	updatedRaw, err := app.toolUpdate(creator, appCtx, map[string]any{
		"task_id": created.ID, "state": running, "progress": progress, "current_step": step,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated := updatedRaw.(*Task)
	if updated.ExecutionThreadID != "thread-a7f3" {
		t.Fatalf("running update did not record trusted executor: %+v", updated)
	}
	completedRaw, err := app.toolComplete(creator, appCtx, map[string]any{"task_id": created.ID, "result": "CRM is healthy"})
	if err != nil {
		t.Fatal(err)
	}
	completed := completedRaw.(*Task)
	if completed.State != stateCompleted || completed.Progress == nil || *completed.Progress != 100 || completed.CurrentStep != "Completed" || completed.Result != "CRM is healthy" {
		t.Fatalf("unexpected completion: %+v", completed)
	}
	events, err := app.store.Events(created.ID)
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if got := events[2].Data["current_step"]; got != "Completed" {
		t.Fatalf("completion event retained stale step: %+v", events[2])
	}
	if got := len(platform.events); got != 0 {
		t.Fatalf("creator and assignee are identical; got %d duplicate terminal receipts", got)
	}
}

func TestEmptyStructuredScheduleRemainsImmediate(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	caller := callerContext(7, "thread-immediate", "project-a")
	emptySchedule := map[string]any{
		"kind": "once", "at": "", "after": "", "every": "", "cron": "", "timezone": "UTC",
		"overlap_policy": "skip", "catchup_policy": "skip",
	}
	raw, err := app.toolCreate(caller, appCtx, map[string]any{
		"title": "Immediate work", "schedule": emptySchedule,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(*Task)
	if task.State != stateQueued || task.ScheduleKind != "" || task.NextRunAt != nil {
		t.Fatalf("empty optional schedule changed immediate task semantics: %+v", task)
	}

	running, progress, step := stateRunning, 25, "Working"
	updatedRaw, err := app.toolUpdate(caller, appCtx, map[string]any{
		"task_id": task.ID, "state": running, "progress": progress,
		"current_step": step, "schedule": emptySchedule,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated := updatedRaw.(*Task)
	if updated.State != stateRunning || updated.Progress == nil || *updated.Progress != progress || updated.CurrentStep != step {
		t.Fatalf("empty optional schedule swallowed progress update: %+v", updated)
	}
}

func TestAssignmentAndExecutorLifecycle(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	creator := callerContext(7, "thread-origin", "project-a")
	const authoritativeDescription = "Title: Freeze Play in Full\nBody: Hello my lovelies! … keep this punctuation exactly.\nMedia: https://iframe.mediadelivery.net/embed/authoritative-id\nAudience: Member, Exclusive\nSell: false\nSchedule: 2026-08-14 20:00 Europe/Madrid\nAllowed final action: Schedule"
	raw, err := app.toolCreate(creator, appCtx, map[string]any{
		"title": "Prepare briefing", "description": authoritativeDescription,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(*Task)

	assignedRaw, err := app.toolAssign(creator, appCtx, map[string]any{
		"task_id": task.ID, "thread_id": "thread-exec-91",
	})
	if err != nil {
		t.Fatal(err)
	}
	assigned := assignedRaw.(*Task)
	if assigned.AssignedThreadID != "thread-exec-91" || assigned.ExecutionThreadID != "" {
		t.Fatalf("assignment should not claim execution before work starts: %+v", assigned)
	}
	if len(platform.events) != 1 || platform.events[0].Target.ThreadID != "thread-exec-91" {
		t.Fatalf("assignment receipt=%+v", platform.events)
	}
	assignment, ok := platform.events[0].Message.(map[string]any)
	if !ok {
		t.Fatalf("assignment payload type=%T", platform.events[0].Message)
	}
	for key, want := range map[string]any{
		"type": "task.assigned", "task_id": task.ID, "title": task.Title,
		"description": authoritativeDescription, "state": task.State,
	} {
		if got := assignment[key]; got != want {
			t.Fatalf("assignment payload %s=%#v, want %#v; payload=%+v", key, got, want, assignment)
		}
	}

	executor := callerContext(7, "thread-exec-91", "project-a")
	runningRaw, err := app.toolUpdate(executor, appCtx, map[string]any{
		"task_id": task.ID, "state": stateRunning, "progress": 40,
		"current_step": "Preparing briefing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if runningRaw.(*Task).ExecutionThreadID != "thread-exec-91" {
		t.Fatalf("assigned worker was not recorded as executor: %+v", runningRaw)
	}
	if _, err := app.toolComplete(executor, appCtx, map[string]any{"task_id": task.ID, "result": "Briefing ready"}); err != nil {
		t.Fatal(err)
	}
	if len(platform.events) != 2 {
		t.Fatalf("want assignment + one terminal receipt, got %+v", platform.events)
	}
	terminal := platform.events[1]
	if terminal.Target.ThreadID != "thread-origin" || terminal.Target.AgentID != 7 {
		t.Fatalf("terminal receipt targeted wrong opaque thread: %+v", terminal)
	}
}

func TestStructuredProgressUpdatePreservesAuthoritativeDescriptionForWorker(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	creator := callerContext(7, "thread-origin", "project-a")
	const description = "TITLE|BODY|MEDIA|AUDIENCE|DATE|TIMEZONE|STOP CONDITIONS"
	raw, err := app.toolCreate(creator, appCtx, map[string]any{
		"title": "Authoritative worker handoff", "description": description,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(*Task)

	// Reproduce a Codex structured call: optional strings and schedule fields are
	// present as empty placeholders even though this is only a progress update.
	if _, err := app.toolUpdate(creator, appCtx, map[string]any{
		"task_id": task.ID, "state": stateRunning, "progress": 10,
		"current_step": "Preparing worker", "description": "", "error": "",
		"result_reference": "", "schedule": map[string]any{
			"kind": "once", "at": "", "after": "", "every": "", "cron": "", "timezone": "",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAssign(creator, appCtx, map[string]any{
		"task_id": task.ID, "thread_id": "thread-worker",
	}); err != nil {
		t.Fatal(err)
	}

	worker := callerContext(7, "thread-worker", "project-a")
	gotRaw, err := app.toolGet(worker, appCtx, map[string]any{"task_id": task.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := gotRaw.(map[string]any)["task"].(*Task)
	if got.Description != description {
		t.Fatalf("progress update erased authoritative worker description: got %q, want %q", got.Description, description)
	}
}

func TestToolUpdateAtomicallyEditsPausedRecurringDefinition(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	creator := callerContext(7, "schedule-owner", "project-a")
	raw, err := app.toolCreate(creator, appCtx, map[string]any{
		"title": "Old recurring title", "description": "Old recurring body",
		"schedule": map[string]any{"kind": "interval", "every": "10m", "timezone": "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	if _, err := app.store.Pause(parent.ID, "schedule-owner"); err != nil {
		t.Fatal(err)
	}
	parent, _ = app.store.Get(parent.ID)

	oldOccurrence, _, err := app.store.Create(CreateTaskInput{
		AgentID: 7, ProjectID: "project-a", Title: parent.Title, Description: parent.Description,
		State: stateCompleted, AssignedThreadID: "schedule-owner", ParentTaskID: parent.ID,
		OccurrenceKey: "old-definition-snapshot",
	})
	if err != nil {
		t.Fatal(err)
	}

	updatedRaw, err := app.toolUpdate(creator, appCtx, map[string]any{
		"task_id": parent.ID, "title": "New recurring title", "description": "New recurring body",
		// Reproduce provider placeholders while changing only interval cadence and timezone.
		"schedule": map[string]any{"kind": "once", "at": "", "after": "", "every": "20m", "cron": "", "timezone": "Europe/Madrid"},
		"state":    stateRunning, "progress": 0, "current_step": "", "error": "", "result_reference": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated := updatedRaw.(*Task)
	if updated.Title != "New recurring title" || updated.Description != "New recurring body" ||
		updated.ScheduleKind != scheduleInterval || updated.ScheduleExpression != "20m0s" ||
		updated.ScheduleTimezone != "Europe/Madrid" || updated.ScheduleEnabled || updated.State != stateWaiting {
		t.Fatalf("atomic recurring definition edit lost fields or pause state: %+v", updated)
	}
	oldOccurrence, _ = app.store.Get(oldOccurrence.ID)
	if oldOccurrence.Title != "Old recurring title" || oldOccurrence.Description != "Old recurring body" {
		t.Fatalf("materialized occurrence was retroactively rewritten: %+v", oldOccurrence)
	}

	beforeInvalid := updated.UpdatedAt
	badTitle := "Must not commit"
	badSchedule := ScheduleInput{Every: "30s"}
	if _, _, err := app.store.Update(parent.ID, "schedule-owner", UpdateTaskInput{Title: &badTitle, Schedule: &badSchedule}); err == nil {
		t.Fatal("invalid schedule should reject the complete definition edit")
	}
	afterInvalid, _ := app.store.Get(parent.ID)
	if afterInvalid.Title != updated.Title || !afterInvalid.UpdatedAt.Equal(beforeInvalid) {
		t.Fatalf("failed atomic edit partially committed: %+v", afterInvalid)
	}

	if _, err := app.store.RunNow(parent.ID, "schedule-owner"); err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.Tick(time.Now().UTC().Add(time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	runs, err := app.store.List(TaskFilter{ProjectID: "project-a", AgentID: 7, ParentTaskID: &parentID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var future *Task
	for i := range runs {
		if runs[i].ID != oldOccurrence.ID {
			future = &runs[i]
			break
		}
	}
	if future == nil || future.Title != updated.Title || future.Description != updated.Description {
		t.Fatalf("future occurrence did not inherit edited parent definition: %+v", runs)
	}
}

func TestToolsRejectMissingOrCrossProjectCaller(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	if _, err := app.toolCreate(context.Background(), appCtx, map[string]any{"title": "x"}); err == nil {
		t.Fatal("expected trusted caller requirement")
	}
	raw, err := app.toolCreate(callerContext(7, "opaque-1", "project-a"), appCtx, map[string]any{"title": "x"})
	if err != nil {
		t.Fatal(err)
	}
	id := raw.(map[string]any)["task"].(*Task).ID
	if _, err := app.toolGet(callerContext(7, "opaque-2", "project-b"), appCtx, map[string]any{"task_id": id}); err == nil {
		t.Fatal("cross-project task read should fail")
	}
}

func TestListIsAgentWideAcrossOpaqueThreads(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	for _, item := range []struct {
		thread string
		title  string
	}{
		{thread: "opaque-default", title: "Created by default thread"},
		{thread: "requester-a", title: "Created by requester"},
		{thread: "execution-b", title: "Created by another thread"},
	} {
		if _, err := app.toolCreate(callerContext(7, item.thread, "project-a"), appCtx, map[string]any{"title": item.title}); err != nil {
			t.Fatal(err)
		}
	}
	otherAgent, _, err := app.store.Create(CreateTaskInput{AgentID: 8, ProjectID: "project-a", Title: "Other agent", AssignedThreadID: "other-default"})
	if err != nil {
		t.Fatal(err)
	}
	rootTasks, err := app.store.List(TaskFilter{ProjectID: "project-a", AgentID: 7, Limit: 10})
	if err != nil || len(rootTasks) != 3 {
		t.Fatalf("seed roots=%+v err=%v", rootTasks, err)
	}
	if _, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Occurrence", AssignedThreadID: "opaque-default", ParentTaskID: rootTasks[0].ID}); err != nil {
		t.Fatal(err)
	}

	// Legacy thread filters are intentionally ignored by the handler. They are
	// also absent from the public schema, so no caller can turn provenance into
	// a visibility boundary.
	raw, err := app.toolList(callerContext(7, "inventory-reader", "project-a"), appCtx, map[string]any{
		"created_by_me": true, "assigned_thread_id": "inventory-reader",
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := raw.(map[string]any)
	tasks := listed["tasks"].([]Task)
	counts := listed["counts"].(TaskCounts)
	if len(tasks) != 3 || counts.Active != 3 {
		t.Fatalf("agent-wide root inventory tasks=%+v counts=%+v", tasks, counts)
	}
	for _, task := range tasks {
		if task.AgentID != 7 || task.ID == otherAgent.ID || task.ParentTaskID != "" {
			t.Fatalf("inventory leaked another agent or occurrence: %+v", task)
		}
	}

	withRunsRaw, err := app.toolList(callerContext(7, "unrelated-reader", "project-a"), appCtx, map[string]any{"include_runs": true})
	if err != nil {
		t.Fatal(err)
	}
	withRuns := withRunsRaw.(map[string]any)
	if got := len(withRuns["tasks"].([]Task)); got != 4 {
		t.Fatalf("include_runs task count=%d, want 4", got)
	}
	if got := withRuns["counts"].(TaskCounts).Active; got != 4 {
		t.Fatalf("include_runs active count=%d, want 4", got)
	}
}

func TestSchedulesMaterializeOnceAndRecurringWithoutSetupDuplicates(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	caller := callerContext(7, "opaque-schedule-owner", "project-a")

	onceRaw, err := app.toolCreate(caller, appCtx, map[string]any{
		"title": "One result", "schedule": map[string]any{"kind": "once", "after": "10m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	once := onceRaw.(map[string]any)["task"].(*Task)
	if once.State != stateWaiting || once.NextRunAt == nil {
		t.Fatalf("bad one-time schedule: %+v", once)
	}
	type liveSnapshot struct {
		event TaskEvent
		task  *Task
	}
	live := []liveSnapshot{}
	app.store.onEvent = func(event TaskEvent) {
		if event.TaskID != once.ID {
			return
		}
		snapshot, getErr := app.store.Get(event.TaskID)
		if getErr != nil {
			t.Errorf("load emitted task snapshot: %v", getErr)
			return
		}
		live = append(live, liveSnapshot{event: event, task: snapshot})
	}
	if err := app.scheduler.Tick(once.NextRunAt.Add(time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	once, _ = app.store.Get(once.ID)
	if once.State != stateQueued || once.ScheduleEnabled || once.NextRunAt != nil || once.ScheduledFor == nil {
		t.Fatalf("one-time task should become the runnable task itself: %+v", once)
	}
	if len(live) != 2 || live[0].event.ToState != stateQueued || live[0].task.State != stateQueued || live[0].task.ScheduleEnabled || live[0].task.NextRunAt != nil || live[1].event.EventType != "occurrence_dispatched" || live[1].task.DispatchedAt == nil {
		t.Fatalf("queued live event exposed a partial schedule transition: %+v", live)
	}
	if enabled, ok := live[0].event.Data["schedule_enabled"].(bool); !ok || enabled {
		t.Fatalf("queued event must explicitly disable the one-time schedule: %+v", live[0].event.Data)
	}
	encoded, err := json.Marshal(once)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"schedule_enabled":false`) {
		t.Fatalf("inactive schedule state omitted from JSON: %s", encoded)
	}

	running, progress, step := stateRunning, 10, "Reviewing CRM leads"
	if _, _, err := app.store.Update(once.ID, "opaque-default", UpdateTaskInput{State: &running, Progress: &progress, CurrentStep: &step}); err != nil {
		t.Fatal(err)
	}
	done, result := stateCompleted, "No newly active leads"
	if _, _, err := app.store.Update(once.ID, "opaque-default", UpdateTaskInput{State: &done, Result: &result}); err != nil {
		t.Fatal(err)
	}
	if len(live) != 4 || live[2].task.State != stateRunning || live[2].task.ScheduleEnabled || live[3].task.State != stateCompleted || live[3].task.ScheduleEnabled {
		t.Fatalf("live lifecycle snapshots=%+v", live)
	}
	roots, _ := app.store.List(TaskFilter{ProjectID: "project-a", AgentID: 7, Limit: 50})
	if len(roots) != 1 {
		t.Fatalf("one user outcome created %d logical tasks", len(roots))
	}

	recurringRaw, err := app.toolCreate(caller, appCtx, map[string]any{
		"title": "Recurring review", "schedule": map[string]any{"kind": "interval", "every": "10m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := recurringRaw.(map[string]any)["task"].(*Task)
	due := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(due, "project-a"); err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.Tick(due, "project-a"); err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	runs, err := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 50})
	if err != nil || len(runs) != 1 {
		t.Fatalf("recurring materialization must be idempotent: runs=%+v err=%v", runs, err)
	}
	if runs[0].CreatedByThreadID != "opaque-schedule-owner" || runs[0].AssignedThreadID != "opaque-default" {
		t.Fatalf("occurrence lost opaque thread provenance: %+v", runs[0])
	}
	// One ready event for once and one for the recurrence; no duplicate wake.
	if len(platform.events) != 2 {
		t.Fatalf("unexpected scheduler notifications: %+v", platform.events)
	}
	for _, event := range platform.events {
		if event.Target.ThreadID != "opaque-default" {
			t.Fatalf("scheduled execution targeted creator instead of configured default: %+v", event)
		}
	}
}

func TestHTTPProjectIsolationAndOperatorLifecycle(t *testing.T) {
	app, _, _ := newTestApp(t)
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	server := httptest.NewServer(mux)
	defer server.Close()

	body := bytes.NewBufferString(`{"agent_id":7,"title":"Operator task"}`)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/tasks", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", resp.StatusCode)
	}
	var created struct {
		Task Task `json:"task"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Task.AssignedThreadID != "opaque-default" {
		t.Fatalf("UI task not assigned to agent default thread: %+v", created.Task)
	}
	if _, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Requester-created", CreatedByThreadID: "requester-a", AssignedThreadID: "requester-a"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.store.Create(CreateTaskInput{AgentID: 8, ProjectID: "project-a", Title: "Other agent", AssignedThreadID: "other-default"}); err != nil {
		t.Fatal(err)
	}

	// Public inventory queries are agent-wide. Obsolete thread query
	// parameters cannot hide tasks created or assigned through another thread.
	listURL := server.URL + "/tasks?project_id=project-a&agent_id=7&thread_id=opaque-default&assigned_thread_id=opaque-default"
	var inventory struct {
		Tasks  []Task     `json:"tasks"`
		Counts TaskCounts `json:"counts"`
	}
	listResp, err := http.Get(listURL)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if err := json.NewDecoder(listResp.Body).Decode(&inventory); err != nil {
		t.Fatal(err)
	}
	if listResp.StatusCode != http.StatusOK || len(inventory.Tasks) != 2 || inventory.Counts.Active != 2 {
		t.Fatalf("agent-wide HTTP inventory status=%d payload=%+v", listResp.StatusCode, inventory)
	}
	for _, task := range inventory.Tasks {
		if task.AgentID != 7 {
			t.Fatalf("HTTP inventory leaked another agent: %+v", task)
		}
	}

	cross, _ := http.NewRequest(http.MethodGet, server.URL+"/tasks/"+created.Task.ID, nil)
	cross.Header.Set("X-Apteva-Project-ID", "project-b")
	crossResp, _ := http.DefaultClient.Do(cross)
	if crossResp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-project read status=%d", crossResp.StatusCode)
	}

	patch := bytes.NewBufferString(`{"state":"running","progress":40,"current_step":"Checking records"}`)
	update, _ := http.NewRequest(http.MethodPatch, server.URL+"/tasks/"+created.Task.ID, patch)
	update.Header.Set("Content-Type", "application/json")
	update.Header.Set("X-Apteva-Project-ID", "project-a")
	updateResp, _ := http.DefaultClient.Do(update)
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d", updateResp.StatusCode)
	}
}

func TestHTTPDescriptionUpdatesAreAppliedStrictAndNoOpSafe(t *testing.T) {
	app, _, _ := newTestApp(t)
	task, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Editable", Description: "Old description", AssignedThreadID: "opaque-default"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/tasks/", app.handleTask)
	server := httptest.NewServer(mux)
	defer server.Close()

	update := func(method, body string) (*http.Response, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+"/tasks/"+task.ID, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Apteva-Project-ID", "project-a")
		resp, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		var payload map[string]any
		if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
		}
		resp.Body.Close()
		return resp, payload
	}

	resp, payload := update(http.MethodPatch, `{"description":"New exact description"}`)
	if resp.StatusCode != http.StatusOK || payload["changed"] != true {
		t.Fatalf("PATCH response status=%d payload=%+v", resp.StatusCode, payload)
	}
	updated, _ := app.store.Get(task.ID)
	if updated.Description != "New exact description" {
		t.Fatalf("PATCH silently ignored description: %+v", updated)
	}
	stamp := updated.UpdatedAt

	resp, payload = update(http.MethodPut, `{"description":"New exact description"}`)
	if resp.StatusCode != http.StatusOK || payload["changed"] != false {
		t.Fatalf("no-op PUT response status=%d payload=%+v", resp.StatusCode, payload)
	}
	unchanged, _ := app.store.Get(task.ID)
	if !unchanged.UpdatedAt.Equal(stamp) {
		t.Fatalf("no-op PUT changed updated_at from %s to %s", stamp, unchanged.UpdatedAt)
	}

	resp, _ = update(http.MethodPatch, `{"description":"ignored","unsupported_field":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown update field status=%d, want 400", resp.StatusCode)
	}
	unchanged, _ = app.store.Get(task.ID)
	if unchanged.Description != "New exact description" || !unchanged.UpdatedAt.Equal(stamp) {
		t.Fatalf("rejected request mutated task: %+v", unchanged)
	}
}

func TestHTTPAtomicallyEditsRecurringDefinitionAndPreservesPause(t *testing.T) {
	app, _, _ := newTestApp(t)
	task, _, err := app.store.Create(CreateTaskInput{
		AgentID: 7, ProjectID: "project-a", Title: "Old title", Description: "Old body",
		AssignedThreadID: "opaque-default", Schedule: &ScheduleInput{Kind: scheduleCron, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Pause(task.ID, "api"); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/tasks/", app.handleTask)
	server := httptest.NewServer(mux)
	defer server.Close()
	body := bytes.NewBufferString(`{"title":"New title","description":"New body","schedule":{"timezone":"Europe/Madrid"}}`)
	req, _ := http.NewRequest(http.MethodPatch, server.URL+"/tasks/"+task.ID, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Apteva-Project-ID", "project-a")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Task    Task `json:"task"`
		Changed bool `json:"changed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !payload.Changed || payload.Task.Title != "New title" ||
		payload.Task.Description != "New body" || payload.Task.ScheduleKind != scheduleCron ||
		payload.Task.ScheduleExpression != "0 9 * * *" || payload.Task.ScheduleTimezone != "Europe/Madrid" ||
		payload.Task.ScheduleEnabled {
		t.Fatalf("HTTP recurring definition edit status=%d payload=%+v", resp.StatusCode, payload)
	}
}

func TestRecurringOccurrenceLifecycleRollsUpDownstreamOutcome(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	creator := callerContext(7, "schedule-owner", "project-a")
	raw, err := app.toolCreate(creator, appCtx, map[string]any{
		"title": "Prepare X post", "schedule": map[string]any{"kind": "interval", "every": "10m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	due := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(due, "project-a"); err != nil {
		t.Fatal(err)
	}
	parent, _ = app.store.Get(parent.ID)
	if parent.LastRunAt != nil || parent.LastDispatchedAt == nil || parent.LastOccurrenceStatus != "dispatched" || parent.LastError != "" {
		t.Fatalf("scheduler dispatch masqueraded as workflow execution: %+v", parent)
	}
	parentID := parent.ID
	runs, err := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if err != nil || len(runs) != 1 || runs[0].DispatchedAt == nil || runs[0].AcceptedAt != nil || runs[0].TelemetryReference == "" {
		t.Fatalf("dispatched occurrence incomplete: runs=%+v err=%v", runs, err)
	}

	executor := callerContext(7, "opaque-default", "project-a")
	got, err := app.toolGet(executor, appCtx, map[string]any{"task_id": runs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	accepted := got.(map[string]any)["task"].(*Task)
	if accepted.AcceptedAt == nil || accepted.ExecutionThreadID != "opaque-default" {
		t.Fatalf("authoritative read did not accept occurrence: %+v", accepted)
	}
	parent, _ = app.store.Get(parent.ID)
	if parent.LastRunAt == nil || parent.LastOccurrenceStatus != "accepted" {
		t.Fatalf("accepted occurrence did not update parent health: %+v", parent)
	}
	if _, err := app.toolUpdate(executor, appCtx, map[string]any{"task_id": accepted.ID, "state": stateRunning, "progress": 60, "current_step": "Drafting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolComplete(executor, appCtx, map[string]any{"task_id": accepted.ID, "result": "Draft ready", "result_reference": "social:draft-42"}); err != nil {
		t.Fatal(err)
	}
	parent, _ = app.store.Get(parent.ID)
	if parent.State != stateWaiting || parent.LastOccurrenceStatus != stateCompleted || parent.LastError != "" || parent.LastResultReference != "social:draft-42" {
		t.Fatalf("terminal occurrence did not roll up separately from scheduler state: %+v", parent)
	}
}

func TestUnacceptedDispatchRetriesBeforeFailure(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	raw, err := app.toolCreate(callerContext(7, "opaque-default", "project-a"), appCtx, map[string]any{
		"title": "Prepare X post", "schedule": map[string]any{"kind": "interval", "every": "10m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	dispatchedAt := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(dispatchedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	runs, _ := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if len(runs) != 1 || runs[0].DispatchAttempts != 1 || runs[0].LastDispatchAttemptAt == nil {
		t.Fatalf("initial dispatch metadata incomplete: %+v", runs)
	}

	for retry := 1; retry < dispatchMaxAttempts; retry++ {
		tick := dispatchedAt.Add(time.Duration(retry)*dispatchRetryInterval + time.Second)
		if err := app.scheduler.reconcileUnaccepted(tick, "project-a"); err != nil {
			t.Fatal(err)
		}
		run, _ := app.store.Get(runs[0].ID)
		wantAttempts := retry + 1
		if run.State != stateQueued || run.AcceptedAt != nil || run.DispatchAttempts != wantAttempts {
			t.Fatalf("retry %d prematurely failed or lost attempt metadata: %+v", retry, run)
		}
		parent, _ = app.store.Get(parent.ID)
		if parent.LastOccurrenceStatus != "dispatched" || parent.LastError != "" {
			t.Fatalf("retry %d made parent falsely unhealthy: %+v", retry, parent)
		}
		for _, call := range platform.events {
			payload, _ := call.Message.(map[string]any)
			if payload["type"] == "task.terminal" {
				t.Fatalf("retry %d alerted before attempts were exhausted: %+v", retry, platform.events)
			}
		}
		// A repeated tick inside the same retry window must not emit again.
		if err := app.scheduler.reconcileUnaccepted(tick, "project-a"); err != nil {
			t.Fatal(err)
		}
		afterDuplicate, _ := app.store.Get(run.ID)
		if afterDuplicate.DispatchAttempts != wantAttempts {
			t.Fatalf("retry %d duplicated in the same window: %+v", retry, afterDuplicate)
		}
	}

	exhaustedAt := dispatchedAt.Add(time.Duration(dispatchMaxAttempts)*dispatchRetryInterval + time.Second)
	if err := app.scheduler.reconcileUnaccepted(exhaustedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	parent, _ = app.store.Get(parent.ID)
	if parent.LastRunAt != nil || parent.LastOccurrenceStatus != stateFailed || !strings.Contains(parent.LastError, "3 delivery attempts") {
		t.Fatalf("exhausted dispatch did not mark parent unhealthy: %+v", parent)
	}
	run, _ := app.store.Get(runs[0].ID)
	if run.State != stateFailed || run.AcceptedAt != nil || run.DispatchAttempts != dispatchMaxAttempts || !strings.Contains(run.Error, "3 delivery attempts") {
		t.Fatalf("exhausted occurrence not failed with reason: %+v", run)
	}
	ready, terminal := 0, 0
	for _, call := range platform.events {
		payload, _ := call.Message.(map[string]any)
		switch payload["type"] {
		case "task.ready":
			ready++
			if payload["task_id"] != run.ID {
				t.Fatalf("retry changed authoritative occurrence: %+v", payload)
			}
		case "task.terminal":
			terminal++
			if payload["attention_required"] != true || payload["reason"] != "dispatch_unaccepted" || call.Target.ThreadID != "opaque-default" {
				t.Fatalf("final dispatch alert incomplete: target=%+v payload=%+v", call.Target, payload)
			}
		}
	}
	if ready != dispatchMaxAttempts || terminal != 1 {
		t.Fatalf("want %d task.ready attempts and one final alert, got ready=%d terminal=%d events=%+v", dispatchMaxAttempts, ready, terminal, platform.events)
	}
	events, err := app.store.Events(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	redispatched := 0
	for _, event := range events {
		if event.EventType == "occurrence_redispatched" {
			redispatched++
		}
	}
	if redispatched != dispatchMaxAttempts-1 {
		t.Fatalf("want %d durable redispatch events, got %+v", dispatchMaxAttempts-1, events)
	}
}

func TestAcceptedRedispatchStopsRetriesAndFailure(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	raw, err := app.toolCreate(callerContext(7, "schedule-owner", "project-a"), appCtx, map[string]any{
		"title": "Prepare accepted X post", "schedule": map[string]any{"kind": "interval", "every": "30m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	dispatchedAt := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(dispatchedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	runs, _ := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if len(runs) != 1 {
		t.Fatalf("want one occurrence, got %+v", runs)
	}
	retryAt := dispatchedAt.Add(dispatchRetryInterval + time.Second)
	if err := app.scheduler.reconcileUnaccepted(retryAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.store.Accept(runs[0].ID, "opaque-default", retryAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.reconcileUnaccepted(retryAt.Add(24*time.Hour), "project-a"); err != nil {
		t.Fatal(err)
	}
	accepted, _ := app.store.Get(runs[0].ID)
	if accepted.State != stateQueued || accepted.AcceptedAt == nil || accepted.DispatchAttempts != 2 || accepted.Error != "" {
		t.Fatalf("accepted occurrence retried or failed: %+v", accepted)
	}
	parent, _ = app.store.Get(parent.ID)
	if parent.LastOccurrenceStatus != "accepted" || parent.LastError != "" {
		t.Fatalf("accepted retry did not preserve healthy parent status: %+v", parent)
	}
	ready, terminal := 0, 0
	for _, call := range platform.events {
		payload, _ := call.Message.(map[string]any)
		if payload["type"] == "task.ready" {
			ready++
		}
		if payload["type"] == "task.terminal" {
			terminal++
		}
	}
	if ready != 2 || terminal != 0 {
		t.Fatalf("accepted occurrence should stop after one retry: ready=%d terminal=%d events=%+v", ready, terminal, platform.events)
	}
}

func TestInitialDeliveryErrorRemainsQueuedForRetry(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	raw, err := app.toolCreate(callerContext(7, "schedule-owner", "project-a"), appCtx, map[string]any{
		"title": "Recover transient delivery", "schedule": map[string]any{"kind": "interval", "every": "30m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	platform.sendErr = errors.New("temporary thread delivery outage")
	dispatchedAt := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(dispatchedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	runs, _ := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if len(runs) != 1 || runs[0].State != stateQueued || runs[0].DispatchAttempts != 1 || runs[0].Error != "" {
		t.Fatalf("transient initial delivery became a terminal failure: %+v", runs)
	}
	parent, _ = app.store.Get(parent.ID)
	if parent.LastOccurrenceStatus != "dispatched" || parent.LastError != "" {
		t.Fatalf("transient initial delivery made parent unhealthy: %+v", parent)
	}
	platform.sendErr = nil
	if err := app.scheduler.reconcileUnaccepted(dispatchedAt.Add(dispatchRetryInterval+time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	retried, _ := app.store.Get(runs[0].ID)
	if retried.State != stateQueued || retried.DispatchAttempts != 2 || retried.Error != "" {
		t.Fatalf("transient initial delivery did not enter retry flow: %+v", retried)
	}
}

func agentLifecycleDelivery(task *Task, lifecycleType string, sequence uint64, at time.Time) sdk.Event {
	return sdk.Event{
		DeliveryID: "transition:" + task.ID + ":" + lifecycleType,
		Event:      sdk.AgentEventLifecycleEvent,
		InstanceID: task.AgentID,
		ProjectID:  task.ProjectID,
		Data: map[string]any{
			"type": lifecycleType, "source_event_id": task.AgentEventSourceID,
			"execution_id": task.AgentExecutionID, "thread_id": task.AssignedThreadID,
			"timestamp": at.UTC().Format(time.RFC3339Nano), "sequence": float64(sequence),
		},
	}
}

func TestTrackedOccurrenceLifecycleAcceptsBeforeAnyTasksMCPCall(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	raw, err := app.toolCreate(callerContext(7, "schedule-owner", "project-a"), appCtx, map[string]any{
		"title": "Delegate Patreon work", "description": "Authoritative body and execution constraints",
		"schedule": map[string]any{"kind": "interval", "every": "24h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	dispatchedAt := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(dispatchedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	runs, err := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	run := &runs[0]
	if run.AgentEventSourceID != taskAgentEventSourceID(run.ID) || run.AgentExecutionID == "" {
		t.Fatalf("tracked dispatch mapping missing: %+v", run)
	}
	if len(platform.events) != 1 {
		t.Fatalf("tracked delivery calls=%+v", platform.events)
	}
	payload, _ := platform.events[0].Message.(map[string]any)
	if payload["type"] != "task.ready" || payload["task_id"] != run.ID {
		t.Fatalf("tracked payload=%+v", payload)
	}
	if _, present := payload["dispatch_attempt"]; present {
		t.Fatalf("tracked retry payload must remain immutable: %+v", payload)
	}

	// No tasks_get or tasks_update has happened. Core's generic claim is enough
	// for Tasks to record authoritative acceptance and stop dispatch retries.
	claimedAt := dispatchedAt.Add(time.Second)
	if err := app.handleAgentEventLifecycle(appCtx, agentLifecycleDelivery(run, sdk.AgentEventClaimed, 1, claimedAt)); err != nil {
		t.Fatal(err)
	}
	eventsBeforeDuplicate, _ := app.store.Events(run.ID)
	if err := app.handleAgentEventLifecycle(appCtx, agentLifecycleDelivery(run, sdk.AgentEventClaimed, 1, claimedAt)); err != nil {
		t.Fatal(err)
	}
	eventsAfterDuplicate, _ := app.store.Events(run.ID)
	if len(eventsAfterDuplicate) != len(eventsBeforeDuplicate) {
		t.Fatalf("retried lifecycle delivery created duplicate task events: before=%d after=%d", len(eventsBeforeDuplicate), len(eventsAfterDuplicate))
	}
	accepted, _ := app.store.Get(run.ID)
	if accepted.AcceptedAt == nil || accepted.ExecutionThreadID != run.AssignedThreadID || accepted.State != stateQueued {
		t.Fatalf("generic claim did not accept occurrence: %+v", accepted)
	}
	if err := app.scheduler.reconcileUnaccepted(claimedAt.Add(24*time.Hour), "project-a"); err != nil {
		t.Fatal(err)
	}
	accepted, _ = app.store.Get(run.ID)
	if accepted.State == stateFailed || accepted.DispatchAttempts != 1 {
		t.Fatalf("accepted tracked occurrence was redelivered or failed: %+v", accepted)
	}

	activeAt := claimedAt.Add(time.Second)
	if err := app.handleAgentEventLifecycle(appCtx, agentLifecycleDelivery(accepted, sdk.AgentEventActive, 2, activeAt)); err != nil {
		t.Fatal(err)
	}
	active, _ := app.store.Get(run.ID)
	if active.State != stateRunning || active.StartedAt == nil || active.Progress != nil {
		t.Fatalf("Core activity should start work without inventing MCP progress: %+v", active)
	}
}

func TestSettledExecutionWithoutTerminalMCPFailsPromptlyAndUnblocksNextOccurrence(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	raw, err := app.toolCreate(callerContext(7, "schedule-owner", "project-a"), appCtx, map[string]any{
		"title": "Daily Patreon calendar", "schedule": map[string]any{"kind": "interval", "every": "24h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	dispatchedAt := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(dispatchedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	runs, _ := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	run := &runs[0]
	claimedAt := dispatchedAt.Add(time.Second)
	if err := app.handleAgentEventLifecycle(appCtx, agentLifecycleDelivery(run, sdk.AgentEventClaimed, 1, claimedAt)); err != nil {
		t.Fatal(err)
	}
	run, _ = app.store.Get(run.ID)
	if err := app.handleAgentEventLifecycle(appCtx, agentLifecycleDelivery(run, sdk.AgentEventActive, 2, claimedAt.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolUpdate(callerContext(7, run.AssignedThreadID, "project-a"), appCtx, map[string]any{
		"task_id": run.ID, "state": stateRunning, "progress": 100, "current_step": "Ready to finalize",
	}); err != nil {
		t.Fatal(err)
	}
	run, _ = app.store.Get(run.ID)
	settledAt := claimedAt.Add(2 * time.Minute)
	if err := app.handleAgentEventLifecycle(appCtx, agentLifecycleDelivery(run, sdk.AgentEventSettled, 3, settledAt)); err != nil {
		t.Fatal(err)
	}
	settled, _ := app.store.Get(run.ID)
	if settled.State != stateRunning || settled.AgentSettleDeadline == nil || settled.Progress == nil || *settled.Progress != 100 {
		t.Fatalf("settled execution should preserve MCP state during grace: %+v", settled)
	}
	if err := app.scheduler.Tick(settledAt.Add(agentSettleGrace-time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	stillRunning, _ := app.store.Get(run.ID)
	if stillRunning.State != stateRunning {
		t.Fatalf("occurrence failed before settled grace elapsed: %+v", stillRunning)
	}
	if err := app.scheduler.Tick(settledAt.Add(agentSettleGrace+time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	failed, _ := app.store.Get(run.ID)
	if failed.State != stateFailed || !strings.Contains(failed.Error, "agent_exited_without_terminal_status") ||
		failed.CurrentStep != "Agent execution ended without terminal status" {
		t.Fatalf("forgotten terminal MCP call was not reconciled promptly: %+v", failed)
	}

	parent, _ = app.store.Get(parent.ID)
	if parent.LastOccurrenceStatus != stateFailed || !strings.Contains(parent.LastError, "agent_exited_without_terminal_status") {
		t.Fatalf("settled failure did not roll up to recurring task: %+v", parent)
	}
	if err := app.scheduler.Tick(parent.NextRunAt.Add(time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	runs, err = app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if err != nil || len(runs) != 2 {
		t.Fatalf("failed settled occurrence still blocked the next run: runs=%+v err=%v", runs, err)
	}
}

func TestTerminalMCPDuringSettledGraceRemainsAuthoritative(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	raw, err := app.toolCreate(callerContext(7, "schedule-owner", "project-a"), appCtx, map[string]any{
		"title": "Finish before grace", "schedule": map[string]any{"kind": "interval", "every": "24h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	dispatchedAt := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(dispatchedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	runs, _ := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	run := &runs[0]
	base := dispatchedAt.Add(time.Second)
	for sequence, lifecycleType := range []string{sdk.AgentEventClaimed, sdk.AgentEventActive, sdk.AgentEventSettled} {
		run, _ = app.store.Get(run.ID)
		if err := app.handleAgentEventLifecycle(appCtx, agentLifecycleDelivery(run, lifecycleType, uint64(sequence+1), base.Add(time.Duration(sequence)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.toolComplete(callerContext(7, run.AssignedThreadID, "project-a"), appCtx,
		map[string]any{"task_id": run.ID, "result": "Published successfully"}); err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.Tick(base.Add(agentSettleGrace+time.Hour), "project-a"); err != nil {
		t.Fatal(err)
	}
	completed, _ := app.store.Get(run.ID)
	if completed.State != stateCompleted || completed.Error != "" || completed.AgentSettleDeadline != nil {
		t.Fatalf("lifecycle safety net overrode terminal MCP outcome: %+v", completed)
	}
}

func TestAgentExecutionErrorFailsOccurrenceImmediately(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	raw, err := app.toolCreate(callerContext(7, "schedule-owner", "project-a"), appCtx, map[string]any{
		"title": "Execution error", "schedule": map[string]any{"kind": "interval", "every": "24h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	dispatchedAt := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(dispatchedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	runs, _ := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	run := &runs[0]
	event := agentLifecycleDelivery(run, sdk.AgentEventError, 1, dispatchedAt.Add(time.Second))
	event.Data["reason"] = "provider_failed"
	if err := app.handleAgentEventLifecycle(appCtx, event); err != nil {
		t.Fatal(err)
	}
	failed, _ := app.store.Get(run.ID)
	if failed.State != stateFailed || !strings.Contains(failed.Error, "agent_execution_error: provider_failed") || failed.AcceptedAt == nil {
		t.Fatalf("execution error did not terminalize accepted occurrence: %+v", failed)
	}
}

func TestConcurrentSchedulerTicksClaimOneRedispatch(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	raw, err := app.toolCreate(callerContext(7, "schedule-owner", "project-a"), appCtx, map[string]any{
		"title": "Concurrent retry", "schedule": map[string]any{"kind": "interval", "every": "30m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	dispatchedAt := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(dispatchedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	retryAt := dispatchedAt.Add(dispatchRetryInterval + time.Second)
	const callers = 16
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- app.scheduler.Tick(retryAt, "project-a")
		}()
	}
	wg.Wait()
	close(errs)
	for tickErr := range errs {
		if tickErr != nil {
			t.Fatal(tickErr)
		}
	}
	parentID := parent.ID
	runs, _ := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if len(runs) != 1 || runs[0].DispatchAttempts != 2 || runs[0].State != stateQueued {
		t.Fatalf("concurrent ticks duplicated retry or occurrence: %+v", runs)
	}
	ready := 0
	for _, call := range platform.events {
		payload, _ := call.Message.(map[string]any)
		if payload["type"] == "task.ready" {
			ready++
		}
	}
	if ready != 2 {
		t.Fatalf("want initial delivery plus one claimed retry, got %d: %+v", ready, platform.events)
	}
}

func TestManifestAndToolContract(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if manifest.Name != "tasks" || manifest.Version != "3.3.3" || manifest.MinAptevaVersion != "0.40.6" || manifest.Icon != "/ui/icon.svg" || manifest.IconStyle != "monochrome" || len(manifest.Provides.UIComponents) != 3 || len(manifest.Provides.UISurfaces) != 1 || len(manifest.Provides.Skills) != 1 {
		t.Fatalf("manifest surfaces incomplete: %+v", manifest.Provides)
	}
	overview := manifest.Provides.UIComponents[0]
	if overview.Name != "task-overview" || overview.DefaultSize != "half" || len(overview.SupportedSizes) != 2 || overview.SettingsSchema["type"] != "object" {
		t.Fatalf("task overview widget contract incomplete: %+v", overview)
	}
	agentWidget := manifest.Provides.UIComponents[1]
	hasThreadSidebar := false
	for _, slot := range agentWidget.Slots {
		hasThreadSidebar = hasThreadSidebar || slot == sdk.UIComponentSlotDashboardThreadSidebar
	}
	if agentWidget.Visibility != sdk.UIComponentVisibilityAttached || !hasThreadSidebar || len(agentWidget.RefreshTopics) == 0 {
		t.Fatalf("agent task widget contextual contract incomplete: %+v", agentWidget)
	}
	if len(manifest.Provides.UIPanels) != 1 || !manifest.Provides.UIPanels[0].Suggested {
		t.Fatalf("task page should be a generic suggested sidebar contribution: %+v", manifest.Provides.UIPanels)
	}
	normalizedManifestSkill := strings.Join(strings.Fields(manifest.Provides.Skills[0].Body), " ")
	for _, required := range []string{
		"combines multiple sources or independent checks",
		"even when its calls can run in parallel",
		"one bounded lookup or action",
		"does not imply delegation",
		"grants `tasks_get` plus only the domain tools it needs",
		"must call `tasks_get` with that ID",
		"must not substitute parent context for the record",
		"stops before making external changes",
		"should not paraphrase the task into a second source of truth",
		"omitted fields are preserved atomically",
		"update the recurring parent ID rather than an occurrence ID",
		"Never create a placeholder task",
	} {
		if !strings.Contains(normalizedManifestSkill, required) {
			t.Fatalf("embedded Tasks skill missing classification rule %q", required)
		}
	}
	want := map[string]bool{"create": false, "list": false, "get": false, "update": false, "assign": false, "complete": false, "cancel": false, "pause": false, "resume": false, "run_now": false}
	for _, tool := range app.MCPTools() {
		if _, ok := want[tool.Name]; !ok {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		want[tool.Name] = true
		if tool.HandlerCtx == nil || tool.Description == "" || tool.InputSchema["type"] != "object" {
			t.Fatalf("invalid tool contract: %+v", tool)
		}
		if tool.Name == "create" {
			for _, required := range []string{
				"combines multiple sources or independent checks",
				"even when calls run in parallel or finish in one turn",
				"one bounded read or action with no multi-source synthesis",
				"does not imply delegation",
			} {
				if !strings.Contains(tool.Description, required) {
					t.Fatalf("create tool description missing classification rule %q: %s", required, tool.Description)
				}
			}
		}
		if tool.Name == "list" {
			properties, _ := tool.InputSchema["properties"].(map[string]any)
			if _, ok := properties["created_by_me"]; ok {
				t.Fatalf("list exposes creator-thread visibility filter: %+v", properties)
			}
			if _, ok := properties["assigned_thread_id"]; ok {
				t.Fatalf("list exposes assignee-thread visibility filter: %+v", properties)
			}
			for _, name := range []string{"states", "include_runs", "limit"} {
				if _, ok := properties[name]; !ok {
					t.Fatalf("list missing generic filter %q: %+v", name, properties)
				}
			}
		}
		if tool.Name == "assign" {
			for _, required := range []string{"existing opaque thread", "grant the worker tasks_get", "retain all Tasks mutation tools", "call tasks_get before any domain action"} {
				if !strings.Contains(tool.Description, required) {
					t.Fatalf("assign tool missing worker retrieval rule %q: %s", required, tool.Description)
				}
			}
		}
		if tool.Name == "get" {
			for _, required := range []string{"authoritative task", "delegated worker", "before any domain action", "without granting Tasks mutation tools"} {
				if !strings.Contains(tool.Description, required) {
					t.Fatalf("get tool missing delegated-read rule %q: %s", required, tool.Description)
				}
			}
		}
		if tool.Name == "update" {
			for _, required := range []string{"Atomically edit a task definition", "preserving every omitted field", "partial patch", "keeps it paused", "recurring parent task", "Keep the task running while any executor is actively working", "Use waiting only when no executor can progress", "at least two or three intermediate milestones"} {
				if !strings.Contains(tool.Description, required) {
					t.Fatalf("update tool description missing progress rule %q: %s", required, tool.Description)
				}
			}
			properties, _ := tool.InputSchema["properties"].(map[string]any)
			if _, ok := properties["title"]; !ok {
				t.Fatalf("update tool cannot rename a task: %+v", properties)
			}
			schedule, _ := properties["schedule"].(map[string]any)
			if required, ok := schedule["required"].([]string); ok && len(required) > 0 {
				t.Fatalf("update schedule must be a partial patch: %+v", schedule)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing tool %q", name)
		}
	}
	skillBody, err := os.ReadFile("skills/how-to-use-tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Provides.Skills[0].Body != string(skillBody) || manifest.Provides.Skills[0].BodyFile != "" {
		t.Fatal("runtime manifest does not embed the canonical Tasks skill body")
	}
	normalizedSkill := strings.Join(strings.Fields(string(skillBody)), " ")
	for _, required := range []string{
		"combines multiple sources or independent checks",
		"even when its calls can run in parallel",
		"one bounded lookup or action",
		"does not imply delegation",
		"Tasks records work but does not create threads",
		"grants `tasks_get` plus only the domain tools it needs",
		"must call `tasks_get` with that ID",
		"must not substitute parent context for the record",
		"stops before making external changes",
		"should not paraphrase the task into a second source of truth",
		"omitted fields are preserved atomically",
		"update the recurring parent ID rather than an occurrence ID",
		"Never create a placeholder task",
		"Keep a task `running` while any executor is actively working",
		"at least two or three intermediate milestones",
		"changing threads or entering `waiting` does not by itself increase progress",
	} {
		if !strings.Contains(normalizedSkill, required) {
			t.Fatalf("Tasks skill missing classification rule %q", required)
		}
	}
}

func TestTerminalMCPGuidanceContract(t *testing.T) {
	app := &App{}
	tools := map[string]string{}
	for _, tool := range app.MCPTools() {
		tools[tool.Name] = tool.Description
	}
	for _, required := range []string{
		"Progress 100 and a final-looking current_step are still nonterminal",
		"call tasks_complete before pace, idle, stopping, or sending the final response",
		"On failure, record failed with a concrete error before stopping",
	} {
		if !strings.Contains(tools["update"], required) {
			t.Fatalf("tasks_update does not prevent omitted terminal writes; missing %q: %s", required, tools["update"])
		}
	}
	for _, required := range []string{
		"Required final write after successful task work",
		"before pace, idle, stopping, or sending the final response",
		"Neither progress 100 nor a final-looking current_step completes the task",
		"When a worker reports success, main must record the result here before it stops",
	} {
		if !strings.Contains(tools["complete"], required) {
			t.Fatalf("tasks_complete does not make the final write mandatory; missing %q: %s", required, tools["complete"])
		}
	}

	skillBody, err := os.ReadFile("skills/how-to-use-tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(skillBody)), " ")
	for _, required := range []string{
		"The terminal write is mandatory",
		"`progress: 100` is not a terminal state",
		"Before the thread calls `pace`, returns idle, stops, or sends its final response",
		"it must call `tasks_complete` exactly once with the concrete result",
		"A worker's final message is not a Tasks terminal write",
		"Core lifecycle settlement is only a safety net",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("Tasks skill does not prevent omitted terminal writes; missing %q", required)
		}
	}
}

func TestAssignNotifiesTheThreadJustAssignedNotCurrentDBAssignee(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	creator := callerContext(7, "thread-main", "project-a")
	raw, err := app.toolCreate(creator, appCtx, map[string]any{"title": "Split inbox"})
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(*Task)

	assignedRaw, err := app.toolAssign(creator, appCtx, map[string]any{
		"task_id": task.ID, "thread_id": "thread-worker-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshotB := assignedRaw.(*Task)

	// A racing second assign lands before the first call's notification: the DB
	// now says thread-worker-a owns the task.
	stolen := "thread-worker-a"
	if _, _, err := app.store.Update(task.ID, "thread-main", UpdateTaskInput{AssignedThreadID: &stolen}); err != nil {
		t.Fatal(err)
	}
	if err := app.notifyAssigned(snapshotB, "thread-worker-b", "task.assigned"); err != nil {
		t.Fatal(err)
	}
	last := platform.events[len(platform.events)-1]
	if last.Target.ThreadID != "thread-worker-b" {
		t.Fatalf("wake event followed the DB's current assignee instead of the call target: %+v", last)
	}
}

func TestOverlapSkipKeepsLastRunAtAndAdvancesNextRun(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	caller := callerContext(7, "opaque-schedule-owner", "project-a")
	raw, err := app.toolCreate(caller, appCtx, map[string]any{
		"title": "Recurring review", "schedule": map[string]any{"kind": "interval", "every": "10m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	firstDue := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(firstDue, "project-a"); err != nil {
		t.Fatal(err)
	}
	parent, _ = app.store.Get(parent.ID)
	if parent.LastRunAt != nil || parent.LastDispatchedAt == nil || parent.NextRunAt == nil || parent.LastOccurrenceStatus != "dispatched" {
		t.Fatalf("dispatch was incorrectly recorded as an accepted run: %+v", parent)
	}
	parentID := parent.ID
	runs, err := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	if _, changed, err := app.store.Accept(runs[0].ID, runs[0].AssignedThreadID, firstDue.Add(time.Second)); err != nil || !changed {
		t.Fatalf("accept occurrence changed=%v err=%v", changed, err)
	}
	parent, _ = app.store.Get(parent.ID)
	if parent.LastRunAt == nil || parent.LastOccurrenceStatus != "accepted" {
		t.Fatalf("accepted occurrence did not roll up: %+v", parent)
	}
	materializedLastRun := *parent.LastRunAt

	// The run is still open at the next occurrence, so the tick must skip —
	// without making the schedule look like it just ran.
	secondDue := parent.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(secondDue, "project-a"); err != nil {
		t.Fatal(err)
	}
	parent, _ = app.store.Get(parent.ID)
	if parent.LastRunAt == nil || !parent.LastRunAt.Equal(materializedLastRun) {
		t.Fatalf("overlap skip advanced last_run_at from %v to %v", materializedLastRun, parent.LastRunAt)
	}
	if !parent.NextRunAt.After(secondDue) {
		t.Fatalf("overlap skip failed to advance next_run_at: %+v", parent)
	}
	events, err := app.store.Events(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].EventType != "occurrence_skipped_overlap" {
		t.Fatalf("skip left no trace event: %+v", events)
	}
}

func TestSchedulerAutoFailsStaleRunAndResumesOccurrences(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	caller := callerContext(7, "opaque-schedule-owner", "project-a")
	raw, err := app.toolCreate(caller, appCtx, map[string]any{
		"title": "Hourly sweep", "schedule": map[string]any{"kind": "interval", "every": "10m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	if err := app.scheduler.Tick(parent.NextRunAt.Add(time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	runs, err := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	running := stateRunning
	if _, _, err := app.store.Update(runs[0].ID, "opaque-default", UpdateTaskInput{State: &running}); err != nil {
		t.Fatal(err)
	}

	// Threshold for a 10m cadence clamps to staleRunMinAge (1h); a run idle past
	// it must be auto-failed instead of blocking every future occurrence.
	parent, _ = app.store.Get(parent.ID)
	staleTick := time.Now().UTC().Add(2 * time.Hour)
	if parent.NextRunAt.After(staleTick) {
		t.Fatalf("test premise broken: next_run_at %v beyond stale tick", parent.NextRunAt)
	}
	if err := app.scheduler.Tick(staleTick, "project-a"); err != nil {
		t.Fatal(err)
	}
	runs, err = app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if err != nil || len(runs) != 2 {
		t.Fatalf("stale run did not unblock materialization: runs=%+v err=%v", runs, err)
	}
	states := map[string]int{}
	for _, run := range runs {
		states[run.State]++
		if run.State == stateFailed && !strings.Contains(run.Error, "auto-failed by scheduler") {
			t.Fatalf("stale failure lacks operator-readable error: %+v", run)
		}
	}
	if states[stateFailed] != 1 || states[stateQueued] != 1 {
		t.Fatalf("want one auto-failed and one fresh run, got %+v", states)
	}
	var creatorReceipt, freshWake bool
	for _, event := range platform.events {
		payload, ok := event.Message.(map[string]any)
		if !ok {
			continue
		}
		if payload["type"] == "task.terminal" && event.Target.ThreadID == "opaque-schedule-owner" {
			creatorReceipt = true
		}
		if payload["type"] == "task.ready" && event.Target.ThreadID == "opaque-default" {
			freshWake = true
		}
	}
	if !creatorReceipt || !freshWake {
		t.Fatalf("auto-fail must tell the creator and wake the fresh run: %+v", platform.events)
	}
}
