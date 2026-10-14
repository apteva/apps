package main

import (
	"context"
	"errors"
	"net"
	"net/url"
	"time"
)

// Preparation happens while the caller is still ringing. Never hold the
// answer/claim lock across the loopback/Core handshake: its handler may need
// the same call. The connection is call-scoped and transferred exactly once.
func (s *sipSession) prepareBridge(row *callRow) error {
	if row == nil || (row.PeerKind != peerKindHuman && row.PeerKind != peerKindExternal) || row.AudioBridgeURL == "" {
		return nil // AI/IVR activation retains its existing realtime spawn order.
	}
	s.preparedMu.Lock()
	defer s.preparedMu.Unlock()
	s.answerMu.Lock()
	bridged, ended := s.bridgeStarted.Load(), s.ended.Load()
	s.answerMu.Unlock()
	if ended {
		return errAnswerCallEnded
	}
	if bridged {
		return nil
	} // An idempotent answer must never replace a live peer.
	if s.preparedBridge != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	s.diagnostics.event("sip_bridge_preparation_started", "core", "", 0)
	conn, err := s.gateway.app.dialSIPBridge(ctx, row)
	if err != nil {
		s.diagnostics.event("sip_bridge_preparation_failed", "core", "bridge_connect_failed", 0)
		return err
	}
	if s.ended.Load() || s.ctx.Err() != nil {
		conn.Close()
		return errAnswerCallEnded
	}
	s.preparedBridge = conn
	s.diagnostics.event("sip_bridge_preparation_ready", "core", "", 0)
	return nil
}

func (s *sipSession) takePreparedBridge() net.Conn {
	s.preparedMu.Lock()
	defer s.preparedMu.Unlock()
	conn := s.preparedBridge
	s.preparedBridge = nil
	return conn
}

func (s *sipSession) closePreparedBridge() {
	if conn := s.takePreparedBridge(); conn != nil {
		conn.Close()
	}
}

func (a *App) dialSIPBridge(ctx context.Context, row *callRow) (net.Conn, error) {
	bridgeURL, err := a.mediaBridgeURL(row)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(bridgeURL)
	if err != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
		return nil, errors.New("invalid SIP audio bridge address")
	}
	conn, buffered, _, err := a.correlatedBridgeDialer(row).Dial(ctx, u.String())
	if err != nil {
		return nil, err
	}
	if buffered != nil {
		conn = hijackedConn{Conn: conn, reader: buffered}
	}
	return conn, nil
}
