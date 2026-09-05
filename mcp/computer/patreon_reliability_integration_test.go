package main

import (
	"net/url"
	"os"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// TestComputerAppPatreonReliabilityFixture reproduces the current Patreon
// composer failure shapes in one deterministic browser flow: unnamed switches
// beside row labels, a persistently busy page ancestor, incidental post/publish
// copy, a >120 character accessible name, live-node replacement, checked-state
// verification, and a final Schedule consequence.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppPatreonReliabilityFixture -timeout 3m .
func TestComputerAppPatreonReliabilityFixture(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1")
	}
	if backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND")); backend != "" && backend != "local" {
		t.Skip("the deterministic Patreon fixture uses the local browser backend")
	}

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	opened := sc.MCP("browser_session", map[string]any{
		"action": "open", "backend": "local", "url": patreonReliabilityDataURL(),
		"viewport": map[string]any{"width": 1100, "height": 760},
	})
	sessionID := stringValue(opened["session_id"])
	if sessionID == "" {
		t.Fatalf("open returned no session id: %v", opened)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sessionID})

	shot := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "screenshot", "include_som": true,
	})
	targets := mapsFromAny(shot["som"])
	createPost := findMapByString(t, targets, "accessible_name", "Create post Did it get posted? Learn how post publishing works")
	publishDate := findMapByString(t, targets, "accessible_name", "Publish date")
	schedulePost := findMapByString(t, targets, "accessible_name", "Schedule post")
	longAudience := findMapContainingString(t, targets, "accessible_name", "Free members and paid members")
	finalSchedule := findMapByString(t, targets, "accessible_name", "Schedule")

	for name, target := range map[string]map[string]any{"Create post": createPost, "Publish date": publishDate} {
		if boolFromAny(target["dangerous"]) || stringValue(target["effect"]) != "navigation_only" {
			t.Fatalf("configuration/navigation target %s was classified as consequential: %v", name, target)
		}
	}
	if stringValue(schedulePost["role"]) != "switch" || boolFromAny(schedulePost["target_loading"]) || !boolFromAny(schedulePost["container_loading"]) {
		t.Fatalf("unnamed switch or scoped loading state is wrong: %v", schedulePost)
	}
	if len(stringValue(longAudience["accessible_name"])) <= 120 {
		t.Fatalf("fixture did not exercise the full-name identity path: %v", longAudience)
	}
	if !boolFromAny(finalSchedule["dangerous"]) || stringValue(finalSchedule["destructive_effect"]) != "schedule_publish" {
		t.Fatalf("final Schedule consequence missing: %v", finalSchedule)
	}

	ready := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "wait_for", "timeout_ms": 1500,
		"conditions": []any{map[string]any{"type": "target_state", "target_id": stringValue(schedulePost["id"]), "state": "ready"}},
	})
	if !boolFromAny(ready["matched"]) || boolFromAny(ready["timed_out"]) {
		t.Fatalf("target-scoped ready wait was blocked by page/container activity: %v", ready)
	}

	checked := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "set_checked", "target_id": stringValue(longAudience["id"]),
		"som_revision": ready["som_revision"], "expected_name": stringValue(longAudience["accessible_name"]),
		"expected_role": "switch", "checked": true, "observation": "som_delta",
	})
	if !boolFromAny(checked["checked"]) || !boolFromAny(checked["verified"]) || !boolFromAny(checked["action_dispatched"]) {
		t.Fatalf("set_checked did not verify one dispatched transition: %v", checked)
	}

	// Re-read the final control because set_checked intentionally dirties the
	// prior semantic revision. Wrong consequence intent must still reject it
	// before the one real scheduling handler can run.
	current := sc.MCP("computer_use", map[string]any{"session_id": sessionID, "action": "screenshot", "include_som": true})
	finalSchedule = findMapByString(t, mapsFromAny(current["som"]), "accessible_name", "Schedule")
	rejected := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "click", "target_id": stringValue(finalSchedule["id"]),
		"som_revision": current["som_revision"], "expected_text": "Schedule", "expected_effect": "open_configuration",
	})
	if stringValue(rejected["error_code"]) != "semantic_intent_mismatch" || boolFromAny(rejected["action_dispatched"]) {
		t.Fatalf("wrong final intent was not atomically rejected: %v", rejected)
	}

	committed := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "batch", "observation": "som_delta",
		"steps": []any{
			map[string]any{"action": "click", "target_id": stringValue(finalSchedule["id"]), "som_revision": current["som_revision"], "expected_text": "Schedule", "expected_effect": "scheduled_external_commit", "confirm_consequence": "scheduled_external_commit"},
			map[string]any{"action": "wait_for", "conditions": []any{map[string]any{"type": "selector_present", "selector": `#status[data-state="scheduled"]`}}, "timeout_ms": 3000},
		},
	})
	steps := mapsFromAny(committed["steps"])
	if len(steps) != 2 || !boolFromAny(steps[0]["action_dispatched"]) || !boolFromAny(steps[0]["outcome_verified"]) {
		t.Fatalf("confirmed Schedule was not dispatched and verified exactly once: %v", committed)
	}
}

func patreonReliabilityDataURL() string {
	longAudience := "Free members and paid members including annual supporters, founding members, complimentary members, and every currently eligible community tier"
	html := `<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;font-family:system-ui;background:#f5f5f5}.busy{position:fixed;top:8px;left:8px}.composer{width:760px;margin:30px auto;background:white;padding:24px;border-radius:12px}.row{min-height:52px;display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid #ddd}.switch{width:44px;height:25px;border-radius:14px;background:#bbb}.switch[aria-checked="true"]{background:#0a7}.loadingSpinnerWrapper{opacity:0}.loadingSpinnerWrapper svg{width:16px;height:16px}.actions{display:flex;gap:12px;justify-content:flex-end;margin-top:24px}button{padding:10px 18px}
</style></head><body><div class="busy" role="progressbar" aria-label="Loading recommendations"></div>
<main class="composer pending" aria-busy="true"><h1>New post</h1>
<button id="create">Create post <span>Did it get posted? Learn how post publishing works</span></button>
<section><div class="row"><span>Schedule post</span><button class="switch" role="switch" aria-checked="false"><span class="loadingSpinnerWrapper"><svg aria-label="Loading"></svg></span></button></div>
<div class="row"><span>` + longAudience + `</span><button id="audience" class="switch" role="switch" aria-checked="false"></button></div>
<div class="row"><span>Publish date</span><button id="date">Publish date</button></div></section>
<p id="status" data-state="draft">Draft autosaves in the background. A post is not published until the final action.</p>
<div class="actions"><button id="schedule">Schedule</button></div></main>
<script>
document.querySelectorAll('[role=switch]').forEach(function(el){el.addEventListener('click',function(){el.setAttribute('aria-checked',el.getAttribute('aria-checked')==='true'?'false':'true');});});
document.querySelector('#schedule').addEventListener('click',function(){var s=document.querySelector('#status');s.dataset.state='scheduled';s.textContent='Scheduled exactly once';});
</script></body></html>`
	return "data:text/html," + url.PathEscape(html)
}
