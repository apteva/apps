package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"sync/atomic"
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

func sipTestResponse(status int) error {
	return sipgo.ErrDialogResponse{Res: &sip.Response{StatusCode: status}}
}

func TestSIPByeRetriesSilenceButNotResponses(t *testing.T) {
	timeout := context.DeadlineExceeded
	for name, c := range map[string]struct {
		results  []error
		calls    int
		want     error
		retried  bool
		finished bool
	}{
		"confirmed first time":  {results: []error{nil}, calls: 1},
		"lost then confirmed":   {results: []error{timeout, nil}, calls: 2, retried: true},
		"rejected is final":     {results: []error{sipTestResponse(500)}, calls: 1, want: sipTestResponse(500)},
		"481 first is an error": {results: []error{sipTestResponse(481)}, calls: 1, want: sipTestResponse(481)},
		"481 after a lost try":  {results: []error{timeout, sipTestResponse(481)}, calls: 2, want: sipTestResponse(481), retried: true, finished: true},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err, retried := sendSIPByeWithRetry(func(context.Context) error {
				calls++
				return c.results[calls-1]
			}, nil, nil, nil)
			if calls != c.calls || retried != c.retried || sipByeRetryFinished(err, retried) != c.finished {
				t.Fatalf("calls=%d retried=%v finished=%v", calls, retried, sipByeRetryFinished(err, retried))
			}
			if (err == nil) != (c.want == nil) {
				t.Fatalf("err=%v want=%v", err, c.want)
			}
		})
	}
}

func TestSIPByeRetryStopsAtDeadlineAndWhenPeerEnds(t *testing.T) {
	calls := 0
	silent := func(ctx context.Context) error {
		calls++
		<-ctx.Done()
		return ctx.Err()
	}
	started := time.Now()
	err, _ := sendSIPByeWithRetry(silent, nil, func() time.Time { return started.Add(300 * time.Millisecond) }, nil)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || time.Since(started) > 2*time.Second {
		t.Fatalf("stop deadline ignored: err=%v calls=%d after=%v", err, calls, time.Since(started))
	}
	calls = 0
	retries := 0
	err, retried := sendSIPByeWithRetry(func(context.Context) error {
		calls++
		return context.DeadlineExceeded
	}, func() bool { return true }, nil, func(error) { retries++ })
	if !errors.Is(err, errSIPByeCrossed) || !retried || calls != 1 || retries != 1 {
		t.Fatalf("peer end ignored: err=%v calls=%d retries=%d", err, calls, retries)
	}
	if got := sipTerminationResult(nil, err, false); got != nil {
		t.Fatalf("crossed BYE should end the call cleanly: %v", got)
	}
}

func TestSIPByeRetryUsesItsWholeBudgetOnSilence(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the retry budget")
	}
	calls := 0
	started := time.Now()
	err, retried := sendSIPByeWithRetry(func(ctx context.Context) error {
		calls++
		<-ctx.Done()
		return ctx.Err()
	}, nil, nil, nil)
	elapsed := time.Since(started)
	if err == nil || !retried || calls < 3 || elapsed > sipByeRetryBudget+time.Second {
		t.Fatalf("err=%v retried=%v calls=%d elapsed=%v", err, retried, calls, elapsed)
	}
}

// A gateway that starts stopping during the retries still gets its deadline.
func TestSIPByeRetryHonoursAStopThatStartsDuringRetries(t *testing.T) {
	var stop atomic.Int64
	started := time.Now()
	calls := 0
	err, _ := sendSIPByeWithRetry(func(ctx context.Context) error {
		calls++
		stop.Store(time.Now().Add(100 * time.Millisecond).UnixNano())
		<-ctx.Done()
		return ctx.Err()
	}, nil, func() time.Time {
		if at := stop.Load(); at != 0 {
			return time.Unix(0, at)
		}
		return time.Time{}
	}, nil)
	if err == nil || calls != 1 || time.Since(started) > sipByeAttemptTimeout+2*sipByeRetryDelay {
		t.Fatalf("err=%v calls=%d after=%v", err, calls, time.Since(started))
	}
}
