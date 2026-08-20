package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	mu     sync.Mutex
	events []threadEventCall
	agents map[int64]*sdk.PlatformInstance
}

func (p *recordingPlatform) SendThreadEvent(target sdk.ThreadRef, message any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, threadEventCall{Target: target, Message: message})
	return nil
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

	running, progress, step := stateRunning, 25, "Reviewing conversations"
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
		{thread: "conversation-a", title: "Created by conversation"},
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
	raw, err := app.toolList(callerContext(7, "conversation-reader", "project-a"), appCtx, map[string]any{
		"created_by_me": true, "assigned_thread_id": "conversation-reader",
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
	if len(live) != 1 || live[0].event.ToState != stateQueued || live[0].task.State != stateQueued || live[0].task.ScheduleEnabled || live[0].task.NextRunAt != nil {
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
	if len(live) != 3 || live[1].task.State != stateRunning || live[1].task.ScheduleEnabled || live[2].task.State != stateCompleted || live[2].task.ScheduleEnabled {
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
	if _, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "Conversation-created", CreatedByThreadID: "conversation-a", AssignedThreadID: "conversation-a"}); err != nil {
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

func TestManifestAndToolContract(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if manifest.Name != "tasks" || manifest.Version != "3.2.9" || manifest.Icon != "/ui/icon.svg" || manifest.IconStyle != "monochrome" || len(manifest.Provides.UIComponents) != 3 || len(manifest.Provides.UISurfaces) != 1 || len(manifest.Provides.Skills) != 1 {
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
			for _, required := range []string{"Keep the task running while any executor is actively working", "Use waiting only when no executor can progress", "at least two or three intermediate milestones"} {
				if !strings.Contains(tool.Description, required) {
					t.Fatalf("update tool description missing progress rule %q: %s", required, tool.Description)
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
		"Keep a task `running` while any executor is actively working",
		"at least two or three intermediate milestones",
		"changing threads or entering `waiting` does not by itself increase progress",
	} {
		if !strings.Contains(normalizedSkill, required) {
			t.Fatalf("Tasks skill missing classification rule %q", required)
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
	if parent.LastRunAt == nil || parent.NextRunAt == nil {
		t.Fatalf("materialization did not stamp run bookkeeping: %+v", parent)
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
