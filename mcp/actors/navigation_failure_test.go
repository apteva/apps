package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExhaustedNavigationLeavesCoverageIncompleteAndNeverCreates(t *testing.T) {
	platform := newFakePlatform()
	current := ""
	navigations := 0
	platform.readViewsResponse = func(tool string, args map[string]any) map[string]any {
		switch tool {
		case "computer.browser_open":
			current = stringFromAny(args["url"])
		case "computer.browser_extract":
			return map[string]any{"rendered": true, "html": readFixtureHTML(1), "current_url": current}
		case "computer.computer_use":
			if args["action"] == "navigate" {
				navigations++
				if args["read_only"] != true {
					t.Fatal("inspection did not declare read-only navigation")
				}
				return map[string]any{"success": false, "failed": true, "error": "navigation_failed: net::ERR_HTTP2_SERVER_REFUSED_STREAM", "coverage_incomplete": true, "retry_exhausted": true, "retryable": false}
			}
		}
		return nil
	}
	ctx, app := newTestCtx(t, platform)
	views := fixtureReadViews()
	views.Views = views.Views[:1]
	b, _ := json.Marshal(views)
	var conf map[string]any
	json.Unmarshal(b, &conf)
	rec := saveFixtureActor(t, ctx, app, map[string]any{
		"schema_version": 1, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"step_retries": 3},
		"steps": []any{
			map[string]any{"action": "inspect_views", "read_only": true, "read_views": conf},
			map[string]any{"action": "click", "locator": map[string]any{"selector": "button#save"}, "effect_phase": "commit", "once_key": "new-performer", "expected_text": "Save", "expected_effect": "record_create", "confirm_consequence": "record_create"},
		},
	})
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
		t.Fatal(err)
	}
	run, err := claimActorRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = app.executeActorRun(context.Background(), ctx, run)
	result, err := getActorRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := mapFromAny(result["output"])
	coverage := mapFromAny(out["coverage"])
	progress := mapFromAny(out["execution_progress"])
	if result["status"] != "failed" || !strings.Contains(stringFromAny(result["error"]), "coverage incomplete") || coverage["inspection_complete"] != false || coverage["more_results_remaining"] != true {
		t.Fatalf("coverage incorrectly accepted: %v", result)
	}
	if progress["commit_attempted"] != false || progress["preparation_attempted"] != false {
		t.Fatalf("write started after incomplete read: %v", progress)
	}
	if navigations != 1 {
		t.Fatalf("exhausted read was retried by actor: %d", navigations)
	}
	for _, call := range platform.callsSnapshot() {
		if call.tool == "computer_use" && (call.args["action"] == "click" || call.args["action"] == "upload_file" || call.args["action"] == "type") {
			t.Fatal("write dispatched after exhausted navigation")
		}
	}
}
