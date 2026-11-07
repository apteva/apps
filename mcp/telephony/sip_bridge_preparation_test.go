package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestSIPBridgePreparationOwnershipAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelled), func(t *testing.T) {
			app, _ := withTelephonyTestContext(t, &answerPlatform{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &sipSession{ctx: ctx, gateway: &sipGateway{app: app}}
			accepted := make(chan net.Conn, 1)
			entered := make(chan struct{})
			allow := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// A synchronous peer callback must be able to take the answer lock.
				if !s.answerMu.TryLock() {
					t.Error("answer lock held over network handshake")
					return
				}
				s.answerMu.Unlock()
				close(entered)
				select {
				case <-allow:
				case <-r.Context().Done():
					return
				}
				conn, _, err := upgradeBuffered(w, r)
				if err == nil {
					accepted <- conn
				}
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			_, port, _ := net.SplitHostPort(u.Host)
			t.Setenv("APTEVA_APP_PORT", port)
			result := make(chan error, 1)
			row := &callRow{ID: "prepared", PeerKind: peerKindHuman, AudioBridgeURL: "pending", CallbackSecret: "secret"}
			go func() { result <- s.prepareBridge(row) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("bridge not prepared")
			}
			if s.preparedBridge != nil {
				t.Fatal("incomplete handshake exposed")
			}
			if cancelled {
				cancel()
			}
			close(allow)
			err := <-result
			if cancelled {
				if err == nil || s.takePreparedBridge() != nil {
					t.Fatal("canceled preparation retained socket", err)
				}
				select {
				case c := <-accepted:
					c.Close()
				default:
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			peer := <-accepted
			defer peer.Close()
			conn := s.takePreparedBridge()
			if conn == nil {
				t.Fatal("prepared socket not retained")
			}
			defer conn.Close()
			if s.takePreparedBridge() != nil {
				t.Fatal("prepared socket transferred twice")
			}
			// Unused prepared connections are closed on session teardown.
			s.preparedMu.Lock()
			s.preparedBridge = conn
			s.preparedMu.Unlock()
			s.closePreparedBridge()
			if s.takePreparedBridge() != nil {
				t.Fatal("cleanup retained connection")
			}
		})
	}
}
func TestSIPBridgePreparationPreservesAIAndIVRSpawnOrder(t *testing.T) {
	s := &sipSession{}
	for _, kind := range []string{peerKindRealtime, ""} {
		if err := s.prepareBridge(&callRow{PeerKind: kind, AudioBridgeURL: "pending"}); err != nil {
			t.Fatal("AI/IVR tried human preparation", err)
		}
	}
}

func TestSIPHumanAnswerPreparesBridgeBeforeResponse(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	connections := make(chan net.Conn, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.session.answerMu.Lock()
		answered := f.session.media != nil
		f.session.answerMu.Unlock()
		if answered {
			t.Error("SIP answered before preparing human bridge")
		}
		conn, _, err := upgradeBuffered(w, r)
		if err == nil {
			connections <- conn
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	t.Setenv("APTEVA_APP_PORT", port)
	f.call.PeerKind = peerKindHuman
	f.call.AudioBridgeURL = "pending"
	if _, err := f.app.db().db.Exec(`UPDATE calls SET peer_kind='human',audio_bridge_url='pending' WHERE id=?`, f.call.ID); err != nil {
		t.Fatal(err)
	}
	f.answer(t)
	peer := <-connections
	defer peer.Close()
	if f.session.preparedBridge == nil {
		t.Fatal("answer lost prepared connection")
	}
	if err := f.gateway.StartMedia(f.call); err != nil {
		t.Fatal(err)
	}
	if err := f.gateway.Answer(f.call); err != nil {
		t.Fatal("idempotent answer", err)
	}
	waitSIPParity(t, func() bool {
		f.session.preparedMu.Lock()
		defer f.session.preparedMu.Unlock()
		return f.session.preparedBridge == nil
	})
	select {
	case c := <-connections:
		c.Close()
		t.Fatal("media redialed instead of transferring preparation")
	default:
	}
}

// An IVR may already have answered SIP before an adviser is selected. That is
// different from an already running human bridge and still needs preparation.
func TestSIPHumanAnswerPreparesBridgeAfterIVR(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	f.answer(t)
	accepted := make(chan net.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := upgradeBuffered(w, r)
		if err == nil {
			accepted <- conn
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	t.Setenv("APTEVA_APP_PORT", port)
	f.call.PeerKind = peerKindHuman
	f.call.AudioBridgeURL = "pending"
	if _, err := f.app.db().db.Exec(`UPDATE calls SET peer_kind='human',audio_bridge_url='pending' WHERE id=?`, f.call.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.gateway.StartMedia(f.call); err != nil {
		t.Fatal(err)
	}
	select {
	case conn := <-accepted:
		defer conn.Close()
	case <-time.After(time.Second):
		t.Fatal("already answered SIP skipped human preparation")
	}
	if !f.session.bridgeStarted.Load() {
		t.Fatal("bridge not activated")
	}
}
