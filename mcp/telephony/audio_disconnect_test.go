package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/webrtc/v4"
)

func TestStructuredSocketErrorClassesExcludeRawCredentials(t *testing.T) {
	cases := []struct {
		err           error
		class, detail string
	}{
		{io.EOF, "eof", "eof"}, {io.ErrUnexpectedEOF, "eof", "unexpected_eof"},
		{syscall.ECONNRESET, "connection_reset", "tcp_connection_reset"}, {syscall.EPIPE, "connection_reset", "broken_pipe"},
		{net.ErrClosed, "local_closure", "socket_closed"}, {io.ErrClosedPipe, "local_closure", "socket_closed"},
		{&net.OpError{Op: "read", Err: context.DeadlineExceeded}, "timeout", "socket_deadline_exceeded"},
		{ws.ProtocolError("DO_NOT_STORE"), "protocol_error", "invalid_websocket_frame"},
		{wsutil.ErrFrameTooLarge, "protocol_error", "invalid_websocket_frame"}, {wsutil.ErrInvalidUTF8, "protocol_error", "invalid_websocket_frame"},
		{wsutil.ClosedError{Code: 1000, Reason: "DO_NOT_STORE"}, "peer_close", "websocket_close_frame"},
		{fmt.Errorf("https://token.example/DO_NOT_STORE"), "transport_error", "unclassified_transport_error"},
	}
	for _, c := range cases {
		t.Run(c.class+":"+c.detail, func(t *testing.T) {
			class, detail := audioSocketError(fmt.Errorf("credential-bearing wrapper DO_NOT_STORE: %w", c.err))
			if class != c.class || detail != c.detail {
				t.Fatalf("%s %s", class, detail)
			}
		})
	}
}

func TestStructuredDisconnectIsSingleFrozenEvent(t *testing.T) {
	var tracker audioCallTelemetry
	var events []audioNetworkEvent
	writer := &websocketWriterPump{}
	correlation := audioSessionCorrelation{SessionID: "session-a", AttemptID: "attempt-b", RecoveryID: "recovery-c", InitiatingAction: "automatic_retry"}
	id := tracker.openedWithNetwork(writer, "hash", "epoch", "socket_peer", audioNetworkEvent{CallID: "call", ProjectID: "project", Session: correlation, Retention: time.Hour}, func(e audioNetworkEvent) { events = append(events, e) })
	writer.activity.observe(ws.OpBinary, nil)
	writer.activity.observe(ws.OpPing, nil)
	writer.activity.observe(ws.OpPong, nil)
	writer.activity.write.Store(time.Now().UnixNano())
	rtt := 42
	tracker.observeBrowserConnection(writer, browserAudioDiagnostics{RTTMS: &rtt, PlaybackQueueMS: 60, WebSocketBufferedBytes: 1200, AudioContextState: "running", MicrophoneMuted: true, MicrophoneTrackState: "live"})
	tracker.attributeTransportSamples(writer, "epoch", []browserTransportSample{{States: map[string]string{"ice": "connected", "dtls": "connected"}}})
	tracker.shutdown(writer, "call_ended")
	tracker.closed(writer, "transport_read_error", fmt.Errorf("DO_NOT_STORE: %w", io.EOF))
	tracker.closed(writer, "handler_closed", nil)
	if len(events) != 2 {
		t.Fatalf("duplicate disconnect: %d", len(events))
	}
	e := events[1]
	d := e.Disconnect
	if e.ConnectionID != id || e.Session != correlation || e.ShutdownIntent != "call_ended" || d == nil || d.ErrorClass != "eof" || d.RTTMS == nil || *d.RTTMS != 42 || d.LastReadAt == "" || d.LastPingAt == "" || d.LastPongAt == "" || d.LastWriteAt == "" || d.LastAudioReadAt == "" || d.LastBrowserSampleAt == "" || d.BrowserICEState != "connected" || d.BrowserDTLSState != "connected" || !d.MicrophoneMuted {
		t.Fatalf("%+v %+v", e, d)
	}
	data, _ := json.Marshal(events)
	if strings.Contains(string(data), "DO_NOT_STORE") {
		t.Fatal("raw errors stored")
	}
	// Replacement must not copy a new tab's measurements into the old socket.
	second := &websocketWriterPump{}
	tracker.opened(second, "h2", "epoch", "socket_peer")
	if e.Disconnect.RTTMS == nil || *e.Disconnect.RTTMS != 42 {
		t.Fatal("closed snapshot changed")
	}
}

func TestRTCDisconnectRetainsSignalingCauseBeforeCleanup(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	signal := &websocketWriterPump{}
	signal.activity.observe(ws.OpText, nil)
	tracker := &rtcDisconnectTracker{writer: signal, pc: pc}
	tracker.capture(fmt.Errorf("DO_NOT_STORE: %w", syscall.ECONNRESET), "")
	before := tracker.snapshot(nil)
	_ = pc.Close()
	tracker.capture(io.ErrClosedPipe, "")
	internal := &websocketWriterPump{conn: &rtcHubConn{disconnect: tracker}}
	snapshot := internal.disconnectInfo(io.ErrClosedPipe)
	if snapshot.ErrorClass != "connection_reset" || snapshot.Transport != "webrtc_signaling" || snapshot.RTCState != before.RTCState || snapshot.ICEState != before.ICEState || snapshot.LastReadAt == "" {
		t.Fatalf("original cause lost: %+v", snapshot)
	}
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), "DO_NOT_STORE") {
		t.Fatal("raw RTC error stored")
	}
}

func TestSessionGenerationReplacementCorrelationAndCollectorPressure(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	alice := phoneTestIdentity("alice")
	p, err := app.phonePrincipal(row.ProjectID, alice)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.setPhoneOwner(&row, p, "sales"); err != nil {
		t.Fatal(err)
	}
	first, err := app.issuePhoneSession(&row, p, audioSessionCorrelation{InitiatingAction: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	reason, _, _, old := app.phoneMediaCheckState(&row, first.SessionToken, true)
	if reason != "" || first.SessionGeneration == "" || old.SessionID != first.SessionGeneration {
		t.Fatal(reason, old)
	}
	hub := app.softphones.hubFor(row.ID)
	writer := &websocketWriterPump{}
	connection := hub.telemetry.openedWithNetwork(writer, "hash", "epoch", "socket_peer", audioNetworkEvent{CallID: row.ID, ProjectID: row.ProjectID, Session: old, Retention: time.Hour}, app.audioNetworks.enqueue)
	// Pressure in observational persistence cannot block session issuance.
	app.audioNetworks.mu.Lock()
	done := make(chan *softphoneSession, 1)
	fail := make(chan error, 1)
	go func() {
		s, e := app.issuePhoneSession(&row, p, audioSessionCorrelation{InitiatingAction: "automatic_retry", RecoveryID: "recovery-a", AttemptID: "attempt-b"})
		if e != nil {
			fail <- e
		} else {
			done <- s
		}
	}()
	var second *softphoneSession
	select {
	case second = <-done:
	case err = <-fail:
		app.audioNetworks.mu.Unlock()
		t.Fatal(err)
	case <-time.After(time.Second):
		app.audioNetworks.mu.Unlock()
		t.Fatal("collector blocked session issuance")
	}
	app.audioNetworks.mu.Unlock()
	if app.audioNetworks.dropped.Load() == 0 {
		t.Fatal("pressure not accounted")
	}
	reason, _, _, fresh := app.phoneMediaCheckState(&row, second.SessionToken, true)
	if reason != "" || fresh.PreviousSessionID != old.SessionID || fresh.AttemptID != "attempt-b" || fresh.InitiatingAction != "automatic_retry" {
		t.Fatal(reason, fresh)
	}
	if app.phoneMediaDenialReason(&row, first.SessionToken) != "media_token_replaced" {
		t.Fatal("replacement authorization changed")
	}
	hub.telemetry.closed(writer, "media_token_replaced", nil)
	app.audioNetworks.mu.Lock()
	event := app.audioNetworks.pending[len(app.audioNetworks.pending)-1]
	app.audioNetworks.mu.Unlock()
	if event.ConnectionID != connection || event.Session.SessionID != old.SessionID || event.ReplacedBy == nil || event.ReplacedBy.SessionID != fresh.SessionID {
		t.Fatalf("replacement chain lost: %+v", event)
	}
	// Preserve the rejection and both generations without copying token or URL.
	server := phoneTestRequest(app, nil, "GET", strings.TrimPrefix(first.MediaURL, "/api/apps/telephony/_install/42")+"?session_generation="+first.SessionGeneration, nil)
	if server.Code != 403 {
		t.Fatalf("stale token admitted: %d", server.Code)
	}
	if _, err = app.audioNetworks.flush(context.Background(), app.db(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := app.db().db.Query(`SELECT event_json FROM telephony_browser_network_events WHERE call_id=?`, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	rejected := false
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, first.SessionToken) || strings.Contains(raw, second.SessionToken) || strings.Contains(raw, "media_url") {
			t.Fatal("credential persisted")
		}
		if strings.Contains(raw, "attachment_rejected") {
			var e audioNetworkEvent
			_ = json.Unmarshal([]byte(raw), &e)
			rejected = e.CurrentSessionID == fresh.SessionID && e.Session.SessionID == old.SessionID
		}
	}
	if !rejected {
		t.Fatal("stale attachment not correlated")
	}
	current, err := app.db().findCall(row.ID)
	if err != nil || isTerminalStatus(current.Status) {
		t.Fatal("telemetry terminated carrier call")
	}
}

func TestSessionDiagnosticSanitization(t *testing.T) {
	event := mediaSessionEvent{Timestamp: "https://DO_NOT_STORE/token", Action: "https://DO_NOT_STORE", Outcome: "failed", Detail: "https://DO_NOT_STORE", RecoveryID: "Bearer DO_NOT_STORE", AttemptID: "attempt-a", SessionID: "session-b"}
	normalized := normalizeBrowserAudioDiagnostics(browserAudioDiagnostics{SessionEvents: []mediaSessionEvent{event}})
	raw, _ := json.Marshal(normalized.SessionEvents)
	if strings.Contains(string(raw), "DO_NOT_STORE") || normalized.SessionEvents[0].AttemptID != "attempt-a" || normalized.SessionEvents[0].SessionID != "session-b" {
		t.Fatal(string(raw))
	}
}

func BenchmarkSocketActivityObservation(b *testing.B) {
	var activity audioSocketActivity
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		activity.observe(ws.OpBinary, nil)
	}
}

func TestRTCSetupFailureIsObservedWithoutRawErrorOrMediaActivation(t *testing.T) {
	signal, peer := net.Pipe()
	events := make(chan audioNetworkEvent, 2)
	done := make(chan error, 1)
	go func() {
		conn, stop, err := connectSoftphoneRTC(signal, signal, softphoneRTCConfig{Enabled: true, NetworkContext: audioNetworkEvent{CallID: "call", ProjectID: "project", ConnectionID: "browser-a", Retention: time.Hour}, CollectNetwork: func(e audioNetworkEvent) { events <- e }})
		if stop != nil {
			stop()
		}
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	if _, _, err := wsutil.ReadServerData(peer); err != nil {
		t.Fatal(err)
	}
	_ = peer.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("setup unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("setup blocked")
	}
	select {
	case e := <-events:
		if e.Event != "softphone.browser.setup_failed" || e.Disconnect == nil || e.Disconnect.ErrorClass != "eof" || e.ConnectionID != "browser-a" {
			t.Fatalf("%+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing setup failure")
	}
}

func TestMalformedObservationalSessionMetadataCannotRejectAttach(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	alice := phoneTestIdentity("alice")
	p, err := app.phonePrincipal(row.ProjectID, alice)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.setPhoneOwner(&row, p, "sales"); err != nil {
		t.Fatal(err)
	}
	if _, err = app.issuePhoneSession(&row, p); err != nil {
		t.Fatal(err)
	}
	// Damaged historical telemetry must not prevent a credential refresh.
	if _, err = app.db().db.Exec(`UPDATE telephony_media_sessions SET diagnostic_json='invalid JSON DO_NOT_STORE' WHERE call_id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	w := phoneTestRequest(app, &alice, "POST", "/softphone/attach/"+row.ID, map[string]any{"media_diagnostics": map[string]any{"recovery_id": 123, "attempt_id": "https://DO_NOT_STORE/token", "initiating_action": "Bearer DO_NOT_STORE"}})
	if w.Code != 200 {
		t.Fatalf("observational metadata rejected call: %d %s", w.Code, w.Body)
	}
	app.audioNetworks.mu.Lock()
	defer app.audioNetworks.mu.Unlock()
	for _, event := range app.audioNetworks.pending {
		raw, _ := json.Marshal(event)
		if strings.Contains(string(raw), "DO_NOT_STORE") {
			t.Fatal("unsafe metadata stored")
		}
	}
}

func TestFailedAuthorizedRecoveryRetainsAttemptWithoutReplacingSession(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	alice := phoneTestIdentity("alice")
	p, err := app.phonePrincipal(row.ProjectID, alice)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.setPhoneOwner(&row, p, "sales"); err != nil {
		t.Fatal(err)
	}
	session, err := app.issuePhoneSession(&row, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.db().db.Exec(`CREATE TRIGGER fail_diagnostic_session BEFORE INSERT ON telephony_media_sessions BEGIN SELECT RAISE(ABORT,'DO_NOT_STORE'); END`); err != nil {
		t.Fatal(err)
	}
	w := phoneTestRequest(app, &alice, "POST", "/softphone/attach/"+row.ID, map[string]any{"media_diagnostics": map[string]any{"recovery_id": "recovery-a", "attempt_id": "attempt-b", "initiating_action": "automatic_retry"}})
	if w.Code != 503 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if reason := app.phoneMediaDenialReason(&row, session.SessionToken); reason != "" {
		t.Fatal("failed telemetry issuance revoked original credential", reason)
	}
	app.audioNetworks.mu.Lock()
	defer app.audioNetworks.mu.Unlock()
	event := app.audioNetworks.pending[len(app.audioNetworks.pending)-1]
	if event.Event != "softphone.media.session_issue_failed" || event.Session.AttemptID != "attempt-b" || event.HTTPStatus != 503 {
		t.Fatalf("%+v", event)
	}
	raw, _ := json.Marshal(event)
	if strings.Contains(string(raw), "DO_NOT_STORE") || strings.Contains(string(raw), session.SessionToken) {
		t.Fatal("raw failure credential persisted")
	}
}

type blockedDiagnosticBody struct {
	reader           io.Reader
	started, release chan struct{}
	once             sync.Once
}

func (b *blockedDiagnosticBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.reader.Read(p)
}
func (*blockedDiagnosticBody) Close() error { return nil }

func TestSessionDiagnosticBodyReadNeverHoldsMediaClaim(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	body := &blockedDiagnosticBody{reader: strings.NewReader(`{}`), started: make(chan struct{}), release: make(chan struct{})}
	r := httptest.NewRequest("POST", "/softphone/attach/"+row.ID, body)
	w := httptest.NewRecorder()
	completed := make(chan struct{})
	go func() { app.handlePhoneSession(w, r, row.ProjectID, "attach", row.ID); close(completed) }()
	<-body.started
	acquired := make(chan struct{})
	go func() { unlock := app.softphones.lockClaim(row.ID); unlock(); close(acquired) }()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		close(body.release)
		<-completed
		t.Fatal("diagnostic body blocked the media claim")
	}
	close(body.release)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("attachment did not finish")
	}
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
