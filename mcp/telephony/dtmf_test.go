package main

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func dtmfToolError(result any) string {
	m, ok := result.(map[string]any)
	if !ok || m["isError"] != true {
		return ""
	}
	return m["content"].([]map[string]any)[0]["text"].(string)
}

func sendDTMFAs(t *testing.T, a *App, agentID int64, args map[string]any) (map[string]any, string) {
	t.Helper()
	result, err := a.toolSendDTMF(sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: agentID}), globalCtx, args)
	if err != nil {
		t.Fatal(err)
	}
	if msg := dtmfToolError(result); msg != "" {
		return nil, msg
	}
	return result.(map[string]any), ""
}

func dtmfCall(t *testing.T, a *App, id, carrier, status string) *callRow {
	t.Helper()
	row := testCall(id, status)
	row.CarrierSlug = carrier
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	return &row
}

func TestSendDTMFToolRejectsInvalidRequests(t *testing.T) {
	p := &answerPlatform{}
	a, _ := withTelephonyTestContext(t, p)
	dtmfCall(t, a, "dtmf-live", "telnyx", "in-progress")
	dtmfCall(t, a, "dtmf-ringing", "telnyx", "ringing")
	dtmfCall(t, a, "dtmf-ended", "telnyx", "completed")
	cases := []struct {
		name  string
		agent int64
		args  map[string]any
		want  string
	}{
		{"no agent", 0, map[string]any{"call_id": "dtmf-live", "digits": "1"}, "calling agent"},
		{"no call", 7, map[string]any{"digits": "1"}, "call_id required"},
		{"empty digits", 7, map[string]any{"call_id": "dtmf-live", "digits": ""}, "digits must be"},
		{"letter", 7, map[string]any{"call_id": "dtmf-live", "digits": "1A"}, "digits must be"},
		{"pause", 7, map[string]any{"call_id": "dtmf-live", "digits": "1w2"}, "digits must be"},
		{"spoken", 7, map[string]any{"call_id": "dtmf-live", "digits": "one"}, "digits must be"},
		{"too long", 7, map[string]any{"call_id": "dtmf-live", "digits": strings.Repeat("1", 17)}, "digits must be"},
		{"long key", 7, map[string]any{"call_id": "dtmf-live", "digits": "1", "idempotency_key": strings.Repeat("k", 201)}, "idempotency_key"},
		{"unknown call", 7, map[string]any{"call_id": "nope", "digits": "1"}, "unknown call_id"},
		{"other agent", 8, map[string]any{"call_id": "dtmf-live", "digits": "1"}, "another agent"},
		{"ringing", 7, map[string]any{"call_id": "dtmf-ringing", "digits": "1"}, "not connected"},
		{"ended", 7, map[string]any{"call_id": "dtmf-ended", "digits": "1"}, "has ended"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, msg := sendDTMFAs(t, a, c.agent, c.args)
			if !strings.Contains(msg, c.want) {
				t.Fatalf("error = %q, want %q", msg, c.want)
			}
		})
	}
	if len(p.integrationCalls) != 0 {
		t.Fatalf("rejected requests reached the carrier: %+v", p.integrationCalls)
	}
	// The longest accepted press is 16 keys.
	if _, msg := sendDTMFAs(t, a, 7, map[string]any{"call_id": "dtmf-live", "digits": "0123456789*#0123"}); msg != "" {
		t.Fatal(msg)
	}
}

func TestSendDTMFIdempotency(t *testing.T) {
	p := &answerPlatform{}
	a, _ := withTelephonyTestContext(t, p)
	dtmfCall(t, a, "dtmf-key", "telnyx", "in-progress")
	args := map[string]any{"call_id": "dtmf-key", "digits": "1", "idempotency_key": "dtmf-key:menu-step:1"}

	first, msg := sendDTMFAs(t, a, 7, args)
	if msg != "" || first["duplicate"] != false || first["status"] != "sent" {
		t.Fatalf("first press: %v %q", first, msg)
	}
	again, msg := sendDTMFAs(t, a, 7, args)
	if msg != "" || again["duplicate"] != true {
		t.Fatalf("retried press: %v %q", again, msg)
	}
	if len(p.integrationCalls) != 1 || p.integrationCalls[0].Tool != "send_dtmf" || p.integrationCalls[0].Input["digits"] != "1" {
		t.Fatalf("carrier commands = %+v, want one send_dtmf", p.integrationCalls)
	}
	if _, msg := sendDTMFAs(t, a, 7, map[string]any{"call_id": "dtmf-key", "digits": "2", "idempotency_key": "dtmf-key:menu-step:1"}); !strings.Contains(msg, "different digits") {
		t.Fatalf("reused key with other digits: %q", msg)
	}
	// Without a key every call is a new press.
	for range 2 {
		if _, msg := sendDTMFAs(t, a, 7, map[string]any{"call_id": "dtmf-key", "digits": "#"}); msg != "" {
			t.Fatal(msg)
		}
	}
	if len(p.integrationCalls) != 3 {
		t.Fatalf("carrier commands = %d, want 3", len(p.integrationCalls))
	}

	// A failed press is recorded and may be retried with the same key.
	p.failTool = "send_dtmf"
	failArgs := map[string]any{"call_id": "dtmf-key", "digits": "3", "idempotency_key": "dtmf-key:menu-step:2"}
	if _, msg := sendDTMFAs(t, a, 7, failArgs); msg == "" {
		t.Fatal("carrier failure was reported as sent")
	}
	var status string
	if err := a.db().db.QueryRow(`SELECT status FROM call_dtmf_commands WHERE call_id=? AND idempotency_key=?`, "dtmf-key", "dtmf-key:menu-step:2").Scan(&status); err != nil || status != "failed" {
		t.Fatalf("failed press status = %q, %v", status, err)
	}
	p.failTool = ""
	if retry, msg := sendDTMFAs(t, a, 7, failArgs); msg != "" || retry["duplicate"] != false {
		t.Fatalf("retry after failure: %v %q", retry, msg)
	}
	if len(p.integrationCalls) != 5 {
		t.Fatalf("carrier commands = %d, want 5", len(p.integrationCalls))
	}
}

// Twilio Media Streams cannot carry DTMF: the tones are played by replacing
// the call's TwiML with <Play digits> followed by a fresh stream.
func TestSendDTMFTwilioPlaysDigitsAndReattachesStream(t *testing.T) {
	p := &answerPlatform{}
	a, _ := withTelephonyTestContext(t, p)
	t.Cleanup(func() { a.stopCarrierBridges(); a.stopRoutingDispatcher() })
	row := dtmfCall(t, a, "dtmf-twilio", "twilio", "in-progress")
	row.RecordingMode = recordingModeAlways
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b.setStream("MZ-original")
	b.media()

	if _, msg := sendDTMFAs(t, a, 7, map[string]any{"call_id": row.ID, "digits": "1#"}); msg != "" {
		t.Fatal(msg)
	}
	if len(p.integrationCalls) != 1 || p.integrationCalls[0].Tool != "update_call" || p.integrationCalls[0].Input["CallSid"] != row.CarrierSID {
		t.Fatalf("carrier commands = %+v", p.integrationCalls)
	}
	twiml, _ := p.integrationCalls[0].Input["Twiml"].(string)
	if !strings.HasPrefix(twiml, `<Response><Play digits="1#"/><Connect><Stream url="`) || !strings.Contains(twiml, "/media/twilio/"+row.ID) {
		t.Fatalf("twiml = %s", twiml)
	}
	// The recording started with the call keeps running; it is not restarted.
	if strings.Contains(twiml, "Recording") {
		t.Fatalf("twiml restarts the recording: %s", twiml)
	}
	if pending, err := a.carrierCommandPending(row.ID); err != nil || !pending {
		t.Fatalf("keypad command does not hold off recovery: %v %v", pending, err)
	}
	// A second press while the tones play is refused, not interleaved.
	if _, msg := sendDTMFAs(t, a, 7, map[string]any{"call_id": row.ID, "digits": "2"}); !strings.Contains(msg, "already being sent") {
		t.Fatalf("overlapping press: %q", msg)
	}

	// Twilio stops the old stream: a planned handover, not a failure.
	b.finish()
	var state, firstFailure, deadline string
	if err := a.db().db.QueryRow(`SELECT state,first_failed_at,deadline_at FROM carrier_media_bridges WHERE generation=?`, b.generation).Scan(&state, &firstFailure, &deadline); err != nil {
		t.Fatal(err)
	}
	if state != "recovering" || firstFailure != "" || deadline == "" {
		t.Fatalf("old stream: state=%q first_failed_at=%q deadline=%q", state, firstFailure, deadline)
	}
	current, _ := a.db().findCall(row.ID)
	if current.MediaStatus != "disconnected" || current.MediaErrorMessage != "" || current.MediaCloseReason != "planned stream handover" {
		t.Fatalf("media status=%q error=%q reason=%q", current.MediaStatus, current.MediaErrorMessage, current.MediaCloseReason)
	}

	// The replacement stream connects and carries media.
	next, err := a.claimCarrierBridge(current, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	next.setStream("MZ-after-dtmf")
	next.media()
	next.mu.Lock()
	recovered := next.recovered
	next.mu.Unlock()
	if recovered {
		t.Fatal("planned handover counted as a media recovery")
	}
	if pending, _ := a.carrierCommandPending(row.ID); pending {
		t.Fatal("keypad lease held after the stream returned")
	}
	current, _ = a.db().findCall(row.ID)
	if current.MediaStatus != "connected" {
		t.Fatalf("media status after handover = %q", current.MediaStatus)
	}
	// The next menu press goes straight through.
	if _, msg := sendDTMFAs(t, a, 7, map[string]any{"call_id": row.ID, "digits": "2"}); msg != "" {
		t.Fatal(msg)
	}
}

// A refused update leaves the stream untouched: no handover is pending and
// the next press is not blocked.
func TestSendDTMFTwilioRejectedUpdateReleasesCall(t *testing.T) {
	p := &answerPlatform{failTool: "update_call"}
	a, _ := withTelephonyTestContext(t, p)
	t.Cleanup(func() { a.stopCarrierBridges(); a.stopRoutingDispatcher() })
	row := dtmfCall(t, a, "dtmf-twilio-refused", "twilio", "in-progress")
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b.setStream("MZ-original")
	b.media()
	if _, msg := sendDTMFAs(t, a, 7, map[string]any{"call_id": row.ID, "digits": "1"}); !strings.Contains(msg, "twilio") {
		t.Fatalf("refused update: %q", msg)
	}
	if b.handoverActive(time.Now()) {
		t.Fatal("handover left pending after a refused update")
	}
	if pending, _ := a.carrierCommandPending(row.ID); pending {
		t.Fatal("refused update kept the keypad lease")
	}
	// A real stream loss afterwards is still a failure.
	b.finish()
	var firstFailure string
	if err := a.db().db.QueryRow(`SELECT first_failed_at FROM carrier_media_bridges WHERE generation=?`, b.generation).Scan(&firstFailure); err != nil || firstFailure == "" {
		t.Fatalf("stream loss not recorded as failure: %q %v", firstFailure, err)
	}
}
