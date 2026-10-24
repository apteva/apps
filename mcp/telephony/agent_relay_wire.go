package main

import (
	"io"
	"net"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// This relay already has one producer per media direction. A serialized message
// writer avoids adding another writer goroutine/request allocation per frame.
// Control replies share its lock, keeping headers and payloads atomic.
type agentRelayWriter struct {
	conn  net.Conn
	state ws.State
	mu    sync.Mutex
}

func (w *agentRelayWriter) Write(op ws.OpCode, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		return err
	}
	if w.state == ws.StateClientSide {
		return wsutil.WriteClientMessage(w.conn, op, data)
	}
	return wsutil.WriteServerMessage(w.conn, op, data)
}
func readAgentRelayData(conn net.Conn, state ws.State, writer *agentRelayWriter) ([]byte, ws.OpCode, error) {
	reader := wsutil.Reader{Source: conn, State: state, CheckUTF8: true, MaxFrameSize: maxCarrierFrameBytes}
	control := func(header ws.Header, payload io.Reader) error {
		if header.Length > 125 {
			return ws.ProtocolError("oversized Core control frame")
		}
		data, err := io.ReadAll(io.LimitReader(payload, 126))
		if err != nil {
			return err
		}
		switch header.OpCode {
		case ws.OpPing:
			return writer.Write(ws.OpPong, data)
		case ws.OpPong:
			return nil
		case ws.OpClose:
			if len(data) == 1 {
				return ws.ProtocolError("invalid Core close frame")
			}
			code, reason := ws.StatusNoStatusRcvd, ""
			if len(data) >= 2 {
				code, reason = ws.ParseCloseFrameData(data)
				if err := ws.CheckCloseFrameData(code, reason); err != nil {
					return err
				}
			}
			// The received frame remains authoritative when its reply fails.
			_ = writer.Write(ws.OpClose, data)
			return wsutil.ClosedError{Code: code, Reason: reason}
		}
		return wsutil.ErrNotControlFrame
	}
	reader.OnIntermediate = control
	for {
		header, err := reader.NextFrame()
		if err != nil {
			return nil, 0, err
		}
		if header.OpCode.IsControl() {
			if err := control(header, &reader); err != nil {
				return nil, 0, err
			}
			continue
		}
		if header.OpCode != ws.OpText && header.OpCode != ws.OpBinary {
			return nil, 0, ws.ProtocolError("unexpected Core opcode")
		}
		var data []byte
		if header.Fin {
			data = make([]byte, int(header.Length))
			_, err = io.ReadFull(&reader, data)
		} else {
			data, err = io.ReadAll(io.LimitReader(&reader, maxCarrierFrameBytes+1))
		}
		if len(data) > maxCarrierFrameBytes {
			return nil, 0, wsutil.ErrFrameTooLarge
		}
		if err == nil && header.OpCode == ws.OpText && !utf8.Valid(data) {
			err = ws.ProtocolError("invalid Core text UTF-8")
		}
		return data, header.OpCode, err
	}
}
