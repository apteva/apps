package main

import (
	"errors"
	"fmt"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"testing"
	"time"
)

func TestSIPCrossedBYECompletesWithPeerTerminationEvidence(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	f.answer(t)
	done := make(chan error, 1)
	go func() { done <- f.gateway.Hangup(f.call) }()
	bye := readSIPResponseContaining(t, f.signaling, "BYE sip:")
	f.send(t, "BYE", 2, "", "")
	readSIPResponseContaining(t, f.signaling, "200 OK")
	response := "SIP/2.0 481 Call Does Not Exist\r\n"
	for _, h := range []string{"Via", "From", "To", "Call-ID", "CSeq"} {
		response += h + ": " + sipTestHeader(bye, h) + "\r\n"
	}
	response += "Content-Length: 0\r\n\r\n"
	if _, err := f.signaling.Write([]byte(response)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("validated peer hangup reported failed", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("hangup blocked")
	}
	f.waitEnded(t)
	row, err := f.app.db().findCall(f.call.ID)
	if err != nil || row.Status != "completed" || row.ErrorMessage != "" {
		t.Fatalf("crossed BYE status=%+v err=%v", row, err)
	}
	signal := f.session.diagnostics.snapshot(time.Now()).Signal
	if signal.BYEStatus != "peer_ended" || signal.BYEFailures != 0 {
		t.Fatalf("peer evidence lost: %+v", signal)
	}
}

func TestSIPCrossedBYERequiresConfirmedPeerAndRetainsOtherErrors(t *testing.T) {
	for _, status := range []int{481, 403, 500} {
		for _, peer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/peer_%t", status, peer), func(t *testing.T) {
				app, _ := withTelephonyTestContext(t, &answerPlatform{})
				row := recoveryCall(t, app, "bye-case", "telnyx")
				d := app.newSIPDiagnostics(row, "dialog")
				err := fmt.Errorf("termination: %w", sipgo.ErrDialogResponse{Res: &sip.Response{StatusCode: status, Reason: "test"}})
				got := sipTerminationResult(d, err, peer)
				s := d.snapshot(time.Now()).Signal
				if status == 481 && peer {
					if got != nil || s.BYEStatus != "peer_ended" || s.BYEFailures != 0 {
						t.Fatal("crossed BYE lost", got, s)
					}
				} else if !errors.Is(got, err) || s.BYEFailures != 1 || s.BYEStatus != "unconfirmed" {
					t.Fatal("unexpected signaling failure hidden", got, s)
				}
			})
		}
	}
	timeout := errors.New("socket timeout")
	if got := sipTerminationResult(nil, timeout, true); got != timeout {
		t.Fatal("timeout hidden")
	}
}
