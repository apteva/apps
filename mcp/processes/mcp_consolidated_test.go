package main

import (
	"context"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestConsolidatedStatusToolsRunTheSameActions(t *testing.T) {
	a, _, p := directSetup(t)
	tools := map[string]sdk.Tool{}
	for _, tool := range a.MCPTools() {
		tools[tool.Name] = tool
	}
	for name := range mergedStatusTools {
		if _, ok := tools[name]; ok {
			t.Fatalf("merged tool %s still registered", name)
		}
	}
	ctx := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7, ThreadID: "t", ProjectID: "project-a"})
	if _, err := tools["status"].HandlerCtx(ctx, a.ctx, map[string]any{"process_id": p.ID, "status": "bogus"}); err == nil {
		t.Fatal("unknown status accepted")
	}
	if _, err := tools["status"].HandlerCtx(ctx, a.ctx, map[string]any{"process_id": p.ID, "status": "paused"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := a.get("project-a", p.ID)
	if err != nil || fresh.Status != "paused" {
		t.Fatalf("status tool did not pause: %+v %v", fresh, err)
	}
	listed, err := tools["assignments"].HandlerCtx(ctx, a.ctx, map[string]any{"process_id": p.ID})
	if err != nil {
		t.Fatal(err)
	}
	_ = listed
	if action, _, _ := statusAction("assignments", map[string]any{"assignment_id": "x"}); action != "assignment_get" {
		t.Fatal("assignment_id did not route to assignment_get")
	}
	if action, rest, _ := statusAction("assignment_status", map[string]any{"status": "archived", "assignment_id": "x"}); action != "assignment_archive" || rest["status"] != nil {
		t.Fatalf("assignment_status routed to %s %v", action, rest)
	}
	if action, _, _ := statusAction("trigger_status", map[string]any{"status": "active"}); action != "trigger_activate" {
		t.Fatal("trigger_status did not route to trigger_activate")
	}
}
