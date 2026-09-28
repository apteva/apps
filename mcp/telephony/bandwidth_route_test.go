package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestBandwidthInboundDecisionChecksBindingAndOffersAdviser(t *testing.T) {
	platform := &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "bandwidth", Fields: map[string]string{
		"account_id": "account-1", "application_id": "application-1",
	}}}
	app, ctx := withTelephonyTestContext(t, platform)
	route := routeRow{ID: "bw-route", ProjectID: "project-a", CarrierSlug: "bandwidth", CarrierConnectionID: 19,
		PhoneNumber: "+33123456789", AgentID: 7, Enabled: true, Secret: "route-secret", AnswerMode: answerModeHumanBrowser,
		TimeoutSec: 60, InboundTransport: inboundTransportProgrammable}
	identity := phoneTestIdentity("bw-adviser")
	if _, err := app.saveRoutingDestination(route.ProjectID, "bw-adviser", "Bandwidth adviser", "browser", map[string]any{
		"capacity": destinationCapacity{Identity: identity, Limit: 1},
	}, true); err != nil {
		t.Fatal(err)
	}
	policy, _ := json.Marshal(phonePolicy{Users: []phoneUser{{Identity: identity, Enabled: true, phoneGrant: phoneGrant{Role: "user", Destinations: []string{"bw-adviser"}}}}})
	if _, err := app.db().db.Exec(`INSERT INTO telephony_access_policies(project_id,revision,policy_json) VALUES(?,?,?)`, route.ProjectID, 1, string(policy)); err != nil {
		t.Fatal(err)
	}
	definition := routingDefinition{Entry: "choose", Nodes: []routingNode{
		{ID: "choose", Type: "decision", Config: map[string]any{"function_id": 42, "timeout_ms": 5000, "destination_ids": []string{"bw-adviser"}}, Branches: map[string]string{"fallback": "end"}},
		{ID: "end", Type: "hangup"},
	}}
	flow, err := app.saveRoutingFlow(route.ProjectID, "", "Bandwidth decision", "", mustRoutingJSON(t, definition))
	if err != nil {
		t.Fatal(err)
	}
	version, problems, err := app.publishRoutingFlow(route.ProjectID, flow.ID)
	if err != nil || len(problems) != 0 {
		t.Fatalf("publish: %v %v", err, problems)
	}
	route.FlowID, route.PublishedFlowVersionID = flow.ID, version.ID
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	stored, _ := app.db().findRoute(route.ID)
	if err := app.configureBandwidthRoute(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 0 {
		t.Fatalf("shared Bandwidth Location was modified: %+v", platform.integrationCalls)
	}
	path := "/inbound/bandwidth/bw-route?secret=route-secret&project_id=project-a"
	callback := `{"eventType":"initiate","accountId":"account-1","applicationId":"application-1","direction":"inbound","callId":"bw-call-1","to":"+33123456789","from":"+33611111111"}`
	request := func(body, password string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.SetBasicAuth("apteva", password)
		response := httptest.NewRecorder()
		app.handleBandwidthInbound(response, req)
		return response
	}
	if got := request(callback, "wrong"); got.Code != http.StatusForbidden {
		t.Fatalf("wrong password accepted: %d", got.Code)
	}
	if got := request(strings.Replace(callback, "application-1", "other-app", 1), "route-secret"); got.Code != http.StatusForbidden {
		t.Fatalf("wrong application accepted: %d", got.Code)
	}
	response := request(callback, "route-secret")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "<Redirect") || !strings.Contains(response.Body.String(), "<Pause") {
		t.Fatalf("initiate response: %d %s", response.Code, response.Body.String())
	}
	call, err := app.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, "bw-call-1")
	if err != nil || call == nil || call.Status != "pending" {
		t.Fatalf("inbound call: %+v %v", call, err)
	}
	decisions, err := app.listDecisions(route.ProjectID, call.ID)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("routing decisions: %+v %v", decisions, err)
	}
	decision := decisions[0]
	if err := app.completeDecision(decision, decisionResponse{DecisionID: decision.ID, Action: "offer", DestinationID: "bw-adviser"}, ""); err != nil {
		t.Fatal(err)
	}
	offers, err := app.db().activeRingOffers(call.ID, route.ProjectID)
	if err != nil || len(offers) != 1 || offers[0].DestinationID != "bw-adviser" {
		t.Fatalf("Bandwidth routing offer: %+v %v", offers, err)
	}
	if len(platform.integrationCalls) != 0 {
		t.Fatalf("adviser offer unexpectedly changed carrier state: %+v", platform.integrationCalls)
	}
	if err := app.answerInboundCarrierCall(ctx, call); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "update_call" ||
		platform.integrationCalls[0].Input["callId"] != "bw-call-1" || platform.integrationCalls[0].Input["state"] != "active" {
		t.Fatalf("selected call was not redirected for media: %+v", platform.integrationCalls)
	}
	streamPath := "/xml/bandwidth/" + call.ID + "?token=" + call.CallbackSecret + "&project_id=" + call.ProjectID
	streamRequest := httptest.NewRequest(http.MethodPost, streamPath, strings.NewReader(`{"eventType":"redirect","callId":"bw-call-1"}`))
	streamRequest.SetBasicAuth("apteva", "route-secret")
	streamResponse := httptest.NewRecorder()
	app.handleBandwidthXML(streamResponse, streamRequest)
	if streamResponse.Code != http.StatusOK || !strings.Contains(streamResponse.Body.String(), `mode="bidirectional"`) {
		t.Fatalf("inbound media callback: %d %s", streamResponse.Code, streamResponse.Body.String())
	}
}

func TestBandwidthInboundRejectsNumberMismatch(t *testing.T) {
	platform := &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "bandwidth", Fields: map[string]string{"account_id": "account-1", "application_id": "application-1"}}}
	app, ctx := withTelephonyTestContext(t, platform)
	route := routeRow{ID: "bw-route", ProjectID: "project-a", CarrierSlug: "bandwidth", CarrierConnectionID: 19, PhoneNumber: "+33123456789", AgentID: 7, Enabled: true, Secret: "route-secret", InboundTransport: inboundTransportProgrammable}
	definition := routingDefinition{Entry: "notice", Nodes: []routingNode{
		{ID: "notice", Type: "announcement", Config: map[string]any{"text": "We are closed"}, Next: "end"},
		{ID: "end", Type: "hangup"},
	}}
	flow, err := app.saveRoutingFlow(route.ProjectID, "", "Bandwidth closed", "", mustRoutingJSON(t, definition))
	if err != nil {
		t.Fatal(err)
	}
	version, problems, err := app.publishRoutingFlow(route.ProjectID, flow.ID)
	if err != nil || len(problems) != 0 {
		t.Fatalf("publish: %v %v", err, problems)
	}
	route.FlowID, route.PublishedFlowVersionID = flow.ID, version.ID
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	stored, _ := app.db().findRoute(route.ID)
	if err := app.configureBandwidthRoute(ctx, stored); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/inbound/bandwidth/bw-route?secret=route-secret&project_id=project-a", strings.NewReader(`{"eventType":"initiate","accountId":"account-1","applicationId":"application-1","direction":"inbound","callId":"bw-call-2","to":"+33123456780","from":"+33611111111"}`))
	req.SetBasicAuth("apteva", "route-secret")
	response := httptest.NewRecorder()
	app.handleBandwidthInbound(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("call for another number accepted: %d %s", response.Code, response.Body.String())
	}
	valid := `{"eventType":"initiate","accountId":"account-1","applicationId":"application-1","direction":"inbound","callId":"bw-call-2","to":"+33123456789","from":"+33611111111"}`
	req = httptest.NewRequest(http.MethodPost, "/inbound/bandwidth/bw-route?secret=route-secret&project_id=project-a", strings.NewReader(valid))
	req.SetBasicAuth("apteva", "route-secret")
	response = httptest.NewRecorder()
	app.handleBandwidthInbound(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `<SpeakSentence>We are closed</SpeakSentence><Hangup/>`) {
		t.Fatalf("closed hours announcement: %d %s", response.Code, response.Body.String())
	}
	status := `{"eventType":"disconnect","eventTime":"2026-09-28T10:00:00Z","accountId":"account-1","applicationId":"application-1","direction":"inbound","callId":"bw-call-2","to":"+33123456789","from":"+33611111111","cause":"hangup"}`
	req = httptest.NewRequest(http.MethodPost, "/inbound/bandwidth/bw-route/status?secret=route-secret&project_id=project-a", strings.NewReader(status))
	req.SetBasicAuth("apteva", "route-secret")
	response = httptest.NewRecorder()
	app.handleBandwidthInbound(response, req)
	if response.Code != http.StatusNoContent {
		t.Fatalf("disconnect callback: %d %s", response.Code, response.Body.String())
	}
	call, err := app.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, "bw-call-2")
	if err != nil || call == nil || call.Status != "completed" {
		t.Fatalf("closed call lifecycle: %+v %v", call, err)
	}
}
