package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestReviewH1RoutingToolsEnforceRouteOwnership(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	now := time.Now().UTC().Format(time.RFC3339)
	for _, route := range []routeRow{
		{ID: "route-a", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 1, PhoneNumber: "+33189000011", AgentID: 7, Enabled: true, TimeoutSec: 60, AnswerMode: answerModeHumanBrowser, Secret: "a", CreatedAt: now, UpdatedAt: now, RecordingMode: recordingModeInherit, InboundTransport: inboundTransportProgrammable},
		{ID: "route-b", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 1, PhoneNumber: "+33189000012", AgentID: 8, Enabled: true, TimeoutSec: 60, AnswerMode: answerModeHumanBrowser, Secret: "b", CreatedAt: now, UpdatedAt: now, RecordingMode: recordingModeInherit, InboundTransport: inboundTransportProgrammable},
	} {
		if err := a.db().insertRoute(route); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(routingDefinition{Entry: "message", Nodes: []routingNode{{ID: "message", Type: "voicemail"}}})
	flow, err := a.saveRoutingFlow("project-a", "", "Voicemail", "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, validation, err := a.publishRoutingFlow("project-a", flow.ID); err != nil || len(validation) != 0 {
		t.Fatalf("publish: validation=%v err=%v", validation, err)
	}
	agentA := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7})
	agentB := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 8})

	// Agent A cannot re-point agent B's route, by either tool.
	result, err := a.toolRoutesSetFlow(agentA, ctx, map[string]any{"route_id": "route-b", "flow_id": flow.ID})
	if err != nil || toolError(result) == "" {
		t.Fatalf("set_flow on foreign route = %#v err=%v", result, err)
	}
	result, _ = a.toolFlowsAssignNumbers(agentA, ctx, map[string]any{"flow_id": flow.ID, "route_ids": []any{"route-a", "route-b"}})
	if toolError(result) == "" {
		t.Fatalf("assign including foreign route = %#v", result)
	}
	if route, _ := a.db().findRoute("route-b"); route.FlowID != "" {
		t.Fatalf("foreign route was re-pointed: %#v", route)
	}

	// Agent B owns route-b and may assign it.
	if result, _ = a.toolRoutesSetFlow(agentB, ctx, map[string]any{"route_id": "route-b", "flow_id": flow.ID}); toolError(result) != "" {
		t.Fatalf("owner set_flow = %#v", result)
	}
	// Now the flow serves B's route: A may neither unassign, edit nor publish it.
	if result, _ = a.toolFlowsUnassignNumbers(agentA, ctx, map[string]any{"flow_id": flow.ID, "route_ids": []any{"route-b"}}); toolError(result) == "" {
		t.Fatalf("unassign foreign route = %#v", result)
	}
	if result, _ = a.toolFlowsUpdate(agentA, ctx, map[string]any{"flow_id": flow.ID, "definition": map[string]any{"entry": "message", "nodes": []any{map[string]any{"id": "message", "type": "voicemail"}}}}); toolError(result) == "" {
		t.Fatalf("update flow serving foreign route = %#v", result)
	}
	if result, _ = a.toolFlowsPublish(agentA, ctx, map[string]any{"flow_id": flow.ID}); toolError(result) == "" {
		t.Fatalf("publish flow serving foreign route = %#v", result)
	}
	if result, _ = a.toolFlowsPublish(agentB, ctx, map[string]any{"flow_id": flow.ID}); toolError(result) != "" {
		t.Fatalf("owner publish = %#v", result)
	}
	// Platform principals keep project-wide access.
	if result, _ = a.toolFlowsUnassignNumbers(context.Background(), ctx, map[string]any{"flow_id": flow.ID, "route_ids": []any{"route-b"}}); toolError(result) != "" {
		t.Fatalf("platform unassign = %#v", result)
	}
}
