package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// coreCloseReading reads client frames on the Core side of a link until a
// Close frame or EOF. answer=false never replies to the Close.
func coreCloseReading(conn net.Conn, answer bool) <-chan error {
	done := make(chan error, 1)
	go func() {
		for {
			frame, err := ws.ReadFrame(conn)
			if err != nil {
				done <- err
				return
			}
			if frame.Header.Masked {
				ws.Cipher(frame.Payload, frame.Header.Mask, 0)
			}
			if frame.Header.OpCode == ws.OpClose {
				code, reason := ws.ParseCloseFrameData(frame.Payload)
				if answer {
					_ = wsutil.WriteServerMessage(conn, ws.OpClose, frame.Payload)
				}
				done <- wsutil.ClosedError{Code: code, Reason: reason}
				return
			}
		}
	}()
	return done
}

func wantCoreClose(t *testing.T, got <-chan error, code ws.StatusCode, reason string) {
	t.Helper()
	select {
	case err := <-got:
		var closed wsutil.ClosedError
		if !errors.As(err, &closed) || closed.Code != code || closed.Reason != reason {
			t.Fatalf("Core received %v, want Close %d %q", err, code, reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Core received nothing")
	}
}

func noRecovery(context.Context) (net.Conn, error) { return nil, io.EOF }

func TestAgentCoreLinkSendsBridgeCloseToCore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coreConn, coreSide := net.Pipe()
	defer coreSide.Close()
	got := coreCloseReading(coreSide, true)
	link := newAgentCoreLink(ctx, coreConn, noRecovery, nil)
	go func() { _, _ = io.Copy(io.Discard, link) }()

	// The bridge ends its side as its call context ends, as direct SIP does.
	closer := newGracefulWebSocket(link, newWebSocketWriterPump(link, ws.StateClientSide))
	go func() { <-ctx.Done(); closer.Close(ws.StatusNormalClosure, "call transferred to an adviser") }()
	cancel()
	wantCoreClose(t, got, ws.StatusNormalClosure, "call transferred to an adviser")
}

func TestAgentCoreLinkClosesNormallyWhenCallEndsWithoutBridgeClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	coreConn, coreSide := net.Pipe()
	defer coreSide.Close()
	got := coreCloseReading(coreSide, true)
	link := newAgentCoreLink(ctx, coreConn, noRecovery, nil)
	defer link.Close()
	cancel()
	wantCoreClose(t, got, ws.StatusNormalClosure, "call ended")
}

func TestAgentCoreLinkForcesCloseWhenCoreNeverAnswers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	coreConn, coreSide := net.Pipe()
	defer coreSide.Close()
	got := coreCloseReading(coreSide, false)
	link := newAgentCoreLink(ctx, coreConn, noRecovery, nil)
	defer link.Close()
	started := time.Now()
	cancel()
	wantCoreClose(t, got, ws.StatusNormalClosure, "call ended")
	// Core never answers: the connection is still closed, within the bound.
	_ = coreSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.Copy(io.Discard, coreSide); err != nil {
		t.Fatalf("Core connection was not closed: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*websocketForcedCloseTimeout+250*time.Millisecond {
		t.Fatalf("forced close took %s", elapsed)
	}
}

func TestAgentCoreLinkFailureIsNotANormalClose(t *testing.T) {
	coreConn, coreSide := net.Pipe()
	defer coreSide.Close()
	got := coreCloseReading(coreSide, true)
	link := newAgentCoreLink(context.Background(), coreConn, noRecovery, nil)
	defer link.Close()
	// An audio frame the link cannot carry ends it as a failure.
	go func() { _ = wsutil.WriteClientMessage(link, ws.OpBinary, make([]byte, 48000)) }()
	wantCoreClose(t, got, ws.StatusInternalServerError, "invalid telephony audio frame")
}

func TestSIPCoreCloseStatusNamesHowMediaEnded(t *testing.T) {
	live, ended := context.Background(), canceledContext()
	failure := &websocketCloseState{}
	if code, reason := sipCoreCloseStatus(ended, nil, failure); code != ws.StatusNormalClosure || reason != "call ended" {
		t.Fatal("hang-up", code, reason)
	}
	handover := &sipBridgeHandover{}
	handover.requested.Store(true)
	if code, reason := sipCoreCloseStatus(live, handover, failure); code != ws.StatusNormalClosure || reason != "call transferred to an adviser" {
		t.Fatal("transfer", code, reason)
	}
	failure.SetLeg(mediaCloseLegCarrier, ws.StatusInternalServerError, "read direct SIP RTP")
	if code, reason := sipCoreCloseStatus(ended, handover, failure); code != ws.StatusInternalServerError || reason != "read direct SIP RTP" {
		t.Fatal("media failure", code, reason)
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// A caller hang-up on a continuous AI call reaches Core as a normal Close,
// not as a dropped connection.
func TestAgentCallerHangupSendsCoreNormalClose(t *testing.T) {
	p := &continuityPlatform{answerPlatform: &answerPlatform{}}
	a, appCtx := withTelephonyTestContext(t, p)
	a.startCarrierMediaRecovery(appCtx)
	t.Cleanup(func() { a.stopCarrierBridges(); a.stopRoutingDispatcher() })
	contract := `{"version":1,"continuous_conversation":true,"local_interruption_required":true,"scoped_cancellation":true,"receipt_evidence":true}`
	var ready atomic.Bool
	coreEnded := make(chan error, 1)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := ws.HTTPUpgrader{Header: http.Header{agentAudioHeader: []string{contract}}}
		conn, _, _, err := u.Upgrade(r, w)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			raw, _, err := wsutil.ReadClientData(conn)
			if err != nil {
				coreEnded <- err
				return
			}
			if bytes.Contains(raw, []byte("playback.ready")) {
				ready.Store(true)
			}
		}
	}))
	defer core.Close()
	p.address = "ws" + strings.TrimPrefix(core.URL, "http")
	row := testCall("continuous-hangup", "in-progress")
	row.ProjectID = appCtx.CurrentProject()
	row.PeerKind = peerKindRealtime
	row.CarrierSlug = "telnyx"
	row.AudioBridgeURL = p.address
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		a.handleTelnyxMediaStream(w, r)
	}))
	defer server.Close()
	conn := dialWS(t, server.URL+"/media/telnyx/"+row.ID+"/"+row.CallbackSecret)
	defer conn.Close()
	go func() { _, _ = io.Copy(io.Discard, conn) }()
	writeRecoveryTelnyxFrame(t, conn, &row, "hangup-stream")
	for deadline := time.Now().Add(3 * time.Second); !ready.Load(); {
		if time.Now().After(deadline) {
			t.Fatal("Core link never came up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := wsutil.WriteClientText(conn, []byte(`{"event":"stop","stream_id":"hangup-stream"}`)); err != nil {
		t.Fatal(err)
	}
	wantCoreClose(t, coreEnded, ws.StatusNormalClosure, "telnyx call media ended normally")
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("carrier handler survived the hang-up")
	}
}
