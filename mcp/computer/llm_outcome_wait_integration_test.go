package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

type llmOutcomeWaitDecision struct {
	SessionID   string `json:"session_id"`
	Action      string `json:"action"`
	Observation string `json:"observation"`
	Steps       []struct {
		Action       string                   `json:"action"`
		Label        int                      `json:"label"`
		TargetID     string                   `json:"target_id"`
		ExpectedText string                   `json:"expected_text"`
		Match        string                   `json:"match"`
		TimeoutMS    int                      `json:"timeout_ms"`
		Conditions   []computer.WaitCondition `json:"conditions"`
	} `json:"steps"`
}

// TestLLMChoosesOutcomeWaitAndSemanticDeltaLive exercises the actual current
// Computer tool description and schema with a real model. The production-shaped
// scenario must not lead it back to global stability or repeated screenshots.
func TestLLMChoosesOutcomeWaitAndSemanticDeltaLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is required for the authenticated LLM regression")
	}

	tool := findTool(t, (&App{}).MCPTools(), "computer_use")
	prompt := `You are choosing the exact arguments for Computer's computer_use tool.
Return one tool call only, as a JSON object.

Production situation:
- Existing session_id: br_prod
- Current URL: https://www.patreon.com/example/posts/title/edit
- The fresh semantic snapshot contains label 10, target_id som_update, accessible name "Update", role "button".
- Clicking Update successfully saves and leaves that exact /edit URL. The destination URL is not known, so verify that the URL changed from the supplied current URL; do not compare the current URL for equality with the old /edit URL.
- Patreon permanently keeps 3 unrelated loading indicators, 2 background requests, and 2 embedded player frames active, so page-wide quiet is not a useful success condition.
- Minimize model payload. Do not request screenshot bytes or a complete SoM after the operation; obtain one compact refreshed semantic observation.
- Make the click semantic and guarded against the target changing immediately before dispatch.
- Verify the known successful outcome. Do not use a fixed sleep and do not wait for global page stability.

Tool description:
` + tool.Description + `

Tool input schema:
` + mustJSON(tool.InputSchema)

	var response struct {
		ArgumentsJSON string `json:"arguments_json"`
	}
	callComputerLLM(t, nil, prompt,
		`{"type":"object","additionalProperties":false,"properties":{"arguments_json":{"type":"string","description":"The exact computer_use arguments as a JSON object."}},"required":["arguments_json"]}`,
		&response)

	var raw map[string]any
	if err := json.Unmarshal([]byte(response.ArgumentsJSON), &raw); err != nil {
		t.Fatalf("decode model tool arguments %q: %v", response.ArgumentsJSON, err)
	}
	var decision llmOutcomeWaitDecision
	if err := json.Unmarshal([]byte(response.ArgumentsJSON), &decision); err != nil {
		t.Fatalf("decode typed model tool arguments %q: %v", response.ArgumentsJSON, err)
	}
	if decision.SessionID != "br_prod" || decision.Action != "batch" {
		t.Fatalf("model did not choose one batch call: %+v", decision)
	}
	if decision.Observation != "som_delta" {
		t.Fatalf("model did not choose the compact semantic observation: %+v", decision)
	}
	if _, present := raw["include_som"]; present {
		t.Fatalf("model requested a full SoM despite compact observation: %s", response.ArgumentsJSON)
	}
	if _, present := raw["annotate"]; present {
		t.Fatalf("model requested screenshot annotation despite compact observation: %s", response.ArgumentsJSON)
	}
	if len(decision.Steps) != 2 {
		t.Fatalf("model should choose guarded click then outcome wait: %+v", decision)
	}
	click, wait := decision.Steps[0], decision.Steps[1]
	if click.Action != "click" || click.ExpectedText != "Update" || (click.Label != 10 && click.TargetID != "som_update") {
		t.Fatalf("model click is not semantically guarded: %+v", click)
	}
	if wait.Action != "wait_for" || len(wait.Conditions) == 0 {
		t.Fatalf("model did not choose an outcome-specific wait: %+v", wait)
	}
	foundURLChange := false
	for _, condition := range wait.Conditions {
		if condition.Type == "url_changed" && condition.Value == "https://www.patreon.com/example/posts/title/edit" {
			foundURLChange = true
		}
	}
	if !foundURLChange {
		t.Fatalf("model did not verify the known URL transition: %+v", wait)
	}

	// Execute the model's exact JSON through Computer's real MCP handler shape,
	// not just a parallel test-only validator.
	fake := &fakeComp{
		png: []byte{0x89, 0x50, 0x4e, 0x47}, url: "https://www.patreon.com/example/posts/title/saved",
		somTargets: []computer.SetOfMarkTarget{{ID: "som_update", Label: 10, Role: "button", AccessibleName: "Update"}},
		waitResult: computer.WaitResult{Matched: true, Match: "any", CurrentURL: "https://www.patreon.com/example/posts/title/saved"},
	}
	app := appWithSession("br_prod", fake, "local")
	executed, err := app.toolComputerUse(tk.NewAppCtx(t, "apteva.yaml"), raw)
	if err != nil {
		t.Fatalf("Computer rejected the real model's chosen arguments: %v\n%s", err, response.ArgumentsJSON)
	}
	executedMap := executed.(map[string]any)
	if executedMap["completed_steps"] != 2 || executedMap["timed_out"] == true {
		t.Fatalf("model-selected batch did not execute as intended: %#v", executedMap)
	}
	if _, embedded := executedMap["screenshot"]; embedded {
		t.Fatalf("model-selected compact observation embedded image bytes: %#v", executedMap)
	}
	t.Logf("LLM chose guarded batch with outcome wait and compact observation: %s", response.ArgumentsJSON)
}
