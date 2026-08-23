package main

import (
	"encoding/json"
	"fmt"
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

func TestLiveRoutingUsesTimeoutBranchForEmptyCarrierGather(t *testing.T) {
	def := routingDefinition{Entry: "menu", Nodes: []routingNode{
		{ID: "menu", Type: "dtmf_menu", Branches: map[string]string{"1": "sales", "default": "end", "timeout": "browser"}},
		{ID: "sales", Type: "destination", Config: map[string]any{"destination_id": "sales"}},
		{ID: "browser", Type: "destination", Config: map[string]any{"destination_id": "timeout-browser"}},
		{ID: "end", Type: "hangup"},
	}}
	result := simulateRoutingDefinition(def, routingSimulationContext{
		StopAtInteraction: true,
		Digits:            map[string]string{"menu": routingTimeoutSelection},
	})
	if !result.Valid || result.DestinationID != "timeout-browser" {
		t.Fatalf("timeout result = %#v", result)
	}
	if got := result.Trace[0].Outcome; got != "timeout" {
		t.Fatalf("timeout outcome = %q", got)
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
	if input["maximum_digits"] != 1 || input["maximum_tries"] != 1 || input["valid_digits"] != "12" || input["gather_id"] != "call-1:menu" {
		t.Fatalf("gather input = %#v", input)
	}
}

func TestTelnyxIVRAnswerStartsRecordingAndBrowserAnswerStartsStream(t *testing.T) {
	db := testCallsDB(t)
	platform := &answerPlatform{}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil)
	app := &App{}
	row := &callRow{
		ID: "call-ivr", ProjectID: "p1", CarrierSlug: "telnyx", CarrierSID: "v3:test",
		CarrierConnectionID: 9, CallbackSecret: "secret", RecordingMode: recordingModeAlways,
		RecordingChannels: "dual", RoutingFlowVersionID: "flow-version-1",
		AnsweredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := app.answerTelnyxIVR(ctx, row); err != nil {
		t.Fatal(err)
	}
	answer := platform.integrationCalls[0]
	if answer.Tool != "answer_call" || answer.Input["record"] != "record-from-answer" || answer.Input["record_channels"] != "dual" {
		t.Fatalf("IVR answer did not start recording: %#v", answer)
	}
	if err := app.answerInboundCarrierCall(ctx, row); err != nil {
		t.Fatal(err)
	}
	stream := platform.integrationCalls[1]
	if stream.Tool != "start_streaming" || stream.Input["call_control_id"] != "v3:test" ||
		stream.Input["stream_codec"] != "L16" || stream.Input["stream_bidirectional_codec"] != "L16" ||
		stream.Input["stream_bidirectional_sampling_rate"] != 16000 || stream.Input["stream_bidirectional_target_legs"] != "self" {
		t.Fatalf("browser answer did not resume the answered IVR leg: %#v", stream)
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

func TestBulkNumberAssignmentIsAtomicAcrossCarrierValidation(t *testing.T) {
	db := testCallsDB(t)
	app := &App{}
	withRoutingTestDB(t, app, db)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, route := range []routeRow{
		{ID: "route-twilio", ProjectID: "p1", CarrierSlug: "twilio", CarrierConnectionID: 1, PhoneNumber: "+33189000001", AgentID: 7, Enabled: true, TimeoutSec: 60, AnswerMode: answerModeHumanBrowser, Secret: "a", CreatedAt: now, UpdatedAt: now, RecordingMode: recordingModeInherit, InboundTransport: inboundTransportProgrammable},
		{ID: "route-telnyx", ProjectID: "p1", CarrierSlug: "telnyx", CarrierConnectionID: 2, PhoneNumber: "+33189000002", AgentID: 7, Enabled: true, TimeoutSec: 60, AnswerMode: answerModeHumanBrowser, Secret: "b", CreatedAt: now, UpdatedAt: now, RecordingMode: recordingModeInherit, InboundTransport: inboundTransportProgrammable},
	} {
		if err := db.insertRoute(route); err != nil {
			t.Fatal(err)
		}
	}
	def := routingDefinition{Entry: "message", Nodes: []routingNode{{ID: "message", Type: "voicemail"}}}
	raw, _ := json.Marshal(def)
	flow, err := app.saveRoutingFlow("p1", "", "Voicemail", "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, validation, err := app.publishRoutingFlow("p1", flow.ID); err != nil || len(validation) != 0 {
		t.Fatalf("publish: validation=%v err=%v", validation, err)
	}
	result, err := app.assignRoutingFlowToNumbers("p1", flow.ID, []string{"route-twilio", "route-telnyx"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || len(result.Numbers) != 2 || result.Numbers[0].Valid == result.Numbers[1].Valid {
		t.Fatalf("validation = %#v", result)
	}
	for _, routeID := range []string{"route-twilio", "route-telnyx"} {
		route, err := db.findRoute(routeID)
		if err != nil {
			t.Fatal(err)
		}
		if route.FlowID != "" || route.PublishedFlowVersionID != "" {
			t.Fatalf("%s was partially assigned: %#v", routeID, route)
		}
	}
}

func TestBulkNumberAssignmentSharesFlowAndExpandsPerNumberVariables(t *testing.T) {
	db := testCallsDB(t)
	app := &App{}
	withRoutingTestDB(t, app, db)
	now := time.Now().UTC().Format(time.RFC3339)
	for index, routeID := range []string{"route-one", "route-two"} {
		route := routeRow{ID: routeID, ProjectID: "p1", CarrierSlug: "twilio", CarrierConnectionID: int64(index + 1), PhoneNumber: fmt.Sprintf("+3318900000%d", index+1), AgentID: 7, Enabled: true, TimeoutSec: 60, AnswerMode: answerModeHumanBrowser, Secret: routeID, CreatedAt: now, UpdatedAt: now, RecordingMode: recordingModeInherit, InboundTransport: inboundTransportProgrammable}
		if err := db.insertRoute(route); err != nil {
			t.Fatal(err)
		}
	}
	destination, err := app.saveRoutingDestination("p1", "browser-shared", "Browser", "browser", map[string]any{"greeting": "Welcome to {{number.brand}}"}, true)
	if err != nil {
		t.Fatal(err)
	}
	def := routingDefinition{Entry: "answer", Nodes: []routingNode{{ID: "answer", Type: "destination", Config: map[string]any{"destination_id": destination.ID}}}}
	raw, _ := json.Marshal(def)
	flow, err := app.saveRoutingFlow("p1", "", "Shared main line", "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	version, validation, err := app.publishRoutingFlow("p1", flow.ID)
	if err != nil || len(validation) != 0 {
		t.Fatalf("publish: version=%#v validation=%v err=%v", version, validation, err)
	}
	result, err := app.assignRoutingFlowToNumbers("p1", flow.ID, []string{"route-one", "route-two"}, map[string]any{
		"route-one": map[string]any{"brand": "Paris", "recording_mode": "always"},
		"route-two": map[string]any{"brand": "Madrid"},
	})
	if err != nil || !result.Valid {
		t.Fatalf("assign: result=%#v err=%v", result, err)
	}
	for routeID, greeting := range map[string]string{"route-one": "Welcome to Paris", "route-two": "Welcome to Madrid"} {
		route, err := db.findRoute(routeID)
		if err != nil {
			t.Fatal(err)
		}
		if route.FlowID != flow.ID || route.PublishedFlowVersionID != version.ID {
			t.Fatalf("%s assignment = %#v", routeID, route)
		}
		if routeID == "route-one" && route.RecordingMode != recordingModeAlways {
			t.Fatalf("recording override = %q", route.RecordingMode)
		}
		plan, err := app.resolveInboundRoutingPlan(route, "+33600000000", nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Greeting != greeting {
			t.Fatalf("%s greeting = %q, want %q", routeID, plan.Greeting, greeting)
		}
	}
	numbers, err := app.listNumbersForRoutingFlow("p1", flow.ID)
	if err != nil || len(numbers) != 2 {
		t.Fatalf("assigned numbers=%#v err=%v", numbers, err)
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
