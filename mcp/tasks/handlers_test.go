package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	spawns []sdk.ThreadSpawnRequest
	agents map[int64]*sdk.PlatformInstance
}

func (p *recordingPlatform) SendThreadEvent(target sdk.ThreadRef, message any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, threadEventCall{Target: target, Message: message})
	return nil
}

func (p *recordingPlatform) SpawnThread(req sdk.ThreadSpawnRequest) (*sdk.ThreadSpawnResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.spawns = append(p.spawns, req)
	return &sdk.ThreadSpawnResult{Status: "created", Thread: sdk.ThreadRef{AgentID: req.AgentID, ThreadID: req.ThreadID}}, nil
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
	if _, _, err := app.store.Update(created.ID, "thread-a7f3", UpdateTaskInput{State: &running, Progress: &progress, CurrentStep: &step}); err != nil {
		t.Fatal(err)
	}
	completedRaw, err := app.toolComplete(creator, appCtx, map[string]any{"task_id": created.ID, "result": "CRM is healthy"})
	if err != nil {
		t.Fatal(err)
	}
	completed := completedRaw.(*Task)
	if completed.State != stateCompleted || completed.Progress == nil || *completed.Progress != 100 || completed.Result != "CRM is healthy" {
		t.Fatalf("unexpected completion: %+v", completed)
	}
	events, err := app.store.Events(created.ID)
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if got := len(platform.events); got != 0 {
		t.Fatalf("creator and assignee are identical; got %d duplicate terminal receipts", got)
	}
}

func TestAssignmentSpawnAndSingleCreatorReceipt(t *testing.T) {
	app, appCtx, platform := newTestApp(t)
	creator := callerContext(7, "thread-origin", "project-a")
	raw, err := app.toolCreate(creator, appCtx, map[string]any{"title": "Prepare briefing"})
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(*Task)

	spawnedRaw, err := app.toolSpawnThread(creator, appCtx, map[string]any{
		"task_id": task.ID, "thread_id": "thread-exec-91", "instructions": "Use current CRM data.",
	})
	if err != nil {
		t.Fatal(err)
	}
	spawned := spawnedRaw.(map[string]any)["task"].(*Task)
	if spawned.AssignedThreadID != "thread-exec-91" || spawned.ExecutionThreadID != "thread-exec-91" {
		t.Fatalf("spawn assignment missing: %+v", spawned)
	}
	if len(platform.spawns) != 1 || platform.spawns[0].ThreadID != "thread-exec-91" {
		t.Fatalf("spawn calls=%+v", platform.spawns)
	}
	if len(platform.events) != 1 || platform.events[0].Target.ThreadID != "thread-exec-91" {
		t.Fatalf("assignment receipt=%+v", platform.events)
	}

	executor := callerContext(7, "thread-exec-91", "project-a")
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

func TestAssociatedThreadFilterUsesOnlyOpaqueTaskRelationships(t *testing.T) {
	app, appCtx, _ := newTestApp(t)
	creator := callerContext(7, "thread-origin-31", "project-a")
	raw, err := app.toolCreate(creator, appCtx, map[string]any{"title": "Related work"})
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(*Task)
	if _, err := app.toolAssign(creator, appCtx, map[string]any{"task_id": task.ID, "thread_id": "thread-owner-92"}); err != nil {
		t.Fatal(err)
	}

	for _, threadID := range []string{"thread-origin-31", "thread-owner-92"} {
		tasks, err := app.store.List(TaskFilter{ProjectID: "project-a", AgentID: 7, AssociatedThread: threadID})
		if err != nil || len(tasks) != 1 || tasks[0].ID != task.ID {
			t.Fatalf("associated thread %q tasks=%+v err=%v", threadID, tasks, err)
		}
	}
	tasks, err := app.store.List(TaskFilter{ProjectID: "project-a", AgentID: 7, AssociatedThread: "unrelated-thread"})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("unrelated opaque thread tasks=%+v err=%v", tasks, err)
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
	if err := app.scheduler.Tick(once.NextRunAt.Add(time.Second), "project-a"); err != nil {
		t.Fatal(err)
	}
	once, _ = app.store.Get(once.ID)
	if once.State != stateQueued || once.ScheduleEnabled || once.ScheduledFor == nil {
		t.Fatalf("one-time task should become the runnable task itself: %+v", once)
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
	if manifest.Name != "tasks" || manifest.Version != "3.1.1" || manifest.Icon != "/ui/icon.svg" || manifest.IconStyle != "monochrome" || len(manifest.Provides.UIComponents) != 4 || len(manifest.Provides.Skills) != 1 {
		t.Fatalf("manifest surfaces incomplete: %+v", manifest.Provides)
	}
	overview := manifest.Provides.UIComponents[0]
	if overview.Name != "task-overview" || overview.DefaultSize != "half" || len(overview.SupportedSizes) != 2 || overview.SettingsSchema["type"] != "object" {
		t.Fatalf("task overview widget contract incomplete: %+v", overview)
	}
	if len(manifest.Provides.UIPanels) != 1 || !manifest.Provides.UIPanels[0].Suggested {
		t.Fatalf("task page should be a generic suggested sidebar contribution: %+v", manifest.Provides.UIPanels)
	}
	want := map[string]bool{"create": false, "list": false, "get": false, "update": false, "assign": false, "spawn_thread": false, "complete": false, "cancel": false, "pause": false, "resume": false, "run_now": false}
	for _, tool := range app.MCPTools() {
		if _, ok := want[tool.Name]; !ok {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		want[tool.Name] = true
		if tool.HandlerCtx == nil || tool.Description == "" || tool.InputSchema["type"] != "object" {
			t.Fatalf("invalid tool contract: %+v", tool)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing tool %q", name)
		}
	}
}
