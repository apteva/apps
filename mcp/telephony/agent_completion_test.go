package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestAgentCoreCompletionRequiresExplicitAuthenticatedClose(t *testing.T) {
	row := &callRow{PeerKind: peerKindRealtime}
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{wsutil.ClosedError{Code: 1000, Reason: "realtime conversation completed"}, true},
		{wsutil.ClosedError{Code: 1000, Reason: "realtime completion timed out"}, true},
		{wsutil.ClosedError{Code: 1000, Reason: "audio bridge unregistered"}, false},
		{wsutil.ClosedError{Code: 1011, Reason: "realtime conversation completed"}, false},
		{io.EOF, false}, {errors.New("realtime conversation completed"), false},
	} {
		if got := agentCoreCompletion(row, tc.err); got != tc.want {
			t.Fatal(tc.err, got)
		}
		if tc.want && retryAgentCore(tc.err) {
			t.Fatal("completion reconnected")
		}
		if agentCoreCompletion(&callRow{PeerKind: peerKindHuman}, tc.err) {
			t.Fatal("human bridge ended")
		}
	}
}
func TestAgentCoreCompletionHangsUpOnceAndRejectsReplacedOwner(t *testing.T) {
	for _, direction := range []string{"inbound", "outbound"} {
		for _, variant := range []string{"normal", "new_owner", "failure"} {
			t.Run(direction+"/"+variant, func(t *testing.T) {
				a, ctx, p, _, row, _ := reliabilityFixture(t)
				durationExec(t, a, `UPDATE calls SET status='answered',peer_kind=?,thread_id='tel-completion',agent_id=7,direction=? WHERE id=?`, peerKindRealtime, direction, row.ID)
				row = durationRow(t, a, row.ID)
				closed := wsutil.ClosedError{Code: ws.StatusNormalClosure, Reason: "realtime conversation completed"}
				if variant == "new_owner" {
					durationExec(t, a, `UPDATE calls SET thread_id='tel-replacement' WHERE id=?`, row.ID)
				}
				p.failCarrier = variant == "failure"
				err := a.completeAgentFromCore(ctx, row, closed)
				if variant != "normal" {
					if err == nil || isTerminalStatus(durationRow(t, a, row.ID).Status) {
						t.Fatal("failure/replacement marked complete", err)
					}
					if variant == "new_owner" && len(p.integrationCalls) != 0 {
						t.Fatal("replaced call hung up")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = a.completeAgentFromCore(ctx, row, closed); err != nil {
					t.Fatal(err)
				}
				if len(p.integrationCalls) != 1 || p.integrationCalls[0].Tool != "hangup_call" || durationRow(t, a, row.ID).Status != "completed" {
					t.Fatal("missing or duplicate carrier hangup", p.integrationCalls)
				}
			})
		}
	}
}

func TestAgentCompletionCloseDoesNotStartTransportRecovery(t *testing.T) {
	for _, expected := range []bool{false, true} {
		t.Run(fmt.Sprint(expected), func(t *testing.T) {
			a, _, _, _, row, _ := reliabilityFixture(t)
			durationExec(t, a, `UPDATE calls SET status='answered',peer_kind=?,thread_id='tel-completion',agent_id=7 WHERE id=?`, peerKindRealtime, row.ID)
			row = durationRow(t, a, row.ID)
			b, err := a.claimCarrierBridge(row, context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { a.stopCarrierBridges(); a.stopRoutingDispatcher() })
			reason := "audio bridge unregistered"
			if expected {
				reason = "realtime conversation completed"
			}
			b.receivedLeg(mediaCloseLegCore, ws.OpClose, ws.NewCloseFrameBody(ws.StatusNormalClosure, reason))
			b.socketError(mediaCloseLegCore, "write", io.ErrClosedPipe)
			b.mu.Lock()
			failure := b.first != nil
			b.mu.Unlock()
			if failure == expected {
				t.Fatal("wrong recovery classification", failure)
			}
			if expected {
				durationExec(t, a, `UPDATE calls SET status='completed' WHERE id=?`, row.ID)
				b.finish()
				if durationRow(t, a, row.ID).MediaStatus == "error" {
					t.Fatal("successful completion marked media error")
				}
			}
		})
	}
}
