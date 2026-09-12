package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeTasks struct {
	tk.BasePlatformClient
	tasks      map[string]map[string]any
	keys       map[string]string
	fail       string
	loseCreate bool
	creates    int
	calls      []string
}

func (f *fakeTasks) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	project := "project-a"
	if id == 9 {
		project = "other"
	}
	return &sdk.PlatformInstance{ID: id, ProjectID: project, DefaultThreadID: "owner-default"}, nil
}
func (f *fakeTasks) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app != "tasks" || tool != "process_task" {
		return errors.New("unexpected dependency")
	}
	action := input["action"].(string)
	f.calls = append(f.calls, action)
	if f.fail == action {
		return errors.New("dependency offline")
	}
	var task map[string]any
	switch action {
	case "create":
		key := input["run_key"].(string)
		if id := f.keys[key]; id != "" {
			task = f.tasks[id]
		} else {
			f.creates++
			id := fmt.Sprintf("task-%d", f.creates)
			f.keys[key] = id
			task = map[string]any{"id": id, "title": input["title"], "description": input["description"], "state": "queued", "schedule_enabled": false}
			if input["schedule"] != nil {
				task["schedule_kind"] = "interval"
			}
			f.tasks[id] = task
		}
		if f.loseCreate {
			f.loseCreate = false
			return errors.New("response lost after create")
		}
	case "get", "pause", "resume":
		task = f.tasks[input["task_id"].(string)]
		if task == nil {
			return errors.New("task missing")
		}
		if action != "get" {
			task["schedule_enabled"] = action == "resume"
		}
	case "list":
		raw, _ := json.Marshal(map[string]any{"runs": []any{}, "has_more": false})
		return json.Unmarshal(raw, out)
	default:
		return errors.New("unexpected action")
	}
	raw, _ := json.Marshal(map[string]any{"task": task})
	return json.Unmarshal(raw, out)
}
func setup(t *testing.T) (*App, *fakeTasks) {
	t.Helper()
	f := &fakeTasks{tasks: map[string]map[string]any{}, keys: map[string]string{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	return a, f
}
func def() Definition {
	return Definition{Name: "Monthly close", Instructions: "Reconcile invoices. Request approval.", CompletionCriteria: "Approved report and evidence", OwnerAgentID: 7}
}
func create(t *testing.T, a *App, d Definition) *Process {
	t.Helper()
	p, err := a.save("project-a", "", "operator", 0, d)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func status(t *testing.T, a *App, id, s string) *Process {
	t.Helper()
	p, err := a.changeStatus("project-a", id, s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestVersionedRunsAndScheduleLifecycle(t *testing.T) {
	a, f := setup(t)
	d := def()
	d.Schedule = &Schedule{Kind: "interval", Every: "24h"}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	if p.SyncPending || f.creates != 1 {
		t.Fatalf("activation=%+v creates=%d", p, f.creates)
	}
	first := f.tasks["task-1"]
	if first["schedule_enabled"] != true {
		t.Fatal("schedule not enabled")
	}
	if _, err := a.save("project-a", p.ID, "operator", 1, d); err == nil {
		t.Fatal("active edit accepted")
	}
	raw, err := a.start("project-a", p.ID, "august", "August records")
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(map[string]any)
	if !strings.Contains(task["description"].(string), "Procedure version: 1") || !strings.Contains(task["description"].(string), "August records") {
		t.Fatal("snapshot missing")
	}
	p = status(t, a, p.ID, "paused")
	if first["schedule_enabled"] != false || p.SyncPending {
		t.Fatal("pause failed")
	}
	d.Instructions = "New procedure"
	p, err = a.save("project-a", p.ID, "operator", 1, d)
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 2 || p.Status != "draft" {
		t.Fatalf("revision=%+v", p)
	}
	p = status(t, a, p.ID, "active")
	if p.SyncPending || f.creates != 3 || first["schedule_enabled"] != false {
		t.Fatal("old schedule reactivated")
	}
	if strings.Contains(first["description"].(string), "New procedure") {
		t.Fatal("old snapshot mutated")
	}
	if _, err = a.save("project-a", p.ID, "operator", 1, d); !errors.Is(err, errConflict) {
		t.Fatalf("stale update=%v", err)
	}
	p = status(t, a, p.ID, "archived")
	if p.SyncPending || f.tasks["task-3"]["schedule_enabled"] != false {
		t.Fatal("archive did not stop schedule")
	}
	if _, err = a.start("project-a", p.ID, "new", "x"); err == nil {
		t.Fatal("archived process started")
	}
	if _, err = a.start("project-a", p.ID, "august", "August records"); err != nil {
		t.Fatal("original execution retry failed", err)
	}
	if f.creates != 3 {
		t.Fatal("duplicate execution")
	}
}
func TestLostResponseAndOfflinePauseRecover(t *testing.T) {
	a, f := setup(t)
	d := def()
	d.Schedule = &Schedule{Kind: "interval", Every: "1h"}
	p := create(t, a, d)
	f.loseCreate = true
	p = status(t, a, p.ID, "active")
	if !p.SyncPending || f.creates != 1 || f.tasks["task-1"]["schedule_enabled"] != false {
		t.Fatal("unknown schedule was not left safely paused")
	}
	if err := a.retryPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = a.get("project-a", p.ID)
	if p.SyncPending || f.creates != 1 {
		t.Fatal("retry duplicated schedule")
	}
	f.fail = "pause"
	p = status(t, a, p.ID, "paused")
	if !p.SyncPending || p.SyncError == "" {
		t.Fatal("pause failure hidden")
	}
	if _, err := a.save("project-a", p.ID, "operator", 1, d); err == nil {
		t.Fatal("edit accepted before pause confirmed")
	}
	f.fail = ""
	if err := a.retryPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = a.get("project-a", p.ID)
	if p.SyncPending || f.tasks["task-1"]["schedule_enabled"] != false {
		t.Fatal("pause not recovered")
	}
}
func TestManualIdempotencyAndRestartRecovery(t *testing.T) {
	a, f := setup(t)
	p := create(t, a, def())
	status(t, a, p.ID, "active")
	f.loseCreate = true
	if _, err := a.start("project-a", p.ID, "same", "inputs"); err == nil {
		t.Fatal("expected lost response")
	}
	restarted := &App{}
	if err := restarted.OnMount(a.ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.retryPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.start("project-a", p.ID, "same", "inputs"); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatal("duplicate after restart")
	}
	if _, err := a.start("project-a", p.ID, "same", "changed"); err == nil {
		t.Fatal("changed idempotent payload accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.execute("project-a", "operator", "start", map[string]any{"process_id": p.ID, "idempotency_key": "parallel", "inputs": "same"})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.creates != 2 {
		t.Fatal("concurrent starts duplicated work")
	}
}
func TestProjectIsolationAndValidation(t *testing.T) {
	a, _ := setup(t)
	p := create(t, a, def())
	if _, err := a.get("other", p.ID); !errors.Is(err, errNotFound) {
		t.Fatal("cross-project read")
	}
	for _, action := range []string{"get", "update", "start", "runs", "pause", "activate", "archive"} {
		if _, err := a.execute("other", "operator", action, map[string]any{"process_id": p.ID, "idempotency_key": "key", "definition": def()}); err == nil {
			t.Fatalf("cross-project %s", action)
		}
	}
	d := def()
	d.OwnerAgentID = 9
	if _, err := a.save("project-a", "", "operator", 0, d); err == nil {
		t.Fatal("foreign owner accepted")
	}
	d = def()
	d.Schedule = &Schedule{Kind: "interval", Every: "1s"}
	if err := d.validate(); err == nil {
		t.Fatal("invalid interval")
	}
	d.Schedule = &Schedule{Kind: "cron", Cron: "0 9 * * 1", Timezone: "Europe/Madrid"}
	if err := d.validate(); err != nil {
		t.Fatal(err)
	}
	d.Schedule.Timezone = "bad"
	if err := d.validate(); err == nil {
		t.Fatal("invalid timezone")
	}
	for _, tool := range a.MCPTools() {
		if _, err := tool.HandlerCtx(context.Background(), a.ctx, map[string]any{}); err == nil {
			t.Fatalf("%s accepted anonymous caller", tool.Name)
		}
	}
	r := httptest.NewRequest("GET", "/processes/"+p.ID+"?project_id=other", nil)
	r.Header.Set("X-Apteva-Project-ID", "project-a")
	w := httptest.NewRecorder()
	a.handleHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("mismatched header query=%d", w.Code)
	}
}
func TestManifestAndPanel(t *testing.T) {
	a, _ := setup(t)
	m := a.Manifest()
	if m.Name != "processes" || len(m.Requires.Apps) != 1 || m.Requires.Apps[0].Name != "tasks" || m.Requires.Apps[0].Version != ">=3.6.0" {
		t.Fatalf("dependency=%+v", m.Requires)
	}
	if len(m.Provides.UIPanels) != 1 || m.Provides.UIPanels[0].Slot != "project.page" {
		t.Fatal("panel missing")
	}
	if len(a.MCPTools()) != len(m.Provides.MCPTools) {
		t.Fatal("tool manifest drift")
	}
}
