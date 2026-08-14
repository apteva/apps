package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

type semanticNavigationDecision struct {
	Action       string `json:"action"`
	TargetID     string `json:"target_id"`
	ExpectedName string `json:"expected_name"`
	ExpectedRole string `json:"expected_role"`
	Direction    string `json:"direction"`
	Amount       int    `json:"amount"`
	Reason       string `json:"reason"`
}

type semanticTextDecision struct {
	Action       string `json:"action"`
	TargetID     string `json:"target_id"`
	ExpectedName string `json:"expected_name"`
	ExpectedRole string `json:"expected_role"`
	Text         string `json:"text"`
	NewlineMode  string `json:"newline_mode"`
	Reason       string `json:"reason"`
}

type semanticProgressAssessment struct {
	Progress        bool   `json:"progress"`
	InspectedTarget string `json:"inspected_target"`
	NextAction      string `json:"next_action"`
	Reason          string `json:"reason"`
}

// TestLLMSemanticNavigationWorkflowLive is an opt-in app-level regression.
// A real model sees the same screenshot, SoM, stable revision, and semantic
// scroll regions exposed by the MCP tool. It must select the Settings region,
// preserve rich-text paragraphs, and correctly interpret a boundary scroll as
// no navigation progress.
func TestLLMSemanticNavigationWorkflowLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is required for the authenticated LLM regression")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(semanticNavigationFixtureHTML))
	}))
	t.Cleanup(server.Close)

	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	opened := sc.MCP("browser_session", map[string]any{
		"action": "open", "backend": "local", "url": server.URL,
		"viewport": map[string]any{"width": 1000, "height": 600},
	})
	sessionID := stringValue(opened["session_id"])
	if sessionID == "" {
		t.Fatalf("open returned no session id: %v", opened)
	}
	defer sc.MCP("browser_close", map[string]any{"session_id": sessionID})

	shot := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "screenshot", "include_som": true,
	})
	frame := decodeScreenshot(t, shot)
	regions := mapsFromAny(shot["scroll_regions"])
	targets := mapsFromAny(shot["som"])
	revision := revisionFromOutput(t, shot)
	settings := findMapByString(t, regions, "name", "Settings")
	body := findMapByString(t, targets, "accessible_name", "Post body")
	publish := findMapByString(t, targets, "accessible_name", "Publish")
	if boolFromAny(body["dangerous"]) {
		t.Fatalf("draft body was classified as dangerous: %v", body)
	}
	if !boolFromAny(publish["dangerous"]) || stringValue(publish["destructive_effect"]) != "immediate_publish" {
		t.Fatalf("Publish risk metadata is incomplete: %v", publish)
	}

	navigationPrompt := fmt.Sprintf(`You are controlling a browser with independently scrollable regions.
Goal: inspect the lower Settings options and reveal "Set publish date". Do not scroll or edit the post body.
Choose one semantic scroll action from the supplied regions. Use its exact stable id, name, and role. Do not use coordinates.
Current SoM: %s
Current scroll regions: %s`, mustJSON(targets), mustJSON(regions))
	var navigation semanticNavigationDecision
	callComputerLLM(t, frame, navigationPrompt,
		`{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["scroll"]},"target_id":{"type":"string"},"expected_name":{"type":"string"},"expected_role":{"type":"string"},"direction":{"type":"string","enum":["down"]},"amount":{"type":"integer","minimum":200,"maximum":1000},"reason":{"type":"string"}},"required":["action","target_id","expected_name","expected_role","direction","amount","reason"]}`,
		&navigation)
	if navigation.TargetID != stringValue(settings["id"]) || navigation.ExpectedName != "Settings" {
		t.Fatalf("model selected the wrong scroll region: decision=%+v settings=%v", navigation, settings)
	}

	scrollOut := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": navigation.Action,
		"target_id": navigation.TargetID, "som_revision": revision,
		"expected_name": navigation.ExpectedName, "expected_role": navigation.ExpectedRole,
		"direction": navigation.Direction, "amount": navigation.Amount,
	})
	if !boolFromAny(scrollOut["scroll_moved"]) || boolFromAny(scrollOut["scroll_wrong_target"]) {
		t.Fatalf("semantic settings scroll did not report correct movement: %v", scrollOut)
	}
	if stringValue(scrollOut["scroll_actual_target_id"]) != navigation.TargetID || stringValue(scrollOut["scroll_actual_target_name"]) != "Settings" {
		t.Fatalf("actual movement identity mismatch: %v", scrollOut)
	}
	if !containsNamedTarget(scrollOut["revealed_targets"], "Set publish date") {
		t.Fatalf("scroll did not report the newly revealed control: %v", scrollOut["revealed_targets"])
	}

	textPrompt := fmt.Sprintf(`Goal: replace the post body with exactly two paragraphs while preserving the blank line:
First paragraph.

Second paragraph.
Choose the stable Post body target from this current SoM. Return one set_text action. Never target Publish.
Current SoM: %s`, mustJSON(mapsFromAny(scrollOut["som_delta"].(map[string]any)["unchanged"])))
	// The delta may omit unchanged targets by design, so include the known live
	// body target explicitly in the model context.
	textPrompt += "\nKnown current Post body target: " + mustJSON(body)
	var textDecision semanticTextDecision
	callComputerLLM(t, nil, textPrompt,
		`{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["set_text"]},"target_id":{"type":"string"},"expected_name":{"type":"string"},"expected_role":{"type":"string"},"text":{"type":"string"},"newline_mode":{"type":"string","enum":["preserve"]},"reason":{"type":"string"}},"required":["action","target_id","expected_name","expected_role","text","newline_mode","reason"]}`,
		&textDecision)
	if textDecision.TargetID != stringValue(body["id"]) || textDecision.ExpectedName != "Post body" {
		t.Fatalf("model selected the wrong text target: decision=%+v body=%v", textDecision, body)
	}
	if textDecision.Text != "First paragraph.\n\nSecond paragraph." {
		t.Fatalf("model changed requested paragraph structure: %q", textDecision.Text)
	}
	textOut := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": textDecision.Action,
		"target_id": textDecision.TargetID, "expected_name": textDecision.ExpectedName,
		"expected_role": textDecision.ExpectedRole, "text": textDecision.Text,
		"newline_mode": textDecision.NewlineMode,
	})
	if !boolFromAny(textOut["text_verified"]) || stringValue(textOut["text_rendered"]) != textDecision.Text {
		t.Fatalf("rendered text readback was not verified: %v", textOut)
	}
	if paragraphs := stringsFromAny(textOut["text_paragraphs"]); len(paragraphs) != 2 || paragraphs[0] != "First paragraph." || paragraphs[1] != "Second paragraph." {
		t.Fatalf("paragraph readback mismatch: %v", paragraphs)
	}

	// The first large scroll reaches the Settings boundary. A second input is
	// delivered but must be reported as no movement, not as successful progress.
	boundary := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "scroll", "target_id": navigation.TargetID,
		"expected_name": "Settings", "expected_role": navigation.ExpectedRole,
		"direction": "down", "amount": 1000,
	})
	if boolFromAny(boundary["scroll_moved"]) {
		boundary = sc.MCP("computer_use", map[string]any{
			"session_id": sessionID, "action": "scroll", "target_id": navigation.TargetID,
			"expected_name": "Settings", "expected_role": navigation.ExpectedRole,
			"direction": "down", "amount": 1000,
		})
	}
	if boolFromAny(boundary["scroll_moved"]) {
		t.Fatalf("could not reach deterministic Settings boundary: %v", boundary)
	}
	var assessment semanticProgressAssessment
	callComputerLLM(t, nil,
		"Assess whether the intended Settings inspection advanced. Input is Computer's observed scroll result, not merely event delivery: "+mustJSON(boundary),
		`{"type":"object","additionalProperties":false,"properties":{"progress":{"type":"boolean"},"inspected_target":{"type":"string"},"next_action":{"type":"string"},"reason":{"type":"string"}},"required":["progress","inspected_target","next_action","reason"]}`,
		&assessment)
	if assessment.Progress || !strings.Contains(strings.ToLower(assessment.InspectedTarget), "settings") {
		t.Fatalf("model misread no-movement feedback: %+v boundary=%v", assessment, boundary)
	}
	t.Logf("LLM semantic decisions: scroll=%+v text=%+v boundary=%+v", navigation, textDecision, assessment)
}

func callComputerLLM(t *testing.T, frame []byte, prompt, schema string, out any) {
	t.Helper()
	tmp := t.TempDir()
	schemaPath := filepath.Join(tmp, "schema.json")
	resultPath := filepath.Join(tmp, "result.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0o600); err != nil {
		t.Fatal(err)
	}
	model := strings.TrimSpace(os.Getenv("COMPUTER_LLM_TEST_MODEL"))
	if model == "" {
		model = "gpt-5.5"
	}
	args := []string{"exec", "--ephemeral", "--ignore-rules", "--skip-git-repo-check", "-s", "read-only", "-m", model}
	if len(frame) > 0 {
		imagePath := filepath.Join(tmp, "frame.jpg")
		if err := os.WriteFile(imagePath, frame, 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--image", imagePath)
	}
	args = append(args, "--output-schema", schemaPath, "--output-last-message", resultPath, prompt)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = tmp
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("LLM semantic decision failed: %v\n%s", err, raw)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode LLM output %q: %v", raw, err)
	}
}

func mapsFromAny(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]any); ok {
			out = append(out, mapped)
		}
	}
	return out
}

func findMapByString(t *testing.T, values []map[string]any, key, want string) map[string]any {
	t.Helper()
	for _, value := range values {
		if stringValue(value[key]) == want {
			return value
		}
	}
	t.Fatalf("missing %s=%q in %v", key, want, values)
	return nil
}

func revisionFromOutput(t *testing.T, out map[string]any) int {
	t.Helper()
	delta, _ := out["som_delta"].(map[string]any)
	switch value := delta["revision"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	}
	t.Fatalf("missing SoM revision: %v", out)
	return 0
}

func boolFromAny(value any) bool {
	result, _ := value.(bool)
	return result
}

func stringsFromAny(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, stringValue(item))
	}
	return out
}

func containsNamedTarget(value any, name string) bool {
	for _, target := range mapsFromAny(value) {
		if stringValue(target["accessible_name"]) == name {
			return true
		}
	}
	return false
}

func mustJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

const semanticNavigationFixtureHTML = `<!doctype html><style>
html,body{margin:0;height:100%;overflow:hidden;font-family:system-ui}.top{height:56px;display:flex;justify-content:flex-end;align-items:center;background:#111;padding:0 20px}.top button{height:36px;width:120px}.layout{height:544px;display:grid;grid-template-columns:1fr 320px;gap:12px}.pane{overflow-y:auto;border:1px solid #aaa;padding:12px}.spacer{height:700px}
</style><div class="top"><button id="publish">Publish</button></div><div class="layout">
<main id="editor" class="pane" contenteditable="true" role="textbox" aria-label="Post body"><p>Draft introduction</p><div class="spacer"></div><p>Editor footer</p></main>
<aside id="settings" class="pane" role="region" aria-label="Settings"><h2>Settings</h2><label>Audience <select><option>Everyone</option></select></label><div class="spacer"></div><button id="schedule">Set publish date</button></aside>
</div>`
