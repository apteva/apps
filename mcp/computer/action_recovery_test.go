package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestScreenshotRecoveryUsesOnlyValidArguments(t *testing.T) {
	for _, value := range []any{"som_delta", "none", "screenshot", "bogus", 42} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			fake := &fakeComp{display: api.DisplaySize{Width: 800, Height: 600}}
			app := appWithSession("br_repair", fake, "local")
			ctx := tk.NewAppCtx(t, "apteva.yaml")
			out, err := app.toolComputerUseMCP(context.Background(), ctx, map[string]any{"session_id": "br_repair", "action": "screenshot", "observation": value})
			if err != nil {
				t.Fatal(err)
			}
			result, ok := out.(sdk.MCPResult)
			if !ok || !result.IsError || result.StructuredContent["action_dispatched"] != false {
				t.Fatalf("result=%#v", out)
			}
			repair := result.StructuredContent["recovery"].(map[string]any)
			arguments := repair["arguments"].(map[string]any)
			if _, present := arguments["observation"]; present {
				t.Fatal("screenshot repair reintroduced observation")
			}
			if arguments["action"] != "screenshot" || arguments["annotate"] != true {
				t.Fatalf("repair=%#v", repair)
			}
			if err := validateObservationArgs("screenshot", arguments); err != nil {
				t.Fatal(err)
			}
			if fake.screenshotCalls != 0 || len(fake.actionOnlyCalls) != 0 {
				t.Fatal("invalid screenshot dispatched")
			}
		})
	}
	for _, value := range []any{nil, ""} {
		if err := validateObservationArgs("screenshot", map[string]any{"observation": value}); err != nil {
			t.Fatal("legacy empty defaults rejected", err)
		}
	}
	if err := validateObservationArgs("scroll", map[string]any{"observation": 42}); err == nil {
		t.Fatal("non-string observation accepted")
	}
	tool := findTool(t, (&App{}).MCPTools(), "computer_use")
	desc := tool.InputSchema["properties"].(map[string]any)["observation"].(map[string]any)["description"].(string)
	if !strings.Contains(desc, "NEVER pass observation with action=screenshot") {
		t.Fatal(desc)
	}
}

func recoveryFixture() (*App, *fakeComp) {
	fake := &fakeComp{url: "https://fixture.example", display: api.DisplaySize{Width: 800, Height: 600},
		somTargets:    []api.SetOfMarkTarget{{ID: "som_input", Label: 1, AccessibleName: "Read details", Role: "button"}},
		scrollRegions: []api.ScrollRegion{{ID: "scroll_document", Name: "Page", Role: "document", CanScrollY: true}, {ID: "scroll_editor", Name: "Editor", Role: "textbox", CanScrollY: true}, {ID: "scroll_horizontal", Name: "Timeline", CanScrollX: true}}}
	return appWithSession("br_repair", fake, "local"), fake
}
func TestAmbiguousScrollRecoveryDoesNotDispatchAndExposesTargets(t *testing.T) {
	app, fake := recoveryFixture()
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	_, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_repair", "action": "scroll", "direction": "down"})
	var repair *actionRecoveryError
	if !errors.As(err, &repair) {
		t.Fatalf("error=%v", err)
	}
	regions := repair.details["scroll_regions"].([]api.ScrollRegion)
	if len(regions) != 2 || regions[0].ID != "scroll_document" || repair.details["action_dispatched"] != false {
		t.Fatalf("details=%#v", repair.details)
	}
	if len(fake.actionOnlyCalls) > 0 || fake.screenshotCalls > 0 {
		t.Fatal("ambiguous scroll dispatched")
	}
	// A copied ID succeeds; no fallback chooses the first region automatically.
	if _, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_repair", "action": "scroll", "direction": "down", "target_id": regions[0].ID, "observation": "none"}); err != nil {
		t.Fatal(err)
	}
	if fake.lastAction.TargetID != regions[0].ID {
		t.Fatalf("target=%+v", fake.lastAction)
	}
}
func TestAmbiguousBatchScrollPreservesCompletedSteps(t *testing.T) {
	app, fake := recoveryFixture()
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	_, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_repair", "action": "batch", "steps": []any{map[string]any{"action": "click", "label": 1}, map[string]any{"action": "scroll", "direction": "down"}, map[string]any{"action": "click", "label": 1}}})
	var repair *actionRecoveryError
	if !errors.As(err, &repair) {
		t.Fatalf("error=%v", err)
	}
	if repair.details["completed_steps"] != 1 || repair.details["failed_step"] != 2 || repair.details["failed_action_dispatched"] != false || repair.details["action_dispatched"] != true {
		t.Fatalf("partial batch=%#v", repair.details)
	}
	if len(fake.actionOnlyCalls) != 1 || fake.actionOnlyCalls[0].Type != "click" {
		t.Fatalf("dispatched=%+v", fake.actionOnlyCalls)
	}
}
func TestHTTPRecoveryIsStructured(t *testing.T) {
	app, _ := recoveryFixture()
	prior := globalCtx
	globalCtx = tk.NewAppCtx(t, "apteva.yaml")
	t.Cleanup(func() { globalCtx = prior })
	request := httptest.NewRequest(http.MethodPost, "/sessions/br_repair/use", strings.NewReader(`{"action":"screenshot","observation":"som_delta"}`))
	response := httptest.NewRecorder()
	app.handleComputerUse(response, request)
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 400 || body["error_code"] != "invalid_observation" || body["recovery"] == nil {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
}
func TestCompactObservationsDeduplicateAndBoundTargetMetadata(t *testing.T) {
	targets := []map[string]any{{"id": "one", "accessible_name": "First", "dangerous": true}, {"id": "two", "accessible_name": "Second"}}
	out := map[string]any{"som": targets, "som_delta": map[string]any{"revision": 1, "added": targets, "changed": []any{}, "removed": []string{}}, "safety_targets": targets[:1], "revealed_targets": targets, "png_b64": "legacy", "screenshot": map[string]any{"base64": "single-image"}}
	compact := compactComputerObservation(out, map[string]any{"compact": true})
	if _, ok := compact["png_b64"]; ok {
		t.Fatal("duplicate image alias")
	}
	delta := compact["som_delta"].(map[string]any)
	if _, ok := delta["added"]; ok {
		t.Fatal("full target metadata repeated beside som")
	}
	if len(delta["added_ids"].([]string)) != 2 || compact["safety_targets"] != nil || compact["revealed_targets"] != nil {
		t.Fatalf("compact=%+v", compact)
	}
	if compact["screenshot"].(map[string]any)["base64"] != "single-image" {
		t.Fatal("fresh image lost")
	}
	var many []map[string]any
	for i := 0; i < 100; i++ {
		many = append(many, map[string]any{"id": fmt.Sprintf("target_%d", i), "accessible_name": strings.Repeat("x", 4096), "dangerous": true})
	}
	bounded := compactComputerObservation(map[string]any{"som": many}, map[string]any{"compact": true})
	encoded, _ := json.Marshal(bounded["som"])
	if len(encoded) > compactObservationBudget+2 || bounded["observation_truncated"] != true || bounded["coverage_incomplete"] != true || bounded["omitted_target_count"].(int) < 1 {
		t.Fatalf("budget: %d bytes %+v", len(encoded), bounded)
	}
	legacy := map[string]any{"som": many, "png_b64": "legacy"}
	if compactComputerObservation(legacy, nil)["png_b64"] != "legacy" {
		t.Fatal("legacy response changed")
	}
}
func TestCompactRepeatedScreenshotKeepsStateAndRevision(t *testing.T) {
	app, fake := recoveryFixture()
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	for i := 0; i < 2; i++ {
		value, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_repair", "action": "screenshot", "compact": true})
		if err != nil {
			t.Fatal(err)
		}
		out := value.(map[string]any)
		delta := out["som_delta"].(map[string]any)
		if out["screenshot"] == nil || out["scroll_regions"] == nil {
			t.Fatal("useful observation lost")
		}
		if i == 1 && delta["unchanged"] != true {
			t.Fatalf("repeated delta=%+v", delta)
		}
	}
	if fake.screenshotCalls != 2 {
		t.Fatalf("explicit screenshots=%d", fake.screenshotCalls)
	}
}

func TestViewHandoffAssertionsRejectDuplicateOrMissingLifecycle(t *testing.T) {
	view := map[string]any{"app": "computer", "name": "browser-view", "resource": map[string]any{"app_install_id": float64(4242), "resource_type": "computer.browser_session", "resource_id": "br_fixture"}, "props": map[string]any{"session_id": "br_fixture"}}
	fixture := func() *deepSeekTranscript {
		return &deepSeekTranscript{OpenedView: view, Closed: true, CloseBeforeFinal: true, Messages: []map[string]any{{"phase": "progress", "text": "The browser view is ready", "components": []any{view}}, {"phase": "final", "text": "Release code MAPLE-731; the browser view above retains the final frame."}}}
	}
	if err := fixture().assertHandoff(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"duplicate", "duplicate final", "missing", "wrong locator", "not closed", "closed late", "wrong answer"} {
		t.Run(name, func(t *testing.T) {
			trace := fixture()
			switch name {
			case "duplicate":
				trace.Messages[1]["components"] = []any{view}
			case "duplicate final":
				trace.Messages = append(trace.Messages, map[string]any{"phase": "final", "text": "MAPLE-731 in the existing browser view"})
			case "missing":
				delete(trace.Messages[0], "components")
			case "wrong locator":
				trace.Messages[0]["components"] = []any{map[string]any{"app": "computer", "name": "browser-view", "props": view["props"]}}
			case "not closed":
				trace.Closed = false
			case "closed late":
				trace.CloseBeforeFinal = false
			case "wrong answer":
				trace.Messages[1]["text"] = "Browser view ready"
			}
			if err := trace.assertHandoff(); err == nil {
				t.Fatal("invalid transcript passed")
			}
		})
	}
}

func TestOpenReturnsLiveHandoffGuidance(t *testing.T) {
	previous := newBackend
	t.Cleanup(func() { newBackend = previous })
	fake := &fakeComp{display: api.DisplaySize{Width: 800, Height: 600}}
	newBackend = func(backends.Config) (backends.Computer, error) { return fake, nil }
	app := &App{installID: 4242, reg: &registry{m: map[string]*session{}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	opened, err := app.toolBrowserSession(ctx, map[string]any{"action": "open", "backend": "local"})
	if err != nil {
		t.Fatal(err)
	}
	result := opened.(map[string]any)
	defer app.toolBrowserClose(ctx, map[string]any{"session_id": result["session_id"]})
	handoff := result["view_handoff"].(map[string]any)
	if handoff["phase"] != "progress" || handoff["present_once"] != true || handoff["component_field"] != "view" {
		t.Fatalf("handoff=%+v", handoff)
	}
	if !strings.Contains(handoff["next_step"].(string), "even for a quick task") {
		t.Fatal("short-task exception still overrides a live-view request")
	}
}
