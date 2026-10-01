package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDirectSIPDecisionUsesSharedOfferLoop(t *testing.T) {
	a, db, plan := decisionFixture(t)
	var execution routingExecutionContext
	if err := json.Unmarshal([]byte(plan.ContextJSON), &execution); err != nil {
		t.Fatal(err)
	}
	execution.Route.CarrierSlug = "didww"
	execution.Route.InboundTransport = inboundTransportSIPDirect
	if errors := a.validateFlowForRoute("p1", execution.Definition, &execution.Route); len(errors) != 0 {
		t.Fatalf("DIDWW direct SIP browser decision rejected: %v", errors)
	}
	plan.ContextJSON = mustRoutingJSON(t, execution)
	insertRingCall(t, db, plan, "sip-decision")
	decision := getDecision(t, a, "sip-decision")
	if err := a.completeDecision(decision, decisionResponse{DecisionID: decision.ID, Action: "offer", DestinationID: "alice"}, ""); err != nil {
		t.Fatal(err)
	}
	offers, err := db.activeRingOffers("sip-decision", "p1")
	if err != nil || len(offers) != 1 || offers[0].DestinationID != "alice" {
		t.Fatalf("direct SIP offer = %+v, %v", offers, err)
	}
}

func TestDirectSIPRejectsMediaNodesItCannotExecute(t *testing.T) {
	a, _, plan := decisionFixture(t)
	var execution routingExecutionContext
	if err := json.Unmarshal([]byte(plan.ContextJSON), &execution); err != nil {
		t.Fatal(err)
	}
	execution.Route.CarrierSlug = "didww"
	execution.Route.InboundTransport = inboundTransportSIPDirect
	for _, node := range []routingNode{
		{ID: "notice", Type: "announcement", Config: map[string]any{"text": "Closed"}},
		{ID: "menu", Type: "dtmf_menu"},
		{ID: "message", Type: "voicemail"},
	} {
		definition := execution.Definition
		definition.Nodes = append(append([]routingNode{}, definition.Nodes...), node)
		if problems := a.validateFlowForRoute("p1", definition, &execution.Route); len(problems) == 0 {
			t.Fatalf("unexecutable %s accepted", node.Type)
		}
	}
	webhook := execution.Route
	webhook.CarrierSlug = "bandwidth"
	webhook.InboundTransport = inboundTransportProgrammable
	if problems := a.validateFlowForRoute("p1", execution.Definition, &webhook); len(problems) != 0 {
		t.Fatalf("Bandwidth inbound decision rejected: %v", problems)
	}
}

func TestPreviouslyPublishedDirectSIPAnnouncementIsRejectedAtIngress(t *testing.T) {
	a, _, _ := decisionFixture(t)
	definition := routingDefinition{Entry: "notice", Nodes: []routingNode{
		{ID: "notice", Type: "announcement", Config: map[string]any{"text": "We are closed"}, Next: "end"},
		{ID: "end", Type: "hangup"},
	}}
	flow, err := a.saveRoutingFlow("p1", "", "Old flow", "", mustRoutingJSON(t, definition))
	if err != nil {
		t.Fatal(err)
	}
	version, problems, err := a.publishRoutingFlow("p1", flow.ID)
	if err != nil || len(problems) != 0 {
		t.Fatalf("publish legacy flow: %v %v", err, problems)
	}
	route := &routeRow{ID: "old-route", ProjectID: "p1", CarrierSlug: "didww", InboundTransport: inboundTransportSIPDirect, PublishedFlowVersionID: version.ID}
	if err := a.validatePublishedFlowForInboundRoute(route); err == nil || !strings.Contains(err.Error(), "announcements") {
		t.Fatalf("old media flow reached direct SIP ingress: %v", err)
	}
	route.CarrierSlug = "twilio"
	route.InboundTransport = inboundTransportProgrammable
	if err := a.validatePublishedFlowForInboundRoute(route); err != nil {
		t.Fatalf("programmable route rejected supported announcement: %v", err)
	}
}

func mustRoutingJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
