package main

// Connection-boundary diagnostics: fixed size snapshots, no database, URLs or
// raw error text. The existing background collector performs all persistence.
import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/webrtc/v4"
)

type audioDisconnectInfo struct {
	ErrorClass             string  `json:"error_class,omitempty"`
	ErrorDetail            string  `json:"error_detail,omitempty"`
	Transport              string  `json:"transport,omitempty"`
	LastAudioReadAt        string  `json:"last_audio_read_at,omitempty"`
	LastAudioWriteAt       string  `json:"last_audio_write_at,omitempty"`
	BrowserConnectionState string  `json:"browser_connection_state,omitempty"`
	BrowserICEState        string  `json:"browser_ice_state,omitempty"`
	BrowserDTLSState       string  `json:"browser_dtls_state,omitempty"`
	LastReadAt             string  `json:"last_read_at,omitempty"`
	LastWriteAt            string  `json:"last_write_at,omitempty"`
	LastPingAt             string  `json:"last_ping_at,omitempty"`
	LastPongAt             string  `json:"last_pong_at,omitempty"`
	LastBrowserSampleAt    string  `json:"last_browser_sample_at,omitempty"`
	RTTMS                  *int    `json:"rtt_ms,omitempty"`
	PlaybackQueueMS        int     `json:"playback_queue_ms"`
	BrowserBufferedBytes   int     `json:"browser_buffered_bytes"`
	ServerQueuedMS         int     `json:"server_queued_ms"`
	MaxWriteMS             float64 `json:"max_write_ms"`
	AudioContextState      string  `json:"audio_context_state,omitempty"`
	MicrophoneMuted        bool    `json:"microphone_muted"`
	MicrophoneTrackState   string  `json:"microphone_track_state,omitempty"`
	ICEState               string  `json:"ice_state,omitempty"`
	DTLSState              string  `json:"dtls_state,omitempty"`
	RTCState               string  `json:"rtc_state,omitempty"`
}

// Canonical details preserve the underlying condition without serializing
// arbitrary error messages (which can contain token-bearing request URLs).
func audioSocketError(err error) (string, string) {
	var close wsutil.ClosedError
	var protocol ws.ProtocolError
	var op *net.OpError
	switch {
	case err == nil:
		return "", ""
	case errors.As(err, &close):
		return "peer_close", "websocket_close_frame"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "eof", "unexpected_eof"
	case errors.Is(err, io.EOF):
		return "eof", "eof"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset", "tcp_connection_reset"
	case errors.Is(err, syscall.EPIPE):
		return "connection_reset", "broken_pipe"
	case errors.Is(err, net.ErrClosed), errors.Is(err, io.ErrClosedPipe):
		return "local_closure", "socket_closed"
	case errors.As(err, &protocol), errors.Is(err, wsutil.ErrInvalidUTF8), errors.Is(err, wsutil.ErrFrameTooLarge):
		return "protocol_error", "invalid_websocket_frame"
	case errors.As(err, &op) && op.Timeout():
		return "timeout", "socket_deadline_exceeded"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout", "socket_deadline_exceeded"
	}
	return "transport_error", "unclassified_transport_error"
}

type audioSocketActivity struct{ read, write, ping, pong, audioRead, audioWrite atomic.Int64 }

func (s *audioSocketActivity) observe(op ws.OpCode, _ []byte) {
	now := time.Now().UnixNano()
	s.read.Store(now)
	if op == ws.OpBinary {
		s.audioRead.Store(now)
	}
	if op == ws.OpPing {
		s.ping.Store(now)
	}
	if op == ws.OpPong {
		s.pong.Store(now)
	}
}
func audioActivityTime(value int64) string {
	if value == 0 {
		return ""
	}
	return time.Unix(0, value).UTC().Format(time.RFC3339Nano)
}
func (p *websocketWriterPump) disconnectInfo(err error) audioDisconnectInfo {
	class, detail := audioSocketError(err)
	d := audioDisconnectInfo{ErrorClass: class, ErrorDetail: detail, Transport: "websocket",
		LastReadAt: audioActivityTime(p.activity.read.Load()), LastWriteAt: audioActivityTime(p.activity.write.Load()),
		LastPingAt: audioActivityTime(p.activity.ping.Load()), LastPongAt: audioActivityTime(p.activity.pong.Load()),
		LastAudioReadAt: audioActivityTime(p.activity.audioRead.Load()), LastAudioWriteAt: audioActivityTime(p.activity.audioWrite.Load())}
	p.audioMu.Lock()
	d.ServerQueuedMS = p.audioBytes * 1000 / 48000
	d.MaxWriteMS = p.transportStats.MaxWriteMS
	p.audioMu.Unlock()
	if c, ok := p.conn.(*rtcHubConn); ok && c.disconnect != nil {
		signal := c.disconnect.snapshot(err)
		signal.LastAudioReadAt = d.LastAudioReadAt
		signal.LastAudioWriteAt = d.LastAudioWriteAt
		signal.ServerQueuedMS = d.ServerQueuedMS
		d = signal
	}
	return d
}

// Keep the actual signaling cause and pre-cleanup RTC states: the hub's
// internal pipe closure is a consequence, not the source of the disconnect.
type rtcDisconnectTracker struct {
	mu       sync.Mutex
	captured bool
	info     audioDisconnectInfo
	code     int
	writer   *websocketWriterPump
	pc       *webrtc.PeerConnection
}

func (s *rtcDisconnectTracker) capture(err error, origin string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.captured {
		return
	}
	s.captured = true
	s.info = s.current(err)
	s.info.Transport = "webrtc_signaling"
	if origin != "" {
		s.info.ErrorClass = origin
		s.info.ErrorDetail = origin
	}
	var closed wsutil.ClosedError
	if errors.As(err, &closed) {
		s.code = int(closed.Code)
	}
}
func (s *rtcDisconnectTracker) current(err error) audioDisconnectInfo {
	d := s.writer.disconnectInfo(err)
	d.Transport = "webrtc_signaling"
	d.ICEState = s.pc.ICEConnectionState().String()
	d.RTCState = s.pc.ConnectionState().String()
	if senders := s.pc.GetSenders(); len(senders) > 0 && senders[0].Transport() != nil {
		d.DTLSState = senders[0].Transport().State().String()
	}
	return d
}
func (s *rtcDisconnectTracker) snapshot(err error) audioDisconnectInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.captured {
		return s.info
	}
	return s.current(err)
}
func (s *rtcDisconnectTracker) closeCode() int { s.mu.Lock(); defer s.mu.Unlock(); return s.code }
