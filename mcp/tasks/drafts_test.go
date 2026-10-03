package main

import (
	"context"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func operatorContext(project, id string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: project, SubjectType: "operator", SubjectID: id})
}

func TestDraftStartValidatesInputsAndDispatchesOnce(t *testing.T) {
	app, ctx, platform := newTestApp(t)
	op := operatorContext("project-a", "operator-1")
	createdRaw, err := app.toolCreate(op, ctx, map[string]any{
		"state": "draft", "title": "Investigate a ticket", "description": "Read and investigate", "expected_outcome": "Findings",
		"inputs": []any{map[string]any{"key": "ticket_reference", "label": "Ticket", "required": true}}, "suggested_agent_id": 7,
		"idempotency_key": "preset:investigate-ticket",
	})
	if err != nil {
		t.Fatal(err)
	}
	draft := createdRaw.(map[string]any)["task"].(*Task)
	if draft.State != stateDraft || draft.AgentID != 0 || draft.AssignedThreadID != "" || draft.ScheduleKind != "" {
		t.Fatalf("invalid draft: %+v", draft)
	}
	if _, err := app.toolStart(op, ctx, map[string]any{"task_id": draft.ID}); err == nil {
		t.Fatal("expected required input validation")
	}
	_, _, err = app.store.Update(draft.ID, "operator-1", UpdateTaskInput{Inputs: &[]TaskInput{{Key: "ticket_reference", Label: "Ticket", Required: true, Value: "T-42"}}})
	if err != nil {
		t.Fatal(err)
	}
	startedRaw, err := app.toolStart(op, ctx, map[string]any{"task_id": draft.ID})
	if err != nil {
		t.Fatal(err)
	}
	started := startedRaw.(map[string]any)["task"].(*Task)
	if started.State != stateQueued || started.AgentID != 7 || started.AssignedThreadID != "opaque-default" {
		t.Fatalf("invalid started task: %+v", started)
	}
	retryRaw, err := app.toolStart(op, ctx, map[string]any{"task_id": draft.ID})
	if err != nil {
		t.Fatal(err)
	}
	if retryRaw.(map[string]any)["started"].(bool) {
		t.Fatal("repeated start launched task twice")
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.events) != 1 {
		t.Fatalf("expected one execution event, got %d", len(platform.events))
	}
}

func TestDraftReapplyKeepsUserEditsAfterStart(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	op := operatorContext("project-a", "operator-1")
	args := map[string]any{"state": "draft", "title": "Ticket", "inputs": []any{map[string]any{"key": "ref", "label": "Ref", "required": true}}, "idempotency_key": "stable"}
	first, err := app.toolCreate(op, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	task := first.(map[string]any)["task"].(*Task)
	value := []TaskInput{{Key: "ref", Label: "Ref", Required: true, Value: "https://tickets/T-1"}}
	if _, _, err = app.store.Update(task.ID, "operator-1", UpdateTaskInput{Title: strPtr("User title"), Inputs: &value}); err != nil {
		t.Fatal(err)
	}
	if _, err = app.toolStart(op, ctx, map[string]any{"task_id": task.ID, "agent_id": 7}); err != nil {
		t.Fatal(err)
	}
	reapplied, err := app.toolCreate(op, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	got := reapplied.(map[string]any)["task"].(*Task)
	if got.ID != task.ID || got.Title != "User title" || got.Inputs[0].Value != "https://tickets/T-1" {
		t.Fatalf("reapply replaced edits: %+v", got)
	}
}

func strPtr(value string) *string { return &value }
