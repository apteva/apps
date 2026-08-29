package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestComputerPatreonRealContenteditableWithLLM is an opt-in regression for
// the real Patreon ProseMirror/Remirror post editor. A real authenticated LLM
// chooses the post-body target from Computer's screenshot and SoM. The test
// replaces only the disposable draft body, verifies the reconciled value and
// protected paywall widget, then restores the exact original body. It never
// clicks Save, Publish, or Schedule.
//
// Required:
//
//	RUN_COMPUTER_PATREON_TEXT_LIVE=1
//	COMPUTER_PATREON_CONTEXT_ID=ctx_...
//	COMPUTER_PATREON_DRAFT_URL=https://www.patreon.com/.../edit
//	COMPUTER_PATREON_ORIGINAL_TEXT='exact disposable draft body'
func TestComputerPatreonRealContenteditableWithLLM(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_PATREON_TEXT_LIVE") != "1" {
		t.Skip("set RUN_COMPUTER_PATREON_TEXT_LIVE=1 to run the real LLM + Patreon contenteditable test")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is required for the authenticated LLM regression")
	}
	contextID := requireLiveEnv(t, "COMPUTER_PATREON_CONTEXT_ID")
	draftURL := requireLiveEnv(t, "COMPUTER_PATREON_DRAFT_URL")
	original := requireLiveEnv(t, "COMPUTER_PATREON_ORIGINAL_TEXT")
	if parsed, err := url.Parse(draftURL); err != nil || !strings.HasSuffix(strings.ToLower(parsed.Hostname()), "patreon.com") || !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/edit") {
		t.Fatalf("COMPUTER_PATREON_DRAFT_URL must be a dedicated patreon.com edit URL, got %q", draftURL)
	}

	client := newLocalComputerMCPClient(t)
	opened := client.call(t, "browser_session", map[string]any{
		"action": "open", "context_id": contextID, "url": draftURL, "timeout": 900,
	})
	sessionID := stringValue(opened["session_id"])
	if sessionID == "" {
		t.Fatalf("open returned no session id: %v", opened)
	}
	defer client.call(t, "browser_session", map[string]any{"action": "close", "session_id": sessionID})

	shot := liveScreenshot(t, client, sessionID)
	frame := decodeScreenshot(t, shot)
	marker := "COMPUTER_SET_TEXT_LIVE_PROBE_20260829"
	var decision semanticTextDecision
	callComputerLLM(t, frame,
		"Select the single editable Patreon post-body textbox for a reversible test. Do not select or click Save, Publish, Schedule, or any button. Return set_text with the exact text "+marker+". Computer targets: "+mustJSON(shot["som"]),
		`{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["set_text"]},"target_id":{"type":"string"},"expected_name":{"type":"string"},"expected_role":{"type":"string"},"text":{"type":"string","const":"COMPUTER_SET_TEXT_LIVE_PROBE_20260829"},"newline_mode":{"type":"string","enum":["preserve"]},"reason":{"type":"string"}},"required":["action","target_id","expected_name","expected_role","text","newline_mode","reason"]}`,
		&decision)
	if decision.TargetID == "" || decision.Text != marker || !strings.EqualFold(decision.ExpectedRole, "textbox") {
		t.Fatalf("LLM did not select the post-body textbox safely: %+v", decision)
	}
	selected := findLiveTarget(t, mapsFromAny(shot["som"]), decision.ExpectedName, true)
	if stringValue(selected["id"]) != decision.TargetID || boolFromAny(selected["dangerous"]) {
		t.Fatalf("LLM target did not match the safe live editor: decision=%+v target=%v", decision, selected)
	}

	selector := "[contenteditable=true][role=textbox]"
	restored := false
	defer func() {
		if restored {
			return
		}
		result := client.call(t, "computer_use", map[string]any{
			"session_id": sessionID, "action": "set_text", "selector": selector,
			"text": original, "newline_mode": "preserve",
		})
		if !boolFromAny(result["text_verified"]) {
			t.Errorf("cleanup could not verify restored Patreon body: %v", result)
		}
	}()

	changed := client.call(t, "computer_use", map[string]any{
		"session_id": sessionID, "action": decision.Action,
		"target_id": decision.TargetID, "som_revision": shot["som_revision"],
		"expected_name": decision.ExpectedName, "expected_role": decision.ExpectedRole,
		"text": decision.Text, "newline_mode": decision.NewlineMode, "observation": "som_delta",
	})
	if got := stringValue(changed["text_selector"]); got != "" {
		selector = got
	}
	if !boolFromAny(changed["text_verified"]) || stringValue(changed["text_verification"]) != "paragraphs_stable" || stringValue(changed["text_rendered"]) != marker {
		t.Fatalf("Patreon editor did not retain the reconciled probe value: %v", changed)
	}
	time.Sleep(1200 * time.Millisecond)
	verifiedShot := liveScreenshot(t, client, sessionID)
	verifiedEditor := findLiveTarget(t, mapsFromAny(verifiedShot["som"]), decision.ExpectedName, true)
	visibleState := mustJSON(verifiedEditor)
	if !strings.Contains(visibleState, marker[:12]) || !strings.Contains(visibleState, "Paid access starts here") {
		t.Fatalf("Patreon visual state lost the probe or protected paywall widget: %s", visibleState)
	}

	restore := client.call(t, "computer_use", map[string]any{
		"session_id": sessionID, "action": "set_text", "selector": selector,
		"text": original, "newline_mode": "preserve",
	})
	if !boolFromAny(restore["text_verified"]) || stringValue(restore["text_rendered"]) != original {
		t.Fatalf("Patreon body restoration was not verified: %v", restore)
	}
	restored = true
	time.Sleep(1200 * time.Millisecond)
	restoredShot := liveScreenshot(t, client, sessionID)
	restoredEditor := findLiveTarget(t, mapsFromAny(restoredShot["som"]), decision.ExpectedName, true)
	restoredState := mustJSON(restoredEditor)
	if !strings.Contains(restoredState, original[:min(12, len(original))]) || !strings.Contains(restoredState, "Paid access starts here") || strings.Contains(restoredState, marker[:12]) {
		t.Fatalf("Patreon final visual state was not restored exactly: %s", restoredState)
	}
	t.Logf("real LLM selected Patreon editor %s; native contenteditable edit, delayed verification, paywall preservation, and exact restoration passed", decision.TargetID)
}

// TestComputerPatreonRealSite is the destructive, opt-in release gate for the
// real Patreon composer. It talks to the user's running local Apteva instance
// so ctx_* resolves to the already-authenticated Computer context.
//
// Required:
//
//	RUN_COMPUTER_PATREON_LIVE=1
//	COMPUTER_PATREON_CONTEXT_ID=ctx_...
//	COMPUTER_PATREON_DRAFT_URL=https://www.patreon.com/...
//	COMPUTER_PATREON_DATE=2026-08-29
//	COMPUTER_PATREON_TIME=7:00 PM
//
// By default the test exercises every configuration step and proves the final
// Schedule is rejected under wrong intent. Actual scheduling requires the
// exact second gate COMPUTER_PATREON_ALLOW_SCHEDULE=I_ACKNOWLEDGE_LIVE_SCHEDULE.
func TestComputerPatreonRealSite(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_PATREON_LIVE") != "1" {
		t.Skip("set RUN_COMPUTER_PATREON_LIVE=1 to run against Patreon")
	}
	contextID := requireLiveEnv(t, "COMPUTER_PATREON_CONTEXT_ID")
	draftURL := requireLiveEnv(t, "COMPUTER_PATREON_DRAFT_URL")
	dateValue := requireLiveEnv(t, "COMPUTER_PATREON_DATE")
	timeValue := requireLiveEnv(t, "COMPUTER_PATREON_TIME")
	if parsed, err := url.Parse(draftURL); err != nil || !strings.HasSuffix(strings.ToLower(parsed.Hostname()), "patreon.com") {
		t.Fatalf("COMPUTER_PATREON_DRAFT_URL must be a dedicated patreon.com draft URL, got %q", draftURL)
	}

	client := newLocalComputerMCPClient(t)
	opened := client.call(t, "browser_session", map[string]any{
		"action": "open", "context_id": contextID, "url": draftURL, "timeout": 1800,
	})
	sessionID := stringValue(opened["session_id"])
	if sessionID == "" {
		t.Fatalf("open returned no session id: %v", opened)
	}
	defer client.call(t, "browser_session", map[string]any{"action": "close", "session_id": sessionID})
	if intFromAny(opened["effective_timeout_seconds"]) < 1800 || stringValue(opened["expires_at"]) == "" {
		t.Fatalf("hosted session lifetime diagnostics are incomplete: %v", opened)
	}
	expectedTimezone := strings.TrimSpace(os.Getenv("COMPUTER_PATREON_EXPECTED_TIMEZONE"))
	actualTimezone := stringValue(opened["effective_timezone"])
	if actualTimezone == "" || !boolFromAny(opened["environment_verified"]) || boolFromAny(opened["environment_applied"]) {
		t.Fatalf("Patreon browser-observed environment is incomplete or an unrequested override was applied: %v", opened)
	}
	if expectedTimezone != "" && actualTimezone != expectedTimezone {
		t.Fatalf("Patreon session timezone=%q, want explicitly requested %q", actualTimezone, expectedTimezone)
	}

	// Reproduce the old five-minute failure boundary while doing read-only
	// status checks. Override only for local debugging; the release gate waits
	// 6m15s by default.
	lifetimeWait := 6*time.Minute + 15*time.Second
	if raw := strings.TrimSpace(os.Getenv("COMPUTER_PATREON_LIFETIME_WAIT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("COMPUTER_PATREON_LIFETIME_WAIT: %v", err)
		}
		lifetimeWait = parsed
	}
	deadline := time.Now().Add(lifetimeWait)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > time.Minute {
			time.Sleep(time.Minute)
		} else if remaining > 0 {
			time.Sleep(remaining)
		}
		status := client.call(t, "browser_session", map[string]any{"action": "status", "session_id": sessionID})
		if stringValue(status["status"]) != "active" {
			t.Fatalf("Patreon session did not survive the historical five-minute boundary: %v", status)
		}
	}

	shot := liveScreenshot(t, client, sessionID)
	audienceControlName := envDefault("COMPUTER_PATREON_AUDIENCE_CONTROL", "Free access")
	audience := findLiveTarget(t, mapsFromAny(shot["som"]), audienceControlName, true)
	assertNonConsequentialLiveTarget(t, audience, "audience configuration")
	audienceResult := configureLiveTarget(t, client, sessionID, shot, audience, true)
	if !boolFromAny(audienceResult["checked"]) || !boolFromAny(audienceResult["verified"]) {
		t.Fatalf("audience state was not verified: %v", audienceResult)
	}
	audienceAgain := client.call(t, "computer_use", map[string]any{
		"session_id": sessionID, "action": "set_checked", "target_id": stringValue(audience["id"]),
		"som_revision": audienceResult["som_revision"], "expected_name": stringValue(audience["accessible_name"]),
		"checked": true, "observation": "som_delta",
	})
	if boolFromAny(audienceAgain["action_dispatched"]) || boolFromAny(audienceAgain["checked_changed"]) || !boolFromAny(audienceAgain["verified"]) {
		t.Fatalf("idempotent audience state dispatched a second action: %v", audienceAgain)
	}

	scheduleName := envDefault("COMPUTER_PATREON_SCHEDULE_CONTROL", "Set publish date")
	shot, scheduleSwitch := liveScreenshotWithTarget(t, client, sessionID, scheduleName, true)
	if stringValue(scheduleSwitch["role"]) != "switch" || boolFromAny(scheduleSwitch["target_loading"]) {
		t.Fatalf("publish-date control did not resolve to a ready named switch: %v", scheduleSwitch)
	}
	ready := client.call(t, "computer_use", map[string]any{
		"session_id": sessionID, "action": "wait_for", "timeout_ms": 5000,
		"conditions": []any{map[string]any{"type": "target_state", "target_id": stringValue(scheduleSwitch["id"]), "state": "ready"}},
	})
	if !boolFromAny(ready["matched"]) || boolFromAny(ready["timed_out"]) {
		t.Fatalf("target-scoped ready wait was blocked by unrelated Patreon activity: %v", ready)
	}
	checked := client.call(t, "computer_use", map[string]any{
		"session_id": sessionID, "action": "set_checked", "target_id": stringValue(scheduleSwitch["id"]),
		"som_revision": ready["som_revision"], "expected_name": stringValue(scheduleSwitch["accessible_name"]),
		"expected_role": "switch", "checked": true, "observation": "som_delta",
	})
	if !boolFromAny(checked["checked"]) || !boolFromAny(checked["verified"]) || !boolFromAny(checked["action_dispatched"]) || !boolFromAny(checked["checked_changed"]) {
		t.Fatalf("publish-date state was not verified as one transition: %v", checked)
	}
	checkedAgain := client.call(t, "computer_use", map[string]any{
		"session_id": sessionID, "action": "set_checked", "target_id": stringValue(scheduleSwitch["id"]),
		"som_revision": checked["som_revision"], "expected_name": stringValue(scheduleSwitch["accessible_name"]),
		"expected_role": "switch", "checked": true, "observation": "som_delta",
	})
	if boolFromAny(checkedAgain["action_dispatched"]) || boolFromAny(checkedAgain["checked_changed"]) || !boolFromAny(checkedAgain["verified"]) {
		t.Fatalf("idempotent publish-date state dispatched a second action: %v", checkedAgain)
	}

	shot = liveScreenshot(t, client, sessionID)
	dateTarget := findTemporalLiveTarget(t, mapsFromAny(shot["som"]), envDefault("COMPUTER_PATREON_DATE_TARGET", "date"), true)
	dateResult := client.call(t, "computer_use", map[string]any{
		"session_id": sessionID, "action": "set_temporal", "target_id": stringValue(dateTarget["id"]),
		"som_revision": shot["som_revision"], "expected_name": stringValue(dateTarget["accessible_name"]), "value": dateValue,
	})
	assertTemporalReadback(t, "date", dateResult)

	shot = liveScreenshot(t, client, sessionID)
	timeTarget := findTemporalLiveTarget(t, mapsFromAny(shot["som"]), envDefault("COMPUTER_PATREON_TIME_TARGET", "time"), false)
	timeResult := client.call(t, "computer_use", map[string]any{
		"session_id": sessionID, "action": "set_temporal", "target_id": stringValue(timeTarget["id"]),
		"som_revision": shot["som_revision"], "expected_name": stringValue(timeTarget["accessible_name"]), "value": timeValue,
	})
	assertTemporalReadback(t, "time", timeResult)

	shot = liveScreenshot(t, client, sessionID)
	finalSchedule := findLiveTarget(t, mapsFromAny(shot["som"]), "Schedule", true)
	if !boolFromAny(finalSchedule["dangerous"]) || stringValue(finalSchedule["destructive_effect"]) != "schedule_publish" {
		t.Fatalf("final Schedule consequence is wrong: %v", finalSchedule)
	}
	rejected := client.call(t, "computer_use", map[string]any{
		"session_id": sessionID, "action": "click", "target_id": stringValue(finalSchedule["id"]),
		"som_revision": shot["som_revision"], "expected_text": stringValue(finalSchedule["accessible_name"]), "expected_effect": "open_configuration",
	})
	if stringValue(rejected["error_code"]) != "semantic_intent_mismatch" || boolFromAny(rejected["action_dispatched"]) {
		t.Fatalf("wrong final consequence intent was not rejected: %v", rejected)
	}
	if os.Getenv("COMPUTER_PATREON_ALLOW_SCHEDULE") != "I_ACKNOWLEDGE_LIVE_SCHEDULE" {
		t.Log("real Patreon configuration completed; final Schedule remained unexecuted because the live scheduling acknowledgement was absent")
		return
	}

	confirmation := envDefault("COMPUTER_PATREON_EXPECTED_CONFIRMATION", "Scheduled for")
	committed := client.call(t, "computer_use", map[string]any{
		"session_id": sessionID, "action": "batch", "observation": "som_delta",
		"steps": []any{
			map[string]any{"action": "click", "target_id": stringValue(finalSchedule["id"]), "som_revision": shot["som_revision"], "expected_text": stringValue(finalSchedule["accessible_name"]), "expected_effect": "scheduled_external_commit", "confirm_consequence": "scheduled_external_commit"},
			map[string]any{"action": "wait_for", "conditions": []any{map[string]any{"type": "text_present", "value": confirmation}}, "timeout_ms": 30000},
		},
	})
	steps := mapsFromAny(committed["steps"])
	if len(steps) != 2 || !boolFromAny(steps[0]["action_dispatched"]) || !boolFromAny(steps[0]["outcome_verified"]) {
		t.Fatalf("live Schedule outcome was not verified; do not retry automatically: %v", committed)
	}
	t.Logf("Patreon scheduled exactly once: url=%s date=%s time=%s timezone=%s", stringValue(committed["current_url"]), dateValue, timeValue, actualTimezone)
}

type localComputerMCPClient struct {
	endpoint  string
	apiKey    string
	projectID string
	http      *http.Client
}

func newLocalComputerMCPClient(t *testing.T) *localComputerMCPClient {
	t.Helper()
	if endpoint := strings.TrimSpace(os.Getenv("COMPUTER_MCP_ENDPOINT")); endpoint != "" {
		token := strings.TrimSpace(os.Getenv("COMPUTER_MCP_TOKEN"))
		if token == "" {
			t.Fatal("COMPUTER_MCP_TOKEN is required with COMPUTER_MCP_ENDPOINT")
		}
		return &localComputerMCPClient{endpoint: endpoint, apiKey: token, projectID: strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")), http: &http.Client{Timeout: 90 * time.Second}}
	}
	base := strings.TrimSpace(os.Getenv("APTEVA_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("APTEVA_API_KEY"))
	if apiKey == "" {
		configPath := strings.TrimSpace(os.Getenv("APTEVA_CONFIG_PATH"))
		if configPath == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			devConfig := filepath.Join(home, ".apteva", "apteva.json")
			prodConfig := filepath.Join(home, ".apteva-prod", "apteva.json")
			if _, err := os.Stat(devConfig); err == nil {
				configPath = devConfig
				if base == "" {
					base = "http://127.0.0.1:5280"
				}
			} else {
				configPath = prodConfig
				if base == "" {
					base = "http://127.0.0.1:5281"
				}
			}
		}
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read local Apteva config: %v", err)
		}
		var config map[string]any
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatalf("decode local Apteva config: %v", err)
		}
		apiKey = stringValue(config["api_key"])
	}
	if apiKey == "" {
		t.Fatal("local Apteva API key is unavailable")
	}
	if base == "" {
		base = "http://127.0.0.1:5280"
	}
	base = strings.TrimRight(base, "/")
	endpoint := base + "/api/apps/computer/mcp"
	if projectID := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); projectID != "" {
		endpoint += "?project_id=" + url.QueryEscape(projectID)
	}
	return &localComputerMCPClient{endpoint: endpoint, apiKey: apiKey, http: &http.Client{Timeout: 90 * time.Second}}
}

func (c *localComputerMCPClient) call(t *testing.T, tool string, arguments map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": arguments}})
	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if c.projectID != "" {
		req.Header.Set("X-Apteva-Project-ID", c.projectID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("call %s HTTP %d: %s", tool, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var rpc struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		t.Fatalf("decode %s response: %v body=%s", tool, err, string(raw))
	}
	if rpc.Error != nil {
		t.Fatalf("call %s MCP %d: %s", tool, rpc.Error.Code, rpc.Error.Message)
	}
	if rpc.Result.Structured != nil {
		return rpc.Result.Structured
	}
	for _, item := range rpc.Result.Content {
		if item.Type == "text" {
			var result map[string]any
			if json.Unmarshal([]byte(item.Text), &result) == nil {
				return result
			}
		}
	}
	t.Fatalf("call %s returned no structured object: %s", tool, string(raw))
	return nil
}

func liveScreenshot(t *testing.T, client *localComputerMCPClient, sessionID string) map[string]any {
	t.Helper()
	return client.call(t, "computer_use", map[string]any{"session_id": sessionID, "action": "screenshot", "include_som": true})
}

func liveScreenshotWithTarget(t *testing.T, client *localComputerMCPClient, sessionID, name string, exact bool) (map[string]any, map[string]any) {
	t.Helper()
	for attempt := 0; attempt < 4; attempt++ {
		shot := liveScreenshot(t, client, sessionID)
		if target := findLiveTargetOptional(mapsFromAny(shot["som"]), name, exact); target != nil {
			return shot, target
		}
		var region map[string]any
		for _, candidate := range mapsFromAny(shot["scroll_regions"]) {
			if strings.Contains(strings.ToLower(stringValue(candidate["name"])), "right scrollable panel") {
				region = candidate
				break
			}
		}
		if region == nil {
			break
		}
		client.call(t, "computer_use", map[string]any{
			"session_id": sessionID, "action": "scroll", "target_id": stringValue(region["id"]),
			"expected_name": stringValue(region["name"]), "expected_role": stringValue(region["role"]),
			"direction": "down", "amount": 600, "observation": "som_delta",
		})
	}
	t.Fatalf("Patreon target %q exact=%t was not revealed after scrolling the settings panel", name, exact)
	return nil, nil
}

func findLiveTarget(t *testing.T, targets []map[string]any, name string, exact bool) map[string]any {
	t.Helper()
	if target := findLiveTargetOptional(targets, name, exact); target != nil {
		return target
	}
	t.Fatalf("Patreon target %q exact=%t not found in %v", name, exact, targets)
	return nil
}

func findLiveTargetOptional(targets []map[string]any, name string, exact bool) map[string]any {
	for _, target := range targets {
		actual := stringValue(target["accessible_name"])
		if (exact && strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(name))) || (!exact && strings.Contains(strings.ToLower(actual), strings.ToLower(name))) {
			return target
		}
	}
	return nil
}

func configureLiveTarget(t *testing.T, client *localComputerMCPClient, sessionID string, shot, target map[string]any, checked bool) map[string]any {
	t.Helper()
	role := strings.ToLower(stringValue(target["role"]))
	if role == "" {
		role = strings.ToLower(stringValue(target["type"]))
	}
	args := map[string]any{"session_id": sessionID, "target_id": stringValue(target["id"]), "som_revision": shot["som_revision"], "expected_name": stringValue(target["accessible_name"]), "expected_role": role, "observation": "som_delta"}
	if role == "switch" || role == "checkbox" || role == "radio" {
		args["action"], args["checked"] = "set_checked", checked
	} else {
		args["action"], args["expected_text"], args["expected_effect"] = "click", stringValue(target["accessible_name"]), "open_configuration"
	}
	result := client.call(t, "computer_use", args)
	if boolFromAny(result["failed"]) {
		t.Fatalf("configure target %v failed: %v", target, result)
	}
	return result
}

func assertNonConsequentialLiveTarget(t *testing.T, target map[string]any, purpose string) {
	t.Helper()
	if boolFromAny(target["dangerous"]) {
		t.Fatalf("%s was incorrectly classified as consequential: %v", purpose, target)
	}
}

func findTemporalLiveTarget(t *testing.T, targets []map[string]any, name string, requireDateLike bool) map[string]any {
	t.Helper()
	for _, target := range targets {
		if strings.Contains(strings.ToLower(stringValue(target["accessible_name"])), strings.ToLower(name)) && (!requireDateLike || boolFromAny(target["date_like"])) {
			return target
		}
	}
	t.Fatalf("temporal Patreon target containing %q not found: %v", name, targets)
	return nil
}

func assertTemporalReadback(t *testing.T, kind string, result map[string]any) {
	t.Helper()
	if boolFromAny(result["failed"]) || !boolFromAny(result["temporal_verified"]) || stringValue(result["temporal_actual_value"]) == "" {
		t.Fatalf("%s value was not read back from Patreon: %v", kind, result)
	}
}

func requireLiveEnv(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("%s is required", key)
	}
	return value
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		value, _ := typed.Int64()
		return int(value)
	default:
		return 0
	}
}
