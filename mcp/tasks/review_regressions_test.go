package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func reviewSchedule(t *testing.T, app *App, kind, value string) *Task {
	t.Helper()
	input := ScheduleInput{Kind: kind}
	if kind == scheduleOnce {
		input.After = value
	} else {
		input.Every = value
	}
	task, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Review fixture", AssignedThreadID: "opaque-default", CreatedByThreadID: "owner", Schedule: &input})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func reviewRun(t *testing.T, app *App, parent *Task) *Task {
	t.Helper()
	if err := app.scheduler.Tick(parent.NextRunAt.Add(time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	if parent.ScheduleKind == scheduleOnce {
		run, err := app.store.Get(parent.ID)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	runs, err := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parent.ID})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%v err=%v", len(runs), err)
	}
	return &runs[0]
}

func TestReviewTerminalRecurringSchedulesMustStop(t *testing.T) {
	for _, state := range []string{stateCompleted, stateFailed, stateCancelled} {
		t.Run(state, func(t *testing.T) {
			app, _, platform := newTestApp(t)
			parent := reviewSchedule(t, app, scheduleInterval, "1h")
			if _, _, err := app.store.Update(parent.ID, "api", UpdateTaskInput{State: &state, Result: stringPtr("verified outcome"), Error: stringPtr("verified failure")}); err != nil {
				t.Fatal(err)
			}
			if err := app.scheduler.Tick(parent.NextRunAt.Add(time.Second), "project-a"); err != nil {
				t.Fatal(err)
			}
			ready := 0
			for _, event := range platform.events {
				if event.Message.(map[string]any)["type"] == "task.ready" {
					ready++
				}
			}
			if ready > 0 {
				t.Errorf("terminal %s parent still dispatched %d wake(s)", state, len(platform.events))
			}
		})
	}
}

func TestReviewPauseMustInvalidateDueSnapshot(t *testing.T) {
	app, _, platform := newTestApp(t)
	parent := reviewSchedule(t, app, scheduleInterval, "1h")
	if _, err := app.store.Pause(parent.ID, "api"); err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.materialize(parent, parent.NextRunAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runs, err := app.store.List(TaskFilter{ProjectID: "project-a", ParentTaskID: &parent.ID})
	if err != nil || len(runs) != 0 {
		t.Fatalf("paused snapshot materialized work: %+v %v", runs, err)
	}
	if len(platform.events) > 0 {
		t.Errorf("paused recurring snapshot dispatched %d wake(s)", len(platform.events))
	}
}

func TestReviewEditMustInvalidateDueSnapshot(t *testing.T) {
	app, _, _ := newTestApp(t)
	parent := reviewSchedule(t, app, scheduleInterval, "1h")
	title := "New instructions"
	edited, _, err := app.store.Update(parent.ID, "api", UpdateTaskInput{Title: &title, Schedule: &ScheduleInput{Every: "24h"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.materialize(parent, parent.NextRunAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	after, _ := app.store.Get(parent.ID)
	if !after.NextRunAt.Equal(*edited.NextRunAt) {
		t.Errorf("stale tick overwrote edited next run: want %v, got %v", edited.NextRunAt, after.NextRunAt)
	}
}

func TestReviewResumeFutureOneTimePreservesDeadline(t *testing.T) {
	app, _, _ := newTestApp(t)
	task := reviewSchedule(t, app, scheduleOnce, "24h")
	if _, err := app.store.Pause(task.ID, "api"); err != nil {
		t.Fatal(err)
	}
	resumed, err := app.store.Resume(task.ID, "api")
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.NextRunAt.Equal(*task.NextRunAt) {
		t.Errorf("future due moved from %v to %v", task.NextRunAt, resumed.NextRunAt)
	}
}

func TestReviewRunNowPreservesPauseAndCadence(t *testing.T) {
	app, _, _ := newTestApp(t)
	task := reviewSchedule(t, app, scheduleInterval, "24h")
	if _, err := app.store.Pause(task.ID, "api"); err != nil {
		t.Fatal(err)
	}
	requested, err := app.store.RunNow(task.ID, "api")
	if err != nil {
		t.Fatal(err)
	}
	if requested.ScheduleEnabled {
		t.Error("run-now enabled the paused recurring series")
	}
	if err := app.scheduler.Tick(time.Now().UTC().Add(time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	after, _ := app.store.Get(task.ID)
	if requested.ID == task.ID || requested.ParentTaskID != task.ID || requested.ScheduledFor == nil {
		t.Fatal("manual run did not return an independent occurrence")
	}
	if after.ScheduleEnabled {
		t.Fatal("manual run resumed the paused definition")
	}
	if !after.NextRunAt.Equal(*task.NextRunAt) {
		t.Errorf("run-now shifted cadence from %v to %v", task.NextRunAt, after.NextRunAt)
	}
}

func TestReviewRestartFindsUndispatchedOneTime(t *testing.T) {
	app, _, platform := newTestApp(t)
	task := reviewSchedule(t, app, scheduleOnce, "1m")
	if _, _, err := app.store.activateOneTime(task.ID, "tasks:scheduler", *task.NextRunAt, *task.NextRunAt); err != nil {
		t.Fatal(err)
	}
	restarted := &scheduler{store: app.store, app: app}
	if err := restarted.Tick(task.NextRunAt.Add(time.Hour), "project-a"); err != nil {
		t.Fatal(err)
	}
	current, _ := app.store.Get(task.ID)
	if current.DispatchedAt == nil || len(platform.events) == 0 {
		t.Error("activated occurrence stayed queued with no dispatch after restart")
	}
}

func TestReviewActiveExecutionMustNotBeAutoFailed(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	parent := reviewSchedule(t, app, scheduleInterval, "10m")
	run := reviewRun(t, app, parent)
	at := time.Now().UTC()
	if err := app.handleAgentEventLifecycle(ctx, agentLifecycleDelivery(run, sdk.AgentEventActive, 1, at)); err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.Tick(at.Add(2*time.Hour), "project-a"); err != nil {
		t.Fatal(err)
	}
	after, _ := app.store.Get(run.ID)
	if after.State == stateFailed {
		t.Errorf("active execution auto-failed: %s", after.Error)
	}
}

func TestReviewTerminalizationReceiptMustPreserveActiveLifecycle(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	run := reviewRun(t, app, reviewSchedule(t, app, scheduleOnce, "1m"))
	at := time.Now().UTC()
	if err := app.handleAgentEventLifecycle(ctx, agentLifecycleDelivery(run, sdk.AgentEventSettled, 1, at)); err != nil {
		t.Fatal(err)
	}
	claimedAt := at.Add(agentSettleGrace + time.Second)
	if _, err := app.store.ClaimSettledTerminalizations(claimedAt, "project-a"); err != nil {
		t.Fatal(err)
	}
	if err := app.handleAgentEventLifecycle(ctx, terminalizationLifecycleDelivery(run, sdk.AgentEventActive, 1, claimedAt)); err != nil {
		t.Fatal(err)
	}
	source := taskTerminalizationSourceID(run.ID)
	if err := app.store.RecordTerminalizationExecution(run.ID, source, "execution:"+source, run.AssignedThreadID, claimedAt); err != nil {
		t.Fatal(err)
	}
	failed, err := app.store.FailExpiredTerminalizations(claimedAt.Add(agentSettleGrace+time.Second), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) > 0 {
		t.Error("late send receipt reinstated deadline and failed active terminalization")
	}
}

func TestReviewImmediateDeliveryFailureMustBeVisible(t *testing.T) {
	app, ctx, platform := newTestApp(t)
	platform.sendErr = errors.New("temporary delivery outage")
	_, err := app.toolCreate(callerContext(7, "owner", "project-a"), ctx, map[string]any{"title": "Work", "assigned_thread_id": "worker"})
	if err == nil {
		t.Error("tool reported success despite failed assignment wake")
	}
	attempts := len(platform.events)
	if err := app.scheduler.Tick(time.Now().UTC().Add(time.Hour), "project-a"); err != nil {
		t.Fatal(err)
	}
	if len(platform.events) == attempts {
		t.Error("no durable retry for immediate assignment")
	}
}

func TestReviewHTTPReassignmentMustWakeNewOwner(t *testing.T) {
	app, _, platform := newTestApp(t)
	task, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Work", AssignedThreadID: "worker-a"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+task.ID+"?project_id=project-a", bytes.NewBufferString(`{"assigned_thread_id":"worker-b"}`))
	w := httptest.NewRecorder()
	app.handleTask(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	if len(platform.events) == 0 {
		t.Error("HTTP reassignment committed without waking worker-b")
	}
}

func TestReviewCompletionRetryMustNotDuplicateReceipt(t *testing.T) {
	app, ctx, platform := newTestApp(t)
	task, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Work", AssignedThreadID: "worker", CreatedByThreadID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := app.toolComplete(callerContext(7, "worker", "project-a"), ctx, map[string]any{"task_id": task.ID, "result": "Done"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(platform.events) != 1 {
		t.Errorf("completion retry sent %d receipts, want 1", len(platform.events))
	}
}

func TestReviewMobileUpdateActivatedOneTime(t *testing.T) {
	app, _, _ := newTestApp(t)
	run := reviewRun(t, app, reviewSchedule(t, app, scheduleOnce, "1m"))
	body := fmt.Sprintf(`{"title":%q,"description":"","state":"running","progress":10,"current_step":"Working"}`, run.Title)
	w := httptest.NewRecorder()
	app.handleTask(w, httptest.NewRequest(http.MethodPatch, "/tasks/"+run.ID+"?project_id=project-a", bytes.NewBufferString(body)))
	if w.Code != 200 {
		t.Errorf("native form cannot update activated one-time work: %d %s", w.Code, w.Body.String())
	}
}

func TestReviewMobileSummaryMustRetainOldActiveTask(t *testing.T) {
	app, _, _ := newTestApp(t)
	old, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Blocked task", State: stateBlocked, AssignedThreadID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 501; i++ {
		if _, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: fmt.Sprint(i), State: stateCompleted, AssignedThreadID: "owner"}); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/mobile/summary", nil)
	request.Header.Set("X-Apteva-Project-ID", "project-a")
	app.handleMobileSummary(w, request)
	if w.Code != 200 {
		t.Fatalf("summary status=%d: %s", w.Code, w.Body.String())
	}
	var summary mobileTaskSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}

	if summary.Counts.Active != 1 {
		t.Errorf("active count is %d; older blocked %s is missing", summary.Counts.Active, old.ID)
	}
}

func TestReviewUpgradePreservesExistingTasks(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, file := range []string{"001_init.sql", "002_rename_instance_id_to_agent_id.sql", "003_add_planning_status.sql"} {
		raw, err := os.ReadFile("migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO tasks (agent_id,title,status) VALUES (7,'Existing task','open')`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/004_durable_tasks.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("migration erased existing task: row count=%d", count)
	}
}

func TestReviewOverlapLookupMustUseIndex(t *testing.T) {
	app, _, _ := newTestApp(t)
	rows, err := app.store.db.Query(`EXPLAIN QUERY PLAN SELECT `+taskColumns+` FROM tasks WHERE parent_task_id=? AND state IN ('queued','running','waiting','blocked')`, "parent")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		if strings.Contains(detail, "SCAN tasks") {
			t.Error("overlap check performs a full task-history table scan")
		}
	}
}

func TestReviewTerminalAssignmentMustNotRestartWork(t *testing.T) {
	app, ctx, platform := newTestApp(t)
	task, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Completed work", State: stateCompleted, AssignedThreadID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.toolAssign(callerContext(7, "owner", "project-a"), ctx, map[string]any{"task_id": task.ID, "thread_id": "new-worker"})
	if err == nil && len(platform.events) > 0 {
		t.Error("terminal task reassigned and task.assigned wake sent")
	}
}

func TestReviewRecoveryMustNotOverlapAnotherRecovery(t *testing.T) {
	app, _, _ := newTestApp(t)
	run := reviewRun(t, app, reviewSchedule(t, app, scheduleOnce, "1m"))
	state := stateFailed
	if _, _, err := app.store.Update(run.ID, "owner", UpdateTaskInput{State: &state, Result: stringPtr("verified outcome"), Error: stringPtr("verified failure")}); err != nil {
		t.Fatal(err)
	}
	one, _, err := app.store.RecoverOccurrence(run.ID, "owner", "owner", "reconcile", "request-1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	two, created, err := app.store.RecoverOccurrence(run.ID, "owner", "owner", "reconcile", "request-2", time.Now().UTC())
	if err == nil && created && two.ID != one.ID {
		t.Error("two simultaneous open recovery attempts created for the same failed occurrence")
	}
}

func TestReviewTerminalizationErrorMustNotLeaveContradictoryState(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	run := reviewRun(t, app, reviewSchedule(t, app, scheduleOnce, "1m"))
	at := time.Now().UTC()
	if err := app.handleAgentEventLifecycle(ctx, agentLifecycleDelivery(run, sdk.AgentEventSettled, 1, at)); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ClaimSettledTerminalizations(at.Add(agentSettleGrace+time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	state := stateWaiting
	if _, _, err := app.store.Update(run.ID, "owner", UpdateTaskInput{State: &state, Result: stringPtr("verified outcome"), Error: stringPtr("verified failure")}); err != nil {
		t.Fatal(err)
	}
	if err := app.handleAgentEventLifecycle(ctx, terminalizationLifecycleDelivery(run, sdk.AgentEventError, 1, at.Add(12*time.Minute))); err != nil {
		t.Fatal(err)
	}
	after, _ := app.store.Get(run.ID)
	if after.State != stateFailed && after.LastOccurrenceStatus == stateFailed {
		t.Errorf("contradictory states: task=%s last_occurrence_status=%s", after.State, after.LastOccurrenceStatus)
	}
}

func stringPtr(value string) *string { return &value }

func TestReviewPaginationOrdersEqualTimestampsAndIncludesOldWork(t *testing.T) {
	app, _, _ := newTestApp(t)
	at := time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC)
	app.store.clock = func() time.Time { return at }
	old, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Old blocked", State: stateBlocked, AssignedThreadID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		_, _, err = app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: fmt.Sprint(i), AssignedThreadID: "owner"})
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 20; i++ {
		page, err := app.store.ListPage(TaskFilter{ProjectID: "project-a", View: "operational", Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && page.Tasks[0].ID != old.ID {
			t.Fatal("old blocked work did not receive priority")
		}
		for _, task := range page.Tasks {
			if seen[task.ID] {
				t.Fatal("duplicated page row")
			}
			seen[task.ID] = true
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" {
			t.Fatal("missing cursor")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 13 {
		t.Fatalf("pagination lost rows: %d", len(seen))
	}
	if _, err := app.store.ListPage(TaskFilter{ProjectID: "project-a", Cursor: "bad"}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func TestReviewHTTPRejectsMissingEvidenceAndUnknownMutationRoute(t *testing.T) {
	app, _, _ := newTestApp(t)
	task, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Evidence", AssignedThreadID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{stateCompleted, stateFailed} {
		w := httptest.NewRecorder()
		app.handleTask(w, httptest.NewRequest("PATCH", "/tasks/"+task.ID+"?project_id=project-a", strings.NewReader(fmt.Sprintf(`{"state":%q}`, state))))
		if w.Code != 400 {
			t.Fatalf("missing evidence status=%d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	app.handleTask(w, httptest.NewRequest("DELETE", "/tasks/"+task.ID+"/typo?project_id=project-a", nil))
	if w.Code != 404 {
		t.Fatalf("unknown subroute status=%d", w.Code)
	}
	current, _ := app.store.Get(task.ID)
	if current.State != stateQueued {
		t.Fatal("invalid route mutated task")
	}
}

func TestReviewDeliveryHealthIsProjectScoped(t *testing.T) {
	app, _, _ := newTestApp(t)
	if _, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Pending", AssignedThreadID: "worker", CreatedByThreadID: "owner"}); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"project-a", "project-b"} {
		w := httptest.NewRecorder()
		app.handleDeliveryHealth(w, httptest.NewRequest("GET", "/delivery-health?project_id="+project, nil))
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := float64(0)
		if project == "project-a" {
			want = 1
		}
		if got["pending"] != want {
			t.Fatalf("health scope leaked: %v", got)
		}
	}
}

func TestReviewTerminalizationOutageRetriesWithoutPrematureFailure(t *testing.T) {
	app, ctx, platform := newTestApp(t)
	run := reviewRun(t, app, reviewSchedule(t, app, scheduleOnce, "1m"))
	at := run.DispatchedAt.Add(time.Second)
	if err := app.handleAgentEventLifecycle(ctx, agentLifecycleDelivery(run, sdk.AgentEventSettled, 1, at)); err != nil {
		t.Fatal(err)
	}
	platform.sendErr = errors.New("temporary outage")
	tick := at.Add(agentSettleGrace + time.Second)
	if err := app.scheduler.Tick(tick, "project-a"); err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.Tick(tick.Add(time.Hour), "project-a"); err != nil {
		t.Fatal(err)
	}
	current, _ := app.store.Get(run.ID)
	if terminalState(current.State) {
		t.Fatalf("failed before terminalization could be delivered: %+v", current)
	}
	executions, err := app.store.AgentExecutions(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, execution := range executions {
		if execution.Purpose == agentTerminalizationPurpose && execution.DeadlineAt != nil {
			t.Fatal("undelivered reconciliation started its expiry clock")
		}
	}
	platform.sendErr = nil
	if err := app.scheduler.Tick(tick.Add(time.Hour+11*time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	executions, err = app.store.AgentExecutions(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	delivered := false
	for _, execution := range executions {
		if execution.Purpose == agentTerminalizationPurpose && execution.ExecutionID != "" && execution.DeadlineAt != nil {
			delivered = true
		}
	}
	if !delivered {
		t.Fatal("reconciliation delivery did not recover")
	}
	count := len(platform.events)
	if err := app.scheduler.Tick(tick.Add(time.Hour+12*time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	if len(platform.events) != count {
		t.Fatal("successful reconciliation redelivered")
	}
}

func TestReviewTerminalReceiptOutageSurvivesCompletionRetry(t *testing.T) {
	app, ctx, platform := newTestApp(t)
	task, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Receipt", AssignedThreadID: "worker", CreatedByThreadID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	platform.sendErr = errors.New("temporary outage")
	if _, err := app.toolComplete(callerContext(7, "worker", "project-a"), ctx, map[string]any{"task_id": task.ID, "result": "Verified"}); err != nil {
		t.Fatal(err)
	}
	platform.sendErr = nil
	at := time.Now().UTC().Add(11 * time.Second)
	if err := app.scheduler.Tick(at, "project-a"); err != nil {
		t.Fatal(err)
	}
	count := len(platform.events)
	if _, err := app.toolComplete(callerContext(7, "worker", "project-a"), ctx, map[string]any{"task_id": task.ID, "result": "Verified"}); err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.Tick(at.Add(time.Minute), "project-a"); err != nil {
		t.Fatal(err)
	}
	if len(platform.events) != count || count != 2 {
		t.Fatalf("receipt was lost or duplicated: attempts=%d final=%d", count, len(platform.events))
	}
	var pending int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM task_deliveries WHERE task_id=? AND delivered_at IS NULL`, task.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatal("receipt remained pending")
	}
}

func TestReviewOldOccurrenceIdentitySurvivesTimestampUpgrade(t *testing.T) {
	app, _, _ := newTestApp(t)
	parent := reviewSchedule(t, app, scheduleInterval, "1m")
	key := parent.NextRunAt.UTC().Format(time.RFC3339Nano)
	// Model an old process that materialized work but died before advancing its
	// parent. Even completed work must retain its original occurrence identity.
	run, _, err := app.store.Create(CreateTaskInput{AgentID: parent.AgentID, ProjectID: parent.ProjectID, Title: parent.Title, AssignedThreadID: parent.AssignedThreadID, ParentTaskID: parent.ID, ScheduledFor: parent.NextRunAt, OccurrenceKey: key, IdempotencyKey: parent.ID + ":" + key})
	if err != nil {
		t.Fatal(err)
	}
	state, result := stateCompleted, "Already verified"
	if _, _, err := app.store.Update(run.ID, run.AssignedThreadID, UpdateTaskInput{State: &state, Result: &result}); err != nil {
		t.Fatal(err)
	}
	// A completed child updates the parent rollup; the scheduler reads that
	// current snapshot but still observes the old due timestamp.
	parent, err = app.store.Get(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.scheduler.materialize(parent, parent.NextRunAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runs, err := app.store.List(TaskFilter{ProjectID: parent.ProjectID, ParentTaskID: &parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("format upgrade duplicated old occurrence: %+v", runs)
	}
}

func TestReviewRetryDecisionCommitsItsPendingDelivery(t *testing.T) {
	app, _, _ := newTestApp(t)
	run := reviewRun(t, app, reviewSchedule(t, app, scheduleOnce, "1m"))
	at := run.DispatchedAt.Add(dispatchRetryInterval + time.Second)
	_, action, err := app.store.ReconcileUnacceptedDispatch(run.ID, "scheduler", at, at.Add(-dispatchRetryInterval), dispatchMaxAttempts)
	if err != nil || action != dispatchReconcileRetry {
		t.Fatalf("reconcile=%s %v", action, err)
	}
	var pending int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM task_deliveries WHERE id=? AND delivered_at IS NULL AND next_attempt_at<=?`, "execution:"+run.ID, at.Format(timeFormat)).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatal("committed retry decision lost its pending delivery")
	}
}
