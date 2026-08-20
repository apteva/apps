package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// TestComputerAppBrowserControlledMaskedDateRange verifies the generic DOM
// contract used by React-controlled masked date widgets: the application owns
// field state, accepts changes through input events, and renders mm/dd/yyyy.
// It runs locally by default and can run unchanged in Browserbase.
//
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 APTEVA_HEADLESS_BROWSER=1 go test -run TestComputerAppBrowserControlledMaskedDateRange -timeout 3m .
//	RUN_COMPUTER_APP_BROWSER_TESTS=1 COMPUTER_APP_BROWSER_BACKEND=browserbase BROWSERBASE_API_KEY=... BROWSERBASE_PROJECT_ID=... go test -run TestComputerAppBrowserControlledMaskedDateRange -timeout 5m .
func TestComputerAppBrowserControlledMaskedDateRange(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_APP_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("Browserbase credentials are required")
	}
	runControlledMaskedDateRange(t, backend, false)
}

// TestComputerAppBrowserbaseControlledMaskedDateRange is the release gate for
// the hosted-browser path that failed on Patreon's Insights date range.
func TestComputerAppBrowserbaseControlledMaskedDateRange(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSERBASE_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSERBASE_TESTS=1")
	}
	if os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "" {
		t.Skip("Browserbase credentials are required")
	}
	runControlledMaskedDateRange(t, "browserbase", false)
}

type temporalLLMDecision struct {
	Action       string `json:"action"`
	TargetID     string `json:"target_id"`
	ExpectedName string `json:"expected_name"`
	ExpectedRole string `json:"expected_role"`
	Value        string `json:"value"`
	Reason       string `json:"reason"`
}

// TestLLMControlledMaskedDateRangeLive gives a real model the actual annotated
// browser frame and SoM. The model must use the stable labelled field for ISO
// entry, then use a semantic calendar gridcell after a structured rejection.
func TestLLMControlledMaskedDateRangeLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is required for the authenticated LLM regression")
	}
	backend := strings.TrimSpace(os.Getenv("COMPUTER_LLM_BROWSER_BACKEND"))
	if backend == "" {
		backend = "local"
	}
	if backend == "browserbase" && (os.Getenv("BROWSERBASE_API_KEY") == "" || os.Getenv("BROWSERBASE_PROJECT_ID") == "") {
		t.Skip("Browserbase credentials are required")
	}
	runControlledMaskedDateRange(t, backend, true)
}

func runControlledMaskedDateRange(t *testing.T, backend string, withLLM bool) {
	t.Helper()
	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"))
	opened := sc.MCP("browser_session", map[string]any{
		"action": "open", "backend": backend, "url": controlledMaskedDateDataURL(),
		"viewport": map[string]any{"width": 1000, "height": 700},
	})
	sessionID := stringValue(opened["session_id"])
	if sessionID == "" {
		t.Fatalf("open returned no session id: %v", opened)
	}
	defer sc.MCP("browser_close", map[string]any{"session_id": sessionID})

	shot := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "screenshot", "include_som": true,
	})
	targets := mapsFromAny(shot["som"])
	start := findMapByString(t, targets, "accessible_name", "Insights start date")
	if stringValue(start["placeholder"]) != "mm/dd/yyyy" || stringValue(start["format_hint"]) != "mm/dd/yyyy" || !boolFromAny(start["date_like"]) {
		t.Fatalf("date SoM metadata incomplete: %v", start)
	}
	if validity, _ := start["validity"].(map[string]any); validity == nil || !boolFromAny(validity["valid"]) {
		t.Fatalf("date validity missing from SoM: %v", start)
	}

	decision := temporalLLMDecision{
		Action: "set_temporal", TargetID: stringValue(start["id"]),
		ExpectedName: "Insights start date", ExpectedRole: stringValue(start["role"]), Value: "2026-08-01",
	}
	if withLLM {
		prompt := fmt.Sprintf(`You are controlling a browser date-range widget. Set the field named "Insights start date" to August 1, 2026. Choose one safe Computer action from the current SoM. Use the field's exact target_id and accessible_name, and pass the date in ISO form so Computer can apply format_hint. Do not invent an aria-label selector or click a calendar day yet.
Current SoM: %s`, mustJSON(targets))
		callComputerLLM(t, decodeScreenshot(t, shot), prompt,
			`{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["set_temporal"]},"target_id":{"type":"string"},"expected_name":{"type":"string"},"expected_role":{"type":"string"},"value":{"type":"string","pattern":"^2026-08-01$"},"reason":{"type":"string"}},"required":["action","target_id","expected_name","expected_role","value","reason"]}`,
			&decision)
		if decision.TargetID != stringValue(start["id"]) || decision.ExpectedName != "Insights start date" {
			t.Fatalf("model selected the wrong temporal target: decision=%+v start=%v", decision, start)
		}
	}

	setOut := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": decision.Action, "target_id": decision.TargetID,
		"expected_name": decision.ExpectedName, "value": decision.Value,
	})
	if !boolFromAny(setOut["temporal_verified"]) || stringValue(setOut["temporal_actual_value"]) != "08/01/2026" {
		t.Fatalf("controlled masked date did not accept ISO conversion: %v", setOut)
	}
	if stringValue(setOut["temporal_requested_value"]) != "2026-08-01" || stringValue(setOut["temporal_normalized_value"]) != "08/01/2026" || stringValue(setOut["temporal_format_hint"]) != "mm/dd/yyyy" {
		t.Fatalf("temporal diagnostics incomplete: %v", setOut)
	}

	shot = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "screenshot", "include_som": true,
	})
	targets = mapsFromAny(shot["som"])
	locked := findMapByString(t, targets, "accessible_name", "Insights end date")
	_, textErr := sc.MCPRaw("tools/call", map[string]any{
		"name": "computer_use", "arguments": map[string]any{
			"session_id": sessionID, "action": "set_text", "selector": "#end", "text": "08/20/2026",
		},
	})
	if textErr == nil || !strings.Contains(textErr.Error(), "actual single-line value") || strings.Contains(textErr.Error(), "paragraph") {
		t.Fatalf("single-line rejection used misleading paragraph verification: %v", textErr)
	}
	failure := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "set_temporal", "target_id": stringValue(locked["id"]),
		"expected_name": "Insights end date", "value": "2026-08-20",
	})
	if boolFromAny(failure["success"]) || stringValue(failure["temporal_error_code"]) != "value_mismatch" {
		t.Fatalf("rejected controlled value was not a structured failure: %v", failure)
	}
	if stringValue(failure["temporal_requested_value"]) != "2026-08-20" || stringValue(failure["temporal_actual_value"]) != "" || stringValue(failure["temporal_placeholder"]) != "mm/dd/yyyy" {
		t.Fatalf("failure discarded readback metadata: %v", failure)
	}

	shot = sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "screenshot", "include_som": true,
	})
	targets = mapsFromAny(shot["som"])
	day := findMapByString(t, targets, "accessible_name", "August 20, 2026")
	if stringValue(day["role"]) != "gridcell" {
		t.Fatalf("calendar date was not exposed as a semantic gridcell: %v", day)
	}
	clickDecision := temporalLLMDecision{
		Action: "click", TargetID: stringValue(day["id"]), ExpectedName: "August 20, 2026", ExpectedRole: "gridcell",
	}
	if withLLM {
		prompt := fmt.Sprintf(`Direct date entry was rejected with this structured Computer result: %s
Choose the safe fallback that sets the Insights end date to August 20, 2026. Use the exact current semantic calendar gridcell target_id, accessible_name, and role. Do not guess coordinates or a CSS selector.
Current SoM: %s`, mustJSON(failure), mustJSON(targets))
		callComputerLLM(t, decodeScreenshot(t, shot), prompt,
			`{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["click"]},"target_id":{"type":"string"},"expected_name":{"type":"string"},"expected_role":{"type":"string","enum":["gridcell"]},"value":{"type":"string"},"reason":{"type":"string"}},"required":["action","target_id","expected_name","expected_role","value","reason"]}`,
			&clickDecision)
		if clickDecision.TargetID != stringValue(day["id"]) || clickDecision.ExpectedName != "August 20, 2026" {
			t.Fatalf("model selected the wrong date-grid target: decision=%+v day=%v", clickDecision, day)
		}
	}
	sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "click", "target_id": clickDecision.TargetID,
		"expected_name": clickDecision.ExpectedName, "expected_role": clickDecision.ExpectedRole,
	})
	finalShot := sc.MCP("computer_use", map[string]any{
		"session_id": sessionID, "action": "screenshot", "include_som": true,
	})
	finalEnd := findMapByString(t, mapsFromAny(finalShot["som"]), "accessible_name", "Insights end date")
	if stringValue(finalEnd["current_value"]) != "08/20/2026" {
		t.Fatalf("calendar fallback did not update controlled state: %v", finalEnd)
	}
	t.Logf("controlled masked date validated with backend=%s llm=%t", backend, withLLM)
}

func controlledMaskedDateDataURL() string {
	html := `<!doctype html>
<html lang="en-US"><meta charset="utf-8"><title>Controlled masked date range</title>
<style>body{font:18px system-ui;margin:32px}.row{margin:18px 0}label{display:block;margin-bottom:6px}input{font:inherit;padding:9px;width:220px}.grid{display:grid;grid-template-columns:repeat(7,92px);gap:5px;margin-top:24px}[role=gridcell]{font:inherit;padding:8px 3px}</style>
<h1>Insights custom range</h1>
<div class="row"><label for="start">Insights start date</label><input id="start" type="text" placeholder="mm/dd/yyyy" pattern="[0-9]{2}/[0-9]{2}/[0-9]{4}" inputmode="numeric"></div>
<div class="row"><label for="end">Insights end date</label><input id="end" type="text" placeholder="mm/dd/yyyy" pattern="[0-9]{2}/[0-9]{2}/[0-9]{4}" inputmode="numeric"></div>
<div class="grid" role="grid" aria-label="August 2026 calendar">
  <button role="gridcell" aria-label="August 1, 2026" data-value="08/01/2026">1</button>
  <button role="gridcell" aria-label="August 20, 2026" data-value="08/20/2026">20</button>
</div>
<output id="state"></output>
<script>
// React-style controlled semantics without framework-specific selectors: the
// application state owns each displayed value and updates only from input
// events. The end field deliberately rejects direct writes so the semantic
// calendar fallback is exercised too.
const values={start:'',end:''};
function render(){start.value=values.start;end.value=values.end;state.textContent=JSON.stringify(values)}
start.addEventListener('input',()=>{if(/^\d{2}\/\d{2}\/\d{4}$/.test(start.value))values.start=start.value;queueMicrotask(render)});
end.addEventListener('input',()=>queueMicrotask(render));
for(const cell of document.querySelectorAll('[role=gridcell]'))cell.addEventListener('click',()=>{values.end=cell.dataset.value;render()});
render();
</script></html>`
	return "data:text/html;charset=utf-8," + url.PathEscape(html)
}
