package main

import (
	"errors"
	"fmt"
	"io"
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
}

// websocketWriterPump is the sole writer for a WebSocket connection. Data,
// control, and close frames all pass through the same queue.
type websocketWriterPump struct {
	audioDropped atomic.Int64
	conn         net.Conn
	state        ws.State
	requests     chan websocketWriteRequest
	audio        chan websocketWriteRequest
	stop         chan struct{}
	done         chan struct{}
	stopOnce     sync.Once

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
		audio:    make(chan websocketWriteRequest, 6),
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
			select {
			case <-p.stop:
				return
			case request = <-p.requests:
			case request = <-p.audio:
			}
		}
		timeout := request.timeout
		if timeout <= 0 {
			timeout = websocketWriteTimeout
		}
		err := p.conn.SetWriteDeadline(time.Now().Add(timeout))
		if err == nil {
			err = wsutil.WriteMessage(p.conn, p.state, request.op, request.payload)
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

// QueueAudio never blocks a carrier read loop. Keep at most 120ms of speech;
// discard the oldest frame on overload and close stalled sockets after 250ms.
func (p *websocketWriterPump) QueueAudio(data []byte) {
	request := websocketWriteRequest{op: ws.OpBinary, payload: append([]byte(nil), data...), timeout: 250 * time.Millisecond}
	select {
	case <-p.done:
		return
	default:
	}
	select {
	case p.audio <- request:
		return
	default:
	}
	select {
	case <-p.audio:
		p.audioDropped.Add(1)
	default:
	}
	select {
	case p.audio <- request:
	default:
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
			return writer.Write(ws.OpPong, data)
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
