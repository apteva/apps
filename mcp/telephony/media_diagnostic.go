package main

// An opt-in transport probe, isolated from call admission, media claims, CRM and
// the audio collectors. Per-frame work only updates a bounded in-memory report.
import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const diagnosticTokenHeader = "X-Telephony-Diagnostic-Token"
const diagnosticMaxSessions = 16
const diagnosticMaxActive = 2
const diagnosticMaxEvents = 128
const diagnosticRetention = 10 * time.Minute

type mediaDiagnosticEvent struct {
	WriteFinishedAt string  `json:"write_finished_at,omitempty"`
	ReceivedAt      string  `json:"received_at,omitempty"`
	SentAt          string  `json:"sent_at,omitempty"`
	At              string  `json:"at"`
	Kind            string  `json:"kind"`
	Sequence        any     `json:"source_sequence,omitempty"`
	SourceTimestamp any     `json:"source_timestamp_ms,omitempty"`
	ReceiveGapMS    float64 `json:"receive_gap_ms"`
	SendGapMS       float64 `json:"send_gap_ms"`
	WriteMS         float64 `json:"write_ms"`
}
type mediaDiagnosticReport struct {
	CloseStartedAt  string                     `json:"close_started_at,omitempty"`
	PeerCloseCode   int                        `json:"peer_close_code,omitempty"`
	ID              string                     `json:"diagnostic_id"`
	Project         string                     `json:"project_id"`
	State           string                     `json:"state"`
	StartedAt       string                     `json:"started_at,omitempty"`
	EndedAt         string                     `json:"ended_at,omitempty"`
	Received        int64                      `json:"received_frames"`
	Sent            int64                      `json:"sent_frames"`
	MediaFrames     int64                      `json:"media_frames"`
	MaxReceiveGapMS float64                    `json:"max_receive_gap_ms"`
	MaxSendGapMS    float64                    `json:"max_send_gap_ms"`
	MaxWriteMS      float64                    `json:"max_write_ms"`
	CloseCode       int                        `json:"close_code,omitempty"`
	CloseReason     string                     `json:"close_reason,omitempty"`
	LocalShutdown   bool                       `json:"local_shutdown"`
	Disconnect      audioDisconnectInfo        `json:"disconnect"`
	Transport       websocketTransportSnapshot `json:"transport"`
	Events          []mediaDiagnosticEvent     `json:"events"`
	EventsEvicted   int64                      `json:"events_evicted"`
}
type mediaDiagnosticSession struct {
	mu           sync.Mutex
	report       mediaDiagnosticReport
	tokenHash    string
	connectUntil time.Time
	retainUntil  time.Time
	duration     time.Duration
	conn         net.Conn // guarded by registry mutex
	used         bool     // guarded by registry mutex
}
type mediaDiagnosticRegistry struct {
	mu       sync.Mutex
	sessions map[string]*mediaDiagnosticSession
	stopped  bool
}

func (d *mediaDiagnosticRegistry) create(project string, duration time.Duration, now time.Time) (*mediaDiagnosticSession, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return nil, "", errors.New("diagnostics stopping")
	}
	if d.sessions == nil {
		d.sessions = make(map[string]*mediaDiagnosticSession)
	}
	for id, s := range d.sessions {
		if s.conn == nil && now.After(s.retainUntil) {
			delete(d.sessions, id)
		}
	}
	if len(d.sessions) >= diagnosticMaxSessions {
		return nil, "", errors.New("diagnostic report capacity reached; retry after retention")
	}
	token := newSecret()
	s := &mediaDiagnosticSession{tokenHash: phoneHash(token), connectUntil: now.Add(time.Minute), retainUntil: now.Add(time.Minute + duration + diagnosticRetention), duration: duration,
		report: mediaDiagnosticReport{ID: newCallID(), Project: project, State: "awaiting_connection", Events: []mediaDiagnosticEvent{}}}
	d.sessions[s.report.ID] = s
	return s, token, nil
}
func (d *mediaDiagnosticRegistry) claim(token string, now time.Time) (*mediaDiagnosticSession, int) {
	if len(token) != 64 {
		return nil, http.StatusUnauthorized
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return nil, http.StatusServiceUnavailable
	}
	active := 0
	for _, s := range d.sessions {
		if s.used && s.conn != nil {
			active++
		}
	}
	for _, s := range d.sessions {
		if secureEqual(s.tokenHash, phoneHash(token)) && !s.used && now.Before(s.connectUntil) {
			// Count reservations too: simultaneous upgrades must not bypass the ceiling.
			for _, pending := range d.sessions {
				if pending.used && pending.conn == nil {
					pending.mu.Lock()
					if pending.report.State == "connecting" {
						active++
					}
					pending.mu.Unlock()
				}
			}
			if active >= diagnosticMaxActive {
				return nil, http.StatusTooManyRequests
			}
			s.used = true
			s.mu.Lock()
			s.report.State = "connecting"
			s.mu.Unlock()
			return s, 0
		}
	}
	return nil, http.StatusUnauthorized
}
func (d *mediaDiagnosticRegistry) bind(s *mediaDiagnosticSession, conn net.Conn) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || d.sessions[s.report.ID] != s {
		_ = conn.Close()
		return false
	}
	s.conn = conn
	return true
}
func (d *mediaDiagnosticRegistry) start() {
	d.stop()
	d.mu.Lock()
	d.sessions = nil
	d.stopped = false
	d.mu.Unlock()
}
func (d *mediaDiagnosticRegistry) stop() {
	d.mu.Lock()
	d.stopped = true
	sockets := []net.Conn{}
	for _, s := range d.sessions {
		if s.conn != nil {
			sockets = append(sockets, s.conn)
		}
	}
	d.mu.Unlock()
	for _, c := range sockets {
		_ = c.Close()
	}
}
func (s *mediaDiagnosticSession) snapshot() mediaDiagnosticReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.report
	r.Events = append([]mediaDiagnosticEvent{}, r.Events...)
	r.Transport.Events = append([]websocketWriteEvent{}, r.Transport.Events...)
	return r
}
func (s *mediaDiagnosticSession) addEvent(e mediaDiagnosticEvent) {
	// Called with the per-probe mutex; no live call uses this mutex.
	if len(s.report.Events) == diagnosticMaxEvents {
		copy(s.report.Events, s.report.Events[1:])
		s.report.Events = s.report.Events[:diagnosticMaxEvents-1]
		s.report.EventsEvicted++
	}
	s.report.Events = append(s.report.Events, e)
}

// This route is SDK-authenticated; delegated call users cannot mint probes or
// read project-wide probe evidence. Tokens only authorise the echo route.
func (a *App) handleMediaDiagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, delegated := phoneRequestIdentity(r); delegated || phoneUserFrom(r) != nil {
		http.Error(w, "operator credentials required", 403)
		return
	}
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, "project not allowed", 403)
		return
	}
	if r.URL.Path == "/media-diagnostics" && r.Method == http.MethodPost {
		var input struct {
			DurationSeconds int `json:"duration_seconds"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid diagnostic request", 400)
			return
		}
		if input.DurationSeconds == 0 {
			input.DurationSeconds = 60
		}
		if input.DurationSeconds < 1 || input.DurationSeconds > 300 {
			http.Error(w, "duration_seconds must be between 1 and 300", 400)
			return
		}
		s, token, err := a.mediaDiagnostics.create(project, time.Duration(input.DurationSeconds)*time.Second, time.Now())
		if err != nil {
			http.Error(w, "diagnostic capacity unavailable", 429)
			return
		}
		endpoint := strings.Replace(strings.Replace(a.publicInstalledAppURL(), "https://", "wss://", 1), "http://", "ws://", 1) + "/media/diagnostic/echo"
		writeJSON(w, map[string]any{"diagnostic_id": s.report.ID, "token": token, "token_header": diagnosticTokenHeader, "connect_before": s.connectUntil.UTC().Format(time.RFC3339Nano), "duration_seconds": input.DurationSeconds, "echo_url": endpoint, "report_path": "/media-diagnostics/" + s.report.ID})
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/media-diagnostics/") {
		id := strings.TrimPrefix(r.URL.Path, "/media-diagnostics/")
		a.mediaDiagnostics.mu.Lock()
		s := a.mediaDiagnostics.sessions[id]
		a.mediaDiagnostics.mu.Unlock()
		if s == nil || s.report.Project != project || time.Now().After(s.retainUntil) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, s.snapshot())
		return
	}
	http.Error(w, "method or path not allowed", 405)
}

func validDiagnosticFrame(data []byte, op ws.OpCode) (carrierMediaFrame, bool) {
	var f carrierMediaFrame
	if op != ws.OpText || len(data) > 16*1024 || json.Unmarshal(data, &f) != nil {
		return f, false
	}
	switch f.Event {
	case "connected", "start", "stop":
		return f, true
	case "media":
		if f.Media == nil {
			return f, false
		}
		pcm, err := base64.StdEncoding.DecodeString(f.Media.Payload)
		return f, err == nil && len(pcm) > 0 && len(pcm) <= 4096
	}
	return f, false
}
func safeDiagnosticCloseReason(reason string) string {
	lower := strings.ToLower(reason)
	for _, needle := range []string{"://", "token", "secret", "bearer", "authorization"} {
		if strings.Contains(lower, needle) {
			return "peer reason redacted"
		}
	}
	return limitDiagnosticText(reason, 128)
}
func (a *App) handleDiagnosticEcho(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/media/diagnostic/echo" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	s, status := a.mediaDiagnostics.claim(r.Header.Get(diagnosticTokenHeader), time.Now())
	if s == nil {
		http.Error(w, "diagnostic unavailable", status)
		return
	}
	conn, reader, err := upgradeBuffered(w, r)
	if err != nil {
		s.mu.Lock()
		s.report.State = "upgrade_failed"
		s.report.EndedAt = audioNetworkTimestamp(time.Now())
		class, detail := audioSocketError(err)
		s.report.Disconnect = audioDisconnectInfo{ErrorClass: class, ErrorDetail: detail}
		s.mu.Unlock()
		return
	}
	if !a.mediaDiagnostics.bind(s, conn) {
		s.mu.Lock()
		s.report.State = "stopped"
		s.report.EndedAt = audioNetworkTimestamp(time.Now())
		s.mu.Unlock()
		return
	}
	writer := newWebSocketWriterPump(conn, ws.StateServerSide)
	writer.setDiagnosticID(s.report.ID)
	closer := newGracefulWebSocket(conn, writer)
	started := time.Now()
	end := started.Add(s.duration)
	s.mu.Lock()
	s.report.State = "connected"
	s.report.StartedAt = audioNetworkTimestamp(started)
	s.mu.Unlock()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if !writer.queueControlFrame(ws.OpPing, []byte("telephony-media")) {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	code, reason, local := ws.StatusNormalClosure, "diagnostic finished", true
	var failure error
	defer func() {
		disconnect := writer.disconnectInfo(failure)
		s.mu.Lock()
		s.report.CloseStartedAt = audioNetworkTimestamp(time.Now())
		s.addEvent(mediaDiagnosticEvent{At: s.report.CloseStartedAt, Kind: "closing"})
		s.mu.Unlock()
		closer.Close(code, reason)
		transport := writer.audioSnapshot().Transport
		s.mu.Lock()
		s.report.State = "finished"
		s.report.EndedAt = audioNetworkTimestamp(time.Now())
		s.report.CloseCode = int(code)
		s.report.CloseReason = reason
		s.report.LocalShutdown = local
		s.report.Disconnect = disconnect
		s.report.Transport = transport
		s.addEvent(mediaDiagnosticEvent{At: s.report.EndedAt, Kind: "closed"})
		s.mu.Unlock()
		a.mediaDiagnostics.mu.Lock()
		s.conn = nil
		a.mediaDiagnostics.mu.Unlock()
	}()
	var lastReceive, lastSend, lastSample time.Time
	window := started
	frames := 0
	for {
		now := time.Now()
		if !now.Before(end) {
			reason = "diagnostic duration reached"
			return
		}
		_ = conn.SetReadDeadline(minTime(end, now.Add(20*time.Second)))
		data, op, readErr := readWebSocketData(reader, ws.StateServerSide, writer, func(_ ws.OpCode, _ []byte) { _ = conn.SetReadDeadline(minTime(end, time.Now().Add(20*time.Second))) })
		received := time.Now()
		if readErr != nil {
			failure = readErr
			local = false
			a.mediaDiagnostics.mu.Lock()
			stopped := a.mediaDiagnostics.stopped
			a.mediaDiagnostics.mu.Unlock()
			if stopped {
				local = true
				code = ws.StatusGoingAway
				reason = "diagnostic process shutdown"
				return
			}
			var closed wsutil.ClosedError
			if errors.As(readErr, &closed) {
				code = closed.Code
				s.mu.Lock()
				s.report.PeerCloseCode = int(closed.Code)
				s.mu.Unlock()
				reason = safeDiagnosticCloseReason(strings.ReplaceAll(closed.Reason, r.Header.Get(diagnosticTokenHeader), "REDACTED"))
			} else {
				code = ws.StatusInternalServerError
				reason = "diagnostic read failed"
			}
			if !received.Before(end) {
				local = true
				code = ws.StatusNormalClosure
				reason = "diagnostic duration reached"
			}
			return
		}
		f, valid := validDiagnosticFrame(data, op)
		if received.Sub(window) >= time.Second {
			window = received
			frames = 0
		}
		frames++
		if !valid || frames > 1000 {
			code = ws.StatusPolicyViolation
			reason = "invalid or excessive diagnostic frames"
			return
		}
		var receiveGap time.Duration
		if f.Event == "media" && !lastReceive.IsZero() {
			receiveGap = received.Sub(lastReceive)
		}
		if f.Event == "media" {
			lastReceive = received
		}
		writeStart := time.Now()
		writeErr := writer.write(op, data, liveAudioWriteTimeout) // Same bounded carrier socket write budget.
		sent := time.Now()
		writeTime := sent.Sub(writeStart)
		var sendGap time.Duration
		if f.Event == "media" && !lastSend.IsZero() {
			sendGap = sent.Sub(lastSend)
		}
		if f.Event == "media" && writeErr == nil {
			lastSend = sent
		}
		s.mu.Lock()
		s.report.Received++
		if writeErr == nil {
			s.report.Sent++
		}
		if f.Event == "media" {
			s.report.MediaFrames++
		}
		s.report.MaxReceiveGapMS = max(s.report.MaxReceiveGapMS, float64(receiveGap)/float64(time.Millisecond))
		s.report.MaxSendGapMS = max(s.report.MaxSendGapMS, float64(sendGap)/float64(time.Millisecond))
		s.report.MaxWriteMS = max(s.report.MaxWriteMS, float64(writeTime)/float64(time.Millisecond))
		if (f.Event == "media" && s.report.MediaFrames == 1) || received.Sub(lastSample) >= time.Second || receiveGap > 40*time.Millisecond || writeTime > 20*time.Millisecond || writeErr != nil {
			e := mediaDiagnosticEvent{At: audioNetworkTimestamp(received), ReceivedAt: audioNetworkTimestamp(received), WriteFinishedAt: audioNetworkTimestamp(sent), Kind: "exchange", Sequence: firstDiagnosticSequence(f), ReceiveGapMS: float64(receiveGap) / float64(time.Millisecond), SendGapMS: float64(sendGap) / float64(time.Millisecond), WriteMS: float64(writeTime) / float64(time.Millisecond)}
			if writeErr == nil {
				e.SentAt = audioNetworkTimestamp(sent)
			} else {
				e.Kind = "write_error"
			}
			if f.Media != nil {
				if value, ok := mediaNumber(f.Media.Timestamp); ok {
					e.SourceTimestamp = value
				}
			}
			s.addEvent(e)
			lastSample = received
		}
		s.mu.Unlock()
		if writeErr != nil {
			failure = writeErr
			code = ws.StatusInternalServerError
			reason = "diagnostic write failed"
			return
		}
		if f.Event == "stop" {
			return
		}
	}
}
func firstDiagnosticSequence(f carrierMediaFrame) any {
	value := f.SequenceNumberSnake
	if value == nil {
		value = f.SequenceNumber
	}
	if n, ok := mediaNumber(value); ok {
		return n
	}
	return nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
