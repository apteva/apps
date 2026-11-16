package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

var testAdviser = hangupActor{Initiator: terminationByUser, Principal: `{"subject":"adviser"}`, Action: "hangup_requested", RequestID: "tel-test"}

func confirmSIPBye(t *testing.T, f *sipDialogFixture) {
	t.Helper()
	bye := readSIPResponseContaining(t, f.signaling, "BYE sip:")
	response := "SIP/2.0 200 OK\r\n"
	for _, h := range []string{"Via", "From", "To", "Call-ID", "CSeq"} {
		response += h + ": " + sipTestHeader(bye, h) + "\r\n"
	}
	if _, err := f.signaling.Write([]byte(response + "Content-Length: 0\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
}

func hangupAudit(t *testing.T, f *sipDialogFixture) (outcome, previous, initiator, requestID string) {
	t.Helper()
	if err := f.app.db().db.QueryRow(`SELECT outcome,previous_status,termination_initiator,request_id FROM telephony_hangup_audit WHERE call_id=? ORDER BY requested_at DESC LIMIT 1`, f.call.ID).Scan(&outcome, &previous, &initiator, &requestID); err != nil {
		t.Fatal("hang-up not audited:", err)
	}
	return
}

// Production 2026-11-16: the carrier's BYE carried Q.850 cause 16 but the
// call's termination initiator stayed empty for the CRM.
func TestSIPCarrierByeRecordsCarrierTermination(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	f.answer(t)
	f.send(t, "BYE", 2, "", "Reason: Q.850;cause=16;text=\"Normal call clearing\"\r\n")
	readSIPResponseContaining(t, f.signaling, "200 OK")
	f.waitEnded(t)
	row, _ := f.app.db().findCall(f.call.ID)
	if row.Status != "completed" || row.TerminationInitiator != terminationByCarrier || row.TerminationCause != "normal_clearing" {
		t.Fatalf("carrier BYE termination = %q/%q/%q", row.Status, row.TerminationInitiator, row.TerminationCause)
	}
	var event string
	if err := f.app.db().db.QueryRow(`SELECT json_extract(payload_json,'$.termination.initiator') FROM call_events WHERE call_id=? AND topic='call.completed'`, row.ID).Scan(&event); err != nil || event != terminationByCarrier {
		t.Fatalf("call.completed initiator %q %v", event, err)
	}
	// The adviser's request after the carrier ended the call changes nothing.
	result, msg := f.app.hangupCall(f.ctx, row.ID, 0, "project-a", testAdviser)
	if msg != "" || !result.AlreadyEnded || result.Call.TerminationInitiator != terminationByCarrier {
		t.Fatalf("late hang-up = %+v %q", result, msg)
	}
	if again, _ := f.app.db().findCall(row.ID); again.TerminationInitiator != terminationByCarrier || again.TerminationCause != "normal_clearing" {
		t.Fatalf("late hang-up overwrote termination: %+v", again)
	}
	if outcome, previous, initiator, requestID := hangupAudit(t, f); outcome != "already_ended" || previous != "completed" || initiator != terminationByCarrier || requestID != "tel-test" {
		t.Fatal("audit", outcome, previous, initiator, requestID)
	}
}

// The carrier's BYE has claimed the session's end but not stored the call yet
// when the adviser's request arrives: the request waits and reports the
// carrier's end.
func TestHangupWaitsForCarrierEndInProgress(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	f.answer(t)
	claimed, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	sipSessionEndHook = func(leg string) {
		if leg == "carrier" {
			close(claimed)
			<-release
		}
	}
	t.Cleanup(func() { once.Do(func() { close(release) }); sipSessionEndHook = nil })
	f.send(t, "BYE", 2, "", "Reason: Q.850;cause=16\r\n")
	select {
	case <-claimed:
	case <-time.After(2 * time.Second):
		t.Fatal("carrier BYE did not end the session")
	}
	done := make(chan hangupResult, 1)
	go func() {
		result, msg := f.app.hangupCall(f.ctx, f.call.ID, 0, "project-a", testAdviser)
		if msg != "" {
			t.Error(msg)
		}
		done <- result
	}()
	select {
	case <-done:
		t.Fatal("hang-up returned before the carrier's end was stored")
	case <-time.After(200 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	select {
	case result := <-done:
		if !result.AlreadyEnded || result.Call.TerminationInitiator != terminationByCarrier || result.Call.TerminationCause != "normal_clearing" {
			t.Fatalf("simultaneous end = %+v", result.Call)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hang-up did not finish")
	}
	f.waitEnded(t)
}

func TestOperatorHangupReturnsTermination(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	f.answer(t)
	req := httptest.NewRequest(http.MethodPost, "/calls/"+f.call.ID+"/hangup?project_id=project-a", nil)
	req.Header.Set("X-Request-ID", "tel-operator")
	recorder := httptest.NewRecorder()
	served := make(chan struct{})
	go func() { defer close(served); f.app.handleCallAction(recorder, req) }()
	confirmSIPBye(t, f)
	<-served
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	var body struct {
		AlreadyEnded bool              `json:"already_ended"`
		Status       string            `json:"status"`
		Termination  map[string]string `json:"termination"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AlreadyEnded || body.Status != "completed" || body.Termination["initiator"] != terminationByOperator || body.Termination["cause"] != "hangup_requested" {
		t.Fatalf("response %s", recorder.Body)
	}
	row, _ := f.app.db().findCall(f.call.ID)
	if row.TerminationInitiator != terminationByOperator {
		t.Fatalf("stored initiator %q", row.TerminationInitiator)
	}
	if outcome, previous, _, requestID := hangupAudit(t, f); outcome != "ended" || previous != "answered" || requestID != "tel-operator" {
		t.Fatal("audit", outcome, previous, requestID)
	}
}

func TestSIPLocalFailureRecordsTelephony(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	f.answer(t)
	go f.session.finish("local_error", errors.New("SIP session refresh expired"))
	confirmSIPBye(t, f)
	f.waitEnded(t)
	if row, _ := f.app.db().findCall(f.call.ID); row.TerminationInitiator != terminationByTelephony {
		t.Fatalf("local failure initiator %q", row.TerminationInitiator)
	}
}

func TestRepeatedEndKeepsRecordedSource(t *testing.T) {
	app, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, app, "repeat-end", "telnyx")
	db := app.db()
	if _, err := db.updateStatusWithFacts(row.ID, "completed", "", lifecycleFacts{Source: "telephony"}); err != nil {
		t.Fatal(err)
	}
	// A first end without a source can still be completed by a later report.
	if _, err := db.updateStatusWithFacts(row.ID, "completed", "", lifecycleFacts{Source: "provider", TerminationInitiator: "caller"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.updateStatusWithFacts(row.ID, "completed", "", testAdviser.facts()); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.findCall(row.ID); got.TerminationInitiator != "caller" || got.TerminationCause != "" {
		t.Fatalf("recorded source replaced: %q %q", got.TerminationInitiator, got.TerminationCause)
	}
}

func TestSIPByeReasonCauses(t *testing.T) {
	for reason, want := range map[string][2]string{
		"":                         {"", ""},
		"Q.850;cause=16":           {"normal_clearing", ""},
		"q.850 ; cause=17":         {"user_busy", ""},
		"Q.850;cause=102":          {"q850_cause_102", ""},
		"SIP;cause=487;text=\"x\"": {"", "487"},
	} {
		request := sip.NewRequest(sip.BYE, sip.Uri{Host: "example.test"})
		if reason != "" {
			request.AppendHeader(sip.NewHeader("Reason", reason))
		}
		if got := sipByeTermination(request); got.TerminationCause != want[0] || got.TerminationCode != want[1] {
			t.Errorf("%q = %q/%q", reason, got.TerminationCause, got.TerminationCode)
		}
	}
}

// An adviser signed in as an application user ends their own call.
func TestAdviserHangupRecordsUser(t *testing.T) {
	app, _ := withTelephonyTestContext(t, &answerPlatform{})
	phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "adviser-call", "pending")
	alice := phoneTestIdentity("alice")
	if w := phoneTestRequest(app, &alice, "POST", "/softphone/answer/"+row.ID, map[string]any{}); w.Code != 200 {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	if _, err := app.db().db.Exec(`UPDATE calls SET carrier_sid='CA-adviser',carrier_connection_id=10,status='in-progress' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	type hangupBody struct {
		AlreadyEnded bool              `json:"already_ended"`
		Termination  map[string]string `json:"termination"`
	}
	hangup := func() hangupBody {
		t.Helper()
		w := phoneTestRequest(app, &alice, "POST", "/calls/"+row.ID+"/hangup", nil)
		var body hangupBody
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("hangup: %d %s", w.Code, w.Body)
		}
		return body
	}
	first := hangup()
	if first.AlreadyEnded || first.Termination["initiator"] != terminationByUser || first.Termination["cause"] != "hangup_requested" {
		t.Fatalf("adviser hang-up = %+v", first)
	}
	var principal, requestID, outcome string
	if err := app.db().db.QueryRow(`SELECT principal,request_id,outcome FROM telephony_hangup_audit WHERE call_id=? AND outcome='ended'`, row.ID).Scan(&principal, &requestID, &outcome); err != nil || principal != alice.key() || requestID == "" {
		t.Fatalf("audit principal=%q request=%q err=%v", principal, requestID, err)
	}
	// A repeated request reports the same termination.
	if again := hangup(); !again.AlreadyEnded || again.Termination["initiator"] != terminationByUser {
		t.Fatalf("repeated hang-up = %+v", again)
	}
}
