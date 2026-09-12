package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
	"time"
)

func processCaller(install int64, name string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{AppInstallID: install, AppName: name})
}
func TestProcessBridgeScopeAndDelivery(t *testing.T) {
	a, ctx, platform := newTestApp(t)
	args := map[string]any{"action": "create", "process_id": "process-a", "run_key": "run-a", "version": float64(1), "agent_id": float64(7), "title": "Close books", "description": "Procedure v1"}
	for _, c := range []context.Context{context.Background(), callerContext(7, "main", "project-a"), processCaller(1, "other")} {
		if _, err := a.toolProcessTask(c, ctx, args); err == nil {
			t.Fatal("unauthorized bridge access")
		}
	}
	c := processCaller(1, "processes")
	raw, err := a.toolProcessTask(c, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(*Task)
	if task.AssignedThreadID != "opaque-default" || len(platform.events) != 1 {
		t.Fatal("owner was not woken")
	}
	raw, err = a.toolProcessTask(c, ctx, args)
	if err != nil || raw.(map[string]any)["task"].(*Task).ID != task.ID {
		t.Fatal("duplicate bridge task")
	}
	for _, c := range []context.Context{processCaller(2, "processes")} {
		if _, err = a.toolProcessTask(c, ctx, map[string]any{"action": "get", "process_id": "process-a", "task_id": task.ID}); err == nil {
			t.Fatal("other install read task")
		}
	}
	if _, err = a.toolProcessTask(c, ctx.WithProject("other"), map[string]any{"action": "get", "process_id": "process-a", "task_id": task.ID}); err == nil {
		t.Fatal("other project read task")
	}
	args["process_id"] = "process-b"
	if _, err = a.toolProcessTask(c, ctx, args); err == nil {
		t.Fatal("run key reused for another process")
	}
	if a.processTool().Exposure != sdk.ToolExposureAppOnly {
		t.Fatal("bridge exposed to agents")
	}
}
func TestProcessScheduleBornPausedAndOccurrencesLinked(t *testing.T) {
	a, ctx, _ := newTestApp(t)
	c := processCaller(1, "processes")
	raw, err := a.toolProcessTask(c, ctx, map[string]any{"action": "create", "process_id": "p", "run_key": "s", "version": float64(2), "agent_id": float64(7), "title": "Weekly review", "description": "Procedure version: 2", "schedule": map[string]any{"kind": "interval", "every": "1h"}})
	if err != nil {
		t.Fatal(err)
	}
	task := raw.(map[string]any)["task"].(*Task)
	if task.ScheduleEnabled {
		t.Fatal("schedule born active")
	}
	if _, err = a.toolProcessTask(c, ctx, map[string]any{"action": "resume", "process_id": "p", "task_id": task.ID}); err != nil {
		t.Fatal(err)
	}
	// Force a due occurrence through the actual Tasks scheduler.
	_, err = a.store.db.Exec(`UPDATE tasks SET next_run_at=? WHERE id=?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.scheduler.Tick(time.Now().UTC(), "project-a"); err != nil {
		t.Fatal(err)
	}
	raw, err = a.toolProcessTask(c, ctx, map[string]any{"action": "list", "process_id": "p"})
	if err != nil {
		t.Fatal(err)
	}
	runs := raw.(map[string]any)["runs"].([]any)
	if len(runs) != 2 {
		t.Fatalf("runs=%v", runs)
	}
	var occurrence *Task
	for _, r := range runs {
		entry := r.(map[string]any)
		t1 := entry["task"].(*Task)
		if entry["version"] != 2 {
			t.Fatal("lost version")
		}
		if t1.ParentTaskID != "" {
			occurrence = t1
		}
	}
	if occurrence == nil || !strings.Contains(occurrence.Description, "version: 2") {
		t.Fatal("snapshot not inherited")
	}
	if _, err = a.toolProcessTask(c, ctx, map[string]any{"action": "pause", "process_id": "p", "task_id": task.ID}); err != nil {
		t.Fatal(err)
	}
	existing, _ := a.store.Get(occurrence.ID)
	if existing.State != stateQueued {
		t.Fatal("pause altered occurrence")
	}
}
