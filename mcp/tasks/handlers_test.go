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
	updatedRaw, err := app.toolSetProgress(creator, appCtx, map[string]any{
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
		"type": "task.assigned", "origin": "tasks.assignment", "task_id": task.ID,
		"title": task.Title, "description": authoritativeDescription, "state": task.State,
		"assigned_thread_id": "thread-exec-91",
	} {
		if got := assignment[key]; got != want {
			t.Fatalf("assignment payload %s=%#v, want %#v; payload=%+v", key, got, want, assignment)
		}
	}
	if _, present := assignment["reply_required"]; present {
		t.Fatalf("assignment event must not suppress its worker's parent report: %+v", assignment)
	}
	firstAction, _ := assignment["required_first_action"].(map[string]any)
	if firstAction["tool"] != "tasks_get" || firstAction["task_id"] != task.ID {
		t.Fatalf("assignment first action is not authoritative fetch: %+v", assignment)
	}

	executor := callerContext(7, "thread-exec-91", "project-a")
	runningRaw, err := app.toolSetProgress(executor, appCtx, map[string]any{
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

func TestFocusedMutationToolsSeparateProgressEditAndFailure(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	caller := callerContext(7, "thread-main", "project-a")
	raw, err := app.toolCreate(caller, appCtx, map[string]any{
		"title": "Focused mutation task", "description": "Authoritative description",
	})
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(*Task)

	progressRaw, err := app.toolSetProgress(caller, appCtx, map[string]any{
		"task_id": task.ID, "state": stateRunning, "progress": 40, "current_step": "Checking records",
	})
	if err != nil {
		t.Fatal(err)
	}
	progressed := progressRaw.(*Task)
	if progressed.State != stateRunning || progressed.Progress == nil || *progressed.Progress != 40 ||
		progressed.Description != "Authoritative description" {
		t.Fatalf("focused progress changed unrelated fields: %+v", progressed)
	}
	if _, err := app.toolSetProgress(caller, appCtx, map[string]any{
		"task_id": task.ID, "state": stateRunning, "progress": 20, "current_step": "Going backwards",
	}); err == nil || !strings.Contains(err.Error(), "cannot decrease") {
		t.Fatalf("decreasing progress error=%v", err)
	}
	backwards := 30
	if _, _, err := app.store.Update(task.ID, "thread-racing-writer", UpdateTaskInput{Progress: &backwards}); err == nil || !strings.Contains(err.Error(), "cannot decrease") {
		t.Fatalf("store accepted a stale concurrent progress write: %v", err)
	}
	if _, err := app.toolSetProgress(caller, appCtx, map[string]any{
		"task_id": task.ID, "state": stateRunning, "progress": 50,
	}); err == nil || !strings.Contains(err.Error(), "current_step required") {
		t.Fatalf("progress without a concrete step error=%v", err)
	}
	if _, err := app.toolFail(caller, appCtx, map[string]any{"task_id": task.ID, "error": ""}); err == nil || !strings.Contains(err.Error(), "error required") {
		t.Fatalf("empty terminal failure error=%v", err)
	}

	failedRaw, err := app.toolFail(caller, appCtx, map[string]any{
		"task_id": task.ID, "error": "upstream rejected the operation", "result_reference": "operation:42",
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := failedRaw.(*Task)
	if failed.State != stateFailed || failed.Error != "upstream rejected the operation" ||
		failed.CurrentStep != "Failed" || failed.ResultReference != "operation:42" {
		t.Fatalf("focused failure did not terminalize task: %+v", failed)
	}

	editableRaw, err := app.toolCreate(caller, appCtx, map[string]any{
		"title": "Old title", "description": "Remove me",
	})
	if err != nil {
		t.Fatal(err)
	}
	editable := editableRaw.(map[string]any)["task"].(*Task)
	editedRaw, err := app.toolEdit(caller, appCtx, map[string]any{
		"task_id": editable.ID, "title": "New title", "clear_description": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	edited := editedRaw.(*Task)
	if edited.Title != "New title" || edited.Description != "" || edited.State != stateQueued {
		t.Fatalf("focused definition edit changed lifecycle or failed to clear description: %+v", edited)
	}
}

func TestFocusedEditRejectsMaterializedOccurrenceDefinition(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	caller := callerContext(7, "schedule-owner", "project-a")
	raw, err := app.toolCreate(caller, appCtx, map[string]any{
		"title": "Recurring parent", "schedule": map[string]any{"kind": "interval", "every": "24h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["task"].(*Task)
	scheduledFor := parent.NextRunAt.UTC()
	occurrence, _, err := app.store.Create(CreateTaskInput{
		AgentID: 7, ProjectID: "project-a", Title: parent.Title, Description: "Snapshot",
		State: stateQueued, AssignedThreadID: "schedule-owner", ParentTaskID: parent.ID,
		ScheduledFor: &scheduledFor, OccurrenceKey: scheduledFor.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolEdit(caller, appCtx, map[string]any{
		"task_id": occurrence.ID, "title": "Rewritten snapshot",
	}); err == nil || !strings.Contains(err.Error(), "occurrence definitions are immutable") {
		t.Fatalf("materialized occurrence edit error=%v", err)
	}
	rewritten := "Store-level rewrite"
	if _, _, err := app.store.Update(occurrence.ID, "schedule-owner", UpdateTaskInput{Title: &rewritten}); err == nil || !strings.Contains(err.Error(), "cannot edit an occurrence definition") {
		t.Fatalf("store accepted an occurrence definition rewrite: %v", err)
	}
	unchanged, _ := app.store.Get(occurrence.ID)
	if unchanged.Title != parent.Title || unchanged.Description != "Snapshot" {
		t.Fatalf("materialized occurrence was mutated: %+v", unchanged)
	}
}

func TestToolEditAtomicallyEditsPausedRecurringDefinition(t *testing.T) {
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

	updatedRaw, err := app.toolEdit(creator, appCtx, map[string]any{
		"task_id": parent.ID, "title": "New recurring title", "description": "New recurring body",
		// Reproduce provider placeholders while changing only interval cadence and timezone.
		"schedule": map[string]any{"kind": "once", "at": "", "after": "", "every": "20m", "cron": "", "timezone": "Europe/Madrid"},
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
	// Two execution wakes plus one durable completion receipt; no duplicates.
	if len(platform.events) != 3 {
		t.Fatalf("unexpected scheduler notifications: %+v", platform.events)
	}
	for _, event := range platform.events {
		if event.Message.(map[string]any)["type"] == "task.ready" && event.Target.ThreadID != "opaque-default" {
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
	if _, err := app.toolSetProgress(executor, appCtx, map[string]any{"task_id": accepted.ID, "state": stateRunning, "progress": 60, "current_step": "Drafting"}); err != nil {
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
		if err := app.drainDeliveries("", "project-a", tick); err != nil {
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
		if err := app.drainDeliveries("", "project-a", tick); err != nil {
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
	if err := app.drainDeliveries("", "project-a", exhaustedAt); err != nil {
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
	if err := app.drainDeliveries("", "project-a", retryAt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.store.Accept(runs[0].ID, "opaque-default", retryAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.reconcileUnaccepted(retryAt.Add(24*time.Hour), "project-a"); err != nil {
		t.Fatal(err)
	}
	if err := app.drainDeliveries("", "project-a", retryAt.Add(24*time.Hour)); err != nil {
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
	if err := app.drainDeliveries("", "project-a", dispatchedAt.Add(dispatchRetryInterval+time.Second)); err != nil {
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

func terminalizationLifecycleDelivery(task *Task, lifecycleType string, sequence uint64, at time.Time) sdk.Event {
	return sdk.Event{
		DeliveryID: "terminalization-transition:" + task.ID + ":" + lifecycleType,
		Event:      sdk.AgentEventLifecycleEvent,
		InstanceID: task.AgentID,
		ProjectID:  task.ProjectID,
		Data: map[string]any{
			"type": lifecycleType, "source_event_id": taskTerminalizationSourceID(task.ID),
			"execution_id": "execution:" + taskTerminalizationSourceID(task.ID),
			"thread_id":    task.AssignedThreadID,
			"timestamp":    at.UTC().Format(time.RFC3339Nano), "sequence": float64(sequence),
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
	if payload["type"] != "task.ready" || payload["origin"] != "tasks.scheduler" ||
		payload["task_id"] != run.ID || payload["occurrence_id"] != run.ID ||
		payload["assigned_thread_id"] != run.AssignedThreadID || payload["reply_required"] != false ||
		payload["reply_thread_id"] != nil || !strings.Contains(payload["instruction"].(string), "Do not call send") {
		t.Fatalf("tracked payload=%+v", payload)
	}
	firstAction, _ := payload["required_first_action"].(map[string]any)
	if firstAction["tool"] != "tasks_get" || firstAction["task_id"] != run.ID {
		t.Fatalf("tracked payload omitted required authoritative fetch: %+v", payload)
	}
	if _, present := payload["dispatch_attempt"]; present {
		t.Fatalf("tracked retry payload must remain immutable: %+v", payload)
	}

	// No tasks_get or tasks_set_progress has happened. Core's generic claim is enough
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
	if err := app.drainDeliveries("", "project-a", claimedAt.Add(24*time.Hour)); err != nil {
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
	if active.State != stateQueued || active.StartedAt != nil || active.Progress != nil ||
		active.CurrentStep != "" || active.AgentExecutionState != sdk.AgentEventActive {
		t.Fatalf("Core activity must remain separate from business progress: %+v", active)
	}
	executions, err := app.store.AgentExecutions(run.ID)
	if err != nil || len(executions) != 1 || executions[0].State != sdk.AgentEventActive {
		t.Fatalf("execution activity was not stored separately: executions=%+v err=%v", executions, err)
	}
}

func TestSettledExecutionGetsOneTerminalizationWakeThenFailsAndUnblocksNextOccurrence(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
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
	if _, err := app.toolSetProgress(callerContext(7, run.AssignedThreadID, "project-a"), appCtx, map[string]any{
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
	terminalizing, _ := app.store.Get(run.ID)
	if terminalizing.State != stateRunning || terminalizing.AgentSettleDeadline != nil {
		t.Fatalf("first settled grace should request reconciliation, not fail or rerun: %+v", terminalizing)
	}
	if len(platform.events) != 2 {
		t.Fatalf("want task.ready plus exactly one terminalization wake, got %+v", platform.events)
	}
	terminalPayload, _ := platform.events[1].Message.(map[string]any)
	if terminalPayload["type"] != "task.terminalization_required" || terminalPayload["reply_required"] != false ||
		!strings.Contains(terminalPayload["instruction"].(string), "do not repeat an ambiguous external action") {
		t.Fatalf("unsafe terminalization payload: %+v", terminalPayload)
	}
	// Repeated scheduler ticks cannot create or deliver a second wake.
	if err := app.scheduler.Tick(settledAt.Add(agentSettleGrace+2*time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	if len(platform.events) != 2 {
		t.Fatalf("terminalization wake repeated: %+v", platform.events)
	}
	terminalBase := settledAt.Add(agentSettleGrace + 3*time.Second)
	for sequence, lifecycleType := range []string{sdk.AgentEventClaimed, sdk.AgentEventActive, sdk.AgentEventSettled} {
		terminalizing, _ = app.store.Get(run.ID)
		if err := app.handleAgentEventLifecycle(appCtx, terminalizationLifecycleDelivery(terminalizing,
			lifecycleType, uint64(sequence+1), terminalBase.Add(time.Duration(sequence)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.scheduler.Tick(terminalBase.Add(2*time.Second+agentSettleGrace+time.Second), "project-a"); err != nil {
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

func TestOneTimeLifecycleErrorUpdatesLatestOccurrenceSummary(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	raw, err := app.toolCreate(callerContext(7, "schedule-owner", "project-a"), appCtx, map[string]any{
		"title": "One-time publish", "schedule": map[string]any{"kind": "once", "after": "1m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(*Task)
	dispatchedAt := task.NextRunAt.Add(time.Second)
	if err := app.scheduler.Tick(dispatchedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	task, _ = app.store.Get(task.ID)
	event := agentLifecycleDelivery(task, sdk.AgentEventError, 1, dispatchedAt.Add(time.Second))
	event.Data["reason"] = "provider_failed"
	if err := app.handleAgentEventLifecycle(appCtx, event); err != nil {
		t.Fatal(err)
	}
	failed, _ := app.store.Get(task.ID)
	if failed.LastOccurrenceStatus != stateFailed ||
		!strings.Contains(failed.LastError, "agent_execution_error: provider_failed") {
		t.Fatalf("one-time task did not expose latest downstream failure: %+v", failed)
	}
}

func TestRecoverOccurrenceCreatesOneLinkedReconciliationAttempt(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	creator := callerContext(7, "schedule-owner", "project-a")
	raw, err := app.toolCreate(creator, appCtx, map[string]any{
		"title": "Publish scheduled update", "description": "Exact authoritative publishing body",
		"operation_key": "publisher:post:stable-42",
		"schedule":      map[string]any{"kind": "interval", "every": "24h"},
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
	original := &runs[0]
	failed, failure := stateFailed, "provider disconnected after an ambiguous response"
	if _, _, err := app.store.Update(original.ID, original.AssignedThreadID,
		UpdateTaskInput{State: &failed, Error: &failure}); err != nil {
		t.Fatal(err)
	}

	platform.mu.Lock()
	platform.events = nil
	platform.mu.Unlock()
	firstRaw, err := app.toolRecoverOccurrence(creator, appCtx, map[string]any{
		"task_id": original.ID, "reason": "Reconcile whether the post was published",
		"idempotency_key": "operator-recovery-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	first := firstRaw.(map[string]any)
	recovery := first["task"].(*Task)
	if first["created"] != true || recovery.RecoveryOfTaskID != original.ID ||
		recovery.OriginalOccurrenceKey != original.ScheduleOccurrenceKey || recovery.RecoveryAttempt != 1 ||
		recovery.OperationKey != "publisher:post:stable-42" || recovery.ParentTaskID != original.ParentTaskID ||
		recovery.State != stateQueued || recovery.DispatchedAt == nil || recovery.AssignedThreadID != "schedule-owner" {
		t.Fatalf("recovery did not preserve safe occurrence context: %+v", recovery)
	}
	if recovery.Description != original.Description || recovery.Title != original.Title {
		t.Fatalf("recovery rewrote authoritative definition: recovery=%+v original=%+v", recovery, original)
	}
	platform.mu.Lock()
	events := append([]threadEventCall(nil), platform.events...)
	platform.mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("recovery delivery count=%d, want 1: %+v", len(events), events)
	}
	payload, _ := events[0].Message.(map[string]any)
	if payload["type"] != "task.recovery.ready" || payload["origin"] != "tasks.recovery" ||
		payload["recovery_of_task_id"] != original.ID || payload["recovery_mode"] != "reconcile_only" ||
		payload["reply_required"] != false || !strings.Contains(payload["instruction"].(string), "do not repeat an ambiguous external action") {
		t.Fatalf("unsafe recovery event: %+v", payload)
	}

	secondRaw, err := app.toolRecoverOccurrence(creator, appCtx, map[string]any{
		"task_id": original.ID, "reason": "Reconcile whether the post was published",
		"idempotency_key": "operator-recovery-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	second := secondRaw.(map[string]any)
	if second["created"] != false || second["task"].(*Task).ID != recovery.ID {
		t.Fatalf("idempotent recovery duplicated attempt: first=%+v second=%+v", first, second)
	}
	platform.mu.Lock()
	eventCount := len(platform.events)
	platform.mu.Unlock()
	if eventCount != 1 {
		t.Fatalf("idempotent recovery redelivered event %d times", eventCount)
	}
	executions, err := app.store.AgentExecutions(recovery.ID)
	if err != nil || len(executions) != 1 || executions[0].Purpose != agentExecutionPurpose {
		t.Fatalf("recovery execution trace=%+v err=%v", executions, err)
	}
}

func TestConcurrentRecoverOccurrenceCreatesOneAttempt(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	creator := callerContext(7, "main", "project-a")
	now := time.Now().UTC()
	original, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a",
		Title: "Ambiguous transfer", Description: "Reconcile transfer TX-42", State: stateFailed,
		CreatedByThreadID: "main", AssignedThreadID: "main", ScheduledFor: &now,
		OccurrenceKey: "original-42", IdempotencyKey: "original-42"})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	results := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, callErr := app.toolRecoverOccurrence(creator, appCtx, map[string]any{
				"task_id": original.ID, "reason": "Reconcile TX-42", "idempotency_key": "recover-tx-42",
			})
			if callErr != nil {
				errs <- callErr
				return
			}
			results <- result.(map[string]any)["task"].(*Task).ID
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var onlyID string
	for id := range results {
		if onlyID == "" {
			onlyID = id
		} else if id != onlyID {
			t.Fatalf("concurrent recovery created multiple IDs: %s and %s", onlyID, id)
		}
	}
	var count int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE recovery_of_task_id=?`, original.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent recovery attempts=%d, want 1", count)
	}
}

func TestConcurrentSchedulerTicksEmitOneTerminalizationWake(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	base := time.Now().UTC().Add(time.Hour)
	run, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a",
		Title: "Terminalization race", State: stateQueued, CreatedByThreadID: "main",
		AssignedThreadID: "opaque-default", ScheduledFor: &base, OccurrenceKey: "terminalization-race"})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err = app.store.MarkDispatched(run.ID, "tasks:scheduler", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.RecordAgentEventExecution(run.ID, run.AgentEventSourceID,
		"execution:"+run.AgentEventSourceID, run.AssignedThreadID, base); err != nil {
		t.Fatal(err)
	}
	run, _ = app.store.Get(run.ID)
	for sequence, lifecycleType := range []string{sdk.AgentEventClaimed, sdk.AgentEventActive, sdk.AgentEventSettled} {
		run, _ = app.store.Get(run.ID)
		if err := app.handleAgentEventLifecycle(appCtx, agentLifecycleDelivery(run, lifecycleType,
			uint64(sequence+1), base.Add(time.Duration(sequence+1)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	platform.mu.Lock()
	platform.events = nil
	platform.mu.Unlock()
	tickAt := base.Add(3*time.Second + agentSettleGrace)
	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tickErr := app.scheduler.Tick(tickAt, "project-a"); tickErr != nil {
				errs <- tickErr
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	platform.mu.Lock()
	events := append([]threadEventCall(nil), platform.events...)
	platform.mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("concurrent ticks emitted %d terminalization wakes: %+v", len(events), events)
	}
	payload, _ := events[0].Message.(map[string]any)
	if payload["type"] != "task.terminalization_required" {
		t.Fatalf("unexpected concurrent wake payload: %+v", payload)
	}
	executions, err := app.store.AgentExecutions(run.ID)
	if err != nil || len(executions) != 2 || executions[1].Purpose != agentTerminalizationPurpose {
		t.Fatalf("execution attempts=%+v err=%v", executions, err)
	}
}

func TestRecoverOccurrenceRejectsNonOccurrenceAndNonFailedTask(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	caller := callerContext(7, "main", "project-a")
	raw, err := app.toolCreate(caller, appCtx, map[string]any{"title": "Immediate work"})
	if err != nil {
		t.Fatal(err)
	}
	immediate := raw.(map[string]any)["task"].(*Task)
	if _, err := app.toolRecoverOccurrence(caller, appCtx, map[string]any{
		"task_id": immediate.ID, "reason": "Should reject",
	}); err == nil || !strings.Contains(err.Error(), "failed scheduled occurrence") {
		t.Fatalf("immediate task recovery err=%v", err)
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
	if manifest.Name != "tasks" || manifest.Version != "3.5.1" || manifest.MinAptevaVersion != "0.40.6" || manifest.Icon != "/ui/icon.svg" || manifest.IconStyle != "monochrome" || len(manifest.Provides.UIComponents) != 3 || len(manifest.Provides.UISurfaces) != 1 || len(manifest.Provides.Skills) != 1 {
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
		"edit the recurring parent ID rather than an occurrence ID",
		"Never create a placeholder task",
		"`reply_required: false`",
		"Core becoming active does not move the occurrence to `running`",
		"exactly one reconciliation-only",
		"Use `tasks_recover_occurrence`, not `tasks_run_now`",
	} {
		if !strings.Contains(normalizedManifestSkill, required) {
			t.Fatalf("embedded Tasks skill missing classification rule %q", required)
		}
	}
	want := map[string]bool{"create": false, "list": false, "get": false, "set_progress": false, "edit": false, "fail": false, "update": false, "assign": false, "complete": false, "cancel": false, "pause": false, "resume": false, "run_now": false, "recover_occurrence": false}
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
		if tool.Name == "set_progress" {
			for _, required := range []string{"meaningful nonterminal task milestone", "Always provide state, progress, and a concrete current_step", "waiting only when nobody can progress", "must not decrease", "tasks_complete after success", "tasks_fail after failure"} {
				if !strings.Contains(tool.Description, required) {
					t.Fatalf("set_progress description missing rule %q: %s", required, tool.Description)
				}
			}
			properties, _ := tool.InputSchema["properties"].(map[string]any)
			for _, forbidden := range []string{"title", "description", "schedule", "error"} {
				if _, ok := properties[forbidden]; ok {
					t.Fatalf("set_progress exposes unrelated field %q: %+v", forbidden, properties)
				}
			}
		}
		if tool.Name == "edit" {
			for _, required := range []string{"Atomically edit", "preserving omitted fields", "clear_description=true", "materialized occurrence definitions are immutable", "keeps it paused", "never create a placeholder"} {
				if !strings.Contains(tool.Description, required) {
					t.Fatalf("edit description missing rule %q: %s", required, tool.Description)
				}
			}
			properties, _ := tool.InputSchema["properties"].(map[string]any)
			for _, forbidden := range []string{"state", "progress", "current_step", "error"} {
				if _, ok := properties[forbidden]; ok {
					t.Fatalf("edit exposes execution field %q: %+v", forbidden, properties)
				}
			}
			schedule, _ := properties["schedule"].(map[string]any)
			if required, ok := schedule["required"].([]string); ok && len(required) > 0 {
				t.Fatalf("edit schedule must be a partial patch: %+v", schedule)
			}
		}
		if tool.Name == "fail" {
			properties, _ := tool.InputSchema["properties"].(map[string]any)
			if _, ok := properties["state"]; ok {
				t.Fatalf("fail should not ask the agent to encode terminal state: %+v", properties)
			}
			for _, required := range []string{"explicit terminal task failure", "concrete error", "exactly once"} {
				if !strings.Contains(tool.Description, required) {
					t.Fatalf("fail description missing rule %q: %s", required, tool.Description)
				}
			}
		}
		if tool.Name == "update" {
			for _, required := range []string{"Legacy compatibility", "Do not use for new work", "tasks_set_progress", "tasks_edit", "tasks_fail", "tasks_complete"} {
				if !strings.Contains(tool.Description, required) {
					t.Fatalf("legacy update description missing migration rule %q: %s", required, tool.Description)
				}
			}
		}
		if tool.Name == "recover_occurrence" {
			for _, required := range []string{"linked reconciliation-only attempt", "must not blindly repeat", "call tasks_get first", "external_outcome_unknown", "instead of run_now"} {
				if !strings.Contains(tool.Description, required) {
					t.Fatalf("recover_occurrence missing safety rule %q: %s", required, tool.Description)
				}
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
		"edit the recurring parent ID rather than an occurrence ID",
		"Never create a placeholder task",
		"Keep a task `running` while any executor is actively working",
		"at least two or three intermediate milestones",
		"changing threads or entering `waiting` does not by itself increase progress",
		"New agent work must use `tasks_set_progress`, `tasks_edit`, `tasks_fail`, and `tasks_complete`",
		"`reply_required: false`",
		"Core becoming active does not move the occurrence to `running`",
		"exactly one reconciliation-only",
		"Use `tasks_recover_occurrence`, not `tasks_run_now`",
		"external_outcome_unknown",
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
		"Progress 100 is still nonterminal",
		"tasks_complete after success",
		"tasks_fail after failure",
	} {
		if !strings.Contains(tools["set_progress"], required) {
			t.Fatalf("tasks_set_progress does not preserve terminal separation; missing %q: %s", required, tools["set_progress"])
		}
	}
	for _, required := range []string{
		"explicit terminal task failure",
		"concrete error",
		"before pace, idle, stopping, or sending the final response",
	} {
		if !strings.Contains(tools["fail"], required) {
			t.Fatalf("tasks_fail does not make terminal failure explicit; missing %q: %s", required, tools["fail"])
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
		"it must call `tasks_fail` exactly once with a concrete error",
		"A worker's final message is not a Tasks terminal write",
		"Core lifecycle settlement is only a safety net",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("Tasks skill does not prevent omitted terminal writes; missing %q", required)
		}
	}
}

func TestAssignmentDeliveriesRespectLatestOwner(t *testing.T) {
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
	if last.Target.ThreadID != "thread-worker-a" {
		t.Fatalf("pending assignment did not wake the current owner: %+v", last)
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

func TestSchedulerPreservesActiveRunDespiteOldProgress(t *testing.T) {
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

	// Old progress is not evidence that an executor has stopped.
	parent, _ = app.store.Get(parent.ID)
	staleTick := time.Now().UTC().Add(2 * time.Hour)
	if parent.NextRunAt.After(staleTick) {
		t.Fatalf("test premise broken: next_run_at %v beyond stale tick", parent.NextRunAt)
	}
	if err := app.scheduler.Tick(staleTick, "project-a"); err != nil {
		t.Fatal(err)
	}
	runs, err = app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parentID, Limit: 10})
	if err != nil || len(runs) != 1 || runs[0].State != stateRunning {
		t.Fatalf("old progress allowed overlapping work: runs=%+v err=%v", runs, err)
	}
	if len(platform.events) != 1 {
		t.Fatalf("unexpected replacement or terminal notice: %+v", platform.events)
	}

}
