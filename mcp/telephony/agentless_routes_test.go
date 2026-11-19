package main

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// createAgentlessRoute creates a number only people answer, as the platform.
func createAgentlessRoute(t *testing.T, a *App, ctx *sdk.AppCtx, phone, sid string) *routeRow {
	t.Helper()
	result, err := a.toolRoutesCreate(context.Background(), ctx, map[string]any{"phone_number": phone, "phone_number_id": sid, "answer_mode": "human_browser"})
	if err != nil {
		t.Fatal(err)
	}
	created := createdRoute(t, result)
	route, err := a.db().findRoute(created["id"].(string))
	if err != nil || route == nil {
		t.Fatalf("route not stored: %v", err)
	}
	return route
}

// A platform caller can create a number only people answer without naming an
// agent. Such a number cannot be switched to agent or realtime answering, and
// agents cannot change it.
func TestPlatformCreatesAgentlessHumanRoute(t *testing.T) {
	a, ctx := platformRouteTestApp(t)
	platformCaller := context.Background()

	route := createAgentlessRoute(t, a, ctx, "+14155550102", "PN2")
	if route.AgentID != 0 || route.AnswerMode != answerModeHumanBrowser {
		t.Fatalf("agent-less route = agent %d mode %s", route.AgentID, route.AnswerMode)
	}

	for _, args := range []map[string]any{
		{"phone_number": "+14155550103", "phone_number_id": "PN3"},
		{"phone_number": "+14155550103", "phone_number_id": "PN3", "answer_mode": "agent"},
		{"phone_number": "+14155550103", "phone_number_id": "PN3", "answer_mode": "realtime_immediate", "directive": "Help the caller."},
	} {
		result, _ := a.toolRoutesCreate(platformCaller, ctx, args)
		if msg := toolError(result); !strings.Contains(msg, "agent_id required") {
			t.Fatalf("agent-less %v accepted: %#v", args["answer_mode"], result)
		}
	}
	result, _ := a.toolRoutesCreate(platformCaller, ctx, map[string]any{"phone_number": "+14155550103", "phone_number_id": "PN3", "answer_mode": "human_browser", "agent_id": float64(-4)})
	if msg := toolError(result); !strings.Contains(msg, "positive") {
		t.Fatalf("negative agent accepted: %#v", result)
	}

	for _, args := range []map[string]any{
		{"route_id": route.ID, "answer_mode": "agent"},
		{"route_id": route.ID, "answer_mode": "realtime_immediate", "directive": "Help the caller."},
	} {
		updated, _ := a.toolRoutesSetAnswerMode(platformCaller, ctx, args)
		if msg := toolError(updated); !strings.Contains(msg, "agent_id required") {
			t.Fatalf("agent-less route switched to %v: %#v", args["answer_mode"], updated)
		}
	}
	if _, err := a.setRouteAnswerMode(ctx, route.ID, answerModeAgent, "", "", ""); err == nil || !strings.Contains(err.Error(), "agent_id required") {
		t.Fatalf("panel switched agent-less route to agent answering: %v", err)
	}
	if updated, _ := a.toolRoutesSetAnswerMode(platformCaller, ctx, map[string]any{"route_id": route.ID, "answer_mode": "human_browser", "greeting": "Hello"}); toolError(updated) != "" {
		t.Fatalf("human answering refused: %#v", updated)
	}
	if stored, _ := a.db().findRoute(route.ID); stored.AnswerMode != answerModeHumanBrowser {
		t.Fatalf("answer mode changed to %s", stored.AnswerMode)
	}

	agent7 := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7})
	if updated, _ := a.toolRoutesSetAnswerMode(agent7, ctx, map[string]any{"route_id": route.ID, "answer_mode": "human_browser"}); !strings.Contains(toolError(updated), "another agent") {
		t.Fatalf("an agent changed a platform-owned number: %#v", updated)
	}
	if listed, _ := a.toolRoutesList(agent7, ctx, nil); len(listed.(map[string]any)["routes"].([]map[string]any)) != 0 {
		t.Fatalf("an agent sees a platform-owned number: %#v", listed)
	}

	// The panel accepts the same request.
	w := numbersRequest(t, a, "/numbers/routes/create", map[string]any{"phone_number": "+14155550104", "phone_number_id": "PN4", "answer_mode": "human_browser", "configure": false})
	if w.Code != 200 {
		t.Fatalf("panel refused an agent-less human route: %d %s", w.Code, w.Body.String())
	}
	w = numbersRequest(t, a, "/numbers/routes/create", map[string]any{"phone_number": "+14155550105", "phone_number_id": "PN5", "configure": false})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "agent_id required") {
		t.Fatalf("panel accepted an agent-less agent route: %d %s", w.Code, w.Body.String())
	}
}

// A call on a number with no agent queues no incoming-call event: the
// platform refuses agent 0, and the outbox would retry it until retention.
func TestAgentlessRouteCallQueuesNoAgentEvent(t *testing.T) {
	a, ctx := platformRouteTestApp(t)
	route := createAgentlessRoute(t, a, ctx, "+14155550102", "PN2")
	call, created, err := a.recordInboundCall(route, "CA-agentless", "+15550001000", route.PhoneNumber)
	if err != nil || !created {
		t.Fatalf("record call: created=%t err=%v", created, err)
	}
	if call.AgentID != 0 || call.PeerKind != peerKindHuman || call.Status != "pending" {
		t.Fatalf("call = agent %d peer %s status %s", call.AgentID, call.PeerKind, call.Status)
	}
	outbox := func(id string) (n int) {
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM inbound_event_outbox WHERE call_id=?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := outbox(call.ID); n != 0 {
		t.Fatalf("agent-less call queued %d agent events", n)
	}

	// A number with an agent still tells it.
	result, _ := a.toolRoutesCreate(context.Background(), ctx, map[string]any{"phone_number": "+14155550103", "phone_number_id": "PN3", "agent_id": float64(5), "answer_mode": "human_browser"})
	owned, _ := a.db().findRoute(createdRoute(t, result)["id"].(string))
	agentCall, _, err := a.recordInboundCall(owned, "CA-owned", "+15550001000", owned.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	if n := outbox(agentCall.ID); n != 1 {
		t.Fatalf("call with an agent queued %d events", n)
	}
}

// Saved AI and agent destinations always name their agent, so a number with
// no agent can still send callers to AI through a flow: the call takes the
// destination's agent, and that agent is told.
func TestAgentlessRouteFlowToAIUsesTheDestinationAgent(t *testing.T) {
	a, ctx := platformRouteTestApp(t)
	route := createAgentlessRoute(t, a, ctx, "+14155550102", "PN2")
	const project = "project-a"
	if _, err := a.saveRoutingDestination(project, "dest-ai-unowned", "AI", "ai", map[string]any{"directive": "Help the caller."}, true); err == nil || !strings.Contains(err.Error(), "require agent_id") {
		t.Fatalf("AI destination saved without an agent: %v", err)
	}
	if _, err := a.saveRoutingDestination(project, "dest-agent", "Support", "agent", map[string]any{"agent_id": 5}, true); err != nil {
		t.Fatal(err)
	}
	definition := routingDefinition{Entry: "answer", Nodes: []routingNode{{ID: "answer", Type: "destination", Config: map[string]any{"destination_id": "dest-agent"}}}}
	flow, err := a.saveRoutingFlow(project, "", "To support", "", mustRoutingJSON(t, definition))
	if err != nil {
		t.Fatal(err)
	}
	if _, problems, err := a.publishRoutingFlow(project, flow.ID); err != nil || len(problems) != 0 {
		t.Fatalf("publish: %v %v", err, problems)
	}
	if validation, err := a.assignRoutingFlowToNumbers(project, flow.ID, []string{route.ID}, nil); err != nil || !validation.Valid {
		t.Fatalf("assign: %+v %v", validation, err)
	}
	assigned, _ := a.db().findRoute(route.ID)
	call, _, err := a.recordInboundCall(assigned, "CA-flow", "+15550001000", assigned.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	if call.AgentID != 5 || call.PeerKind == peerKindHuman {
		t.Fatalf("flow call = agent %d peer %s", call.AgentID, call.PeerKind)
	}
	var agent int64
	if err := ctx.AppDB().QueryRow(`SELECT agent_id FROM inbound_event_outbox WHERE call_id=?`, call.ID).Scan(&agent); err != nil || agent != 5 {
		t.Fatalf("destination agent not told: agent=%d err=%v", agent, err)
	}
}
