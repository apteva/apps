package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// These are tier 3 tests: model decisions drive the browser, while Go checks
// outcomes independently. Live runs require an explicitly configured test
// creator/context; local fixture coverage runs with the normal tier 3 gate.
func TestLLMPatreonReliabilityFixtureLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	backend := envDefault("COMPUTER_LLM_BROWSER_BACKEND", "local")
	if backend == "browserbase" && !browserbaseCredentialsAvailable() {
		t.Fatal("Browserbase credentials required")
	}
	sc := tk.SpawnSidecar(t, ".")
	c := &localComputerMCPClient{sidecar: sc}
	// No saved context is needed for this deterministic fixture.
	opened := sc.MCP("browser_session", map[string]any{"action": "open", "backend": backend, "url": patreonReliabilityDataURL()})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	defer closePatreonTestSession(t, c, sid)
	shot := runPatreonAgent(t, c, sid, "On this disposable publishing fixture, enable the audience switch named Free members and paid members (its label has additional explanatory text), then enable Schedule post. Do not activate the final Schedule button. Verify both switches are on and finish.", 12)
	for _, name := range []string{"Free members and paid members", "Schedule post"} {
		target := findLiveTarget(t, mapsFromAny(shot["som"]), name, false)
		if !boolFromAny(target["checked"]) {
			t.Fatalf("agent did not enable %s: %v", name, target)
		}
	}
	draft := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "timeout_ms": 500, "conditions": []any{map[string]any{"type": "selector_present", "selector": "#status[data-state=draft]"}}})
	if !boolFromAny(draft["matched"]) {
		t.Fatal("agent committed instead of configuring")
	}
}

func TestLLMPatreonMediaPublishLive(t *testing.T) {
	requirePatreonTier3(t)
	c := newLocalComputerMCPClient(t)
	opened := c.call(t, "browser_session", map[string]any{"action": "open", "context_id": os.Getenv("COMPUTER_PATREON_CONTEXT_ID"), "url": requireLiveEnv(t, "COMPUTER_PATREON_CREATOR_URL")})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	defer closePatreonTestSession(t, c, sid)
	title := "Computer tier 3 media " + time.Now().UTC().Format("20060102-150405")
	goal := fmt.Sprintf("This is the user's disposable Patreon test creator. Create and publish exactly ONE video post titled %q, using the embed URL %s. Use the Create post flow, select video and embed URL. Verify media loaded and the draft saved before publishing. Publishing to this test creator is authorized. Do not add the media again if it has loaded. Do not send messages, change account settings, or create additional posts. Verify the final post URL, exact title, and rendered media before finishing.", title, patreonMediaPublishFixtureURL)
	shot := runPatreonAgent(t, c, sid, goal, 30)
	u := firstNonEmpty(stringValue(shot["current_url"]), stringValue(shot["url"]))
	if !strings.Contains(u, "/posts/") || strings.Contains(u, "/edit") || strings.Contains(u, "/new") {
		t.Fatalf("agent did not reach published post: %s", u)
	}
	verified := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "match": "all", "timeout_ms": 15000, "conditions": []any{map[string]any{"type": "text_present", "value": title}, map[string]any{"type": "media_present"}}})
	if !boolFromAny(verified["matched"]) {
		t.Fatalf("published title/media not verified: %s", mustJSON(patreonEvidence(verified)))
	}
	assertRealMediaLoaded(t, verified, false)
	t.Logf("Verified published test post: %s", u)
}

func TestLLMPatreonSchedulingLive(t *testing.T) {
	requirePatreonTier3(t)
	c := newLocalComputerMCPClient(t)
	opened := c.call(t, "browser_session", map[string]any{"action": "open", "context_id": os.Getenv("COMPUTER_PATREON_CONTEXT_ID"), "url": requireLiveEnv(t, "COMPUTER_PATREON_CREATOR_URL")})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	started := time.Now()
	defer closePatreonTestSession(t, c, sid)
	scheduledDate := time.Now().AddDate(0, 0, 14)
	date := scheduledDate.Format("2006-01-02")
	title := "Computer tier 3 scheduling " + time.Now().UTC().Format("20060102-150405")
	goal := fmt.Sprintf("On this disposable Patreon test creator, create exactly one new text draft titled %q, with body Scheduling test draft. Enable Free access and Set publish date, and set the date to %s and time to 7:00 PM. Verify the configured values. Stop BEFORE the final Schedule or Publish action; leave the draft for inspection. Do not change account settings or create additional drafts.", title, date)
	shot := runPatreonAgent(t, c, sid, goal, 30)
	u := firstNonEmpty(stringValue(shot["current_url"]), stringValue(shot["url"]))
	if !strings.Contains(u, "/edit") {
		t.Fatalf("scheduling should remain a draft: %s", u)
	}
	assertPatreonTitle(t, shot, title)
	final := findLiveTarget(t, mapsFromAny(shot["som"]), "Schedule", true)
	if !boolFromAny(final["dangerous"]) {
		t.Fatalf("Schedule consequence missing: %v", final)
	}
	shot = observePatreonScheduleFields(t, c, sid, shot)
	dateTarget := findTemporalLiveTarget(t, mapsFromAny(shot["som"]), "date", true)
	t.Logf("Configured scheduling draft %s, date target: %s", u, mustJSON(dateTarget))
	// Date/time values are checked against the tool's browser readback, not the model's done claim.
	state := mustJSON(shot["som"])
	if !strings.Contains(state, date) && !strings.Contains(state, time.Now().AddDate(0, 0, 14).Format("01/02/2006")) {
		t.Fatalf("requested date absent from final controls: %s", state)
	}
	if !strings.Contains(state, "7:00 PM") && !strings.Contains(state, "19:00") {
		t.Fatalf("requested time absent from final controls: %s", state)
	}
	audience := c.call(t, "computer_use", map[string]any{
		"session_id": sid, "action": "wait_for", "match": "all", "timeout_ms": 1000,
		"conditions": []any{
			map[string]any{"type": "selector_present", "selector": `input[value="public"]:checked`},
			map[string]any{"type": "text_present", "value": "Scheduling test draft"},
		},
	})
	if !boolFromAny(audience["matched"]) {
		t.Fatalf("Free access and draft body were not preserved: %s", mustJSON(patreonEvidence(audience)))
	}
	// The preparation and commit are separate model phases so the test can
	// independently validate values before authorizing the final consequence.
	t.Run("scheduled_publication", func(t *testing.T) {
		goal := fmt.Sprintf("The user authorizes final scheduling on this disposable Patreon creator. This exact draft, titled %q, has already been independently verified with Free access enabled and publish date %s at 7:00 PM in the page timezone. Activate the final Schedule action and complete any required scheduling confirmation. Schedule exactly this one post, do not publish immediately, create another draft, or change the configured date/time. Once dispatched, inspect the outcome instead of blindly repeating Schedule. Dismiss the success dialog with OK, then verify this same editor shows Scheduled for with the correct date/time and exact title before finishing.", title, date)
		committed := runPatreonAgent(t, c, sid, goal, 12)
		writePatreonEvidence(t, "committed.json", []byte(mustJSON(patreonEvidence(committed))))
		host, postID := patreonPostIdentity(u)
		if postID == "" {
			t.Fatalf("draft URL has no post identity: %s", u)
		}
		conditions := []any{
			map[string]any{"type": "text_present", "value": "Scheduled for " + scheduledDate.Format("Jan 2, 2006") + " at 7:00 PM"},
			map[string]any{"type": "url_contains", "value": postID},
		}
		assertScheduled := func(stage string) {
			t.Helper()
			result := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "match": "all", "timeout_ms": 30000, "conditions": conditions})
			writePatreonEvidence(t, stage+".json", []byte(mustJSON(patreonEvidence(result))))
			if !boolFromAny(result["matched"]) {
				t.Fatalf("Patreon did not independently confirm scheduled post (%s): %s", stage, mustJSON(patreonEvidence(result)))
			}
			actualHost, actualID := patreonPostIdentity(stringValue(result["current_url"]))
			if actualHost != host || actualID != postID {
				t.Fatalf("scheduled confirmation belongs to another post: %v", result["current_url"])
			}
			readback := liveScreenshot(t, c, sid)
			writePatreonEvidence(t, stage+"-state.json", []byte(mustJSON(patreonEvidence(readback))))
			assertPatreonTitle(t, readback, title)
		}
		assertScheduled("scheduled-confirmation")
		// A reload rules out relying solely on a transient success toast or
		// an optimistic client-side state update.
		c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "reload"})
		assertScheduled("scheduled-after-reload")
		t.Logf("Verified persisted scheduled post: title=%q url=%s date=%s time=19:00", title, firstNonEmpty(stringValue(committed["current_url"]), stringValue(committed["url"])), date)
	})
	// Retain the old live release gate's real five-minute-boundary regression.
	// Most of this time is already spent driving and verifying the composer.
	t.Run("session_lifetime", func(t *testing.T) {
		deadline := started.Add(6*time.Minute + 15*time.Second)
		if remaining := time.Until(deadline); remaining > 0 {
			t.Logf("Checking saved-context session beyond five minutes in %s", remaining.Round(time.Second))
			time.Sleep(remaining)
		}
		status := c.call(t, "browser_session", map[string]any{"action": "status", "session_id": sid})
		writePatreonEvidence(t, "status.json", []byte(mustJSON(patreonEvidence(status))))
		if boolFromAny(status["failed"]) || stringValue(status["status"]) != "active" || intFromAny(status["session_age_seconds"]) <= 300 {
			t.Fatalf("saved-context browser expired during the workflow: %s", mustJSON(patreonEvidence(status)))
		}
	})
}

// Patreon replaces /posts/<id>/edit with /posts/<title>-<id>/edit after
// scheduling. The numeric suffix, not the cosmetic slug, identifies the post.
func patreonPostIdentity(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "posts" {
			continue
		}
		segments := strings.Split(parts[i+1], "-")
		id := segments[len(segments)-1]
		if n, err := strconv.ParseUint(id, 10, 64); err == nil && n > 0 {
			return strings.ToLower(u.Hostname()), id
		}
		return "", ""
	}
	return "", ""
}

func TestPatreonPostIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, id string }{
		{"https://www.patreon.com/creator/posts/168676125/edit", "168676125"},
		{"https://www.patreon.com/creator/posts/computer-tier-3-168676125/edit", "168676125"},
		{"https://www.patreon.com/posts/another-post-42", "42"},
		{"https://www.patreon.com/creator?post=168676125", ""},
		{"https://www.patreon.com/posts/no-number/edit", ""},
		{"/posts/168676125/edit", ""},
	} {
		_, id := patreonPostIdentity(tc.raw)
		if id != tc.id {
			t.Errorf("identity(%q)=%q, want %q", tc.raw, id, tc.id)
		}
	}
}

// The agent may finish at Audience after checking the date/time earlier.
// Move only the viewport to observe the configured fields; never set values.
func observePatreonScheduleFields(t *testing.T, c *localComputerMCPClient, sid string, shot map[string]any) map[string]any {
	t.Helper()
	for attempt := 0; attempt < 4; attempt++ {
		hasDate, hasTime := false, false
		for _, target := range mapsFromAny(shot["som"]) {
			hasDate = hasDate || stringValue(target["type"]) == "date"
			hasTime = hasTime || stringValue(target["type"]) == "time"
		}
		if hasDate && hasTime {
			return shot
		}
		var panel string
		for _, region := range mapsFromAny(shot["scroll_regions"]) {
			if !boolFromAny(region["document"]) && strings.Contains(mustJSON(region["content_hints"]), "Set publish date") {
				panel = stringValue(region["id"])
				break
			}
		}
		if panel == "" {
			t.Fatal("scheduling settings panel missing")
		}
		c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "scroll", "target_id": panel, "direction": "down", "amount": 450})
		shot = liveScreenshot(t, c, sid)
	}
	t.Fatal("configured date/time controls not found after scrolling settings")
	return nil
}

// Titles are textarea values, not body text. Read the semantic field value so
// a wrapped or truncated visible label cannot produce a false mismatch.
func assertPatreonTitle(t *testing.T, shot map[string]any, title string) {
	t.Helper()
	target := findLiveTarget(t, mapsFromAny(shot["som"]), "Title", true)
	if stringValue(target["current_value"]) != title {
		t.Fatalf("scheduled post title mismatch: %v", target)
	}
}

func requirePatreonTier3(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	if os.Getenv("COMPUTER_PATREON_CREATOR_URL") == "" {
		t.Skip("configure COMPUTER_PATREON_CREATOR_URL and test context for live tier 3")
	}
	if os.Getenv("COMPUTER_PATREON_CONTEXT_ID") == "" && os.Getenv("COMPUTER_PATREON_PROVIDER_CONTEXT_ID") == "" {
		t.Fatal("Patreon test context required")
	}
}

func closePatreonTestSession(t *testing.T, c *localComputerMCPClient, sid string) {
	t.Helper()
	result := c.call(t, "browser_session", map[string]any{"action": "close", "session_id": sid})
	writePatreonEvidence(t, "close.json", []byte(mustJSON(patreonEvidence(result))))
	if boolFromAny(result["failed"]) || boolFromAny(result["cleanup_pending"]) || (!boolFromAny(result["closed"]) && !boolFromAny(result["already_closed"])) {
		t.Errorf("test browser release was not confirmed: %s", mustJSON(patreonEvidence(result)))
	}
}

type patreonAgentDecision struct {
	Action    string `json:"action"`
	Arguments string `json:"arguments_json"`
	Reason    string `json:"reason"`
}

func runPatreonAgent(t *testing.T, c *localComputerMCPClient, sid, goal string, maxSteps int) map[string]any {
	t.Helper()
	var toolDescription string
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "computer_use" {
			toolDescription = tool.Description + "\nInput schema: " + mustJSON(tool.InputSchema)
		}
	}
	var history []any
	for step := 0; step < maxSteps; step++ {
		shot := liveScreenshot(t, c, sid)
		frame := decodeScreenshot(t, shot)
		writePatreonEvidence(t, fmt.Sprintf("%02d-frame.jpg", step), frame)
		writePatreonEvidence(t, fmt.Sprintf("%02d-state.json", step), []byte(mustJSON(patreonEvidence(shot))))
		prompt := "You are driving Computer through its computer_use tool. Choose ONE next action based on the screenshot and current tool output, or done when the task is verified. Return the exact computer_use arguments as a JSON string; omit session_id (the harness supplies it). No browser scripting or external tools.\nTask: " + goal + "\nTool: " + toolDescription + "\nHistory (field observations describe state before that action): " + mustJSON(history) + "\nCurrent observation: " + mustJSON(patreonEvidence(shot))
		var d patreonAgentDecision
		callComputerLLM(t, frame, prompt, `{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["computer_use","done"]},"arguments_json":{"type":"string"},"reason":{"type":"string"}},"required":["action","arguments_json","reason"]}`, &d)
		t.Logf("AGENT step=%d decision=%s reason=%s args=%s", step, d.Action, d.Reason, d.Arguments)
		writePatreonEvidence(t, fmt.Sprintf("%02d-decision.json", step), []byte(mustJSON(d)))
		if d.Action == "done" {
			return shot
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(d.Arguments), &args); err != nil {
			t.Fatalf("agent invalid arguments: %v", err)
		}
		args["session_id"] = sid
		start := time.Now()
		result := c.call(t, "computer_use", args)
		observed := patreonObservedFields(shot)
		entry := map[string]any{"arguments": args, "reason": d.Reason, "observed_fields_before_action": observed, "result": patreonEvidence(result), "elapsed_ms": time.Since(start).Milliseconds()}
		compact := map[string]any{}
		for k, v := range result {
			if k == "error" || k == "error_code" || k == "failed" || k == "text" || k == "current_url" || k == "matched" || k == "timed_out" || k == "checked" || k == "verified" || k == "action_dispatched" || k == "outcome_verified" || k == "draft_save_state" || strings.HasPrefix(k, "text_") || strings.HasPrefix(k, "media_") || strings.HasPrefix(k, "temporal_") {
				compact[k] = patreonEvidence(v)
			}
		}
		history = append(history, map[string]any{"arguments": args, "reason": d.Reason, "observed_fields_before_action": observed, "result": compact})
		writePatreonEvidence(t, fmt.Sprintf("%02d-action.json", step), []byte(mustJSON(entry)))
		t.Logf("RESULT step=%d action=%v elapsed=%s error=%v dispatched=%v verified=%v", step, args["action"], time.Since(start).Round(time.Millisecond), result["error_code"], result["action_dispatched"], result["outcome_verified"])
	}
	t.Fatalf("agent exceeded %d actions; inspect decision/state/action artifacts", maxSteps)
	return nil
}

// Keep field evidence when the next screenshot scrolls it out of view, as a
// normal conversation with the complete tool outputs would. IDs/coordinates
// are intentionally excluded: historical observations are not fresh targets.
func patreonObservedFields(shot map[string]any) []any {
	var fields []any
	for _, target := range mapsFromAny(shot["som"]) {
		if target["current_value"] == nil && target["checked"] == nil && !boolFromAny(target["indeterminate"]) {
			continue
		}
		field := map[string]any{}
		for _, key := range []string{"accessible_name", "type", "role", "current_value", "checked", "indeterminate"} {
			if value, ok := target[key]; ok {
				if text, ok := value.(string); ok && len(text) > 512 {
					value = text[:512]
				}
				field[key] = value
			}
		}
		fields = append(fields, field)
		if len(fields) == 20 {
			break
		}
	}
	return fields
}

// Keep semantic evidence while excluding binary images and connection URLs.
func patreonEvidence(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			switch k {
			case "screenshot", "png_b64", "image", "base64", "debug_url", "connect_url", "recording_url":
				continue
			}
			out[k] = patreonEvidence(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = patreonEvidence(v)
		}
		return out
	case string:
		if strings.HasPrefix(x, "data:text/html") {
			return "data:text/html,[fixture]"
		}
		return x
	default:
		return v
	}
}

func writePatreonEvidence(t *testing.T, name string, data []byte) {
	t.Helper()
	root := os.Getenv("COMPUTER_LLM_ARTIFACT_DIR")
	if root == "" {
		return
	}
	dir := filepath.Join(root, strings.ReplaceAll(t.Name(), "/", "_"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}
