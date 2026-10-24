package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// A renderer connection is independent of the carrier session. Complete WebSocket
// messages cross this local boundary; a partially written upstream message is
// never replayed onto another connection. Human/external bridges bypass it.
const agentCoreRecoveryLimit = 3
const agentCoreRecoveryExhausted = "Core connection recovery exhausted"
const agentCoreStableWindow = 30 * time.Second
const agentCoreInputMaxAge = 250 * time.Millisecond

type agentCoreLinkSnapshot struct {
	Reconnects       int                  `json:"reconnects"`
	AmbiguousInputMS int64                `json:"ambiguous_input_ms"`
	InputDiscardedMS int64                `json:"input_discarded_ms"`
	LastCause        string               `json:"last_cause,omitempty"`
	MaxRecoveryMS    float64              `json:"max_recovery_ms"`
	Recovering       bool                 `json:"recovering"`
	Events           []agentCoreLinkEvent `json:"events,omitempty"`
}
type agentCoreLinkEvent struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Code   int    `json:"close_code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func (l *agentCoreLink) event(kind string, code int, reasons ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	reason := ""
	if len(reasons) > 0 {
		reason = limitDiagnosticText(reasons[0], 512)
	}
	l.stats.Events = append(l.stats.Events, agentCoreLinkEvent{At: ringTime(time.Now()), Kind: kind, Code: code, Reason: reason})
	if len(l.stats.Events) > 12 {
		l.stats.Events = append([]agentCoreLinkEvent(nil), l.stats.Events[len(l.stats.Events)-12:]...)
	}
}

type agentLinkMessage struct {
	op   ws.OpCode
	data []byte
	at   time.Time
}
type agentCoreLink struct {
	net.Conn
	ctx          context.Context
	cancel       context.CancelFunc
	peer         net.Conn
	mu           sync.Mutex
	stats        agentCoreLinkSnapshot
	ready        []byte
	speechKnown  bool
	speech       bool
	audio        chan agentLinkMessage
	control      chan agentLinkMessage
	interrupt    func(realtimeBridgeControl)
	recovering   atomic.Bool
	connected    atomic.Bool
	outputFrames atomic.Uint64
}

func (l *agentCoreLink) snapshot() agentCoreLinkSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.stats
	v.Recovering = l.recovering.Load()
	v.Events = append([]agentCoreLinkEvent(nil), l.stats.Events...)
	return v
}
func (l *agentCoreLink) Close() error { l.cancel(); _ = l.peer.Close(); return l.Conn.Close() }
func (l *agentCoreLink) discard(m agentLinkMessage) {
	if m.op == ws.OpBinary {
		l.mu.Lock()
		l.stats.InputDiscardedMS += int64(len(m.data)) * 1000 / 48000
		l.mu.Unlock()
	}
}

type agentLocalRelayError struct{ error }

func retryAgentCore(err error) bool {
	var local agentLocalRelayError
	if errors.As(err, &local) {
		return false
	}
	if err == nil {
		return false
	}
	var closed wsutil.ClosedError
	if errors.As(err, &closed) {
		return closed.Code == 1011 || closed.Code == 1001 || closed.Code == 1006
	}
	var netErr net.Error
	return errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &netErr)
}

func (a *App) resilientAgentBridge(ctx context.Context, row *callRow, conn net.Conn, response *http.Response, session *agentAudioSession) net.Conn {
	if session == nil || !session.continuous() || !session.capabilities.Continuous || session.capabilities.Version != 1 || row.PeerKind != peerKindRealtime {
		return conn
	}
	dial := func(ctx context.Context) (net.Conn, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		current, err := a.db().findCall(row.ID)
		if err != nil || current == nil || isTerminalStatus(current.Status) || current.ProjectID != row.ProjectID || current.AgentID != row.AgentID || current.ThreadID != row.ThreadID || current.PeerKind != peerKindRealtime {
			return nil, errors.New("call no longer active")
		}
		// Renew only Core's single-use token, never the carrier stream or call.
		if globalCtx == nil || globalCtx.PlatformAPI() == nil {
			return nil, errors.New("app context unavailable")
		}
		renewed, err := globalCtx.WithProject(row.ProjectID).PlatformAPI().RenewRealtimeAudioBridge(current.AgentID, current.ThreadID)
		if err == nil && (renewed == nil || renewed.AudioBridgeURL == "") {
			err = errors.New("Core renewal returned no URL")
		}
		address := ""
		if err == nil {
			address = agentBridgeURL(renewed.AudioBridgeURL, current)
		}
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if fresh, e := a.db().findCall(row.ID); e != nil || fresh == nil || isTerminalStatus(fresh.Status) {
			return nil, errors.New("call ended during Core renewal")
		}
		res, err := a.db().db.Exec(`UPDATE calls SET audio_bridge_url=? WHERE id=? AND project_id=? AND agent_id=? AND thread_id=? AND peer_kind=? AND status NOT IN ('completed','failed','no-answer','busy','canceled')`, renewed.AudioBridgeURL, row.ID, row.ProjectID, row.AgentID, row.ThreadID, peerKindRealtime)
		if err != nil {
			return nil, err
		}
		if n, e := res.RowsAffected(); e != nil || n != 1 {
			return nil, errors.New("call ownership changed during Core renewal")
		}
		d, headers := a.agentBridgeDialer(current)
		next, buffered, _, err := d.Dial(ctx, address)
		if err != nil {
			return nil, err
		}
		if headers.Header.Get(agentAudioHeader) == "" {
			_ = next.Close()
			return nil, errors.New("renewed Core renderer contract unavailable")
		}
		var caps agentAudioCapabilities
		if json.Unmarshal([]byte(headers.Header.Get(agentAudioHeader)), &caps) != nil || caps != session.capabilities {
			_ = next.Close()
			return nil, errors.New("renewed Core renderer contract changed")
		}
		if buffered != nil {
			next = hijackedConn{Conn: next, reader: buffered}
		}
		return next, nil
	}
	l := newAgentCoreLink(ctx, conn, dial, func(c realtimeBridgeControl) { session.cancel(c.ItemID, c.Generation) })
	session.mu.Lock()
	session.link = l
	session.mu.Unlock()
	return l
}

func newAgentCoreLink(parent context.Context, initial net.Conn, dial func(context.Context) (net.Conn, error), interrupt func(realtimeBridgeControl)) *agentCoreLink {
	client, peer := net.Pipe()
	ctx, cancel := context.WithCancel(parent)
	l := &agentCoreLink{Conn: client, peer: peer, ctx: ctx, cancel: cancel, audio: make(chan agentLinkMessage, 12), control: make(chan agentLinkMessage, 16), interrupt: interrupt}
	l.connected.Store(true)
	local := &agentRelayWriter{conn: peer, state: ws.StateServerSide}
	go func() { <-ctx.Done(); _ = peer.Close(); _ = initial.Close() }()
	go func() {
		defer cancel()
		for {
			raw, op, err := readAgentRelayData(peer, ws.StateServerSide, local)
			if err != nil {
				return
			}
			m := agentLinkMessage{op: op, data: raw, at: time.Now()}
			if op == ws.OpText {
				c, ok := parseRealtimeBridgeControl(raw)
				if ok && (c.Type == "input.speech_started" || c.Type == "input.speech_stopped") {
					l.mu.Lock()
					l.speechKnown = true
					l.speech = c.Type == "input.speech_started"
					l.mu.Unlock()
				}
				if ok && c.Type == "playback.ready" {
					l.mu.Lock()
					l.ready = append(l.ready[:0], raw...)
					l.mu.Unlock()
				}

				if !l.connected.Load() && ok && c.Type != "playback.overflow" {
					continue
				}
				select {
				case l.control <- m:
				case <-ctx.Done():
					return
				default:
					cancel()
					return
				}
			} else if op == ws.OpBinary {
				// Never allow a catch-up backlog or unbounded memory during recovery.
				if len(raw) > 5760 || len(raw)%2 != 0 {
					cancel()
					return
				}
				for offset := 0; offset < len(raw); offset += 960 {
					part := m
					part.data = raw[offset:min(offset+960, len(raw))]
					select {
					case l.audio <- part:
					default:
						select {
						case old := <-l.audio:
							l.discard(old)
						default:
						}
						select {
						case l.audio <- part:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}
	}()
	go func() {
		defer cancel()
		defer peer.Close()
		remote := initial
		for attempt := 0; ; {
			connectedAt, outputBefore := time.Now(), l.outputFrames.Load()
			err := l.relay(remote, local)
			if time.Since(connectedAt) >= agentCoreStableWindow && l.outputFrames.Load() > outputBefore {
				attempt = 0
			}
			_ = remote.Close()
			if ctx.Err() != nil {
				return
			}
			if !retryAgentCore(err) || attempt >= agentCoreRecoveryLimit {
				code, reason := websocketCloseDetails(err)
				var closed wsutil.ClosedError
				if errors.As(err, &closed) && closed.Code != 1006 && closed.Code != ws.StatusNoStatusRcvd {
					code, reason = closed.Code, closed.Reason
				}
				if retryAgentCore(err) && attempt >= agentCoreRecoveryLimit {
					code, reason = 1011, agentCoreRecoveryExhausted
				}
				_ = local.Write(ws.OpClose, ws.NewCloseFrameBody(code, reason))
				return
			}
			var closed wsutil.ClosedError
			code := 0
			if errors.As(err, &closed) {
				code = int(closed.Code)
			}
			l.event("core_transport_lost", code, transportEvidenceError(err))
			started := time.Now()
			l.mu.Lock()
			l.recovering.Store(true)
			l.connected.Store(false)
			l.stats.LastCause = transportEvidenceError(err)
			l.mu.Unlock()
			var next net.Conn
			for attempt < agentCoreRecoveryLimit && ctx.Err() == nil {
				attempt++
				timer := time.NewTimer(time.Duration(attempt) * 100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				next, err = dial(ctx)
				if err == nil {
					break
				}
			}
			if err != nil || next == nil {
				_ = local.Write(ws.OpClose, ws.NewCloseFrameBody(1011, agentCoreRecoveryExhausted))
				return
			}
			// Old audio/receipts belong to the disconnected bridge. Explicitly count
			// omitted input instead of bursting it at Core or replaying old receipts.
			for {
				select {
				case m := <-l.audio:
					l.discard(m)
				default:
					goto audioDrained
				}
			}
		audioDrained:

			var pendingOverflow []agentLinkMessage
			for {
				select {
				case m := <-l.control:
					c, ok := parseRealtimeBridgeControl(m.data)
					if ok && c.Type == "playback.overflow" {
						pendingOverflow = append(pendingOverflow, m)
					}
				default:
					goto controlsDrained
				}
			}

		controlsDrained:
			l.mu.Lock()
			ready := append([]byte(nil), l.ready...)
			speechKnown, speech := l.speechKnown, l.speech
			l.stats.Reconnects++
			l.stats.MaxRecoveryMS = max(l.stats.MaxRecoveryMS, float64(time.Since(started))/float64(time.Millisecond))
			l.mu.Unlock()
			if len(ready) > 0 {
				w := &agentRelayWriter{conn: next, state: ws.StateClientSide}
				err = w.Write(ws.OpText, ready)

				for _, m := range pendingOverflow {
					if err == nil {
						err = w.Write(m.op, m.data)
					}
				}
				if err == nil && speechKnown {
					kind := "input.speech_stopped"
					if speech {
						kind = "input.speech_started"
					}
					raw, _ := json.Marshal(map[string]string{"type": kind})
					err = w.Write(ws.OpText, raw)
				}
				if err != nil {
					_ = next.Close()
					return
				}
			}
			l.event("core_connection_renewed", 0)
			l.connected.Store(true)
			remote = next
		}
	}()
	return l
}

func (l *agentCoreLink) relay(remote net.Conn, local *agentRelayWriter) error {
	writer := &agentRelayWriter{conn: remote, state: ws.StateClientSide}
	ended := make(chan error, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var metadata []byte
		for {
			raw, op, err := readAgentRelayData(remote, ws.StateClientSide, writer)
			if err != nil {
				ended <- err
				return
			}
			if op == ws.OpText {
				c, ok := parseRealtimeBridgeControl(raw)
				if ok && c.Type == "interrupt" && l.interrupt != nil {
					l.interrupt(c)
				}
				if ok && c.Type == "audio.frame" {
					metadata = raw
					continue
				}
			}
			// Forward an audio metadata/payload pair only once both were received.
			if op == ws.OpBinary && len(metadata) > 0 {
				if err = local.Write(ws.OpText, metadata); err != nil {
					ended <- agentLocalRelayError{err}
					return
				}
				metadata = nil
			}
			if err = local.Write(op, raw); err != nil {
				ended <- agentLocalRelayError{err}
				return
			}
			if op == ws.OpBinary {
				l.outputFrames.Add(1)
			}
			if op == ws.OpBinary && l.recovering.CompareAndSwap(true, false) {
				l.event("core_media_resumed", 0)
			}
		}
	}()
	defer func() { _ = remote.Close(); <-readerDone }()
	for {
		var m agentLinkMessage
		select {
		case <-l.ctx.Done():
			return l.ctx.Err()
		case err := <-ended:
			return err
		case m = <-l.control:
		default:
			select {
			case <-l.ctx.Done():
				return l.ctx.Err()
			case err := <-ended:
				return err
			case m = <-l.control:
			case m = <-l.audio:
			}
		}
		if m.op == ws.OpBinary && time.Since(m.at) > agentCoreInputMaxAge {
			l.discard(m)
			continue
		}
		if err := writer.Write(m.op, m.data); err != nil {
			if m.op == ws.OpBinary {
				l.mu.Lock()
				l.stats.AmbiguousInputMS += int64(len(m.data)) * 1000 / 48000
				l.mu.Unlock()
			}

			_ = remote.Close()
			<-readerDone
			select {
			case received := <-ended:
				var closeErr wsutil.ClosedError
				if errors.As(received, &closeErr) {
					return received
				}
			default:
			}
			return err
		}
	}
}
