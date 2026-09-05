package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestLifecycleManifestDeclarationsMatchDisk(t *testing.T) {
	diskBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(diskBytes)
	if err != nil {
		t.Fatalf("parse disk manifest: %v", err)
	}
	embedded := (&App{}).Manifest()
	diskTopics := publishedTopics(disk.Provides.Publishes)
	embeddedTopics := publishedTopics(embedded.Provides.Publishes)
	if !reflect.DeepEqual(diskTopics, embeddedTopics) {
		t.Fatalf("published event declaration drift:\ndisk: %#v\nembedded: %#v", diskTopics, embeddedTopics)
	}
	diskTools := manifestToolNames(disk.Provides.MCPTools)
	embeddedTools := manifestToolNames(embedded.Provides.MCPTools)
	if !reflect.DeepEqual(diskTools, embeddedTools) {
		t.Fatalf("MCP tool declaration drift:\ndisk: %#v\nembedded: %#v", diskTools, embeddedTools)
	}
	if !reflect.DeepEqual(disk.ConfigSchema, embedded.ConfigSchema) {
		t.Fatalf("config schema declaration drift:\ndisk: %#v\nembedded: %#v", disk.ConfigSchema, embedded.ConfigSchema)
	}
	if len(disk.Requires.Integrations) != 1 || len(embedded.Requires.Integrations) != 1 {
		t.Fatalf("carrier dependency missing: disk=%#v embedded=%#v", disk.Requires.Integrations, embedded.Requires.Integrations)
	}
	if len(disk.Requires.Integrations[0].Tools) != 0 || len(embedded.Requires.Integrations[0].Tools) != 0 {
		t.Fatalf("carrier dependency declares a provider-specific tool as generic: disk=%#v embedded=%#v",
			disk.Requires.Integrations[0].Tools, embedded.Requires.Integrations[0].Tools)
	}
	if !containsString(embeddedTools, "telephony_routes_set_transport") {
		t.Fatal("direct SIP transport tool is not declared in the manifest")
	}
	want := []string{
		"call.routing.started", "call.routing.node_entered", "call.offered",
		"call.incoming", "call.initiated", "call.ringing", "call.answered",
		"call.completed", "call.failed", "call.busy", "call.no_answer",
		"call.canceled", "recording.ready", "recording.stored", "recording.deleted",
	}
	if !reflect.DeepEqual(embeddedTopics, want) {
		t.Fatalf("published topics = %#v, want %#v", embeddedTopics, want)
	}
}

func manifestToolNames(tools []sdk.MCPToolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

func publishedTopics(events []sdk.EventDecl) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Name)
	}
	return out
}

func TestLifecycleTransitionMergesOutOfOrderProviderCallbacks(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("lifecycle-ordering", "initiated")
	call.PlacedAt = "2026-07-24T10:00:00Z"
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		status string
		facts  lifecycleFacts
	}{
		{"initiated", lifecycleFacts{OccurredAt: "2026-07-24T10:00:01Z", Source: "provider", ProviderEventID: "evt-1"}},
		{"ringing", lifecycleFacts{OccurredAt: "2026-07-24T10:00:02Z", Source: "provider", ProviderEventID: "evt-2", ProviderSequence: 1}},
		{"completed", lifecycleFacts{
			OccurredAt: "2026-07-24T10:00:10Z", Source: "provider", ProviderEventID: "evt-4",
			ProviderSequence: 3, DurationSeconds: 10, TerminationCause: "normal-clearing",
			TerminationCode: "200", TerminationInitiator: "callee",
		}},
		// Providers do not guarantee callback arrival order. This late answer
		// must enrich facts without regressing the terminal call status.
		{"in-progress", lifecycleFacts{OccurredAt: "2026-07-24T10:00:05Z", Source: "provider", ProviderEventID: "evt-3", ProviderSequence: 2}},
		// Duplicate provider delivery must not create another public event.
		{"completed", lifecycleFacts{OccurredAt: "2026-07-24T10:00:10Z", Source: "provider", ProviderEventID: "evt-4", ProviderSequence: 3}},
	}
	for _, step := range steps {
		if _, err := db.updateStatusWithFacts(call.ID, step.status, "", step.facts); err != nil {
			t.Fatalf("update %s: %v", step.status, err)
		}
	}

	stored, err := db.findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "completed" {
		t.Fatalf("status regressed to %q", stored.Status)
	}
	if stored.AnsweredAt != "2026-07-24T10:00:05Z" || stored.EndedAt != "2026-07-24T10:00:10Z" {
		t.Fatalf("timestamps answered=%q ended=%q", stored.AnsweredAt, stored.EndedAt)
	}
	if stored.DurationSeconds != 10 || stored.TalkDurationSeconds != 5 {
		t.Fatalf("durations total=%d talk=%d", stored.DurationSeconds, stored.TalkDurationSeconds)
	}
	if stored.TerminationCause != "normal-clearing" || stored.TerminationCode != "200" || stored.TerminationInitiator != "callee" {
		t.Fatalf("termination facts not retained: %#v", stored)
	}

	events, err := db.listLifecycleEvents(call.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	gotTopics := make([]string, 0, len(events))
	for _, event := range events {
		gotTopics = append(gotTopics, event.Topic)
	}
	wantTopics := []string{"call.initiated", "call.ringing", "call.completed", "call.answered"}
	if !reflect.DeepEqual(gotTopics, wantTopics) {
		t.Fatalf("topics = %#v, want %#v", gotTopics, wantTopics)
	}
	if got := events[3].Payload["status"]; got != "answered" {
		t.Fatalf("late answer event status = %#v", got)
	}
	termination, _ := events[2].Payload["termination"].(map[string]any)
	if termination["cause"] != "normal-clearing" {
		t.Fatalf("completed event termination = %#v", termination)
	}
}

func TestInboundCallAndLifecycleEventAreAtomicAndDeduplicated(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("incoming-event", "pending")
	call.Direction = "inbound"
	call.RouteID = "route-1"
	call.CarrierSID = "provider-incoming-1"
	call.ThreadID = "pending-" + call.ID
	call.AudioBridgeURL = "pending"

	stored, created, err := db.insertInboundCallWithEvent(call, "incoming")
	if err != nil || !created {
		t.Fatalf("first insert created=%v err=%v", created, err)
	}
	if stored.LifecycleRevision != 1 {
		t.Fatalf("lifecycle revision = %d", stored.LifecycleRevision)
	}
	duplicate := call
	duplicate.ID = "duplicate-local-id"
	duplicate.ThreadID = "pending-" + duplicate.ID
	stored, created, err = db.insertInboundCallWithEvent(duplicate, "duplicate")
	if err != nil || created || stored.ID != call.ID {
		t.Fatalf("duplicate stored=%#v created=%v err=%v", stored, created, err)
	}
	events, err := db.listLifecycleEvents(call.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Topic != "call.incoming" {
		t.Fatalf("events = %#v", events)
	}
}

func TestTwilioCarrierRequestsAllProgressCallbacks(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"make_call": json.RawMessage(`{"sid":"CA-progress"}`),
	}}
	app, ctx := withTelephonyTestContext(t, platform)
	carrier := &twilioCarrier{app: app, connID: 9}
	_, err := carrier.Place(ctx, carrierPlaceRequest{
		CallID: "progress", CallbackSecret: "secret", ProjectID: "project-a",
		To: "+14155550100", From: "+14155550101", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := platform.integrationCalls[0].Input
	if input["StatusCallbackMethod"] != "POST" {
		t.Fatalf("callback method = %#v", input["StatusCallbackMethod"])
	}
	want := []string{"initiated", "ringing", "answered", "completed"}
	if !reflect.DeepEqual(input["StatusCallbackEvent"], want) {
		t.Fatalf("callback events = %#v", input["StatusCallbackEvent"])
	}
}

func TestTwilioCallbackCapturesProviderLifecycleFacts(t *testing.T) {
	form := url.Values{
		"CallSid":         {"CA-facts"},
		"CallStatus":      {"completed"},
		"Timestamp":       {"Fri, 24 Jul 2026 10:00:10 +0000"},
		"SequenceNumber":  {"4"},
		"CallDuration":    {"12"},
		"SipResponseCode": {"200"},
	}
	req := httptest.NewRequest("POST", "https://example.test/status", nil)
	req.Form = form
	req.PostForm = form
	update := callbackUpdateFor("twilio", req)
	if update.Status != "completed" || update.CarrierSID != "CA-facts" {
		t.Fatalf("callback update = %#v", update)
	}
	if update.Facts.ProviderSequence != 4 || update.Facts.DurationSeconds != 12 ||
		update.Facts.TerminationCode != "200" || update.Facts.ProviderEventID != "CA-facts:4" {
		t.Fatalf("callback facts = %#v", update.Facts)
	}
	if normalizeCallStatus("queued") != "initiated" {
		t.Fatal("Twilio queued status was not normalized to initiated")
	}
}

func TestPlivoStreamCallbacksPreserveMediaHealth(t *testing.T) {
	tests := []struct {
		event     string
		wantMedia string
		wantError bool
	}{
		{event: "StartStream", wantMedia: "connected"},
		{event: "StopStream", wantMedia: "disconnected"},
		{event: "DroppedStream", wantMedia: "error", wantError: true},
		{event: "DegradedStream", wantMedia: "degraded", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.event, func(t *testing.T) {
			form := url.Values{
				"Event":     {test.event},
				"CallUUID":  {"call-uuid"},
				"StreamID":  {"stream-id"},
				"Timestamp": {"2026-07-28T10:00:00Z"},
			}
			req := httptest.NewRequest(http.MethodPost, "https://example.test/status", nil)
			req.Form = form
			req.PostForm = form
			update := callbackUpdateFor("plivo", req)
			if update.Status != "" || update.MediaStatus != test.wantMedia || update.CarrierSID != "call-uuid" {
				t.Fatalf("callback update = %#v", update)
			}
			if (update.MediaError != "") != test.wantError {
				t.Fatalf("callback media error=%q, wantError=%v", update.MediaError, test.wantError)
			}
			if update.Facts.ProviderEventID == "" || update.Facts.Source != "provider" {
				t.Fatalf("callback facts = %#v", update.Facts)
			}
		})
	}
}

func TestMediaErrorRemainsDiagnosticAfterDisconnectAndCarrierCompletion(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("media-error-order", "in-progress")
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	if err := db.updateMediaStatusWithLeg(call.ID, "error", "upstream reset", 1011, "transport failed", "core"); err != nil {
		t.Fatal(err)
	}
	if err := db.updateMediaStatus(call.ID, "disconnected", "", 1000, "bridge complete"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.updateStatusWithFacts(call.ID, "completed", "", lifecycleFacts{
		Source: "provider", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := db.findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "completed" || stored.MediaStatus != "error" ||
		stored.MediaErrorMessage != "upstream reset" || stored.MediaCloseCode != 1011 ||
		stored.MediaCloseReason != "transport failed" || stored.MediaCloseLeg != "core" {
		t.Fatalf("call state = %+v", stored)
	}
	payload := lifecycleEventPublic(*stored, "event-media-error", "call.completed", time.Now().UTC().Format(time.RFC3339Nano), lifecycleFacts{})
	media, _ := payload["media"].(map[string]any)
	if media["close_leg"] != "core" {
		t.Fatalf("lifecycle media diagnostics = %#v", media)
	}
}

func TestFirstNormalMediaCloseCauseSurvivesCarrierStopCallback(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("media-close-order", "in-progress")
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	if err := db.updateMediaStatusWithLeg(call.ID, "disconnected", "", 1000, "core bridge complete", "core"); err != nil {
		t.Fatal(err)
	}
	if err := db.updateMediaStatusWithLeg(call.ID, "disconnected", "", 1000, "carrier stream stopped", "carrier"); err != nil {
		t.Fatal(err)
	}
	stored, err := db.findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MediaCloseCode != 1000 || stored.MediaCloseReason != "core bridge complete" || stored.MediaCloseLeg != "core" {
		t.Fatalf("first close cause was overwritten: %+v", stored)
	}
}

func TestTerminalCarrierMediaStopReconcilesGenericTransportClose(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("media-terminal-reconcile", "in-progress")
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	endedAt := time.Now().UTC()
	if err := db.updateMediaStatusWithLeg(call.ID, "error", "unexpected EOF", 1011, "media bridge transport error", "carrier"); err != nil {
		t.Fatal(err)
	}
	reconciled, err := db.reconcileTerminalCarrierMediaStop(call.ID, endedAt.Format(time.RFC3339Nano), true)
	if err != nil {
		t.Fatal(err)
	}
	if !reconciled {
		t.Fatal("expected terminal carrier close to be reconciled")
	}
	stored, err := db.findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MediaStatus != "disconnected" || stored.MediaErrorMessage != "" ||
		stored.MediaCloseCode != 1000 || stored.MediaCloseReason != "carrier stream ended with call" ||
		stored.MediaCloseLeg != "carrier" {
		t.Fatalf("reconciled media state = %+v", stored)
	}
}

func TestTerminalCarrierMediaStopPreservesEarlierTransportFailure(t *testing.T) {
	db := testCallsDB(t)
	call := testCall("media-earlier-error", "in-progress")
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	if err := db.updateMediaStatusWithLeg(call.ID, "error", "unexpected EOF", 1011, "media bridge transport error", "carrier"); err != nil {
		t.Fatal(err)
	}
	reconciled, err := db.reconcileTerminalCarrierMediaStop(call.ID, time.Now().UTC().Add(5*time.Second).Format(time.RFC3339Nano), true)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled {
		t.Fatal("an earlier transport failure must remain diagnostic")
	}
	stored, err := db.findCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MediaStatus != "error" || stored.MediaCloseCode != 1011 {
		t.Fatalf("earlier media failure was overwritten: %+v", stored)
	}
}

func TestTelnyxStreamingFailureIsMediaOnly(t *testing.T) {
	body := `{"data":{"id":"evt-stream","event_type":"streaming.failed","occurred_at":"2026-07-28T10:00:00Z","payload":{"call_control_id":"v3:test","hangup_cause":"websocket timeout"}}}`
	req := httptest.NewRequest(http.MethodPost, "https://example.test/status", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	update := callbackUpdateFor("telnyx", req)
	if update.Status != "" || update.MediaStatus != "error" ||
		update.MediaError != "websocket timeout" || update.CarrierSID != "v3:test" {
		t.Fatalf("callback update = %#v", update)
	}
}

func TestLifecycleReconciliationCursorRoundTrip(t *testing.T) {
	cursor := lifecycleCursor{UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), ID: "call-1", Revision: 7}
	decoded, err := decodeLifecycleCursor(encodeLifecycleCursor(cursor))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, cursor) {
		t.Fatalf("decoded cursor = %#v, want %#v", decoded, cursor)
	}
}

func TestLifecycleTerminalTopicsAndPayloadRedaction(t *testing.T) {
	for _, terminal := range []string{"completed", "failed", "busy", "no-answer", "canceled"} {
		t.Run(terminal, func(t *testing.T) {
			db := testCallsDB(t)
			call := testCall("terminal-"+terminal, "ringing")
			call.CallbackSecret = "must-not-escape"
			call.AudioBridgeURL = "wss://bridge.test/audio?token=must-not-escape"
			if err := db.insertCall(call); err != nil {
				t.Fatal(err)
			}
			if _, err := db.updateStatusWithFacts(call.ID, terminal, "provider result", lifecycleFacts{
				OccurredAt: "2026-07-24T10:00:10Z", Source: "provider", ProviderEventID: "terminal-event",
			}); err != nil {
				t.Fatal(err)
			}
			events, err := db.listLifecycleEvents(call.ID, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Topic != publicLifecycleTopic(terminal) {
				t.Fatalf("events = %#v", events)
			}
			encoded, err := json.Marshal(events[0].Payload)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "must-not-escape") {
				t.Fatalf("private call material leaked into event: %s", encoded)
			}
		})
	}
}

func TestLifecycleReconciliationIsProjectScoped(t *testing.T) {
	db := testCallsDB(t)
	for _, project := range []string{"project-a", "project-b"} {
		call := testCall("scope-"+project, "initiated")
		call.ProjectID = project
		if err := db.insertCall(call); err != nil {
			t.Fatal(err)
		}
		if err := db.updateStatus(call.ID, "ringing", ""); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.listCallsForReconciliation("project-a", "", "", "", "", lifecycleCursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ProjectID != "project-a" {
		t.Fatalf("project-scoped rows = %#v", rows)
	}
}

func TestCommittedLifecycleEventCanPublishAfterWorkerRestart(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/app-events/internal/emit" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	db := &callsDB{db: ctx.AppDB()}
	call := testCall("restart-publish", "initiated")
	if err := db.insertCall(call); err != nil {
		t.Fatal(err)
	}
	if _, err := db.updateStatusWithFacts(call.ID, "ringing", "", lifecycleFacts{
		OccurredAt: "2026-07-24T10:00:02Z", Source: "provider", ProviderEventID: "restart-event",
	}); err != nil {
		t.Fatal(err)
	}
	var unpublished int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM call_events WHERE call_id = ? AND published_at = ''`, call.ID).Scan(&unpublished); err != nil {
		t.Fatal(err)
	}
	if unpublished != 1 {
		t.Fatalf("unpublished event count = %d", unpublished)
	}
	if err := app.publishLifecycleEvents(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM call_events WHERE call_id = ? AND published_at <> ''`, call.ID).Scan(&unpublished); err != nil {
		t.Fatal(err)
	}
	if unpublished != 1 {
		t.Fatalf("published event count = %d", unpublished)
	}
}
