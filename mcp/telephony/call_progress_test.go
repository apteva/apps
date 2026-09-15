package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestTerminationReasonMapper(t *testing.T) {
	cases := []struct{ status, cause, code, want string }{
		{"completed", "normal_clearing", "", terminationCompleted},
		{"completed", "time_limit", "", terminationTimeLimit},
		{"busy", "user_busy", "486", terminationBusy},
		{"no-answer", "no_answer", "", terminationNoAnswer},
		{"canceled", "originator_cancel", "", terminationCanceled},
		{"failed", "call_rejected", "", terminationRejected},
		{"failed", "", "603", terminationRejected},
		{"failed", "unallocated_number", "", terminationInvalidNumber},
		{"failed", "", "404", terminationInvalidNumber},
		{"failed", "no_route_destination", "", terminationUnreachable},
		{"failed", "", "503", terminationUnreachable},
		{"failed", "time_limit", "", terminationTimeLimit},
		{"failed", "something_new", "", terminationFailed},
		{"failed", "", "", terminationFailed},
		{"ringing", "", "", ""},
	}
	for _, c := range cases {
		if got := terminationReasonFor(c.status, c.cause, c.code); got != c.want {
			t.Errorf("terminationReasonFor(%q,%q,%q)=%q want %q", c.status, c.cause, c.code, got, c.want)
		}
	}
	for raw, want := range map[string]string{
		"human": answeredByHuman, "human_residence": answeredByHuman, "machine_start": answeredByMachine,
		"machine_end_silence": answeredByMachine, "fax_detected": answeredByFax, "silence": answeredBySilence,
		"not_sure": answeredByUnknown, "true": answeredByMachine, "false": answeredByHuman, "": "",
	} {
		if got := normalizeAnsweredBy(raw); got != want {
			t.Errorf("normalizeAnsweredBy(%q)=%q want %q", raw, got, want)
		}
	}
}

func TestTelnyxTimeLimitHangupCompletes(t *testing.T) {
	if got := telnyxHangupStatus("time_limit"); got != "completed" {
		t.Fatalf("time_limit hangup status=%q, want completed", got)
	}
	body := `{"data":{"event_type":"call.hangup","payload":{"call_control_id":"v2:test","hangup_cause":"time_limit"}}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/status/call?token=x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	status, reason, _ := callbackStatusFor("telnyx", req)
	if status != "completed" || reason != "time_limit" {
		t.Fatalf("time_limit callback: status=%q reason=%q", status, reason)
	}
	if got := telnyxHangupStatus("call_rejected"); got != "failed" {
		t.Fatalf("rejected hangup status=%q, want failed", got)
	}
}

func TestMachineDetectionCallbackParsing(t *testing.T) {
	form := url.Values{"AnsweredBy": {"machine_end_beep"}, "CallSid": {"CA1"}}
	req := httptest.NewRequest(http.MethodPost, "/webhook/status/x?token=t", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	update := callbackUpdateFor("twilio", req)
	if update.AnsweredBy != answeredByMachine || update.Status != "" {
		t.Fatalf("twilio AMD callback: %+v", update)
	}

	body := `{"data":{"id":"evt-amd","event_type":"call.machine.premium.detection.ended","occurred_at":"2026-09-15T10:00:05Z","payload":{"call_control_id":"v2:x","result":"human_residence"}}}`
	req = httptest.NewRequest(http.MethodPost, "/webhook/status/x?token=t", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	update = callbackUpdateFor("telnyx", req)
	if update.AnsweredBy != answeredByHuman || update.Status != "" || update.Facts.ProviderEventID != "evt-amd" {
		t.Fatalf("telnyx AMD callback: %+v", update)
	}

	form = url.Values{"Machine": {"true"}, "CallUUID": {"uuid-1"}}
	req = httptest.NewRequest(http.MethodPost, "/webhook/status/x?token=t", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if update = callbackUpdateFor("plivo", req); update.AnsweredBy != answeredByMachine {
		t.Fatalf("plivo AMD callback: %+v", update)
	}

	body = `{"data":{"event_type":"call.hangup","payload":{"call_control_id":"v2:x","hangup_cause":"normal_clearing"}}}`
	req = httptest.NewRequest(http.MethodPost, "/webhook/status/x?token=t", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if update = callbackUpdateFor("telnyx", req); update.AnsweredBy != "" {
		t.Fatalf("hangup must not carry answered_by: %+v", update)
	}
}

func callEventPayload(t *testing.T, db sqlRowQuerier, callID, topic string) map[string]any {
	t.Helper()
	var encoded string
	if err := db.QueryRow(`SELECT payload_json FROM call_events WHERE call_id = ? AND topic = ?`, callID, topic).Scan(&encoded); err != nil {
		t.Fatalf("event %s for %s: %v", topic, callID, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestTelnyxOutboundRingingIsSynthesized(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	call := testCall("telnyx-out", "initiated")
	call.CarrierSlug = "telnyx"
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	update := callbackUpdate{Status: "initiated", Facts: lifecycleFacts{Source: "provider", OccurredAt: "2026-09-15T10:00:00Z", ProviderEventID: "evt-1"}}
	if _, err := a.db().updateStatusWithFacts(call.ID, update.Status, "", update.Facts); err != nil {
		t.Fatal(err)
	}
	if err := a.applyProgressUpdate(&call, update); err != nil {
		t.Fatal(err)
	}
	stored, _ := a.db().findCall(call.ID)
	if stored == nil || stored.Status != "ringing" {
		t.Fatalf("telnyx outbound call should ring after the carrier accepts the dial: %+v", stored)
	}
	payload := callEventPayload(t, sqlRowQuerier{ctx.AppDB()}, call.ID, "call.ringing")
	if payload["synthesized"] != true || payload["source"] != "telephony" || payload["previous_status"] != "initiated" {
		t.Fatalf("synthesized ringing payload: %#v", payload)
	}
	// Applying the same update again must not regress an answered call.
	if _, err := a.db().updateStatusWithFacts(call.ID, "answered", "", lifecycleFacts{Source: "provider"}); err != nil {
		t.Fatal(err)
	}
	if err := a.applyProgressUpdate(&call, update); err != nil {
		t.Fatal(err)
	}
	if stored, _ = a.db().findCall(call.ID); stored.Status != "answered" {
		t.Fatalf("late initiated callback changed an answered call: %+v", stored)
	}

	twilio := testCall("twilio-out", "initiated")
	if err := a.db().insertCall(twilio); err != nil {
		t.Fatal(err)
	}
	if err := a.applyProgressUpdate(&twilio, update); err != nil {
		t.Fatal(err)
	}
	if stored, _ = a.db().findCall(twilio.ID); stored.Status != "initiated" {
		t.Fatalf("twilio reports ringing itself; nothing should be synthesized: %+v", stored)
	}
}

type sqlRowQuerier struct{ db *sql.DB }

func (q sqlRowQuerier) QueryRow(query string, args ...any) *sql.Row {
	return q.db.QueryRow(query, args...)
}

func TestAnsweringMachineDetectionRecordsAndHangsUp(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{"update_call": json.RawMessage(`{"sid":"CA1"}`)}}
	a, ctx := withTelephonyTestContext(t, platform)

	hangup := testCall("amd-hangup", "answered")
	hangup.MachineDetection = machineDetectionDetect
	hangup.MachineDetectionAction = machineDetectionHangup
	if err := a.db().insertCall(hangup); err != nil {
		t.Fatal(err)
	}
	facts := lifecycleFacts{Source: "provider", OccurredAt: "2026-09-15T10:00:05Z", ProviderEventID: "amd-1"}
	if err := a.applyProgressUpdate(&hangup, callbackUpdate{AnsweredBy: answeredByMachine, Facts: facts}); err != nil {
		t.Fatal(err)
	}
	stored, _ := a.db().findCall(hangup.ID)
	if stored.AnsweredBy != answeredByMachine || stored.Status != "completed" || stored.TerminationCause != "machine_detected" || stored.TerminationReason != terminationCompleted {
		t.Fatalf("machine answer with hangup action: %+v", stored)
	}
	payload := callEventPayload(t, sqlRowQuerier{ctx.AppDB()}, hangup.ID, topicMachineDetected)
	if payload["answered_by"] != answeredByMachine || payload["status"] != "answered" || payload["provider_event_id"] != "amd-1" {
		t.Fatalf("machine_detected payload: %#v", payload)
	}
	hungUp := false
	for _, call := range platform.integrationCalls {
		if call.Tool == "update_call" && call.Input["Status"] == "completed" {
			hungUp = true
		}
	}
	if !hungUp {
		t.Fatalf("carrier hangup was not requested: %+v", platform.integrationCalls)
	}
	// A repeated detection callback is idempotent.
	if err := a.applyProgressUpdate(&hangup, callbackUpdate{AnsweredBy: answeredByMachine, Facts: facts}); err != nil {
		t.Fatal(err)
	}

	notify := testCall("amd-notify", "answered")
	notify.MachineDetection = machineDetectionDetect
	if err := a.db().insertCall(notify); err != nil {
		t.Fatal(err)
	}
	before := len(platform.integrationCalls)
	facts.ProviderEventID = "amd-2"
	if err := a.applyProgressUpdate(&notify, callbackUpdate{AnsweredBy: answeredByMachine, Facts: facts}); err != nil {
		t.Fatal(err)
	}
	stored, _ = a.db().findCall(notify.ID)
	if stored.AnsweredBy != answeredByMachine || stored.Status != "answered" || len(platform.integrationCalls) != before {
		t.Fatalf("notify action must keep the call up: %+v calls=%d", stored, len(platform.integrationCalls)-before)
	}

	human := testCall("amd-human", "answered")
	human.MachineDetectionAction = machineDetectionHangup
	if err := a.db().insertCall(human); err != nil {
		t.Fatal(err)
	}
	facts.ProviderEventID = "amd-3"
	if err := a.applyProgressUpdate(&human, callbackUpdate{AnsweredBy: answeredByHuman, Facts: facts}); err != nil {
		t.Fatal(err)
	}
	if stored, _ = a.db().findCall(human.ID); stored.Status != "answered" || stored.AnsweredBy != answeredByHuman {
		t.Fatalf("human answer must never hang up: %+v", stored)
	}
}

func TestOutboundSettingsDriveMachineDetection(t *testing.T) {
	platform := &answerPlatform{
		bindings: map[string]any{"carrier": int64(9)},
		credentials: &sdk.ConnectionCredentials{
			Slug:   "twilio",
			Fields: map[string]string{"auth_token": "test-auth-token", "phone_number": "+14155550101"},
		},
		integrationResponse: map[string]json.RawMessage{"make_call": json.RawMessage(`{"sid":"CAoutbound"}`)},
	}
	a, ctx := withTelephonyTestContext(t, platform)

	got, _ := a.toolOutboundSettingsGet(context.Background(), ctx, nil)
	if settings := got.(map[string]any); settings["machine_detection"] != machineDetectionOff || settings["machine_detection_action"] != machineDetectionNotify {
		t.Fatalf("default outbound settings: %#v", got)
	}
	if bad, _ := a.toolOutboundSettingsSet(context.Background(), ctx, map[string]any{"machine_detection": "sometimes"}); toolError(bad) == "" {
		t.Fatalf("invalid mode accepted: %#v", bad)
	}
	set, _ := a.toolOutboundSettingsSet(context.Background(), ctx, map[string]any{"machine_detection": "premium", "machine_detection_action": "hangup"})
	if msg := toolError(set); msg != "" {
		t.Fatalf("set outbound settings: %s", msg)
	}

	callerCtx := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7})
	result, err := a.toolPlaceCall(callerCtx, ctx, map[string]any{"to": "+14155550100", "directive": "Confirm the appointment."})
	if err != nil || toolError(result) != "" {
		t.Fatalf("place call: %v %s", err, toolError(result))
	}
	dial := lastIntegrationCall(platform, "make_call")
	if dial == nil || dial.Input["MachineDetection"] != "DetectMessageEnd" || dial.Input["AsyncAmd"] != "true" || dial.Input["AsyncAmdStatusCallback"] == "" {
		t.Fatalf("project default did not reach the carrier: %#v", dial)
	}
	rows, _ := a.db().listWhere("project_id = ? ORDER BY placed_at DESC", "project-a")
	if len(rows) == 0 || rows[0].MachineDetection != machineDetectionPremium || rows[0].MachineDetectionAction != machineDetectionHangup {
		t.Fatalf("call row did not persist detection settings: %+v", rows)
	}

	result, err = a.toolPlaceCall(callerCtx, ctx, map[string]any{"to": "+14155550102", "directive": "Say hi.", "machine_detection": "off"})
	if err != nil || toolError(result) != "" {
		t.Fatalf("place call without detection: %v %s", err, toolError(result))
	}
	if dial = lastIntegrationCall(platform, "make_call"); dial == nil || dial.Input["MachineDetection"] != nil {
		t.Fatalf("explicit off must not send detection parameters: %#v", dial)
	}
	if bad, _ := a.toolPlaceCall(callerCtx, ctx, map[string]any{"to": "+14155550103", "directive": "x", "machine_detection": "maybe"}); toolError(bad) == "" {
		t.Fatalf("invalid per-call mode accepted: %#v", bad)
	}
}

func lastIntegrationCall(platform *answerPlatform, tool string) *integrationCall {
	for i := len(platform.integrationCalls) - 1; i >= 0; i-- {
		if platform.integrationCalls[i].Tool == tool {
			call := platform.integrationCalls[i]
			return &call
		}
	}
	return nil
}

func TestCallReadRouteAndSocketStatusExposeTermination(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	call := testCall("read-1", "answered")
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db().updateStatusWithFacts(call.ID, "completed", "", lifecycleFacts{Source: "provider", TerminationCause: "time_limit", OccurredAt: "2026-09-15T10:10:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := a.db().setAnsweredBy(call.ID, answeredByHuman); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/calls/read-1", nil)
	w := httptest.NewRecorder()
	a.handleCallAction(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /calls/{id} = %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Call struct {
			Status      string `json:"status"`
			AnsweredBy  string `json:"answered_by"`
			Termination struct {
				Reason string `json:"reason"`
				Cause  string `json:"cause"`
			} `json:"termination"`
		} `json:"call"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Call.Status != "completed" || body.Call.Termination.Reason != terminationTimeLimit || body.Call.Termination.Cause != "time_limit" || body.Call.AnsweredBy != answeredByHuman {
		t.Fatalf("single call payload: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	a.handleCallAction(w, httptest.NewRequest(http.MethodGet, "/calls/missing", nil))
	if w.Code != 404 {
		t.Fatalf("unknown call = %d", w.Code)
	}
	w = httptest.NewRecorder()
	a.handleCallAction(w, httptest.NewRequest(http.MethodPost, "/calls/read-1", bytes.NewReader(nil)))
	if w.Code != 404 {
		t.Fatalf("POST /calls/{id} without an action must stay unknown: %d", w.Code)
	}
	if action := phoneAction(httptest.NewRequest(http.MethodGet, "/calls/read-1", nil)); action != "call.read" {
		t.Fatalf("application users need call.read for a single call, got %q", action)
	}
	if action := phoneAction(httptest.NewRequest(http.MethodGet, "/calls/events", nil)); action != "call.read" {
		t.Fatalf("event stream action changed: %q", action)
	}

	stored, _ := a.db().findCall(call.ID)
	var event map[string]any
	if err := json.Unmarshal(softphoneStatusEvent(*stored), &event); err != nil {
		t.Fatal(err)
	}
	termination, _ := event["termination"].(map[string]any)
	if event["type"] != "call.status" || event["direction"] != "outbound" || event["status"] != "completed" || event["answered_by"] != answeredByHuman || termination["reason"] != terminationTimeLimit || event["ended_at"] == nil {
		t.Fatalf("call.status frame: %s", softphoneStatusEvent(*stored))
	}
	list, err := a.db().listWhere("id = ?", call.ID)
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	public := callsPublic(list)[0]
	if public["answered_by"] != answeredByHuman || public["termination"].(map[string]any)["reason"] != terminationTimeLimit || public["ended_at"] == "" {
		t.Fatalf("calls list payload: %#v", public)
	}
	_ = ctx
}

func TestOutboundDefaultTimeoutAppliesWhenOmitted(t *testing.T) {
	platform := &answerPlatform{
		bindings: map[string]any{"carrier": int64(9)},
		credentials: &sdk.ConnectionCredentials{
			Slug:   "twilio",
			Fields: map[string]string{"auth_token": "test-auth-token", "phone_number": "+14155550101"},
		},
		integrationResponse: map[string]json.RawMessage{"make_call": json.RawMessage(`{"sid":"CAoutbound"}`)},
	}
	a, ctx := withTelephonyTestContext(t, platform)
	callerCtx := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7})

	// Built-in defaults before any setting exists: 30 s for agents, 60 s for the softphone.
	if result, _ := a.toolPlaceCall(callerCtx, ctx, map[string]any{"to": "+14155550100", "directive": "x"}); toolError(result) != "" {
		t.Fatal(toolError(result))
	}
	if dial := lastIntegrationCall(platform, "make_call"); dial == nil || dial.Input["Timeout"] != 30 {
		t.Fatalf("agent built-in timeout: %#v", dial)
	}
	if _, err := a.placeHumanCallForUser(ctx, nil, "project-a", "+14155550110", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if dial := lastIntegrationCall(platform, "make_call"); dial == nil || dial.Input["Timeout"] != 60 {
		t.Fatalf("softphone built-in timeout: %#v", dial)
	}

	if bad, _ := a.toolOutboundSettingsSet(context.Background(), ctx, map[string]any{"default_timeout_sec": float64(3)}); toolError(bad) == "" {
		t.Fatalf("out-of-range timeout accepted: %#v", bad)
	}
	set, _ := a.toolOutboundSettingsSet(context.Background(), ctx, map[string]any{"default_timeout_sec": float64(45)})
	if msg := toolError(set); msg != "" || set.(map[string]any)["default_timeout_sec"] != 45 {
		t.Fatalf("set default timeout: %s %#v", msg, set)
	}
	if result, _ := a.toolPlaceCall(callerCtx, ctx, map[string]any{"to": "+14155550120", "directive": "x"}); toolError(result) != "" {
		t.Fatal(toolError(result))
	}
	if dial := lastIntegrationCall(platform, "make_call"); dial == nil || dial.Input["Timeout"] != 45 {
		t.Fatalf("agent call ignored the project default: %#v", dial)
	}
	if _, err := a.placeHumanCallForUser(ctx, nil, "project-a", "+14155550130", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if dial := lastIntegrationCall(platform, "make_call"); dial == nil || dial.Input["Timeout"] != 45 {
		t.Fatalf("softphone call ignored the project default: %#v", dial)
	}
	if result, _ := a.toolPlaceCall(callerCtx, ctx, map[string]any{"to": "+14155550140", "directive": "x", "timeout_sec": float64(20)}); toolError(result) != "" {
		t.Fatal(toolError(result))
	}
	if dial := lastIntegrationCall(platform, "make_call"); dial == nil || dial.Input["Timeout"] != 20 {
		t.Fatalf("explicit timeout must win: %#v", dial)
	}
}
