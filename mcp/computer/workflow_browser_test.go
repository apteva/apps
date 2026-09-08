package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestWorkflowConstraintBrowserDispatchLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1")
	}
	var immediate, scheduled atomic.Int32
	deadline := time.Now().AddDate(0, 0, 2).Truncate(time.Minute)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/immediate" {
			immediate.Add(1)
			return
		}
		if r.URL.Path == "/scheduled" {
			scheduled.Add(1)
			return
		}
		fmt.Fprintf(w, `<html><body><input type="date" value="%s"><input type="time" value="%s"><button type="button" id="publish" onclick="fetch('/immediate')">Publish</button><button type="button" id="schedule" onclick="fetch('/scheduled')">Schedule</button></body></html>`, deadline.Format("2006-01-02"), deadline.Format("15:04"))
	}))
	defer fixture.Close()
	sc := tk.SpawnSidecar(t, ".")
	c := &localComputerMCPClient{sidecar: sc}
	created := sc.MCP("computer_context_create", map[string]any{"name": "workflow fixture", "backend": "local"})
	id := stringValue(created["context"].(map[string]any)["id"])
	u := fixture.URL + "/posts/1/edit"
	opened := c.call(t, "browser_session", map[string]any{"action": "open", "context_id": id, "url": u})
	sid := stringValue(opened["session_id"])
	defer func() { closePatreonTestSession(t, c, sid) }()
	zone := stringValue(opened["effective_timezone"])
	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture renders host-local wall time; register that same wall time in
	// the browser's timezone, which the live guard checks independently.
	deadline = time.Date(deadline.Year(), deadline.Month(), deadline.Day(), deadline.Hour(), deadline.Minute(), 0, 0, location)
	p := workflowRecord{ContextID: id, WorkflowConstraint: computer.WorkflowConstraint{ID: "fixture-workflow", ResourceURL: u, AllowedEffect: "scheduled_external_commit", ScheduledAt: deadline.Format(time.RFC3339), Timezone: zone, DateSelector: "input[type=date]", TimeSelector: "input[type=time]"}}
	resp := sc.RequestWithHeaders("POST", "/workflow-constraints", p, nil, map[string]string{workflowOperatorHeader: "1"})
	if resp.Status != 200 {
		t.Fatalf("policy registration: %s", resp.Body)
	}
	for _, mode := range []string{"label", "target_id", "selector", "coordinate", "double_click", "batch", "reopened"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "reopened" {
				closePatreonTestSession(t, c, sid)
				opened = c.call(t, "browser_session", map[string]any{"action": "open", "context_id": id, "url": u})
				sid = stringValue(opened["session_id"])
			}
			shot := liveScreenshot(t, c, sid)
			target := findLiveTarget(t, mapsFromAny(shot["som"]), "Publish", true)
			args := map[string]any{"session_id": sid, "action": "click", "expected_text": "Publish", "expected_effect": "immediate_external_commit", "confirm_consequence": "immediate_external_commit", "som_revision": shot["som_revision"], "allowed_effect": "immediate_external_commit", "workflow_id": "invented"}
			switch mode {
			case "target_id":
				args["target_id"] = target["id"]
			case "selector":
				args["selector"] = "#publish"
			case "coordinate":
				args["coordinate"] = fmt.Sprintf("%d,%d", intFromAny(target["x"])+intFromAny(target["w"])/2, intFromAny(target["y"])+intFromAny(target["h"])/2)
			case "double_click":
				args["action"] = "double_click"
				args["label"] = target["label"]
			default:
				args["label"] = target["label"]
			}
			if mode == "batch" {
				args = map[string]any{"session_id": sid, "action": "batch", "steps": []any{args}}
			}
			result := c.call(t, "computer_use", args)
			if stringValue(result["error_code"]) != "workflow_effect_mismatch" || boolFromAny(result["action_dispatched"]) {
				t.Fatalf("workflow did not reject %s: %s", mode, mustJSON(result))
			}
			if immediate.Load() != 0 || scheduled.Load() != 0 {
				t.Fatal("rejected click dispatched")
			}
		})
	}
	shot := liveScreenshot(t, c, sid)
	target := findLiveTarget(t, mapsFromAny(shot["som"]), "Schedule", true)
	result := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "click", "label": target["label"], "som_revision": shot["som_revision"], "expected_text": "Schedule", "expected_effect": "scheduled_external_commit", "confirm_consequence": "scheduled_external_commit"})
	if !boolFromAny(result["action_dispatched"]) || scheduled.Load() != 1 || immediate.Load() != 0 {
		t.Fatalf("authorized schedule: %s counters %d/%d", mustJSON(result), scheduled.Load(), immediate.Load())
	}
}
