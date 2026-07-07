package main

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestToolTrackUsesCurrentProjectAndRejectsSpoofedProject(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	app := &App{}

	if _, err := app.toolTrack(ctx, map[string]any{
		"event":      "page_view",
		"app":        "site",
		"project_id": "ashley-hypnotized",
	}); err == nil || !strings.Contains(err.Error(), "project_id is assigned by the platform") {
		t.Fatalf("spoofed project_id error = %v, want platform-owned project error", err)
	}

	if _, err := app.toolTrack(ctx, map[string]any{
		"event": "page_view",
		"app":   "site",
		"props": map[string]any{"path": "/"},
	}); err != nil {
		t.Fatalf("track current project: %v", err)
	}

	rows, err := queryRows(ctx.AppDB(), Filter{}, 10)
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 1 || rows[0].ProjectID != "h-sites" {
		t.Fatalf("rows = %#v, want one h-sites row", rows)
	}
}

func TestQueryToolsDefaultToCurrentProjectAndRejectSpoofedProject(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	app := &App{}

	for _, ev := range []EventInsert{
		{TS: 1, App: "site", Topic: "page_view", ProjectID: "h-sites", Source: "test", Props: `{"path":"/h"}`},
		{TS: 2, App: "site", Topic: "page_view", ProjectID: "ashley-hypnotized", Source: "test", Props: `{"path":"/a"}`},
	} {
		if _, err := insertEvent(ctx.AppDB(), ev); err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
	}

	got, err := app.toolQuery(ctx, map[string]any{"app": "site"})
	if err != nil {
		t.Fatalf("query current project: %v", err)
	}
	events := got.(map[string]any)["events"].([]EventRow)
	if len(events) != 1 || events[0].ProjectID != "h-sites" {
		t.Fatalf("events = %#v, want only h-sites", events)
	}

	if _, err := app.toolQuery(ctx, map[string]any{
		"app":        "site",
		"project_id": "ashley-hypnotized",
	}); err == nil || !strings.Contains(err.Error(), "project_id is assigned by the platform") {
		t.Fatalf("spoofed query error = %v, want platform-owned project error", err)
	}
}

func TestEventSpecToolsCannotWriteAcrossProjects(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	app := &App{}

	if _, err := app.toolEventSpecUpsert(ctx, map[string]any{
		"project_id": "ashley-hypnotized",
		"app":        "patreon",
		"event":      "daily_earnings_snapshot",
	}); err == nil || !strings.Contains(err.Error(), "project_id is assigned by the platform") {
		t.Fatalf("spoofed spec upsert error = %v, want platform-owned project error", err)
	}

	got, err := app.toolEventSpecUpsert(ctx, map[string]any{
		"app":   "patreon",
		"event": "daily_earnings_snapshot",
	})
	if err != nil {
		t.Fatalf("spec upsert: %v", err)
	}
	spec := got.(map[string]any)["spec"].(*EventSpec)
	if spec.ProjectID != "h-sites" {
		t.Fatalf("spec project = %q, want h-sites", spec.ProjectID)
	}

	if _, err := app.toolEventSpecDelete(ctx.WithProject("ashley-hypnotized"), map[string]any{
		"id": spec.ID,
	}); err == nil || !strings.Contains(err.Error(), "not found in current project") {
		t.Fatalf("cross-project delete error = %v, want not found", err)
	}
	if _, err := getEventSpecByID(ctx.AppDB(), spec.ID); err != nil {
		t.Fatalf("spec should remain after cross-project delete: %v", err)
	}
}

func TestTrackValidationFailureReturnsSpecHints(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	app := &App{}

	if _, err := app.toolEventSpecUpsert(ctx, map[string]any{
		"app":             "patreon",
		"event":           "daily_earnings_snapshot",
		"validation_mode": "reject",
		"properties": []any{
			map[string]any{"key": "props.page_id", "type": "string", "required": true, "example_value": "monika-hypnosis-reactions"},
			map[string]any{"key": "props.currency", "type": "string", "required": true, "example_value": "USD"},
		},
	}); err != nil {
		t.Fatalf("spec upsert: %v", err)
	}

	got, err := app.toolTrack(ctx, map[string]any{
		"app":   "patreon",
		"event": "daily_earnings_snapshot",
		"props": map[string]any{"page_id": "monika-hypnosis-reactions"},
	})
	if err != nil {
		t.Fatalf("track validation failure should return hints, got error: %v", err)
	}
	resp := got.(map[string]any)
	if resp["error"] != "missing_required" || resp["spec"] != "patreon.daily_earnings_snapshot" {
		t.Fatalf("validation response = %#v, want missing_required spec hint", resp)
	}
	summary := resp["summary"].(map[string]any)
	missing := summary["missing"].([]string)
	if len(missing) != 1 || missing[0] != "props.currency" {
		t.Fatalf("missing = %#v, want props.currency", missing)
	}
	example := resp["example"].(map[string]any)
	props := example["props"].(map[string]any)
	if props["page_id"] != "monika-hypnosis-reactions" || props["currency"] != "USD" {
		t.Fatalf("example props = %#v, want page_id and currency hints", props)
	}

	n, err := countEvents(ctx.AppDB(), Filter{ProjectID: "h-sites"})
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 0 {
		t.Fatalf("reject-mode validation inserted %d events, want 0", n)
	}
}
