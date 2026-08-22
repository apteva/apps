package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestRoutingValidationRejectsDanglingEdgesAndCycles(t *testing.T) {
	def := routingDefinition{Entry: "start", Nodes: []routingNode{
		{ID: "start", Type: "announcement", Next: "loop"},
		{ID: "loop", Type: "announcement", Next: "start"},
		{ID: "orphan", Type: "hangup"},
	}}
	errs := validateRoutingDefinition(def)
	for _, want := range []string{"cycle", "unreachable"} {
		found := false
		for _, got := range errs {
			if containsFold(got, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("validation errors %v do not contain %q", errs, want)
		}
	}
}

func TestRoutingSimulationScheduleAndDTMF(t *testing.T) {
	def := routingDefinition{Entry: "hours", Nodes: []routingNode{
		{ID: "hours", Type: "schedule", Branches: map[string]string{"open": "menu", "closed": "closed"}, Config: map[string]any{"timezone": "Europe/Paris", "days": []any{"mon"}, "start": "09:00", "end": "18:00"}},
		{ID: "menu", Type: "dtmf_menu", Branches: map[string]string{"1": "sales", "default": "closed"}},
		{ID: "sales", Type: "destination", Config: map[string]any{"destination_id": "browser-sales"}},
		{ID: "closed", Type: "voicemail"},
	}}
	result := simulateRoutingDefinition(def, routingSimulationContext{
		At:     "2026-08-24T10:00:00+02:00",
		Digits: map[string]string{"menu": "1"},
	})
	if !result.Valid || result.DestinationID != "browser-sales" || result.TerminalNodeID != "sales" {
		t.Fatalf("simulation = %#v", result)
	}
	if got := result.Trace[0].Outcome; got != "open" {
		t.Fatalf("schedule outcome = %q", got)
	}
	if got := result.Trace[1].Outcome; got != "digit:1" {
		t.Fatalf("DTMF outcome = %q", got)
	}
}

func TestLiveRoutingPausesAtDTMFUntilCarrierReturnsDigits(t *testing.T) {
	def := routingDefinition{Entry: "menu", Nodes: []routingNode{
		{ID: "menu", Type: "dtmf_menu", Config: map[string]any{"prompt": "Press one"}, Branches: map[string]string{"1": "browser", "default": "end"}},
		{ID: "browser", Type: "destination", Config: map[string]any{"destination_id": "browser"}},
		{ID: "end", Type: "hangup"},
	}}
	waiting := simulateRoutingDefinition(def, routingSimulationContext{StopAtInteraction: true})
	if !waiting.Valid || waiting.TerminalType != "dtmf_menu" || waiting.TerminalNodeID != "menu" {
		t.Fatalf("waiting result = %#v", waiting)
	}
	selected := simulateRoutingDefinition(def, routingSimulationContext{StopAtInteraction: true, Digits: map[string]string{"menu": "1"}})
	if !selected.Valid || selected.DestinationID != "browser" {
		t.Fatalf("selected result = %#v", selected)
	}
}

func TestTwilioRoutingPlanRendersSingleDigitGather(t *testing.T) {
	db := testCallsDB(t)
	app := &App{}
	withRoutingTestDB(t, app, db)
	recorder := httptest.NewRecorder()
	row := &callRow{ID: "call-1", CallbackSecret: "secret", ProjectID: "p1"}
	plan := &inboundRoutingPlan{TerminalType: "dtmf_menu", NodeID: "menu", Prompt: "Press one for sales", ValidDigits: "12"}
	if err := app.writeTwilioRoutingPlan(recorder, row, &routeRow{}, plan); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	for _, want := range []string{`<Gather`, `numDigits="1"`, `actionOnEmptyResult="true"`, `Press one for sales`, `/ivr/twilio/call-1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("TwiML %q does not contain %q", body, want)
		}
	}
}

func TestTelnyxRoutingUsesGatherUsingSpeakContract(t *testing.T) {
	db := testCallsDB(t)
	platform := &answerPlatform{}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil)
	app := &App{}
	row := &callRow{ID: "call-1", CarrierSID: "v3:test", CarrierConnectionID: 9}
	plan := &inboundRoutingPlan{TerminalType: "dtmf_menu", NodeID: "menu", Prompt: "Press one", ValidDigits: "12"}
	if err := app.startTelnyxGather(ctx, row, plan); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "gather_using_speak" {
		t.Fatalf("calls = %#v", platform.integrationCalls)
	}
	input := platform.integrationCalls[0].Input
	if input["maximum_digits"] != 1 || input["valid_digits"] != "12" || input["gather_id"] != "call-1:menu" {
		t.Fatalf("gather input = %#v", input)
	}
}

func TestPublishingPinsImmutableVersion(t *testing.T) {
	db := testCallsDB(t)
	app := &App{}
	// App.db() uses globalCtx in production. Use the tiny routing test seam.
	withRoutingTestDB(t, app, db)

	destination, err := app.saveRoutingDestination("p1", "browser", "Browser", "browser", map[string]any{}, true)
	if err != nil {
		t.Fatal(err)
	}
	def := routingDefinition{Entry: "answer", Nodes: []routingNode{{ID: "answer", Type: "destination", Config: map[string]any{"destination_id": destination.ID}}}}
	raw, _ := json.Marshal(def)
	flow, err := app.saveRoutingFlow("p1", "", "Main line", "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	v1, validation, err := app.publishRoutingFlow("p1", flow.ID)
	if err != nil || len(validation) != 0 {
		t.Fatalf("publish v1: version=%#v validation=%v err=%v", v1, validation, err)
	}

	def.Nodes[0].Label = "Changed draft"
	raw, _ = json.Marshal(def)
	if _, err := app.saveRoutingFlow("p1", flow.ID, flow.Name, "", string(raw)); err != nil {
		t.Fatal(err)
	}
	stored, err := app.findRoutingVersion("p1", v1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if containsFold(stored.Definition, "Changed draft") {
		t.Fatal("published version changed with draft")
	}
	v2, validation, err := app.publishRoutingFlow("p1", flow.ID)
	if err != nil || len(validation) != 0 || v2.Version != 2 {
		t.Fatalf("publish v2: %#v %v %v", v2, validation, err)
	}
}

func TestLegacyRoutesBecomeEquivalentGeneratedFlows(t *testing.T) {
	db := testCallsDB(t)
	app := &App{}
	withRoutingTestDB(t, app, db)
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{ID: "route-1", ProjectID: "p1", CarrierSlug: "telnyx", CarrierConnectionID: 3, PhoneNumber: "+33189000000", AgentID: 7, Enabled: true, TimeoutSec: 60, AnswerMode: answerModeHumanBrowser, Secret: "secret", CreatedAt: now, UpdatedAt: now, RecordingMode: recordingModeInherit, InboundTransport: inboundTransportProgrammable}
	if err := db.insertRoute(route); err != nil {
		t.Fatal(err)
	}
	if err := app.ensureLegacyRoutingFlows(nil); err != nil {
		t.Fatal(err)
	}
	stored, err := db.findRoute(route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FlowID == "" || stored.PublishedFlowVersionID == "" {
		t.Fatalf("route was not migrated: %#v", stored)
	}
	plan, err := app.resolveInboundRoutingPlan(stored, "+33600000000", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AnswerMode != answerModeHumanBrowser {
		t.Fatalf("answer mode = %q", plan.AnswerMode)
	}
	if plan.AgentID != route.AgentID {
		t.Fatalf("agent = %d", plan.AgentID)
	}
	// The migration is idempotent.
	if err := app.ensureLegacyRoutingFlows(nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM routing_flows WHERE project_id='p1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("flow count=%d err=%v", count, err)
	}
}

func containsFold(value, fragment string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(fragment))
}

func withRoutingTestDB(t *testing.T, _ *App, db *callsDB) {
	t.Helper()
	previous := globalCtx
	globalCtx = sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, &answerPlatform{}, nil)
	t.Cleanup(func() { globalCtx = previous })
}
