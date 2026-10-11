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
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const diagnosticTokenHeader = "X-Telephony-Diagnostic-Token"
const diagnosticMaxSessions = 16
const diagnosticMaxActive = 2
const diagnosticMaxEvents = 128
const diagnosticRetention = 10 * time.Minute
const diagnosticMaxMessageBytes = 16 * 1024
const diagnosticMaxPayloadBytes = 4096
const diagnosticMaxMessagesPerSecond = 1000

// Effective shared carrier transport settings, with diagnostic-only bounds
// disclosed separately. No proxy/TLS configuration is inferred from the socket.
type mediaDiagnosticSettings struct {
	Provider                    string `json:"provider"`
	MediaKind                   string `json:"media_kind"`
	DurationSeconds             int64  `json:"duration_seconds"` // zero = no time limit
	WriteDeadlineMS             int64  `json:"write_deadline_ms"`
	ControlWriteDeadlineMS      int64  `json:"control_write_deadline_ms"`
	ProtocolTimeoutMS           int64  `json:"protocol_timeout_ms"`
	PingIntervalMS              int64  `json:"ping_interval_ms"`
	CloseGraceMS                int64  `json:"close_grace_ms"`
	ForcedCloseMS               int64  `json:"forced_close_ms"`
	ReadDeadlineMS              int64  `json:"read_deadline_ms"`
	CarrierMessageLimitBytes    int    `json:"carrier_message_limit_bytes"`
	DiagnosticMessageLimitBytes int    `json:"diagnostic_message_limit_bytes"`
	DiagnosticPayloadLimitBytes int    `json:"diagnostic_payload_limit_bytes"`
	DiagnosticMessagesPerSecond int    `json:"diagnostic_messages_per_second"`
	ControlQueueCapacity        int    `json:"control_queue_capacity"`
	AudioQueueCapacity          int    `json:"audio_queue_capacity"`
	Codec                       string `json:"codec"`
	SampleRate                  int    `json:"sample_rate"`
	FrameDurationMS             int    `json:"frame_duration_ms"`
	BidirectionalMode           string `json:"bidirectional_mode"`
	TargetLegs                  string `json:"target_legs"`
	SendSilenceWhenIdle         bool   `json:"send_silence_when_idle"`
}

func diagnosticSettings(kind string, duration time.Duration) mediaDiagnosticSettings {
	if kind == "" {
		kind = "human"
	}
	peer := peerKindHuman
	if kind == "ai" {
		peer = peerKindRealtime
	}
	p := mediaProfileForCarrier("telnyx")
	return mediaDiagnosticSettings{Provider: "telnyx", MediaKind: kind, DurationSeconds: int64(duration / time.Second),
		WriteDeadlineMS:        carrierMediaWriteTimeout(&callRow{PeerKind: peer}).Milliseconds(),
		ControlWriteDeadlineMS: websocketWriteTimeout.Milliseconds(), ProtocolTimeoutMS: carrierProtocolTimeout.Milliseconds(),
		PingIntervalMS: carrierProtocolPollInterval.Milliseconds(), CloseGraceMS: websocketCloseGracePeriod.Milliseconds(),
		ForcedCloseMS: websocketForcedCloseTimeout.Milliseconds(), CarrierMessageLimitBytes: maxCarrierFrameBytes,
		DiagnosticMessageLimitBytes: diagnosticMaxMessageBytes, DiagnosticPayloadLimitBytes: diagnosticMaxPayloadBytes, DiagnosticMessagesPerSecond: diagnosticMaxMessagesPerSecond,
		ControlQueueCapacity: websocketWriteQueueSize, AudioQueueCapacity: 128, Codec: p.Provider, SampleRate: p.SampleRate,
		FrameDurationMS: 20, BidirectionalMode: "rtp", TargetLegs: "self", SendSilenceWhenIdle: true}
}

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
	Settings        mediaDiagnosticSettings    `json:"settings"`
	CloseTrigger    string                     `json:"close_trigger,omitempty"`
	SocketPeer      string                     `json:"socket_peer,omitempty"`
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
	writer       *websocketWriterPump // guarded by session mutex
	stopReason   string               // first local shutdown trigger, guarded by session mutex
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
	s := &mediaDiagnosticSession{tokenHash: phoneHash(token), connectUntil: now.Add(time.Minute), retainUntil: now.Add(time.Minute + diagnosticRetention), duration: duration,
		report: mediaDiagnosticReport{ID: newCallID(), Project: project, State: "awaiting_connection", Settings: diagnosticSettings("human", duration), Events: []mediaDiagnosticEvent{}}}
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
	if d.stopped || d.sessions[s.report.ID] != s || s.stopTrigger() != "" {
		_ = conn.Close()
		return false
	}
	s.conn = conn
	s.retainUntil = time.Time{} // active sessions never expire
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
			s.requestStop("process_shutdown")
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
	if s.writer != nil {
		r.Transport = s.writer.audioSnapshot().Transport
		r.Disconnect = s.writer.disconnectInfo(nil)
	}
	r.Transport.Events = append([]websocketWriteEvent{}, r.Transport.Events...)
	return r
}
func (s *mediaDiagnosticSession) requestStop(trigger string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopReason == "" {
		s.stopReason = trigger
	}
}
func (s *mediaDiagnosticSession) stopTrigger() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopReason
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
			DurationSeconds int64  `json:"duration_seconds"`
			MediaKind       string `json:"media_kind"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid diagnostic request", 400)
			return
		}
		// Zero/absent means indefinite. Only time.Duration overflow is rejected.
		if input.DurationSeconds < 0 || input.DurationSeconds > int64((1<<63-1)/int64(time.Second)) {
			http.Error(w, "duration_seconds must be nonnegative and representable", 400)
			return
		}
		if input.MediaKind != "" && input.MediaKind != "human" && input.MediaKind != "ai" {
			http.Error(w, "media_kind must be human or ai", 400)
			return
		}
		s, token, err := a.mediaDiagnostics.create(project, time.Duration(input.DurationSeconds)*time.Second, time.Now())
		if err != nil {
			http.Error(w, "diagnostic capacity unavailable", 429)
			return
		}
		s.mu.Lock()
		s.report.Settings = diagnosticSettings(input.MediaKind, s.duration)
		settings := s.report.Settings
		s.mu.Unlock()
		endpoint := strings.Replace(strings.Replace(a.publicInstalledAppURL(), "https://", "wss://", 1), "http://", "ws://", 1) + "/media/diagnostic/echo"
		writeJSON(w, map[string]any{"diagnostic_id": s.report.ID, "token": token, "token_header": diagnosticTokenHeader, "connect_before": s.connectUntil.UTC().Format(time.RFC3339Nano), "duration_seconds": input.DurationSeconds, "settings": settings, "echo_url": endpoint, "report_path": "/media-diagnostics/" + s.report.ID})
		return
	}
	if (r.Method == http.MethodGet || r.Method == http.MethodDelete) && strings.HasPrefix(r.URL.Path, "/media-diagnostics/") {
		id := strings.TrimPrefix(r.URL.Path, "/media-diagnostics/")
		a.mediaDiagnostics.mu.Lock()
		s := a.mediaDiagnostics.sessions[id]
		expired := s != nil && !s.retainUntil.IsZero() && time.Now().After(s.retainUntil)
		a.mediaDiagnostics.mu.Unlock()
		if s == nil || s.report.Project != project || expired {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			a.mediaDiagnostics.mu.Lock()
			s.requestStop("operator_stop")
			s.used = true // invalidate admission for an unconnected probe
			if s.conn != nil {
				_ = s.conn.SetReadDeadline(time.Now())
			} else {
				s.mu.Lock()
				if s.report.EndedAt == "" {
					s.report.State = "finished"
					s.report.EndedAt = audioNetworkTimestamp(time.Now())
					s.report.CloseTrigger = "operator_stop"
					s.report.LocalShutdown = true
				}
				s.mu.Unlock()
			}
			a.mediaDiagnostics.mu.Unlock()
		}
		writeJSON(w, s.snapshot())
		return
	}
	http.Error(w, "method or path not allowed", 405)
}

func validDiagnosticFrame(data []byte, op ws.OpCode) (carrierMediaFrame, bool) {
	var f carrierMediaFrame
	if op != ws.OpText || len(data) > diagnosticMaxMessageBytes || json.Unmarshal(data, &f) != nil {
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
		return f, err == nil && len(pcm) > 0 && len(pcm) <= diagnosticMaxPayloadBytes
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
		s.report.CloseTrigger = "upgrade_error"
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

	s.mu.Lock()
	s.report.State = "connected"
	s.report.StartedAt = audioNetworkTimestamp(started)
	s.report.SocketPeer = conn.RemoteAddr().String()
	s.writer = writer
	s.report.Settings.ControlQueueCapacity = cap(writer.requests)
	s.report.Settings.AudioQueueCapacity = cap(writer.audio)
	s.mu.Unlock()
	done := make(chan struct{})

	var lastRead atomic.Int64
	lastRead.Store(started.UnixNano())
	go watchCarrierProtocol(done, writer, func(now time.Time) bool {
		return now.Sub(time.Unix(0, lastRead.Load())) <= carrierProtocolTimeout
	}, func(trigger string) {
		s.requestStop(trigger)
		_ = conn.SetReadDeadline(time.Now()) // wake reader; shared graceful close remains bounded
	})
	if s.duration > 0 {
		timer := time.AfterFunc(s.duration, func() { s.requestStop("duration_reached"); _ = conn.SetReadDeadline(time.Now()) })
		defer timer.Stop()
	}
	code, reason, local := ws.StatusNormalClosure, "diagnostic finished", true
	var failure error
	trigger := "client_stop"
	defer func() {
		close(done)
		disconnect := writer.disconnectInfo(failure)
		if t := s.stopTrigger(); t != "" {
			trigger = t
		}
		if trigger == "client_stop" || trigger == "duration_reached" || trigger == "process_shutdown" || trigger == "operator_stop" {
			disconnect.ErrorClass = "local_closure"
			disconnect.ErrorDetail = trigger
		}
		if trigger == "socket_write" {
			disconnect.ErrorClass = "transport_error"
			disconnect.ErrorDetail = "carrier_liveness_write_unavailable"
		}
		if trigger == "protocol_timeout" {
			disconnect.ErrorClass = "timeout"
			disconnect.ErrorDetail = "carrier_protocol_liveness"
		}
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
		s.writer = nil
		s.report.CloseTrigger = trigger
		s.addEvent(mediaDiagnosticEvent{At: s.report.EndedAt, Kind: "closed"})
		s.mu.Unlock()
		a.mediaDiagnostics.mu.Lock()
		s.conn = nil
		s.retainUntil = time.Now().Add(diagnosticRetention)
		a.mediaDiagnostics.mu.Unlock()
	}()
	var lastReceive, lastSend, lastSample time.Time
	window := started
	frames := 0
	for {
		data, op, readErr := readWebSocketData(reader, ws.StateServerSide, writer, func(op ws.OpCode, data []byte) {
			lastRead.Store(time.Now().UnixNano())
			if op == ws.OpClose && len(data) >= 2 {
				peerCode, peerReason := ws.ParseCloseFrameData(data)
				if ws.CheckCloseFrameData(peerCode, peerReason) != nil {
					return
				}
				s.mu.Lock()
				s.report.PeerCloseCode = int(peerCode)
				s.mu.Unlock()
			}
		})
		received := time.Now()
		if readErr != nil {
			failure = readErr
			local = false
			if t := s.stopTrigger(); t != "" {
				local = true
				trigger = t
				code = ws.StatusNormalClosure
				switch t {
				case "duration_reached":
					reason = "diagnostic duration reached"
				case "process_shutdown":
					code = ws.StatusGoingAway
					reason = "diagnostic process shutdown"
				case "operator_stop":
					reason = "diagnostic operator stop"
				default:
					code = ws.StatusInternalServerError
					reason = "diagnostic " + t
				}
				return
			}
			trigger = "read_error"
			var closed wsutil.ClosedError
			if errors.As(readErr, &closed) {
				trigger = "peer_close"
				code = closed.Code
				if code == ws.StatusNoStatusRcvd {
					code = ws.StatusNormalClosure
				} // 1005 is observation-only, never send it on the wire.
				s.mu.Lock()
				s.report.PeerCloseCode = int(closed.Code)
				s.mu.Unlock()
				reason = safeDiagnosticCloseReason(strings.ReplaceAll(closed.Reason, r.Header.Get(diagnosticTokenHeader), "REDACTED"))
			} else {
				code = ws.StatusInternalServerError
				reason = "diagnostic read failed"
			}

			return
		}
		f, valid := validDiagnosticFrame(data, op)
		if received.Sub(window) >= time.Second {
			window = received
			frames = 0
		}
		frames++
		if !valid || frames > diagnosticMaxMessagesPerSecond {
			code = ws.StatusPolicyViolation
			reason = "invalid or excessive diagnostic frames"
			trigger = "frame_policy"
			return
		}
		var receiveGap time.Duration
		if f.Event == "media" && !lastReceive.IsZero() {
			receiveGap = received.Sub(lastReceive)
		}
		if f.Event == "media" {
			writer.activity.audioRead.Store(received.UnixNano())
			lastReceive = received
		}
		writeStart := time.Now()
		writeErr := writer.write(op, data, time.Duration(s.report.Settings.WriteDeadlineMS)*time.Millisecond) // Same bounded carrier socket write budget.
		sent := time.Now()
		writeTime := sent.Sub(writeStart)
		var sendGap time.Duration
		if f.Event == "media" && !lastSend.IsZero() {
			sendGap = sent.Sub(lastSend)
		}
		if f.Event == "media" && writeErr == nil {
			writer.activity.audioWrite.Store(sent.UnixNano())
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
			trigger = "write_error"
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
