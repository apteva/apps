package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Production crash on Telephony 0.12.28: a Telnyx call on direct SIP that
// Telephony had answered itself (carrier_answered_at set by its own 200 OK and
// ACK) but no adviser had answered. terminateCarrierCall judged it unanswered
// and rejected; rejectInboundCarrierCall judged it carrier-answered and
// terminated; they recursed until "fatal error: stack overflow" killed the app.

func telnyxSIPDialogFixture(t *testing.T) *sipDialogFixture {
	t.Helper()
	return newSIPDialogFixtureWithRoute(t, false, 2*time.Second, func(_ *App, route *routeRow) {
		route.CarrierSlug = "telnyx"
		route.TransportConfig = `{"provider":"telnyx","trunk_id":"TK1"}`
	})
}

// ackSIPRequest answers a request the gateway sent (a BYE) with 200 OK.
func ackSIPRequest(t *testing.T, f *sipDialogFixture, request string) {
	t.Helper()
	response := "SIP/2.0 200 OK\r\n"
	for _, h := range []string{"Via", "From", "To", "Call-ID", "CSeq"} {
		response += h + ": " + sipTestHeader(request, h) + "\r\n"
	}
	response += "Content-Length: 0\r\n\r\n"
	if _, err := f.signaling.Write([]byte(response)); err != nil {
		t.Fatal(err)
	}
}

// inviteTransaction sends a request inside the original INVITE transaction
// (same Via branch, CSeq 1): a CANCEL, or the ACK for a non-2xx final response.
func inviteTransaction(t *testing.T, f *sipDialogFixture, method, to string) {
	t.Helper()
	request := fmt.Sprintf("%s sip:+12025550100@%s SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:%d;branch=z9hG4bK-INVITE-1;rport\r\nMax-Forwards: 70\r\nFrom: <sip:+12025550101@127.0.0.1>;tag=caller\r\nTo: %s\r\nCall-ID: native-dialog@local\r\nCSeq: 1 %s\r\nContent-Length: 0\r\n\r\n", method, f.gateway.cfg.ListenAddress, f.localPort, to, method)
	if _, err := f.signaling.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
}

func waitTermination(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("%s blocked", what)
	}
}

// The incident state, for both entry points: the dialog was answered by
// Telephony, so ending it must send BYE (a decline is no longer possible), once.
func TestTerminationOfTelephonyAnsweredSIPCallWithoutAdviser(t *testing.T) {
	for _, entry := range []string{"terminate", "reject"} {
		t.Run(entry, func(t *testing.T) {
			f := telnyxSIPDialogFixture(t)
			f.answer(t)
			row, err := f.app.db().findCall(f.call.ID)
			if err != nil {
				t.Fatal(err)
			}
			if row.CarrierAnsweredAt == "" {
				t.Fatal("fixture did not record the carrier answer")
			}
			// No adviser has answered.
			row.AnsweredAt, row.MediaConnectedAt, row.Status = "", "", "answering"
			ctx := f.ctx
			done := make(chan error, 1)
			go func() {
				if entry == "terminate" {
					done <- f.app.terminateCarrierCall(ctx, row)
				} else {
					done <- f.app.rejectInboundCarrierCall(ctx, row)
				}
			}()
			bye := readSIPResponseContaining(t, f.signaling, "BYE sip:")
			ackSIPRequest(t, f, bye)
			waitTermination(t, done, entry)
			f.waitEnded(t)
			// Ending an ended call is a no-op, not an error or another BYE.
			if err := f.app.terminateCarrierCall(ctx, row); err != nil {
				t.Fatalf("duplicate termination: %v", err)
			}
			if err := f.app.rejectInboundCarrierCall(ctx, row); err != nil {
				t.Fatalf("duplicate rejection: %v", err)
			}
		})
	}
}

// A ringing direct SIP call is declined, never sent a BYE, and ending it again
// is a no-op.
func TestTerminationOfRingingSIPCallDeclines(t *testing.T) {
	for _, entry := range []string{"terminate", "reject"} {
		t.Run(entry, func(t *testing.T) {
			f := telnyxSIPDialogFixture(t)
			ctx := f.ctx
			done := make(chan error, 1)
			go func() {
				if entry == "terminate" {
					done <- f.app.terminateCarrierCall(ctx, f.call)
				} else {
					done <- f.app.rejectInboundCarrierCall(ctx, f.call)
				}
			}()
			// A decline completes when the caller acknowledges it.
			response := readSIPResponseContaining(t, f.signaling, "603 Decline")
			inviteTransaction(t, f, "ACK", sipTestHeader(response, "To"))
			waitTermination(t, done, entry)
			f.waitEnded(t)
			if err := f.app.terminateCarrierCall(ctx, f.call); err != nil {
				t.Fatalf("duplicate termination: %v", err)
			}
		})
	}
}

// The caller hangs up (CANCEL) before anyone answers; Telephony's termination
// that follows finds the dialog gone and succeeds without signaling.
func TestTerminationAfterCallerCancel(t *testing.T) {
	f := telnyxSIPDialogFixture(t)
	inviteTransaction(t, f, "CANCEL", "<sip:+12025550100@127.0.0.1>")
	readSIPResponseContaining(t, f.signaling, "487")
	f.waitEnded(t)
	ctx := f.ctx
	row := *f.call
	row.CarrierAnsweredAt = "2026-11-03T08:32:38Z" // any stale answer evidence
	if err := f.app.terminateCarrierCall(ctx, &row); err != nil {
		t.Fatalf("terminate after cancel: %v", err)
	}
	if err := f.app.rejectInboundCarrierCall(ctx, &row); err != nil {
		t.Fatalf("reject after cancel: %v", err)
	}
}

// Without a running gateway the same disagreeing state must fail fast. On
// 0.12.28 this input overflowed the stack before reaching the gateway check.
func TestTerminationDisagreeingAnswerStateCannotRecurse(t *testing.T) {
	f := telnyxSIPDialogFixture(t)
	row := *f.call
	row.CarrierAnsweredAt, row.AnsweredAt, row.Status = "2026-11-03T08:32:38Z", "", "answering"
	f.app.sip.gateway = nil
	ctx := f.ctx
	for name, end := range map[string]func() error{
		"terminate": func() error { return f.app.terminateCarrierCall(ctx, &row) },
		"reject":    func() error { return f.app.rejectInboundCarrierCall(ctx, &row) },
	} {
		if err := end(); err == nil || !strings.Contains(err.Error(), "gateway is not running") {
			t.Fatalf("%s: err=%v, want gateway not running", name, err)
		}
	}
}
