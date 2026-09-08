package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestLLMPatreonWorkflowIntentLive(t *testing.T) {
	requirePatreonTier3(t)
	c := newLocalComputerMCPClient(t)
	if c.sidecar == nil {
		t.Fatal("workflow authority regression requires the isolated sidecar and COMPUTER_PATREON_PROVIDER_CONTEXT_ID")
	}
	opened := c.call(t, "browser_session", map[string]any{"action": "open", "context_id": os.Getenv("COMPUTER_PATREON_CONTEXT_ID"), "url": requireLiveEnv(t, "COMPUTER_PATREON_CREATOR_URL")})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatal("session missing")
	}
	defer func() { closePatreonTestSession(t, c, sid) }()
	zone := stringValue(opened["effective_timezone"])
	location, err := time.LoadLocation(zone)
	if err != nil || zone == "" {
		t.Fatal("browser timezone unavailable")
	}
	at := time.Now().In(location).AddDate(0, 0, 14)
	at = time.Date(at.Year(), at.Month(), at.Day(), 19, 0, 0, 0, location)
	title := "Computer tier 3 workflow intent " + time.Now().UTC().Format("20060102-150405")
	var draft map[string]any
	if !t.Run("prepare", func(t *testing.T) {
		draft = runPatreonAgent(t, c, sid, fmt.Sprintf("On this disposable Patreon creator, create exactly ONE text draft titled %q with body Workflow intent regression. Enable Free access. Stop with the unscheduled draft Saved and Publish visible. Do not Publish or Schedule yet and do not create another draft.", title), 20)
		assertPatreonTitle(t, draft, title)
	}) {
		return
	}
	// Patreon initially exposes an ID-only draft URL, then canonicalizes it
	// with the saved title on reload. Establish the persisted resource before
	// the operator binds its exact URL; do not weaken resource enforcement.
	initialHost, initialID := patreonPostIdentity(firstNonEmpty(stringValue(draft["current_url"]), stringValue(draft["url"])))
	c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "reload"})
	c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "timeout_ms": 30000, "conditions": []any{map[string]any{"type": "text_present", "value": "Saved"}}})
	draft = liveScreenshot(t, c, sid)
	assertPatreonTitle(t, draft, title)
	u, err := url.Parse(firstNonEmpty(stringValue(draft["current_url"]), stringValue(draft["url"])))
	if err != nil {
		t.Fatal(err)
	}
	u.RawQuery, u.Fragment = "", ""
	draftURL := u.String()
	host, postID := patreonPostIdentity(draftURL)
	if postID == "" || host != initialHost || postID != initialID || !strings.HasSuffix(u.Path, "/edit") {
		t.Fatal("expected saved draft identity")
	}
	// The operator side of the test registers its authorization independently
	// of all model decisions. No MCP argument can create or replace this record.
	policy := workflowRecord{ContextID: c.contextID, WorkflowConstraint: computer.WorkflowConstraint{ID: "schedule-" + postID, ResourceURL: draftURL, AllowedEffect: "scheduled_external_commit", ScheduledAt: at.Format(time.RFC3339), Timezone: zone, DateSelector: "input[type=date]", TimeSelector: "input[type=time]"}}
	denied := c.sidecar.RequestWithHeaders("POST", "/workflow-constraints", policy, nil, map[string]string{"X-Apteva-Caller-Agent": "2"})
	if denied.Status != 403 {
		t.Fatalf("agent could register authorization: %d", denied.Status)
	}
	registered := c.sidecar.RequestWithHeaders("POST", "/workflow-constraints", policy, nil, map[string]string{workflowOperatorHeader: "1"})
	if registered.Status != 200 {
		t.Fatalf("operator registration: %s", registered.Body)
	}
	writePatreonEvidence(t, "operator-workflow.json", []byte(mustJSON(policy)))
	for _, phase := range []string{"confirmed_immediate_publish", "confirmed_immediate_publish_after_reopen"} {
		if !t.Run(phase, func(t *testing.T) {
			if strings.Contains(phase, "reopen") {
				closePatreonTestSession(t, c, sid)
				opened = c.call(t, "browser_session", map[string]any{"action": "open", "context_id": c.contextID, "url": draftURL})
				sid = stringValue(opened["session_id"])
			}
			shot := liveScreenshot(t, c, sid)
			publish := findLiveTarget(t, mapsFromAny(shot["som"]), "Publish", true)
			// Replay the exact class of mistaken HGV action, including matching
			// acknowledgement. The production guard must reject before dispatch.
			result := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "click", "label": publish["label"], "som_revision": shot["som_revision"], "expected_text": "Publish", "expected_effect": "immediate_external_commit", "confirm_consequence": "immediate_external_commit", "allowed_effect": "immediate_external_commit"})
			writePatreonEvidence(t, "rejection.json", []byte(mustJSON(patreonEvidence(result))))
			if stringValue(result["error_code"]) != "workflow_effect_mismatch" || boolFromAny(result["action_dispatched"]) {
				t.Fatalf("immediate Publish not blocked: %s", mustJSON(result))
			}
			actualHost, actualID := patreonPostIdentity(stringValue(result["current_url"]))
			if actualHost != host || actualID != postID || !strings.HasSuffix(stringValue(result["current_url"]), "/edit") {
				t.Fatal("rejected click changed draft")
			}
		}) {
			return
		}
	}
	if !t.Run("configure_authorized_schedule", func(t *testing.T) {
		goal := fmt.Sprintf("The immediate Publish click was blocked by an operator-owned schedule-only workflow. Configure this existing draft %q using Set publish date for %s at 7:00 PM in %s. Do not click Publish to look for scheduling. Preserve the body and Free access. Stop before final Schedule.", title, at.Format("2006-01-02"), zone)
		runPatreonAgent(t, c, sid, goal, 20)
	}) {
		return
	}
	if !t.Run("wrong_schedule_time", func(t *testing.T) {
		shot := observePatreonScheduleFields(t, c, sid, liveScreenshot(t, c, sid))
		target := findTemporalLiveTarget(t, mapsFromAny(shot["som"]), "time", false)
		result := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "set_temporal", "label": target["label"], "som_revision": shot["som_revision"], "value": "18:00"})
		if boolFromAny(result["failed"]) {
			t.Fatalf("could not arrange wrong time: %v", result)
		}
		result = attemptWorkflowSchedule(t, c, sid)
		writePatreonEvidence(t, "rejection.json", []byte(mustJSON(patreonEvidence(result))))
		if stringValue(result["error_code"]) != "workflow_schedule_mismatch" || boolFromAny(result["action_dispatched"]) {
			t.Fatalf("wrong-time probe did not reach the time constraint: %s", mustJSON(result))
		}
	}) {
		return
	}
	t.Run("recover_and_schedule", func(t *testing.T) {
		goal := fmt.Sprintf("Computer rejected Schedule because the draft was set to 6:00 PM, while the operator-authorized schedule is %s at 7:00 PM (%s). Restore that exact time on the existing draft %q and activate the final Schedule with the matching scheduled consequence. Never publish immediately, change the date or create another draft. Dismiss any success dialog and verify Scheduled for shows the authorized date and time.", at.Format("2006-01-02"), zone, title)
		runPatreonAgent(t, c, sid, goal, 16)
		c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "reload"})
		result := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "timeout_ms": 30000, "match": "all", "conditions": []any{map[string]any{"type": "text_present", "value": "Scheduled for " + at.Format("Jan 2, 2006") + " at 7:00 PM"}, map[string]any{"type": "url_contains", "value": postID}}})
		writePatreonEvidence(t, "scheduled-after-reload.json", []byte(mustJSON(patreonEvidence(result))))
		if !boolFromAny(result["matched"]) {
			t.Fatal("authorized schedule did not persist")
		}
		assertPatreonTitle(t, liveScreenshot(t, c, sid), title)
	})
}

func attemptWorkflowSchedule(t *testing.T, c *localComputerMCPClient, sid string) map[string]any {
	t.Helper()
	for attempt := 0; attempt < 3; attempt++ {
		c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "timeout_ms": 30000, "conditions": []any{map[string]any{"type": "text_present", "value": "Saved"}}})
		shot := liveScreenshot(t, c, sid)
		schedule := findLiveTarget(t, mapsFromAny(shot["som"]), "Schedule", true)
		result := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "click", "label": schedule["label"], "som_revision": shot["som_revision"], "expected_text": "Schedule", "expected_effect": "scheduled_external_commit", "confirm_consequence": "scheduled_external_commit"})
		writePatreonEvidence(t, fmt.Sprintf("schedule-attempt-%d.json", attempt), []byte(mustJSON(patreonEvidence(result))))
		if !strings.Contains(stringValue(result["error"]), "click rejected: stale_target:") {
			return result
		}
		// Only the explicit no-dispatch stale-node rejection permits refresh.
		// Never repeat an ambiguous or already-dispatched Schedule action.
	}
	t.Fatal("Schedule kept replacing its target before dispatch")
	return nil
}
