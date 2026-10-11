package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func diagnosticMedia(seq int) []byte {
	b, _ := json.Marshal(map[string]any{"event": "media", "stream_id": "diagnostic-stream", "sequence_number": seq, "media": map[string]any{"track": "inbound", "chunk": seq, "timestamp": seq * 20, "payload": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, 160))}})
	return b
}
func diagnosticDial(t *testing.T, server *httptest.Server, token string) net.Conn {
	t.Helper()
	d := ws.Dialer{Header: ws.HandshakeHeaderHTTP(http.Header{diagnosticTokenHeader: {token}})}
	c, _, _, err := d.Dial(context.Background(), strings.Replace(server.URL, "http://", "ws://", 1)+"/media/diagnostic/echo")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func diagnosticEcho(t *testing.T, c net.Conn, payload []byte) {
	t.Helper()
	if err := wsutil.WriteClientText(c, payload); err != nil {
		t.Fatal(err)
	}
	b, op, err := wsutil.ReadServerData(c)
	if err != nil || op != ws.OpText || !bytes.Equal(b, payload) {
		t.Fatalf("changed echo %s %v %v", b, op, err)
	}
}
func diagnosticFinished(t *testing.T, s *mediaDiagnosticSession) mediaDiagnosticReport {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r := s.snapshot()
		if r.EndedAt != "" {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("probe did not terminate")
	return mediaDiagnosticReport{}
}
func TestMediaDiagnosticTransportEchoAndNoCalls(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	s, token, err := a.mediaDiagnostics.create("project-a", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(a.handleDiagnosticEcho))
	defer server.Close()
	c := diagnosticDial(t, server, token)
	diagnosticEcho(t, c, []byte(`{"event":"connected","version":"1.0.0"}`))
	diagnosticEcho(t, c, []byte(`{"event":"start","stream_id":"diagnostic-stream","start":{"media_format":{"encoding":"PCMU","sample_rate":8000,"channels":1}}}`))
	for i := 1; i <= 12; i++ {
		if i == 7 {
			time.Sleep(65 * time.Millisecond)
		} else {
			time.Sleep(5 * time.Millisecond)
		}
		diagnosticEcho(t, c, diagnosticMedia(i))
	}
	diagnosticEcho(t, c, []byte(`{"event":"stop","stream_id":"diagnostic-stream"}`))
	_, _, _ = wsutil.ReadServerData(c)
	r := diagnosticFinished(t, s)
	if r.Received != 15 || r.Sent != 15 || r.MediaFrames != 12 || r.MaxReceiveGapMS < 60 || r.CloseCode != 1000 || !r.LocalShutdown || r.Transport.Writes < 15 {
		t.Fatalf("bad evidence: %+v", r)
	}
	found := false
	for _, e := range r.Events {
		if e.ReceiveGapMS >= 60 {
			found = e.ReceivedAt != "" && e.SentAt != "" && e.SourceTimestamp != nil && e.Sequence != nil
		}
	}
	if !found {
		t.Fatal("missing timestamped gap/source correlation")
	}
	var calls, claims int
	if err := a.db().db.QueryRow(`SELECT count(*) FROM calls`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if err := a.db().db.QueryRow(`SELECT count(*) FROM carrier_media_bridges`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || claims != 0 {
		t.Fatal("probe created call state", calls, claims)
	}
	raw, _ := json.Marshal(r)
	if bytes.Contains(raw, []byte(token)) || bytes.Contains(raw, []byte(`"payload":`)) {
		t.Fatal("secret/audio retained")
	}
}
func TestMediaDiagnosticTokenAuthorizationAndBounds(t *testing.T) {
	var d mediaDiagnosticRegistry
	now := time.Now()
	for _, token := range []string{"", strings.Repeat("a", 64)} {
		if _, status := d.claim(token, now); status != 401 {
			t.Fatal(status)
		}
	}
	first, token, _ := d.create("a", time.Minute, now)
	if _, status := d.claim(token, now.Add(time.Minute)); status != 401 {
		t.Fatal("expired token accepted")
	}
	if s, status := d.claim(token, now); status != 0 || s != first {
		t.Fatal("valid token denied", status)
	}
	if _, status := d.claim(token, now); status != 401 {
		t.Fatal("replayed token")
	}
	_, token2, _ := d.create("b", time.Minute, now)
	if _, status := d.claim(token2, now); status != 0 {
		t.Fatal(status)
	}
	_, token3, _ := d.create("a", time.Minute, now)
	if _, status := d.claim(token3, now); status != 429 {
		t.Fatal("upgrade reservations bypassed cap", status)
	}
	for len(d.sessions) < diagnosticMaxSessions {
		if _, _, err := d.create("a", time.Minute, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := d.create("a", time.Minute, now); err == nil {
		t.Fatal("unbounded sessions")
	}
	if _, _, err := d.create("a", time.Minute, now.Add(time.Hour)); err != nil {
		t.Fatal("expired reports not removed", err)
	}
	d.stop()
	if _, _, err := d.create("a", time.Minute, now); err == nil {
		t.Fatal("creation after stop")
	}
}
func TestMediaDiagnosticOperatorProjectIsolation(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	identity := phoneTestIdentity("alice")
	if w := phoneTestRequest(a, &identity, "POST", "/media-diagnostics", map[string]any{}); w.Code != 403 {
		t.Fatal("delegated user could create probe", w.Code)
	}
	if w := phoneTestRequest(a, nil, "POST", "/media-diagnostics", map[string]any{"duration_seconds": -1}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	w := phoneTestRequest(a, nil, "POST", "/media-diagnostics", map[string]any{"duration_seconds": 3})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var result map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	id := result["diagnostic_id"].(string)
	if w := phoneTestRequest(a, nil, "GET", "/media-diagnostics/"+id, nil); w.Code != 200 || strings.Contains(w.Body.String(), result["token"].(string)) {
		t.Fatal(w.Code, w.Body)
	}
	r := httptest.NewRequest("GET", "/media-diagnostics/"+id, nil)
	r.Header.Set("X-Apteva-Project-ID", "other-project")
	rec := httptest.NewRecorder()
	a.handleMediaDiagnostics(rec, r)
	if rec.Code != 403 {
		t.Fatal("cross project read", rec.Code, rec.Body)
	}
	other, _, _ := a.mediaDiagnostics.create("other-project", time.Minute, time.Now())
	if w := phoneTestRequest(a, nil, "GET", "/media-diagnostics/"+other.report.ID, nil); w.Code != 404 {
		t.Fatal("foreign project evidence exposed", w.Code)
	}
}
func TestMediaDiagnosticPeerErrorsDurationAndShutdown(t *testing.T) {
	for _, mode := range []string{"close", "empty_close", "eof", "invalid", "duration", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			a := &App{}
			duration := time.Minute
			if mode == "duration" {
				duration = 80 * time.Millisecond
			}
			s, token, _ := a.mediaDiagnostics.create("a", duration, time.Now())
			server := httptest.NewServer(http.HandlerFunc(a.handleDiagnosticEcho))
			defer server.Close()
			c := diagnosticDial(t, server, token)
			diagnosticEcho(t, c, diagnosticMedia(1))
			switch mode {
			case "close":
				_ = wsutil.WriteClientMessage(c, ws.OpClose, ws.NewCloseFrameBody(ws.StatusGoingAway, "peer left"))
				_, _, _ = wsutil.ReadServerData(c)
			case "empty_close":
				_ = wsutil.WriteClientMessage(c, ws.OpClose, nil)
				_, _, _ = wsutil.ReadServerData(c)
			case "eof":
				_ = c.Close()
			case "invalid":
				_ = wsutil.WriteClientText(c, []byte(`{"event":"media","media":{"payload":"not-base64"}}`))
				_, _, _ = wsutil.ReadServerData(c)
			case "duration":
				_, _, _ = wsutil.ReadServerData(c)
			case "shutdown":
				a.mediaDiagnostics.stop()
			}
			r := diagnosticFinished(t, s)
			switch mode {
			case "close":
				if r.PeerCloseCode != 1001 || r.CloseReason != "peer left" || r.LocalShutdown || r.Disconnect.ErrorClass != "peer_close" {
					t.Fatal(r)
				}
			case "empty_close":
				if r.PeerCloseCode != 1005 || r.CloseCode != 1000 || r.CloseTrigger != "peer_close" || r.Disconnect.ErrorClass != "peer_close" {
					t.Fatal(r)
				}
			case "eof":
				if r.Disconnect.ErrorClass != "eof" || r.PeerCloseCode != 0 {
					t.Fatal(r)
				}
			case "invalid":
				if r.CloseCode != 1008 {
					t.Fatal(r)
				}
			case "duration":
				if r.CloseCode != 1000 || !r.LocalShutdown || r.CloseReason != "diagnostic duration reached" {
					t.Fatal(r)
				}
			case "shutdown":
				if r.Disconnect.ErrorClass != "local_closure" || !r.LocalShutdown {
					t.Fatal(r)
				}
			}
		})
	}
}
func TestMediaDiagnosticConcurrentClaimsAndEventRetention(t *testing.T) {
	var d mediaDiagnosticRegistry
	now := time.Now()
	tokens := []string{}
	for range 8 {
		_, token, _ := d.create("a", time.Minute, now)
		tokens = append(tokens, token)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for _, token := range tokens {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, status := d.claim(token, now)
			if status == 0 {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}(token)
	}
	wg.Wait()
	if admitted != 2 {
		t.Fatal("concurrent cap", admitted)
	}
	s := &mediaDiagnosticSession{}
	for i := range 1000 {
		s.addEvent(mediaDiagnosticEvent{Kind: "gap", WriteMS: float64(i)})
	}
	r := s.snapshot()
	if len(r.Events) != 128 || r.EventsEvicted != 872 || r.Events[0].WriteMS != 872 {
		t.Fatal("unbounded evidence", r)
	}
	r.Events[0].Kind = "changed"
	if s.snapshot().Events[0].Kind == "changed" {
		t.Fatal("aliased snapshot")
	}
	class, detail := audioSocketError(&net.OpError{Op: "read", Err: io.EOF})
	if class != "eof" || detail != "eof" {
		t.Fatal(class, detail)
	}
}

func TestMediaDiagnosticHeaderOnlyAndPipelinedBurst(t *testing.T) {
	a := &App{}
	s, token, _ := a.mediaDiagnostics.create("a", time.Minute, time.Now())
	for _, path := range []string{"/media/diagnostic/echo", "/media/diagnostic/echo?token=" + token} {
		w := httptest.NewRecorder()
		a.handleDiagnosticEcho(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal("unauthorised socket", w.Code)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(a.handleDiagnosticEcho))
	defer server.Close()
	c := diagnosticDial(t, server, token)
	// A catch-up batch is accepted; bounds must not mistake a burst for 1000
	// sequential request/response exchanges. The probe itself never invents one.
	for i := 1; i <= 200; i++ {
		if err := wsutil.WriteClientText(c, diagnosticMedia(i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 200; i++ {
		b, _, err := wsutil.ReadServerData(c)
		if err != nil || !bytes.Equal(b, diagnosticMedia(i)) {
			t.Fatal("burst changed or blocked", i, err)
		}
	}
	_ = wsutil.WriteClientMessage(c, ws.OpClose, ws.NewCloseFrameBody(ws.StatusNormalClosure, token))
	_, _, _ = wsutil.ReadServerData(c)
	report := diagnosticFinished(t, s)
	raw, _ := json.Marshal(report)
	if report.MediaFrames != 200 || report.Sent != 200 || strings.Contains(string(raw), token) || report.CloseReason != "REDACTED" {
		t.Fatal("burst/security evidence", report.MediaFrames, report.Sent, report.CloseReason)
	}
}

type diagnosticWriteFailure struct{ net.Conn }

func (c diagnosticWriteFailure) Write(p []byte) (int, error) {
	if len(p) > 0 && p[0] == 0x81 {
		return 0, &net.OpError{Op: "write", Net: "tcp", Err: diagnosticTimeout{}}
	}
	return c.Conn.Write(p)
}

type diagnosticTimeout struct{}

func (diagnosticTimeout) Error() string   { return "socket deadline exceeded" }
func (diagnosticTimeout) Timeout() bool   { return true }
func (diagnosticTimeout) Temporary() bool { return true }

type diagnosticFailingUpgrade struct{ http.ResponseWriter }

func (w diagnosticFailingUpgrade) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, b, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	return diagnosticWriteFailure{c}, b, nil
}
func TestMediaDiagnosticWriteFailureHasNoFalseSend(t *testing.T) {
	a := &App{}
	s, token, _ := a.mediaDiagnostics.create("a", time.Minute, time.Now())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleDiagnosticEcho(diagnosticFailingUpgrade{w}, r) }))
	defer server.Close()
	c := diagnosticDial(t, server, token)
	if err := wsutil.WriteClientText(c, diagnosticMedia(1)); err != nil {
		t.Fatal(err)
	}
	_, _, _ = wsutil.ReadServerData(c)
	r := diagnosticFinished(t, s)
	if r.Received != 1 || r.Sent != 0 || r.Transport.WriteTimeouts != 1 || r.Disconnect.ErrorClass != "timeout" {
		t.Fatalf("failure recorded as delivery: %+v", r)
	}
	found := false
	for _, e := range r.Events {
		if e.Kind == "write_error" {
			found = e.ReceivedAt != "" && e.WriteFinishedAt != "" && e.SentAt == ""
		}
	}
	if !found {
		t.Fatal("failed write timing missing")
	}
}

func TestMediaDiagnosticRemountInvalidatesOldAdmission(t *testing.T) {
	var d mediaDiagnosticRegistry
	now := time.Now()
	s, token, _ := d.create("old-project", time.Minute, now)
	if _, status := d.claim(token, now); status != 0 {
		t.Fatal(status)
	}
	d.start()
	if _, status := d.claim(token, now); status != 401 {
		t.Fatal("old installation token survived remount")
	}
	local, remote := net.Pipe()
	defer remote.Close()
	if d.bind(s, local) {
		t.Fatal("late upgrade from prior mount admitted")
	}
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := remote.Read(make([]byte, 1)); err == nil {
		t.Fatal("old socket left open")
	}
	_, fresh, _ := d.create("new-project", time.Minute, now)
	if _, status := d.claim(fresh, now); status != 0 {
		t.Fatal("new mount cannot probe", status)
	}
}

func TestMediaDiagnosticUnlimitedSettingsAndOperatorStop(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	for _, body := range []map[string]any{{}, {"duration_seconds": 0}, {"duration_seconds": 3600, "media_kind": "ai"}} {
		w := phoneTestRequest(a, nil, "POST", "/media-diagnostics", body)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var result struct {
			ID       string                  `json:"diagnostic_id"`
			Token    string                  `json:"token"`
			Settings mediaDiagnosticSettings `json:"settings"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		settings := result.Settings
		if settings.ReadDeadlineMS != 0 || settings.ProtocolTimeoutMS != carrierProtocolTimeout.Milliseconds() || settings.PingIntervalMS != carrierProtocolPollInterval.Milliseconds() || settings.ForcedCloseMS != websocketForcedCloseTimeout.Milliseconds() {
			t.Fatal(settings)
		}
		if body["media_kind"] == "ai" {
			if settings.WriteDeadlineMS != 5000 {
				t.Fatal(settings)
			}
		} else if settings.WriteDeadlineMS != 250 || settings.DurationSeconds != 0 {
			t.Fatal(settings)
		}
		a.mediaDiagnostics.mu.Lock()
		s := a.mediaDiagnostics.sessions[result.ID]
		a.mediaDiagnostics.mu.Unlock()
		server := httptest.NewServer(http.HandlerFunc(a.handleDiagnosticEcho))
		c := diagnosticDial(t, server, result.Token)
		diagnosticEcho(t, c, diagnosticMedia(1))
		// Once attached the report lifetime is independent of token admission and duration.
		a.mediaDiagnostics.mu.Lock()
		s.connectUntil = time.Now().Add(-time.Hour)
		if !s.retainUntil.IsZero() {
			t.Fatal("active retention expires", s.retainUntil)
		}
		a.mediaDiagnostics.mu.Unlock()
		w = phoneTestRequest(a, nil, "GET", "/media-diagnostics/"+result.ID, nil)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		live := s.snapshot()
		if live.Transport.Writes == 0 || live.Disconnect.LastReadAt == "" || live.Disconnect.LastAudioReadAt == "" {
			t.Fatal(live)
		}
		w = phoneTestRequest(a, nil, "DELETE", "/media-diagnostics/"+result.ID, nil)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		_, _, _ = wsutil.ReadServerData(c)
		r := diagnosticFinished(t, s)
		if r.CloseTrigger != "operator_stop" || r.CloseCode != 1000 || !r.LocalShutdown || r.Disconnect.ErrorClass != "local_closure" {
			t.Fatal(r)
		}
		_ = c.Close()
		server.Close()
		a.mediaDiagnostics.mu.Lock()
		if time.Until(s.retainUntil) < 9*time.Minute {
			t.Fatal("retention not started at completion")
		}
		a.mediaDiagnostics.mu.Unlock()
	}
	for _, body := range []map[string]any{{"duration_seconds": -1}, {"duration_seconds": int64(9223372037)}, {"duration_seconds": 1.5}, {"media_kind": "bad"}} {
		if w := phoneTestRequest(a, nil, "POST", "/media-diagnostics", body); w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
}
func TestMediaDiagnosticStopInvalidatesPendingToken(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	w := phoneTestRequest(a, nil, "POST", "/media-diagnostics", map[string]any{})
	var result map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	id := result["diagnostic_id"].(string)
	if w := phoneTestRequest(a, nil, "DELETE", "/media-diagnostics/"+id, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, status := a.mediaDiagnostics.claim(result["token"].(string), time.Now()); status != 401 {
		t.Fatal("stopped token accepted", status)
	}
}
func TestMediaDiagnosticSharesCarrierLiveness(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	writer := newWebSocketWriterPump(left, ws.StateServerSide)
	defer writer.Stop()
	defer left.Close()
	failure := ""
	if carrierProtocolTick(writer, false, func(s string) { failure = s }) || failure != "protocol_timeout" {
		t.Fatal(failure)
	}
	if !carrierProtocolTick(writer, true, func(s string) { failure = s }) {
		t.Fatal("healthy socket failed")
	}
	_ = right.SetReadDeadline(time.Now().Add(time.Second))
	frame, err := ws.ReadFrame(right)
	if err != nil || frame.Header.OpCode != ws.OpPing || string(frame.Payload) != "telephony-media" {
		t.Fatal(frame, err)
	}
	b := &carrierBridgeLease{}
	b.lastRead.Store(time.Now().UnixNano())
	if !b.protocolHealthy(time.Now().Add(19*time.Second)) || b.protocolHealthy(time.Now().Add(21*time.Second)) {
		t.Fatal("carrier health window changed")
	}
}

// Actual same-socket liveness failure; the client intentionally does not pong.
// ws.ReadFrame observes server pings without automatically acknowledging them.
func TestMediaDiagnosticProtocolIdleReason(t *testing.T) {
	a := &App{}
	s, token, _ := a.mediaDiagnostics.create("local", 0, time.Now())
	server := httptest.NewServer(http.HandlerFunc(a.handleDiagnosticEcho))
	defer server.Close()
	c := diagnosticDial(t, server, token)
	diagnosticEcho(t, c, diagnosticMedia(1))
	started := time.Now()
	_ = c.SetDeadline(time.Now().Add(28 * time.Second))
	pings := 0
	for {
		frame, err := ws.ReadFrame(c)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Header.OpCode == ws.OpPing {
			pings++
			continue
		}
		if frame.Header.OpCode != ws.OpClose {
			t.Fatal(frame.Header)
		}
		break
	}
	r := diagnosticFinished(t, s)
	if time.Since(started) < 19*time.Second || pings < 3 || r.CloseTrigger != "protocol_timeout" || r.Disconnect.ErrorClass != "timeout" || r.Disconnect.ErrorDetail != "carrier_protocol_liveness" || !r.LocalShutdown || r.Disconnect.LastReadAt == "" || r.Disconnect.LastPingSentAt == "" {
		t.Fatal(pings, r)
	}
}
