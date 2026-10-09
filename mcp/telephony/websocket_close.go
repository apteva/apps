package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const websocketCloseGracePeriod = 200 * time.Millisecond
const websocketWriteTimeout = 5 * time.Second

type mediaCloseLeg string

const (
	mediaCloseLegCarrier    mediaCloseLeg = "carrier"
	mediaCloseLegCore       mediaCloseLeg = "core"
	mediaCloseLegRequest    mediaCloseLeg = "request_context"
	mediaCloseLegLocalError mediaCloseLeg = "local_error"
	websocketWriteQueueSize               = 64
	maxControlFramePayload                = 125
)

type websocketWriteRequest struct {
	op       ws.OpCode
	payload  []byte
	timeout  time.Duration
	complete chan error
	enqueued time.Time
	pcmBytes int
	whisper  bool
	valid    func() bool
}

// Count the entire frame, including its header. Once any bytes enter a TCP
// WebSocket, abandoning that frame would corrupt the next frame's boundary.
type websocketCountingWriter struct {
	io.Writer
	bytes int
}

func (w *websocketCountingWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	w.bytes += n
	return n, err
}

// websocketWriterPump is the sole writer for a WebSocket connection. Data,
// control, and close frames all pass through the same queue.
type websocketWriterPump struct {
	whisperDropped atomic.Int64
	whisperSent    atomic.Int64
	whisperMu      sync.Mutex
	whisper        chan websocketWriteRequest
	audioDropped   atomic.Int64
	audioMu        sync.Mutex
	audioBytes     int
	audioStats     liveAudioQueueSnapshot
	conn           net.Conn
	state          ws.State
	requests       chan websocketWriteRequest
	audio          chan websocketWriteRequest
	stop           chan struct{}
	done           chan struct{}
	stopOnce       sync.Once

	enqueueMu sync.Mutex
	stateMu   sync.Mutex
	err       error
	closeSent bool
}

type gracefulWebSocket struct {
	conn   net.Conn
	writer *websocketWriterPump
	once   sync.Once
}

type websocketCloseState struct {
	mu     sync.Mutex
	set    bool
	leg    mediaCloseLeg
	code   ws.StatusCode
	reason string
}

func (s *websocketCloseState) Set(code ws.StatusCode, reason string) {
	s.SetLeg(mediaCloseLegLocalError, code, reason)
}

func (s *websocketCloseState) SetLeg(leg mediaCloseLeg, code ws.StatusCode, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set {
		return
	}
	s.set = true
	s.leg = leg
	s.code = code
	s.reason = reason
}

func (s *websocketCloseState) Details() (ws.StatusCode, string) {
	_, code, reason := s.Cause()
	return code, reason
}

func (s *websocketCloseState) Cause() (mediaCloseLeg, ws.StatusCode, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set {
		return mediaCloseLegLocalError, ws.StatusGoingAway, "media bridge shutting down"
	}
	return s.leg, s.code, s.reason
}

func websocketCloseDetails(err error) (ws.StatusCode, string) {
	var closed wsutil.ClosedError
	if errors.As(err, &closed) {
		if closed.Code == ws.StatusNormalClosure || closed.Code == ws.StatusGoingAway {
			return closed.Code, closed.Reason
		}
	}
	if err == nil {
		return ws.StatusNormalClosure, "call media ended normally"
	}
	return ws.StatusInternalServerError, "media bridge transport error"
}

func newWebSocketWriterPump(conn net.Conn, state ws.State) *websocketWriterPump {
	pump := &websocketWriterPump{
		conn:     conn,
		state:    state,
		requests: make(chan websocketWriteRequest, websocketWriteQueueSize),
		audio:    make(chan websocketWriteRequest, 128),
		whisper:  make(chan websocketWriteRequest, 3),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go pump.run()
	return pump
}

func (p *websocketWriterPump) run() {
	defer close(p.done)
	for {
		var request websocketWriteRequest
		// Controls take priority over queued audio, but cannot interrupt an active write.
		select {
		case <-p.stop:
			return
		case request = <-p.requests:
		default:
			// Caller playback wins over optional coaching when both are queued.
			select {
			case request = <-p.audio:
			default:
				select {
				case <-p.stop:
					return
				case request = <-p.requests:
				case request = <-p.audio:
				case request = <-p.whisper:
				}
			}
		}
		if request.whisper && (time.Since(request.enqueued) >= coachAudioMaxAge || request.valid == nil || !request.valid()) {
			p.whisperDropped.Add(1)
			continue
		}
		if request.pcmBytes > 0 {
			p.audioMu.Lock()
			p.audioBytes -= request.pcmBytes
			age := time.Since(request.enqueued)
			p.audioStats.MaxResidenceMS = max(p.audioStats.MaxResidenceMS, age.Milliseconds())
			sourceRemaining := liveAudioMaxAge
			if sourceAudioHeader(request.payload) == sourceAudioHeaderBytes {
				mapped := math.Float64frombits(binary.LittleEndian.Uint64(request.payload[48:]))
				if !math.IsNaN(mapped) && !math.IsInf(mapped, 0) {
					sourceRemaining = time.Duration((liveSourceBudgetMS - (mediaClockMS() - mapped)) * float64(time.Millisecond))
				}
			}
			if sourceRemaining <= 0 {
				p.audioStats.SourceStaleBytes += int64(request.pcmBytes)
				p.audioMu.Unlock()
				continue
			}
			if age >= liveAudioMaxAge {
				p.audioStats.StaleBytes += int64(request.pcmBytes)
				p.audioMu.Unlock()
				continue
			}
			p.audioMu.Unlock()
			// Expired source frames are discarded before writing. Do not shorten
			// a socket write to a near-zero source deadline: a partial WebSocket
			// write would force a healthy media leg closed. The next stage checks
			// the SAME source age again after any in-flight transport delay.
			request.timeout = min(request.timeout, liveAudioMaxAge-age)
		}
		if request.pcmBytes > 0 && sourceAudioHeader(request.payload) > 0 {
			binary.LittleEndian.PutUint64(request.payload[16:], math.Float64bits(mediaClockMS()))
			binary.LittleEndian.PutUint64(request.payload[24:], math.Float64bits(float64(time.Since(request.enqueued))/float64(time.Millisecond)))
		}
		started := time.Now()
		timeout := request.timeout
		if timeout <= 0 {
			timeout = websocketWriteTimeout
		}
		err := p.conn.SetWriteDeadline(time.Now().Add(timeout))
		counted := websocketCountingWriter{Writer: p.conn}
		attempted := err == nil
		if err == nil {
			err = wsutil.WriteMessage(&counted, p.state, request.op, request.payload)
		}
		var timeoutError net.Error
		expiredUnsentAudio := attempted && request.pcmBytes > 0 && counted.bytes == 0 && errors.As(err, &timeoutError) && timeoutError.Timeout()
		if request.pcmBytes > 0 {
			p.audioMu.Lock()
			p.audioStats.MaxWriteMS = max(p.audioStats.MaxWriteMS, time.Since(started).Milliseconds())
			if err == nil {
				p.audioStats.SentBytes += int64(request.pcmBytes)
			} else {
				p.audioStats.WriteErrors++
				if expiredUnsentAudio {
					p.audioStats.StaleBytes += int64(request.pcmBytes)
					p.audioStats.WriteTimeoutDrops++
				} else {
					p.audioStats.FailedBytes += int64(request.pcmBytes)
				}
			}
			p.audioStats.LastWriteAt = time.Now().UTC().Format(time.RFC3339Nano)
			p.audioMu.Unlock()
		}
		if expiredUnsentAudio {
			// A scheduling pause or backpressure may consume the live frame's
			// deadline before a single byte is written. Drop that old frame;
			// the next request gets a fresh deadline on the same healthy socket.
			continue
		}
		if request.whisper && err == nil {
			p.whisperSent.Add(1)
		}
		if request.complete != nil {
			request.complete <- err
		}
		if err != nil {
			p.setError(err)
			_ = p.conn.Close()
			return
		}
	}
}

// Live PCM queues are bounded by duration AND residence time. These limits do
// not apply to buffered model speech or control messages.
const liveAudioMaxBytes = 24000 * 2 * 120 / 1000
const liveAudioMaxAge = 250 * time.Millisecond

type liveAudioQueueSnapshot struct {
	QueuedMS             int    `json:"queued_ms"`
	MaxQueuedMS          int    `json:"max_queued_ms"`
	MaxResidenceMS       int64  `json:"max_residence_ms"`
	MaxWriteMS           int64  `json:"max_write_ms"`
	EnqueuedBytes        int64  `json:"enqueued_bytes"`
	WhisperSentFrames    int64  `json:"coaching_sent_frames"`
	WhisperDroppedFrames int64  `json:"coaching_dropped_frames"`
	SentBytes            int64  `json:"sent_bytes"`
	OverflowBytes        int64  `json:"overflow_bytes"`
	SourceStaleBytes     int64  `json:"source_stale_bytes"`
	StaleBytes           int64  `json:"stale_bytes"`
	FlushedBytes         int64  `json:"flushed_bytes"`
	FailedBytes          int64  `json:"failed_bytes"`
	WriteErrors          int64  `json:"write_errors"`
	WriteTimeoutDrops    int64  `json:"write_timeout_drops"`
	LastWriteAt          string `json:"last_write_at,omitempty"`
}

func (p *websocketWriterPump) audioSnapshot() liveAudioQueueSnapshot {
	if p == nil {
		return liveAudioQueueSnapshot{}
	}
	p.audioMu.Lock()
	defer p.audioMu.Unlock()
	s := p.audioStats
	s.WhisperSentFrames = p.whisperSent.Load()
	s.WhisperDroppedFrames = p.whisperDropped.Load()
	s.QueuedMS = p.audioBytes * 1000 / 48000
	return s
}

// QueueAudio never waits for the socket. Framing metadata, when present, is
// excluded from the PCM budget and is preserved when trimming oversized audio.
func (p *websocketWriterPump) QueueAudio(data []byte) { p.queueAudio(data, 0) }

func (p *websocketWriterPump) queueAudio(data []byte, headerBytes int) {
	pcmBytes := (len(data) - headerBytes) &^ 1
	if pcmBytes <= 0 {
		return
	}
	p.audioMu.Lock()
	defer p.audioMu.Unlock()
	select {
	case <-p.done:
		return
	case <-p.stop:
		return
	default:
	}
	p.stateMu.Lock()
	closed := p.closeSent
	p.stateMu.Unlock()
	if closed {
		return
	}
	p.audioStats.EnqueuedBytes += int64(pcmBytes)
	if pcmBytes > liveAudioMaxBytes {
		p.audioStats.OverflowBytes += int64(pcmBytes - liveAudioMaxBytes)
		trimmed := make([]byte, headerBytes+liveAudioMaxBytes)
		copy(trimmed, data[:headerBytes])
		copy(trimmed[headerBytes:], data[headerBytes+pcmBytes-liveAudioMaxBytes:headerBytes+pcmBytes])
		if headerBytes == sourceAudioHeaderBytes {
			advance := float64(pcmBytes-liveAudioMaxBytes) * 1000 / 48000
			for _, offset := range []int{32, 48} {
				value := math.Float64frombits(binary.LittleEndian.Uint64(trimmed[offset:]))
				binary.LittleEndian.PutUint64(trimmed[offset:], math.Float64bits(value+advance))
			}
		}
		data, pcmBytes = trimmed, liveAudioMaxBytes
	}
	for p.audioBytes+pcmBytes > liveAudioMaxBytes || len(p.audio) == cap(p.audio) {
		select {
		case old := <-p.audio:
			p.audioBytes -= old.pcmBytes
			p.audioStats.OverflowBytes += int64(old.pcmBytes)
			p.audioDropped.Add(1)
		default:
			// The writer has dequeued a frame and will account for it once
			// this lock is released. Drop the new frame rather than block.
			p.audioStats.OverflowBytes += int64(pcmBytes)
			return
		}
	}
	p.audio <- websocketWriteRequest{op: ws.OpBinary, payload: append([]byte(nil), data...), timeout: liveAudioMaxAge, enqueued: time.Now(), pcmBytes: pcmBytes}
	p.audioBytes += pcmBytes
	p.audioStats.MaxQueuedMS = max(p.audioStats.MaxQueuedMS, p.audioBytes*1000/48000)
}

func (p *websocketWriterPump) FlushAudio() {
	p.audioMu.Lock()
	defer p.audioMu.Unlock()
	for {
		select {
		case old := <-p.audio:
			p.audioBytes -= old.pcmBytes
			p.audioStats.FlushedBytes += int64(old.pcmBytes)
		default:
			return
		}
	}
}

func (p *websocketWriterPump) setError(err error) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.err == nil {
		p.err = err
	}
}

func (p *websocketWriterPump) terminalError() error {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.err != nil {
		return p.err
	}
	return net.ErrClosed
}

// Control replies and advisory notices use the sole writer but never wait
// for its socket. Receiving caller audio must not wait for a blocked pong.
func (p *websocketWriterPump) queueControl(data []byte) bool {
	return p.queueControlFrame(ws.OpText, data)
}
func (p *websocketWriterPump) queueControlFrame(op ws.OpCode, data []byte) bool {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.closeSent {
		return false
	}
	select {
	case <-p.done:
		return false
	case <-p.stop:
		return false
	default:
	}
	select {
	case p.requests <- websocketWriteRequest{op: op, payload: append([]byte(nil), data...), timeout: liveAudioMaxAge}:
		return true
	default:
		return false
	}
}

func (p *websocketWriterPump) Write(op ws.OpCode, payload []byte) error {
	return p.write(op, payload, websocketWriteTimeout)
}

func (p *websocketWriterPump) write(op ws.OpCode, payload []byte, timeout time.Duration) error {
	if p == nil {
		return net.ErrClosed
	}
	p.enqueueMu.Lock()
	p.stateMu.Lock()
	if p.closeSent {
		p.stateMu.Unlock()
		p.enqueueMu.Unlock()
		if op == ws.OpClose {
			return nil
		}
		return net.ErrClosed
	}
	if op == ws.OpClose {
		p.closeSent = true
	}
	p.stateMu.Unlock()
	request := websocketWriteRequest{
		op:       op,
		payload:  append([]byte(nil), payload...),
		timeout:  timeout,
		complete: make(chan error, 1),
	}
	select {
	case p.requests <- request:
		p.enqueueMu.Unlock()
	case <-p.done:
		p.enqueueMu.Unlock()
		return p.terminalError()
	}
	select {
	case err := <-request.complete:
		return err
	case <-p.done:
		select {
		case err := <-request.complete:
			return err
		default:
			return p.terminalError()
		}
	}
}

func (p *websocketWriterPump) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		close(p.stop)
	})
}

func newGracefulWebSocket(conn net.Conn, writer *websocketWriterPump) *gracefulWebSocket {
	return &gracefulWebSocket{conn: conn, writer: writer}
}

func (c *gracefulWebSocket) Close(code ws.StatusCode, reason string) {
	if c == nil || c.conn == nil {
		return
	}
	c.once.Do(func() {
		reason = strings.ToValidUTF8(reason, "")
		if len(reason) > 120 {
			reason = reason[:120]
		}
		if c.writer != nil {
			_ = c.writer.write(ws.OpClose, ws.NewCloseFrameBody(code, reason), time.Second)
		}
		time.Sleep(websocketCloseGracePeriod)
		_ = c.conn.Close()
		if c.writer != nil {
			c.writer.Stop()
		}
	})
}

func readWebSocketData(conn net.Conn, state ws.State, writer *websocketWriterPump) ([]byte, ws.OpCode, error) {
	reader := wsutil.Reader{
		Source:          conn,
		State:           state,
		CheckUTF8:       true,
		SkipHeaderCheck: false,
		MaxFrameSize:    maxCarrierFrameBytes,
	}
	handleControl := func(header ws.Header, payload io.Reader) error {
		data, err := io.ReadAll(io.LimitReader(payload, maxControlFramePayload+1))
		if err != nil {
			return err
		}
		if len(data) > maxControlFramePayload {
			return closeWebSocketProtocolError(writer, "control frame payload exceeds 125 bytes")
		}
		switch header.OpCode {
		case ws.OpPing:
			if !writer.queueControlFrame(ws.OpPong, data) {
				return errors.New("websocket pong queue unavailable")
			}
			return nil
		case ws.OpPong:
			return nil
		case ws.OpClose:
			if len(data) == 1 {
				return closeWebSocketProtocolError(writer, "close frame payload is one byte")
			}
			code := ws.StatusNoStatusRcvd
			reason := ""
			if len(data) >= 2 {
				code, reason = ws.ParseCloseFrameData(data)
				if err := ws.CheckCloseFrameData(code, reason); err != nil {
					return closeWebSocketProtocolError(writer, err.Error())
				}
			}
			if err := writer.Write(ws.OpClose, data); err != nil {
				return err
			}
			return wsutil.ClosedError{Code: code, Reason: reason}
		default:
			return wsutil.ErrNotControlFrame
		}
	}
	reader.OnIntermediate = handleControl

	for {
		header, err := reader.NextFrame()
		if err != nil {
			return nil, 0, err
		}
		if header.OpCode.IsControl() {
			if err := handleControl(header, &reader); err != nil {
				return nil, 0, err
			}
			continue
		}
		if header.OpCode != ws.OpText && header.OpCode != ws.OpBinary {
			if err := reader.Discard(); err != nil {
				return nil, 0, err
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(&reader, maxCarrierFrameBytes+1))
		if len(data) > maxCarrierFrameBytes {
			_ = writer.Write(ws.OpClose, ws.NewCloseFrameBody(ws.StatusMessageTooBig, "message exceeds limit"))
			return nil, 0, errors.New("websocket message exceeds limit")
		}
		return data, header.OpCode, err
	}
}

func closeWebSocketProtocolError(writer *websocketWriterPump, reason string) error {
	_ = writer.Write(ws.OpClose, ws.NewCloseFrameBody(ws.StatusProtocolError, reason))
	return fmt.Errorf("websocket protocol error: %s", reason)
}

// Coaching has its own small, lower-priority queue; it cannot evict caller PCM.
func (p *websocketWriterPump) queueWhisper(data []byte, valid func() bool) bool {
	if rtc, ok := p.conn.(*rtcHubConn); ok {
		return rtc.queueWhisper(data, valid)
	}
	p.whisperMu.Lock()
	defer p.whisperMu.Unlock()
	select {
	case <-p.done:
		return false
	case <-p.stop:
		return false
	default:
	}
	request := websocketWriteRequest{op: ws.OpBinary, payload: append([]byte(nil), data...), enqueued: time.Now(), timeout: liveAudioMaxAge, whisper: true, valid: valid}
	select {
	case p.whisper <- request:
		return true
	default:
	}
	select {
	case <-p.whisper:
		p.whisperDropped.Add(1)
	default:
	}
	select {
	case p.whisper <- request:
		return true
	default:
		p.whisperDropped.Add(1)
		return false
	}
}
func (p *websocketWriterPump) clearWhisper() {
	p.whisperMu.Lock()
	defer p.whisperMu.Unlock()
	for {
		select {
		case <-p.whisper:
			p.whisperDropped.Add(1)
		default:
			return
		}
	}
}
