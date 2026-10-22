package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// Only loopback is permitted. This fixture models the credential contract, not
// the deployed TURN app's firewall/DNS/certificate configuration.
func localTURNTestRelay(t *testing.T, mode string) (string, *tls.Config) {
	t.Helper()
	generator := &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"}
	config := turn.ServerConfig{Realm: "telephony-local", AuthHandler: func(a *turn.RequestAttributes) (string, []byte, bool) {
		if a.Username != "temporary-v1" && a.Username != "temporary-v2" {
			return "", nil, false
		}
		return a.Username, turn.GenerateAuthKey(a.Username, "telephony-local", "local-test-password"), true
	}}
	var endpoint string
	var clientTLS *tls.Config
	permission := func(_ net.Addr, ip net.IP) bool { return ip.IsLoopback() }
	if mode == "udp" {
		conn, e := net.ListenPacket("udp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		endpoint = "turn:" + conn.LocalAddr().String() + "?transport=udp"
		config.PacketConnConfigs = []turn.PacketConnConfig{{PacketConn: conn, RelayAddressGenerator: generator, PermissionHandler: permission}}
	} else {
		conn, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		endpoint = "turn:" + conn.Addr().String() + "?transport=tcp"
		if mode == "tls" {
			key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if e != nil {
				t.Fatal(e)
			}
			cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "loopback TURN fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
			if e != nil {
				t.Fatal(e)
			}
			conn = tls.NewListener(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
			root, e := x509.ParseCertificate(der)
			if e != nil {
				t.Fatal(e)
			}
			roots := x509.NewCertPool()
			roots.AddCert(root)
			clientTLS = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "127.0.0.1"}
			endpoint = "turns:" + conn.Addr().String() + "?transport=tcp"
		}
		config.ListenerConfigs = []turn.ListenerConfig{{Listener: conn, RelayAddressGenerator: generator, PermissionHandler: permission}}
	}
	server, e := turn.NewServer(config)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = server.Close() })
	return endpoint, clientTLS
}

func TestTURNRelayMediaAndCredentialRotation(t *testing.T) {
	for _, mode := range []string{"udp", "tcp"} {
		t.Run(mode, func(t *testing.T) {
			endpoint, _ := localTURNTestRelay(t, mode)
			app, ctx, platform := turnTestFixture(t)
			row := insertSoftphoneCall(t, app, "in-progress")
			platform.result = rtcTURNCredentials{ICEServers: []webrtc.ICEServer{{URLs: []string{endpoint}, Username: "temporary-v1", Credential: "local-test-password"}}, ExpiresAt: time.Now().Add(time.Hour).Unix(), TTLSeconds: 3600}
			saveTURNTestPolicy(t, app, ctx, nil, phoneTURNPolicy{Source: "app", Transport: mode, ICEPolicy: "relay"})
			server := softphoneTestServer(t, app)
			defer server.Close()
			carrier := dialWS(t, server.URL+"/peer/"+row.ID+"/cb-secret")
			browser := rtcTestConnect(t, server.URL+"/softphone/media/"+row.ID+"/peer-secret?transport=webrtc")
			before := app.softphones.lookup(row.ID).browserWriter()
			sender := browser.pc.GetSenders()[0]
			assertRelay := func() {
				t.Helper()
				pair, e := sender.Transport().ICETransport().GetSelectedCandidatePair()
				if e != nil || pair == nil || pair.Local.Typ != webrtc.ICECandidateTypeRelay {
					t.Fatal("forced relay was bypassed", pair, e)
				}
			}
			assertRelay()
			checkAudio := func() {
				t.Helper()
				for range 8 {
					if e := browser.mic.WriteSample(media.Sample{Data: []byte{0xf8, 0xff, 0xfe}, Duration: 20 * time.Millisecond}); e != nil {
						t.Fatal(e)
					}
					if e := wsutil.WriteClientBinary(carrier, make([]byte, 960)); e != nil {
						t.Fatal(e)
					}
					time.Sleep(20 * time.Millisecond)
				}
				if data := readBinaryWithin(t, carrier, 2*time.Second); len(data) < 960 {
					t.Fatal("microphone did not cross relay")
				}
				select {
				case <-browser.received:
				case <-time.After(2 * time.Second):
					t.Fatal("playback did not cross relay")
				}
			}
			checkAudio()
			platform.result.ICEServers[0].Username = "temporary-v2"
			if e := wsutil.WriteClientText(browser.socket, []byte(`{"type":"webrtc.ice_refresh"}`)); e != nil {
				t.Fatal(e)
			}
			browser.socket.SetReadDeadline(time.Now().Add(5 * time.Second))
			var refresh struct {
				Type     string             `json:"type"`
				Revision uint64             `json:"revision"`
				Servers  []webrtc.ICEServer `json:"ice_servers"`
			}
			for {
				raw, _, e := wsutil.ReadServerData(browser.socket)
				if e != nil {
					t.Fatal(e)
				}
				if json.Unmarshal(raw, &refresh) == nil && refresh.Type == "webrtc.ice_config" {
					break
				}
			}
			if refresh.Revision != 2 || refresh.Servers[0].Username != "temporary-v2" {
				t.Fatal("wrong renewal", refresh)
			}
			if e := browser.pc.SetConfiguration(webrtc.Configuration{ICEServers: refresh.Servers, ICETransportPolicy: webrtc.ICETransportPolicyRelay}); e != nil {
				t.Fatal(e)
			}
			offer, e := browser.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
			if e != nil {
				t.Fatal(e)
			}
			gather := webrtc.GatheringCompletePromise(browser.pc)
			if e = browser.pc.SetLocalDescription(offer); e != nil {
				t.Fatal(e)
			}
			select {
			case <-gather:
			case <-time.After(5 * time.Second):
				t.Fatal("restart gather timeout")
			}
			raw, _ := json.Marshal(map[string]any{"type": "webrtc.restart_offer", "revision": refresh.Revision, "sdp": browser.pc.LocalDescription().SDP})
			if e = wsutil.WriteClientText(browser.socket, raw); e != nil {
				t.Fatal(e)
			}
			var answer struct {
				Type     string `json:"type"`
				SDP      string `json:"sdp"`
				Revision uint64 `json:"revision"`
			}
			for {
				data, _, e := wsutil.ReadServerData(browser.socket)
				if e != nil {
					t.Fatal(e)
				}
				if json.Unmarshal(data, &answer) == nil && answer.Type == "webrtc.restart_answer" {
					break
				}
			}
			if answer.Revision != 2 {
				t.Fatal("uncorrelated answer")
			}
			if e = browser.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); e != nil {
				t.Fatal(e)
			}
			deadline := time.Now().Add(5 * time.Second)
			for browser.pc.ICEConnectionState() != webrtc.ICEConnectionStateConnected && browser.pc.ICEConnectionState() != webrtc.ICEConnectionStateCompleted {
				if time.Now().After(deadline) {
					t.Fatal("relay restart did not connect")
				}
				time.Sleep(10 * time.Millisecond)
			}
			// Drain old observations so the assertion measures audio after renewal.
			for len(browser.received) > 0 {
				<-browser.received
			}
			checkAudio()
			assertRelay()
			if app.softphones.lookup(row.ID).browserWriter() != before || browser.pc.GetSenders()[0] != sender {
				t.Fatal("credential renewal replaced media attachment/sender")
			}
			if platform.calls.Load() != 2 {
				t.Fatal("unexpected TURN credential requests", platform.calls.Load())
			}
		})
	}
}

func TestTURNTLSRelayValidatesCertificateAndTransfersMedia(t *testing.T) {
	endpoint, trust := localTURNTestRelay(t, "tls")
	address := strings.TrimSuffix(strings.TrimPrefix(endpoint, "turns:"), "?transport=tcp")
	// Verify trust is required, rather than disabling certificate verification.
	bad := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: x509.NewCertPool(), ServerName: "127.0.0.1"}
	if c, e := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, bad); e == nil {
		c.Close()
		t.Fatal("untrusted TLS relay accepted")
	}
	socket, e := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, trust)
	if e != nil {
		t.Fatal(e)
	}
	defer socket.Close()
	client, e := turn.NewClient(&turn.ClientConfig{STUNServerAddr: address, TURNServerAddr: address, Conn: turn.NewSTUNConn(socket), Username: "temporary-v1", Password: "local-test-password", Realm: "telephony-local"})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	if e = client.Listen(); e != nil {
		t.Fatal(e)
	}
	relay, e := client.Allocate()
	if e != nil {
		t.Fatal(e)
	}
	defer relay.Close()
	peer, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer peer.Close()
	peer.SetDeadline(time.Now().Add(2 * time.Second))
	relay.SetDeadline(time.Now().Add(2 * time.Second))
	if _, e = relay.WriteTo([]byte("microphone"), peer.LocalAddr()); e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, 100)
	n, remote, e := peer.ReadFrom(buf)
	if e != nil || string(buf[:n]) != "microphone" {
		t.Fatal("TLS relay upload", e)
	}
	if _, e = peer.WriteTo([]byte("playback"), remote); e != nil {
		t.Fatal(e)
	}
	n, _, e = relay.ReadFrom(buf)
	if e != nil || string(buf[:n]) != "playback" {
		t.Fatal("TLS relay playback", e)
	}
}

func TestTURNRefreshCancellationStopsInFlightProvider(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	writer := newWebSocketWriterPump(local, ws.StateServerSide)
	defer writer.Stop()
	done := make(chan struct{})
	entered := make(chan struct{})
	stopped := make(chan struct{})
	provider := &rtcTURNProvider{fetch: func(ctx context.Context) (rtcTURNCredentials, error) {
		close(entered)
		<-ctx.Done()
		return rtcTURNCredentials{}, ctx.Err()
	}}
	session := &rtcICESession{config: softphoneRTCConfig{TURN: provider}, done: done, refresh: make(chan struct{}, 1)}
	go func() { session.runAfter(writer, time.Millisecond); close(stopped) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("provider not called")
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("canceled provider blocked session cleanup")
	}
}
