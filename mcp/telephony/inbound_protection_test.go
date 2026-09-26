package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestSignedTelnyxAnnouncementWebhooksWaitForSpeechEnd(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	platform := &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "telnyx", Fields: map[string]string{
		"public_key": base64.StdEncoding.EncodeToString(publicKey),
	}}}
	app, _ := withTelephonyTestContext(t, platform)
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{ID: "signed-announcement", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 9,
		PhoneNumber: "+33189000001", Enabled: true, Secret: "route-secret", TimeoutSec: 60,
		PreviousVoiceURL: `{"application_id":"app-123"}`, CreatedAt: now, UpdatedAt: now}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	call := callRow{ID: "signed-call", ThreadID: "pending-signed-call", Direction: "inbound", RouteID: route.ID,
		CarrierSID: "carrier-signed", CarrierSlug: "telnyx", CarrierConnectionID: 9,
		ToNumber: route.PhoneNumber, FromNumber: "+33611111111", Directive: "inbound pending",
		AudioBridgeURL: "pending", Status: "pending", PlacedAt: now, ProjectID: route.ProjectID,
		RoutingFlowVersionID: "published-version", AnnouncementState: "awaiting_answer", AnnouncementText: "Nous sommes fermés.",
		HandlingReason: handlingClosedHours}
	if _, _, err := app.db().insertInboundCallWithEvent(call, "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db().db.Exec(`UPDATE calls SET routing_flow_version_id=? WHERE id=?`, call.RoutingFlowVersionID, call.ID); err != nil {
		t.Fatal(err)
	}
	send := func(eventType string) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"data": map[string]any{
			"id": eventType, "event_type": eventType, "occurred_at": now,
			"payload": map[string]any{"call_control_id": call.CarrierSID, "connection_id": "app-123"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		signature := ed25519.Sign(privateKey, append([]byte(timestamp+"|"), body...))
		request := httptest.NewRequest(http.MethodPost,
			"/inbound/telnyx/"+route.ID+"?secret="+route.Secret+"&project_id="+route.ProjectID,
			strings.NewReader(string(body)))
		request.Header.Set("Telnyx-Timestamp", timestamp)
		request.Header.Set("Telnyx-Signature-Ed25519", base64.StdEncoding.EncodeToString(signature))
		response := httptest.NewRecorder()
		app.handleTelnyxInbound(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s returned %d: %s", eventType, response.Code, response.Body.String())
		}
	}
	send("call.answered")
	send("call.answered")
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "speak_text" {
		t.Fatalf("commands before speech end=%+v", platform.integrationCalls)
	}
	send("call.speak.ended")
	send("call.speak.ended")
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[1].Tool != "hangup_call" {
		t.Fatalf("commands after speech end=%+v", platform.integrationCalls)
	}
}

func TestInboundBurstCountsNewCarrierIDsAndRotatingCallers(t *testing.T) {
	db := testCallsDB(t)
	app := &App{}
	withRoutingTestDB(t, app, db)
	route := &routeRow{ID: "r", ProjectID: "p", CarrierSlug: "telnyx", CarrierConnectionID: 9, PhoneNumber: "+33189000001"}
	policy := inboundBurstPolicy{WindowSeconds: 60, PerCaller: 2, PerNumber: 4, CooldownSeconds: 120, TrustedNumbers: map[string]bool{"33612345678": true}}
	now := time.Unix(1_800_000_000, 0)
	check := func(id, caller, want string, at time.Time) {
		t.Helper()
		got, err := app.registerInboundAttempt(route, id, caller, route.PhoneNumber, at, policy)
		if err != nil || got != want {
			t.Fatalf("attempt %s caller %s: reason=%q err=%v, want %q", id, caller, got, err, want)
		}
	}
	check("id1", "+33611111111", "", now)
	check("id1", "+33611111111", "", now) // webhook retry does not count
	check("id2", "+33611111111", "", now)
	check("id3", "+33611111111", burstPerCaller, now)
	check("id4", "+33622222222", "", now)
	check("id5", "+33633333333", burstPerNumber, now)
	check("id6", "+33612345678", burstPerNumber, now)                     // trusted caller still counts toward destination
	check("id7", "+33644444444", burstPerNumber, now.Add(61*time.Second)) // cooldown remains
	check("id8", "+33644444444", "", now.Add(181*time.Second))
}

func TestConcurrentInboundBurstAdmitsOnlyThreshold(t *testing.T) {
	db := testCallsDB(t)
	app := &App{}
	withRoutingTestDB(t, app, db)
	route := &routeRow{ID: "r", ProjectID: "p", CarrierSlug: "telnyx", CarrierConnectionID: 9, PhoneNumber: "+33189000001"}
	policy := inboundBurstPolicy{WindowSeconds: 60, PerCaller: 4, CooldownSeconds: 120, TrustedNumbers: map[string]bool{}}
	now := time.Unix(1_800_000_000, 0)
	var group sync.WaitGroup
	results := make(chan string, 20)
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			reason, err := app.registerInboundAttempt(route, fmt.Sprintf("parallel-%d", index), "+33611111111", route.PhoneNumber, now, policy)
			if err != nil {
				results <- "error:" + err.Error()
				return
			}
			results <- reason
		}(i)
	}
	group.Wait()
	close(results)
	admitted, suppressed := 0, 0
	for reason := range results {
		switch reason {
		case "":
			admitted++
		case burstPerCaller:
			suppressed++
		default:
			t.Fatalf("unexpected concurrent decision %q", reason)
		}
	}
	if admitted != 4 || suppressed != 16 {
		t.Fatalf("concurrent decisions admitted=%d suppressed=%d", admitted, suppressed)
	}
}

func TestBurstSuppressionNeverOffersAnAdviserOrBecomesMissed(t *testing.T) {
	db := testCallsDB(t)
	platform := &answerPlatform{}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil)
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	app := &App{}
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{ID: "route-burst", ProjectID: "p", CarrierSlug: "telnyx", CarrierConnectionID: 9,
		PhoneNumber: "+33189000001", AgentID: 7, Enabled: true, TimeoutSec: 60,
		AnswerMode: answerModeHumanBrowser, Secret: "secret", CreatedAt: now, UpdatedAt: now,
		RecordingMode: recordingModeInherit, InboundTransport: inboundTransportProgrammable}
	if err := db.insertRoute(route); err != nil {
		t.Fatal(err)
	}
	var last *callRow
	for i := 1; i <= 13; i++ {
		var err error
		last, _, err = app.recordInboundCall(&route, fmt.Sprintf("carrier-%d", i), "+33611111111", route.PhoneNumber)
		if err != nil {
			t.Fatalf("record attempt %d: %v", i, err)
		}
	}
	if last.HandlingReason != handlingBurstSuppressed || last.ErrorMessage != burstPerCaller {
		t.Fatalf("suppressed call = %+v", last)
	}
	var offers int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM inbound_event_outbox WHERE call_id=?`, last.ID).Scan(&offers); err != nil || offers != 0 {
		t.Fatalf("suppressed call offers=%d err=%v", offers, err)
	}
	if err := app.suppressInboundCall(ctx, last); err != nil {
		t.Fatal(err)
	}
	stored, err := db.findCall(last.ID)
	if err != nil || stored.Status != "canceled" || stored.HandlingReason != handlingBurstSuppressed {
		t.Fatalf("terminal suppressed call=%+v err=%v", stored, err)
	}
	payload := lifecycleEventPublic(*stored, "event", "call.canceled", now, lifecycleFacts{})
	if payload["handling_reason"] != handlingBurstSuppressed {
		t.Fatalf("lifecycle handling reason=%#v", payload)
	}
	if payload["missed_pool_eligible"] != false {
		t.Fatalf("suppressed call is eligible for missed pool: %#v", payload)
	}
}

func TestTelnyxAnnouncementRetriesFailedCarrierCommands(t *testing.T) {
	db := testCallsDB(t)
	platform := &answerPlatform{failTool: "speak_text"}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil)
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	app := &App{}
	now := time.Now().UTC().Format(time.RFC3339)
	call := callRow{ID: "retry-announcement", ThreadID: "pending-retry-announcement", Direction: "inbound",
		CarrierSID: "carrier-retry", CarrierSlug: "telnyx", CarrierConnectionID: 9,
		ToNumber: "+33189000001", FromNumber: "+33611111111", Directive: "inbound pending",
		AudioBridgeURL: "pending", Status: "answered", PlacedAt: now, ProjectID: "p",
		AnnouncementState: "awaiting_answer", AnnouncementText: "Nous sommes fermés."}
	if _, _, err := db.insertInboundCallWithEvent(call, "pending"); err != nil {
		t.Fatal(err)
	}
	stored, err := db.findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.startTelnyxTerminalAnnouncement(ctx, stored); err == nil {
		t.Fatal("failed speak command was accepted")
	}
	stored, err = db.findCall(call.ID)
	if err != nil || stored.AnnouncementState != "awaiting_answer" {
		t.Fatalf("failed speak state=%+v err=%v", stored, err)
	}
	platform.failTool = ""
	if err := app.startTelnyxTerminalAnnouncement(ctx, stored); err != nil {
		t.Fatal(err)
	}
	stored, err = db.findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	platform.failTool = "hangup_call"
	if err := app.finishTelnyxTerminalAnnouncement(ctx, stored); err == nil {
		t.Fatal("failed hangup command was accepted")
	}
	stored, err = db.findCall(call.ID)
	if err != nil || stored.AnnouncementState != "speaking" {
		t.Fatalf("failed hangup state=%+v err=%v", stored, err)
	}
	platform.failTool = ""
	if err := app.finishTelnyxTerminalAnnouncement(ctx, stored); err != nil {
		t.Fatal(err)
	}
	stored, err = db.findCall(call.ID)
	if err != nil || stored.AnnouncementState != "finished" {
		t.Fatalf("retried hangup state=%+v err=%v", stored, err)
	}
}

func TestTerminalAnnouncementPlaysBeforeTelnyxHangup(t *testing.T) {
	db := testCallsDB(t)
	platform := &answerPlatform{}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil)
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	app := &App{}
	definition := routingDefinition{Entry: "closed", Nodes: []routingNode{
		{ID: "closed", Type: "announcement", Config: map[string]any{"text": "Nous sommes fermés."}, Next: "bye"},
		{ID: "bye", Type: "hangup"},
	}}
	raw, _ := json.Marshal(definition)
	flow, err := app.saveRoutingFlow("p", "", "Closed test", "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	version, validation, err := app.publishRoutingFlow("p", flow.ID)
	if err != nil || len(validation) != 0 {
		t.Fatalf("publish: version=%+v validation=%v err=%v", version, validation, err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{ID: "route-closed", ProjectID: "p", CarrierSlug: "telnyx", CarrierConnectionID: 9,
		PhoneNumber: "+33189000001", AgentID: 7, Enabled: true, TimeoutSec: 60,
		AnswerMode: answerModeHumanBrowser, Secret: "secret", CreatedAt: now, UpdatedAt: now,
		RecordingMode: recordingModeInherit, InboundTransport: inboundTransportProgrammable,
		FlowID: flow.ID, PublishedFlowVersionID: version.ID}
	if err := db.insertRoute(route); err != nil {
		t.Fatal(err)
	}
	call, created, err := app.recordInboundCall(&route, "carrier-closed", "+33611111111", route.PhoneNumber)
	if err != nil || !created || call.AnnouncementText != "Nous sommes fermés." || call.AnnouncementState != "awaiting_answer" {
		t.Fatalf("record closed call=%+v created=%v err=%v", call, created, err)
	}
	if err := app.answerTelnyxIVR(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := db.updateStatus(call.ID, "answered", ""); err != nil {
		t.Fatal(err)
	}
	call, err = db.findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.startTelnyxTerminalAnnouncement(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := app.startTelnyxTerminalAnnouncement(ctx, call); err != nil {
		t.Fatal(err)
	} // retry is idempotent
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[0].Tool != "answer_call" || platform.integrationCalls[1].Tool != "speak_text" {
		t.Fatalf("commands before speech completion=%+v", platform.integrationCalls)
	}
	if platform.integrationCalls[1].Input["payload"] != "Nous sommes fermés." {
		t.Fatalf("announcement payload=%+v", platform.integrationCalls[1].Input)
	}
	call, err = db.findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.finishTelnyxTerminalAnnouncement(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := app.finishTelnyxTerminalAnnouncement(ctx, call); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 3 || platform.integrationCalls[2].Tool != "hangup_call" {
		t.Fatalf("commands after speech completion=%+v", platform.integrationCalls)
	}
	plan, err := app.resolveInboundRoutingPlan(&route, "+33611111111", nil)
	if err != nil {
		t.Fatal(err)
	}
	// An already answered IVR can reach the same terminal path after a gather.
	// It must speak directly, then wait for the completion callback to hang up.
	ivr := *call
	ivr.ID = "answered-ivr-terminal"
	ivr.ThreadID = "pending-answered-ivr-terminal"
	ivr.CarrierSID = "carrier-ivr-terminal"
	ivr.AnnouncementState = ""
	ivr.AnnouncementText = ""
	ivr.Status = "answered"
	if _, _, err := db.insertInboundCallWithEvent(ivr, "pending"); err != nil {
		t.Fatal(err)
	}
	if err := app.executeTelnyxRoutingPlan(ctx, &ivr, &route, plan); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 4 || platform.integrationCalls[3].Tool != "speak_text" {
		t.Fatalf("IVR terminal commands before completion=%+v", platform.integrationCalls)
	}
	ivrStored, err := db.findCall(ivr.ID)
	if err != nil || ivrStored.AnnouncementState != "speaking" {
		t.Fatalf("IVR terminal state=%+v err=%v", ivrStored, err)
	}
	if err := app.finishTelnyxTerminalAnnouncement(ctx, ivrStored); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 5 || platform.integrationCalls[4].Tool != "hangup_call" {
		t.Fatalf("IVR terminal commands after completion=%+v", platform.integrationCalls)
	}
	recorder := httptest.NewRecorder()
	if err := app.writeTwilioRoutingPlan(recorder, call, &route, plan); err != nil {
		t.Fatal(err)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "Nous sommes fermés.") || !strings.Contains(body, "<Hangup/>") {
		t.Fatalf("Twilio terminal XML=%s", body)
	}
	backup := httptest.NewRecorder()
	writePlivoSayHangup(backup, call.AnnouncementText)
	if body := backup.Body.String(); !strings.Contains(body, "Nous sommes fermés.") || !strings.Contains(body, "<Hangup/>") {
		t.Fatalf("Plivo terminal XML=%s", body)
	}
	twilioRoute := route
	twilioRoute.ID = "route-closed-twilio"
	twilioRoute.CarrierSlug = "twilio"
	twilioRoute.CarrierConnectionID = 10
	twilioRoute.PhoneNumber = "+33189000002"
	twilioRoute.Secret = "twilio-secret"
	if err := db.insertRoute(twilioRoute); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"CallSid": {"CA-closed"}, "From": {"+33611111111"}, "To": {twilioRoute.PhoneNumber}}
	request := httptest.NewRequest(http.MethodPost,
		"https://example.test/inbound/twilio/"+twilioRoute.ID+"?secret="+twilioRoute.Secret,
		strings.NewReader(form.Encode()))
	signTwilioTestRequest(t, app, request, form)
	response := httptest.NewRecorder()
	app.handleTwilioInbound(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Nous sommes fermés.") || !strings.Contains(response.Body.String(), "<Hangup/>") {
		t.Fatalf("Twilio ingress status=%d XML=%s", response.Code, response.Body.String())
	}
}

func TestClosedScheduleProducesDistinctHandlingReason(t *testing.T) {
	plan := &inboundRoutingPlan{TerminalType: "hangup", Trace: []routingTraceStep{
		{NodeID: "hours", NodeType: "schedule", Outcome: "closed"},
		{NodeID: "closed", NodeType: "announcement", Outcome: "play"},
		{NodeID: "bye", NodeType: "hangup", Outcome: "hangup"},
	}}
	if got := routeHandlingReason(plan); got != handlingClosedHours {
		t.Fatalf("handling reason=%q", got)
	}
	plan.Trace[0].Outcome = "open"
	if got := routeHandlingReason(plan); got != "" {
		t.Fatalf("open route handling reason=%q", got)
	}
}

func TestSaturdayBusinessHoursAndAfterHoursRouting(t *testing.T) {
	definition := routingDefinition{Entry: "hours", Nodes: []routingNode{
		{ID: "hours", Type: "schedule", Config: map[string]any{
			"timezone": "Europe/Paris", "days": []any{"mon", "tue", "wed", "thu", "fri", "sat"},
			"start": "09:00", "end": "18:00",
		}, Branches: map[string]string{"open": "adviser", "closed": "notice"}},
		{ID: "adviser", Type: "destination", Config: map[string]any{"destination_id": "desk"}},
		{ID: "notice", Type: "announcement", Config: map[string]any{"text": "Nous sommes fermés."}, Next: "bye"},
		{ID: "bye", Type: "hangup"},
	}}
	open := simulateRoutingDefinition(definition, routingSimulationContext{At: "2026-09-26T10:00:00+02:00"})
	if !open.Valid || open.TerminalType != "destination" || open.Trace[0].Outcome != "open" {
		t.Fatalf("Saturday open route=%+v", open)
	}
	closed := simulateRoutingDefinition(definition, routingSimulationContext{At: "2026-09-26T18:01:00+02:00"})
	if !closed.Valid || closed.TerminalType != "hangup" || closed.Trace[0].Outcome != "closed" || !planHasAnnouncement(&inboundRoutingPlan{Trace: closed.Trace}) {
		t.Fatalf("Saturday after-hours route=%+v", closed)
	}
	if got := routeHandlingReason(&inboundRoutingPlan{TerminalType: closed.TerminalType, Trace: closed.Trace}); got != handlingClosedHours {
		t.Fatalf("after-hours handling reason=%q", got)
	}
}
