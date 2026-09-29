package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func reliabilityFixture(t *testing.T) (*App, *sdk.AppCtx, *answerPlatform, *routeRow, *callRow, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	platform := &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "telnyx", Fields: map[string]string{"public_key": base64.StdEncoding.EncodeToString(pub)}}}
	a, ctx := withTelephonyTestContext(t, platform)
	route := &routeRow{ID: "audit-route", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 9, PhoneNumber: "+33189000001", Enabled: true, Secret: "audit-secret", TimeoutSec: 60, AnswerMode: answerModeHumanBrowser, InboundTransport: inboundTransportProgrammable, PreviousVoiceURL: `{"application_id":"app-123"}`}
	if e := a.db().insertRoute(*route); e != nil {
		t.Fatal(e)
	}
	row := callRow{ID: "audit-call", ThreadID: "pending-audit-call", Direction: "inbound", RouteID: route.ID, CarrierSID: "audit-carrier", CarrierSlug: "telnyx", CarrierConnectionID: 9, ToNumber: route.PhoneNumber, FromNumber: "+33611111111", Directive: "inbound pending", AudioBridgeURL: "pending", Status: "pending", PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: route.ProjectID, PeerKind: peerKindHuman}
	stored, _, e := a.db().insertInboundCallWithEvent(row, "pending")
	if e != nil {
		t.Fatal(e)
	}
	return a, ctx, platform, route, stored, priv
}
func reliabilityEvent(t *testing.T, a *App, route *routeRow, row *callRow, key ed25519.PrivateKey, event string, extra ...map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload := map[string]any{"call_control_id": row.CarrierSID, "connection_id": "app-123", "direction": "incoming", "to": route.PhoneNumber, "from": row.FromNumber, "client_state": base64.StdEncoding.EncodeToString([]byte("earlier-unrelated-media"))}
	for _, fields := range extra {
		for key, value := range fields {
			payload[key] = value
		}
	}
	raw, e := json.Marshal(map[string]any{"data": map[string]any{"id": event + ":" + row.CarrierSID, "event_type": event, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "payload": payload}})
	if e != nil {
		t.Fatal(e)
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/inbound/telnyx/"+route.ID+"?secret="+route.Secret+"&project_id="+route.ProjectID, strings.NewReader(string(raw)))
	req.Header.Set("Telnyx-Timestamp", stamp)
	req.Header.Set("Telnyx-Signature-Ed25519", base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(stamp+"|"), raw...))))
	rec := httptest.NewRecorder()
	a.handleTelnyxInbound(rec, req)
	return rec
}
func TestReliabilityUnansweredFallbackMustAnswerBeforeSpeech(t *testing.T) {
	a, ctx, p, route, row, _ := reliabilityFixture(t)
	def := routingDefinition{Entry: "notice", Nodes: []routingNode{{ID: "notice", Type: "announcement", Config: map[string]any{"text": "Advisers unavailable"}, Next: "end"}, {ID: "end", Type: "hangup"}}}
	plan, e := a.resolveRoutingDefinition(route, row.FromNumber, nil, &routingFlowVersionRow{ID: "v-audit", FlowID: "f-audit"}, def, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = a.finishTerminalRoutingPlan(ctx, row, route, plan); e != nil {
		t.Fatal(e)
	}
	if len(p.integrationCalls) != 1 || p.integrationCalls[0].Tool != "answer_call" {
		t.Fatalf("unanswered fallback must answer and wait for confirmation; commands=%+v", p.integrationCalls)
	}
}
func TestReliabilityLateRoutingMustPreserveAnswerClaim(t *testing.T) {
	a, ctx, _, route, row, _ := reliabilityFixture(t)
	if _, e := a.db().db.Exec(`UPDATE calls SET status='answering',thread_id='human-audit-call',peer_token='claimed-token' WHERE id=?`, row.ID); e != nil {
		t.Fatal(e)
	}
	plan := &inboundRoutingPlan{TerminalType: "destination", AnswerMode: answerModeHumanBrowser, DestinationID: "desk", NodeID: "desk"}
	if e := a.executeTelnyxRoutingPlan(ctx, row, route, plan); e != nil {
		t.Fatal(e)
	}
	current, e := a.db().findCall(row.ID)
	if e != nil {
		t.Fatal(e)
	}
	if current.Status != "answering" {
		t.Fatalf("late routing reset claimed call: status=%s peer_token=%s", current.Status, current.PeerToken)
	}
}
func TestReliabilityDisabledRouteMustAcceptExistingHangup(t *testing.T) {
	a, _, _, route, row, key := reliabilityFixture(t)
	if e := a.db().disableRoute(route.ID); e != nil {
		t.Fatal(e)
	}
	rec := reliabilityEvent(t, a, route, row, key, "call.hangup")
	current, e := a.db().findCall(row.ID)
	if e != nil {
		t.Fatal(e)
	}
	if rec.Code != http.StatusNoContent || !isTerminalStatus(current.Status) {
		t.Fatalf("hangup lost after route disabled: HTTP=%d call_status=%s", rec.Code, current.Status)
	}
}
func TestReliabilityUnrelatedMediaCompletionMustNotHangup(t *testing.T) {
	a, _, p, route, row, key := reliabilityFixture(t)
	if _, e := a.db().db.Exec(`UPDATE calls SET routing_flow_version_id='v-audit',announcement_state='speaking',announcement_text='Advisers unavailable' WHERE id=?`, row.ID); e != nil {
		t.Fatal(e)
	}
	rec := reliabilityEvent(t, a, route, row, key, "call.playback.ended")
	if rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	if len(p.integrationCalls) != 0 {
		t.Fatalf("unrelated playback completion interrupted terminal speech: %+v", p.integrationCalls)
	}
}

func TestReliabilityFailedAnswerSetupMustReleaseHumanClaim(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	row := phoneTestCall(t, a, "owner-failure", "pending")
	if _, err := a.db().db.Exec(`CREATE TRIGGER fail_owner BEFORE INSERT ON telephony_call_owners BEGIN SELECT RAISE(ABORT,'injected owner write failure'); END`); err != nil {
		t.Fatal(err)
	}
	alice := phoneTestIdentity("alice")
	result := phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{})
	if result.Code != 500 {
		t.Fatalf("injected setup failure: %d %s", result.Code, result.Body)
	}
	current, err := a.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "pending" || current.PeerToken != "" {
		t.Fatalf("failed setup cleanup stranded call: status=%s thread=%s", current.Status, current.ThreadID)
	}
	if _, err = a.db().db.Exec(`DROP TRIGGER fail_owner`); err != nil {
		t.Fatal(err)
	}
	result = phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{})
	if result.Code != 200 {
		t.Fatalf("call was not retryable: %d %s", result.Code, result.Body)
	}
}
func TestReliabilityNormalRoutingEndMustNotBecomeDeadlineFailure(t *testing.T) {
	a, ctx, _, route, row, _ := reliabilityFixture(t)
	if e := a.finishTerminalRoutingPlan(ctx, row, route, &inboundRoutingPlan{TerminalType: "hangup", RoutingResolution: "routing_exhausted"}); e != nil {
		t.Fatal(e)
	}
	current, e := a.db().findCall(row.ID)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(current.ErrorMessage, "deadline") {
		t.Fatalf("normal routing end mislabeled: status=%s error=%q", current.Status, current.ErrorMessage)
	}
}

func TestReliabilityFailedRingFallbackMustRemainRetryable(t *testing.T) {
	a, db, plan := ringFixture(t, "sequential")
	platform := &answerPlatform{failTool: "reject_call"}
	previous := globalCtx
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil).WithProject("p1")
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	route := routeRow{ID: "retry-route", ProjectID: "p1", CarrierSlug: "telnyx", CarrierConnectionID: 9, Enabled: true, InboundTransport: inboundTransportProgrammable}
	if e := db.insertRoute(route); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(routingExecutionContext{Route: route, Definition: routingDefinition{Entry: "end", Nodes: []routingNode{{ID: "end", Type: "hangup"}}}})
	if e != nil {
		t.Fatal(e)
	}
	plan.ContextJSON = string(raw)
	plan.OverflowNodeID = "end"
	insertRingCall(t, db, plan, "ring-retry")
	if _, e := db.db.Exec(`UPDATE calls SET carrier_slug='telnyx',route_id=?,carrier_connection_id=9 WHERE id='ring-retry'`, route.ID); e != nil {
		t.Fatal(e)
	}
	ringAdvance(t, db, "ring-retry", time.Now().Add(11*time.Minute))
	if e := a.finishRingRun(ctx, "ring_ring-retry_team", "ring-retry", "end"); e == nil {
		t.Fatal("expected injected carrier failure")
	}
	platform.failTool = ""
	// A fresh App object must recover the failed effect from the database,
	// without an in-memory reminder or another carrier webhook.
	if _, e := db.db.Exec(`UPDATE routing_effects SET next_attempt_at=?`, ringTime(time.Now().Add(-time.Second))); e != nil {
		t.Fatal(e)
	}
	restarted := &App{}
	if e := restarted.runRoutingEffects(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	row, e := db.findCall("ring-retry")
	if e != nil {
		t.Fatal(e)
	}
	if len(platform.integrationCalls) != 2 || !isTerminalStatus(row.Status) {
		t.Fatalf("failed fallback was abandoned: commands=%d status=%s", len(platform.integrationCalls), row.Status)
	}
}

func reliabilityPublishTerminal(t *testing.T, a *App, route *routeRow) {
	t.Helper()
	def := routingDefinition{Entry: "notice", Nodes: []routingNode{{ID: "notice", Type: "announcement", Config: map[string]any{"text": "Advisers unavailable"}, Next: "end"}, {ID: "end", Type: "hangup"}}}
	flow, err := a.saveRoutingFlow(route.ProjectID, "", "Reliability terminal", "", mustRoutingJSON(t, def))
	if err != nil {
		t.Fatal(err)
	}
	version, problems, err := a.publishRoutingFlow(route.ProjectID, flow.ID)
	if err != nil || len(problems) != 0 {
		t.Fatalf("publish: %v %v", err, problems)
	}
	route.FlowID, route.PublishedFlowVersionID = flow.ID, version.ID
	if _, err = a.db().db.Exec(`UPDATE inbound_routes SET flow_id=?,published_flow_version_id=? WHERE id=?`, flow.ID, version.ID, route.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReliabilityTerminalCommandsRecoverAcrossFailuresAndRestart(t *testing.T) {
	a, ctx, platform, route, row, key := reliabilityFixture(t)
	reliabilityPublishTerminal(t, a, route)
	row.CarrierSID = "fresh-terminal"
	platform.failTool = "answer_call"
	send := func(event string, want int, extra ...map[string]any) {
		t.Helper()
		r := reliabilityEvent(t, a, route, row, key, event, extra...)
		if r.Code != want {
			t.Fatalf("%s: %d %s", event, r.Code, r.Body)
		}
	}
	send("call.initiated", 503)
	stored, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, row.CarrierSID)
	if err != nil || stored == nil || stored.Status != "pending" {
		t.Fatalf("retryable call: %+v %v", stored, err)
	}
	row = stored
	platform.failTool = ""
	send("call.initiated", 204)
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[0].Input["command_id"] != platform.integrationCalls[1].Input["command_id"] {
		t.Fatalf("answer retry lost idempotency: %+v", platform.integrationCalls)
	}
	platform.failTool = "speak_text"
	send("call.answered", 503)
	platform.failTool = ""
	if _, err = a.db().db.Exec(`UPDATE routing_effects SET next_attempt_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID); err != nil {
		t.Fatal(err)
	}
	restarted := &App{installID: 42}
	if err = restarted.runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	a = restarted
	send("call.answered", 204)
	if len(platform.integrationCalls) != 4 || platform.integrationCalls[2].Tool != "speak_text" || platform.integrationCalls[2].Input["command_id"] != platform.integrationCalls[3].Input["command_id"] {
		t.Fatalf("speech retry: %+v", platform.integrationCalls)
	}
	send("call.playback.ended", 204)
	send("call.speak.ended", 204, map[string]any{"client_state": terminalAnnouncementClientState(row.ID), "status": "cancelled_amd"})
	if len(platform.integrationCalls) != 4 {
		t.Fatal("unrelated or incomplete speech ended the call")
	}
	platform.failTool = "hangup_call"
	send("call.speak.ended", 503, map[string]any{"client_state": terminalAnnouncementClientState(row.ID), "status": "completed"})
	platform.failTool = ""
	if _, err = a.db().db.Exec(`UPDATE routing_effects SET next_attempt_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID); err != nil {
		t.Fatal(err)
	}
	if err = (&App{}).runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	send("call.speak.ended", 204, map[string]any{"client_state": terminalAnnouncementClientState(row.ID), "status": "completed"})
	if len(platform.integrationCalls) != 6 || platform.integrationCalls[4].Tool != "hangup_call" || platform.integrationCalls[4].Input["command_id"] != platform.integrationCalls[5].Input["command_id"] {
		t.Fatalf("hangup recovery: %+v", platform.integrationCalls)
	}
	send("call.hangup", 204)
	row, err = a.db().findCall(row.ID)
	if err != nil || !isTerminalStatus(row.Status) {
		t.Fatalf("final state: %+v %v", row, err)
	}
	var recorded int
	if err = a.db().db.QueryRow(`SELECT COUNT(*) FROM carrier_command_events WHERE call_id=?`, row.ID).Scan(&recorded); err != nil || recorded != 6 {
		t.Fatalf("command journal %d %v", recorded, err)
	}
}

func TestReliabilityCancellationStopsPendingCarrierRecovery(t *testing.T) {
	a, ctx, platform, route, row, key := reliabilityFixture(t)
	reliabilityPublishTerminal(t, a, route)
	row.CarrierSID = "cancel-before-answer"
	platform.failTool = "answer_call"
	if r := reliabilityEvent(t, a, route, row, key, "call.initiated"); r.Code != 503 {
		t.Fatal(r.Code, r.Body)
	}
	if r := reliabilityEvent(t, a, route, row, key, "call.hangup"); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	platform.failTool = ""
	if _, err := a.db().db.Exec(`UPDATE routing_effects SET next_attempt_at=?`, ringTime(time.Now().Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := a.runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if r := reliabilityEvent(t, a, route, row, key, "call.answered"); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	if len(platform.integrationCalls) != 1 {
		t.Fatalf("late event or recovery controlled a canceled call: %+v", platform.integrationCalls)
	}
}

func TestReliabilityCarrierRetriesHaveABoundedBudget(t *testing.T) {
	a, ctx, platform, route, row, key := reliabilityFixture(t)
	reliabilityPublishTerminal(t, a, route)
	row.CarrierSID = "retry-budget"
	platform.failTool = "answer_call"
	for i := 0; i < routingEffectAttempts+3; i++ {
		reliabilityEvent(t, a, route, row, key, "call.initiated")
	}
	if len(platform.integrationCalls) != routingEffectAttempts {
		t.Fatalf("unbounded commands: %d", len(platform.integrationCalls))
	}
	stored, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, row.CarrierSID)
	if err != nil || stored.RoutingResolution != "routing_error" {
		t.Fatalf("retry classification: %+v %v", stored, err)
	}
	if err = a.runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != routingEffectAttempts {
		t.Fatal("recovery ignored exhausted budget")
	}
}

func TestReliabilityTerminalPlanRetainsPromptOnLaterCallbacks(t *testing.T) {
	a, ctx, _, route, row, _ := reliabilityFixture(t)
	route.CarrierSlug = "twilio"
	if _, err := a.db().db.Exec(`UPDATE inbound_routes SET carrier_slug='twilio' WHERE id=?`, route.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db().db.Exec(`UPDATE calls SET carrier_slug='twilio' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	row.CarrierSlug = "twilio"
	reliabilityPublishTerminal(t, a, route)
	plan, err := a.resolveInboundRoutingPlan(route, row.FromNumber, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.updateCallRoutingPlan(row, plan); err != nil {
		t.Fatal(err)
	}
	if err = a.finishTerminalRoutingPlan(ctx, row, route, plan); err != nil {
		t.Fatal(err)
	}
	current, err := a.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	pinned, replayed, err := a.routingPlanForCall(current, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err = a.writeTwilioRoutingPlan(rec, current, pinned, replayed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), "Advisers unavailable") || !strings.Contains(rec.Body.String(), "<Hangup/>") {
		t.Fatalf("callback lost prompt: %s", rec.Body)
	}
}

func TestReliabilityCarrierSignalingIsBoundedAndKeepsDistinctIDs(t *testing.T) {
	a, _, _, route, row, key := reliabilityFixture(t)
	headers := []carrierSIPHeader{{"Diversion", "<sip:+33123456789@example.test>"}, {"Authorization", "secret-do-not-store"}, {"History-Info", strings.Repeat("x", 1000)}}
	if r := reliabilityEvent(t, a, route, row, key, "call.hangup", map[string]any{"call_leg_id": "leg-123", "call_session_id": "session-456", "sip_headers": headers}); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	stored, err := a.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CarrierSID != row.CarrierSID || stored.CarrierLegID != "leg-123" || stored.CarrierSessionID != "session-456" {
		t.Fatalf("identities: %+v", stored)
	}
	if strings.Contains(stored.CarrierSignalingJSON, "secret") || strings.Contains(stored.CarrierSignalingJSON, strings.Repeat("x", 513)) {
		t.Fatalf("unbounded or sensitive headers retained: %s", stored.CarrierSignalingJSON)
	}
	if !strings.Contains(stored.CarrierSignalingJSON, "diversion") {
		t.Fatal("diversion missing")
	}
	if r := reliabilityEvent(t, a, route, row, key, "call.initiated", map[string]any{"call_control_id": "", "call_leg_id": "not-a-control-id"}); r.Code != 400 {
		t.Fatalf("leg ID accepted as control ID: %d", r.Code)
	}
}

func TestReliabilityFailedSessionCreationReleasesOwnerAndCapacity(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	row := phoneTestCall(t, a, "session-failure", "pending")
	if _, err := a.db().db.Exec(`CREATE TRIGGER fail_session BEFORE INSERT ON telephony_media_sessions BEGIN SELECT RAISE(ABORT,'injected session write failure'); END`); err != nil {
		t.Fatal(err)
	}
	alice := phoneTestIdentity("alice")
	response := phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{})
	if response.Code != 403 {
		t.Fatalf("session failure %d %s", response.Code, response.Body)
	}
	current, err := a.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "pending" || current.PeerToken != "" {
		t.Fatalf("session failure stranded claim: %+v", current)
	}
	for _, table := range []string{"telephony_call_owners", "phone_capacity", "telephony_media_sessions"} {
		var count int
		if err = a.db().db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE call_id=?`, row.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s retained %d rows: %v", table, count, err)
		}
	}
}

func TestReliabilityDisabledBandwidthRouteSettlesExistingCall(t *testing.T) {
	platform := &answerPlatform{}
	a, _ := withTelephonyTestContext(t, platform)
	route := routeRow{ID: "disabled-bw", ProjectID: "project-a", CarrierSlug: "bandwidth", CarrierConnectionID: 9, PhoneNumber: "+33123456789", Enabled: false, Secret: "secret", InboundTransport: inboundTransportProgrammable, PreviousVoiceURL: `{"account_id":"account-1","application_id":"application-1"}`}
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	row := callRow{ID: "disabled-bw-call", ThreadID: "pending-disabled-bw-call", Direction: "inbound", Status: "pending", RouteID: route.ID, ProjectID: route.ProjectID, CarrierSlug: "bandwidth", CarrierConnectionID: 9, CarrierSID: "bw-123", ToNumber: route.PhoneNumber, FromNumber: "+33611111111", PlacedAt: time.Now().UTC().Format(time.RFC3339)}
	if _, _, err := a.db().insertInboundCallWithEvent(row, "pending"); err != nil {
		t.Fatal(err)
	}
	send := func(phase, event string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"eventType": event, "accountId": "account-1", "applicationId": "application-1", "direction": "inbound", "callId": row.CarrierSID, "to": row.ToNumber, "from": row.FromNumber})
		r := httptest.NewRequest("POST", "/inbound/bandwidth/"+route.ID+phase+"?project_id="+route.ProjectID+"&secret=secret", strings.NewReader(string(body)))
		r.SetBasicAuth("apteva", "secret")
		w := httptest.NewRecorder()
		a.handleBandwidthInbound(w, r)
		return w
	}
	if w := send("", "initiate"); w.Code != 404 {
		t.Fatalf("disabled route admitted a call: %d", w.Code)
	}
	if w := send("/status", "disconnect"); w.Code != 204 {
		t.Fatalf("existing disconnect rejected: %d %s", w.Code, w.Body)
	}
	current, err := a.db().findCall(row.ID)
	if err != nil || !isTerminalStatus(current.Status) {
		t.Fatalf("existing call not settled: %+v %v", current, err)
	}
}

func TestReliabilityConcurrentLateRoutingAndAnswerHasOneOwner(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	phoneTestPolicy(t, a)
	row := phoneTestCall(t, a, "race-routing-answer", "pending")
	if _, err := a.db().db.Exec(`UPDATE calls SET peer_token='',thread_id='pending-'||id,carrier_slug='telnyx' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	row.CarrierSlug = "telnyx"
	row.PeerToken = ""
	row.ThreadID = "pending-" + row.ID
	route := routeRow{ProjectID: row.ProjectID, CarrierSlug: "telnyx", TimeoutSec: 60}
	plan := &inboundRoutingPlan{NodeID: "desk", TerminalType: "destination", AnswerMode: answerModeHumanBrowser, DestinationID: "sales"}
	done := make(chan error, 1)
	go func() { done <- a.executeTelnyxRoutingPlan(globalCtx.WithProject(row.ProjectID), &row, &route, plan) }()
	alice := phoneTestIdentity("alice")
	response := phoneTestRequest(a, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 {
		t.Fatalf("answer failed: %d %s", response.Code, response.Body)
	}
	current, err := a.db().findCall(row.ID)
	if err != nil || current.Status != "answering" || current.PeerToken == "" {
		t.Fatalf("claim was reset: %+v %v", current, err)
	}
	bob := phoneTestIdentity("bob")
	if second := phoneTestRequest(a, &bob, "POST", "/softphone/answer/"+row.ID, map[string]any{}); second.Code == 200 {
		t.Fatal("second adviser claimed same call")
	}
}

func TestReliabilityCarrierEvidenceLookupStaysProjectScoped(t *testing.T) {
	a, ctx, platform, route, row, key := reliabilityFixture(t)
	reliabilityPublishTerminal(t, a, route)
	row.CarrierSID = "evidence-call"
	r := reliabilityEvent(t, a, route, row, key, "call.initiated", map[string]any{"call_leg_id": "evidence-leg", "call_session_id": "evidence-session", "sip_headers": []carrierSIPHeader{{"Diversion", "<sip:+33123456789@example.test>"}, {"Authorization", "must-not-escape"}}})
	if r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	stored, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, row.CarrierSID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.toolCallGet(context.Background(), ctx, map[string]any{"call_id": stored.ID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "evidence-session") || !strings.Contains(string(raw), "carrier_commands") || !strings.Contains(string(raw), "diversion") || strings.Contains(string(raw), "must-not-escape") {
		t.Fatalf("diagnostic response %s", raw)
	}
	result, err = a.toolCallGet(context.Background(), ctx.WithProject("different-project"), map[string]any{"call_id": stored.ID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(result)
	if strings.Contains(string(raw), "evidence-session") {
		t.Fatal("carrier evidence crossed project boundary")
	}
	if len(platform.integrationCalls) != 1 {
		t.Fatal("diagnostic read changed carrier state")
	}
}

func TestReliabilityUpgradePreservesCallHistory(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	files, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() < "033" {
			applyMigrationFile(t, db, filepath.Join("migrations", file.Name()))
		}
	}
	_, err = db.Exec(`INSERT INTO calls(id,thread_id,to_number,from_number,directive,voice,audio_bridge_url,status,placed_at,project_id,announcement_state,announcement_text) VALUES('old','pending-old','+33111111111','+33611111111','','','pending','pending','then','project-a','speaking','Stored announcement')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO call_events(event_id,call_id,project_id,topic,revision,occurred_at,payload_json,created_at,published_at) VALUES('old-event','old','project-a','call.incoming',1,'then','{}','then','sent')`)
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationFile(t, db, "migrations/033_routing_reliability.sql")
	var state, message, published, headers string
	err = db.QueryRow(`SELECT announcement_state,announcement_text,carrier_signaling_json FROM calls WHERE id='old'`).Scan(&state, &message, &headers)
	if err != nil || state != "speaking" || message != "Stored announcement" || headers != "{}" {
		t.Fatalf("upgrade lost state: %s %s %s %v", state, message, headers, err)
	}
	err = db.QueryRow(`SELECT published_at FROM call_events WHERE event_id='old-event'`).Scan(&published)
	if err != nil || published != "sent" {
		t.Fatalf("upgrade lost history: %s %v", published, err)
	}
}

func TestReliabilityLegacyAnnouncementRecovery(t *testing.T) {
	a, ctx, platform, route, row, key := reliabilityFixture(t)
	_, err := a.db().db.Exec(`UPDATE calls SET routing_flow_version_id='legacy',announcement_state='awaiting_answer',announcement_text='Stored announcement',answered_at=?,carrier_answered_at=? WHERE id=?`, ringTime(time.Now()), ringTime(time.Now()), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "speak_text" || platform.integrationCalls[0].Input["payload"] != "Stored announcement" {
		t.Fatalf("legacy recovery: %+v", platform.integrationCalls)
	}
	rec := reliabilityEvent(t, a, route, row, key, "call.speak.ended", map[string]any{"client_state": terminalAnnouncementClientState(row.ID), "status": "completed"})
	if rec.Code != 204 || len(platform.integrationCalls) != 2 || platform.integrationCalls[1].Tool != "hangup_call" {
		t.Fatalf("legacy completion: %d %+v", rec.Code, platform.integrationCalls)
	}
}

func TestReliabilityDisabledXMLRoutesKeepExistingCallbacks(t *testing.T) {
	for _, provider := range []string{"twilio", "plivo"} {
		t.Run(provider, func(t *testing.T) {
			platform := &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: provider, Fields: map[string]string{"auth_token": "test-auth-token", "password": "test-auth-token"}}}
			a, _ := withTelephonyTestContext(t, platform)
			route := routeRow{ID: "disabled-xml", ProjectID: "project-a", CarrierSlug: provider, CarrierConnectionID: 9, PhoneNumber: "+33111111111", Secret: "secret", Enabled: false, AnswerMode: answerModeHumanBrowser, TimeoutSec: 60}
			if err := a.db().insertRoute(route); err != nil {
				t.Fatal(err)
			}
			row := callRow{ID: "existing-xml", ThreadID: "pending-existing-xml", Direction: "inbound", Status: "pending", PlacedAt: ringTime(time.Now()), ProjectID: route.ProjectID, RouteID: route.ID, CarrierSlug: provider, CarrierConnectionID: 9, CarrierSID: "existing-carrier", ToNumber: route.PhoneNumber, FromNumber: "+33611111111"}
			if err := a.db().insertCall(row); err != nil {
				t.Fatal(err)
			}
			send := func(suffix string, want int) {
				t.Helper()
				form := url.Values{"CallSid": {row.CarrierSID}, "CallUUID": {row.CarrierSID}, "CallStatus": {"completed"}, "To": {row.ToNumber}, "From": {row.FromNumber}}
				req := httptest.NewRequest("POST", "/inbound/"+provider+"/"+route.ID+suffix+"?project_id=project-a&secret=secret&call_id="+row.ID, strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if provider == "twilio" {
					signTwilioTestRequest(t, a, req, form)
				} else {
					signPlivoTestRequest(a, req, form, "test-auth-token")
				}
				rec := httptest.NewRecorder()
				if provider == "twilio" {
					a.handleTwilioInbound(rec, req)
				} else {
					a.handlePlivoInbound(rec, req)
				}
				if rec.Code != want {
					t.Fatalf("%s: %d %s", suffix, rec.Code, rec.Body)
				}
			}
			send("", 404)
			send("/wait", 200)
			send("/status", 204)
			current, err := a.db().findCall(row.ID)
			if err != nil || !isTerminalStatus(current.Status) {
				t.Fatalf("existing call not settled: %+v %v", current, err)
			}
		})
	}
}
