package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

type sipDialogFixture struct {
	app              *App
	gateway          *sipGateway
	signaling, media *net.UDPConn
	call             *callRow
	session          *sipSession
	to, body         string
	key              []byte
	localPort        int
}

func newSIPDialogFixture(t *testing.T, secure bool, ackTimeout time.Duration) *sipDialogFixture {
	t.Helper()
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()
	media, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { media.Close() })
	cfg := directSIPTestConfig()
	cfg.Transport = "udp"
	cfg.ListenAddress = fmt.Sprintf("127.0.0.1:%d", port)
	cfg.PublicHost = "127.0.0.1"
	cfg.PublicIP = netip.MustParseAddr("127.0.0.1")
	cfg.RTPBindIP = cfg.PublicIP
	cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	gateway, err := newSIPGateway(app, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	gateway.ackTimeout = ackTimeout
	if err = gateway.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gateway.Stop)
	app.sip.gateway = gateway
	route := routeRow{ID: "dialog-route", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10, PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, Secret: "test", AnswerMode: answerModeAgent, TimeoutSec: 60, InboundTransport: inboundTransportSIPDirect, TransportConfig: `{"provider":"twilio","trunk_id":"TK1"}`}
	if err = app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	f := &sipDialogFixture{app: app, gateway: gateway, signaling: conn, media: media, to: "<sip:+12025550100@127.0.0.1>", localPort: conn.LocalAddr().(*net.UDPAddr).Port}
	protocol := "RTP/AVP"
	crypto := ""
	if secure {
		protocol = "RTP/SAVP"
		f.key = bytes.Repeat([]byte{0x42}, 30)
		crypto = "a=crypto:7 " + sipSDESCryptoSuite + " inline:" + base64.StdEncoding.EncodeToString(f.key) + "\r\n"
	}
	f.body = fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=test\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d %s 0\r\na=rtpmap:0 PCMU/8000\r\n%s", media.LocalAddr().(*net.UDPAddr).Port, protocol, crypto)
	f.send(t, "INVITE", 1, f.body, "Supported: timer\r\nSession-Expires: 90;refresher=uac\r\n")
	response := readSIPResponseContaining(t, conn, "180 Ringing")
	f.to = sipTestHeader(response, "To")
	f.call, err = app.db().findInboundCallByProviderID("native-dialog@local")
	if err != nil || f.call == nil {
		t.Fatal(err)
	}
	f.session = gateway.sessionByCall(f.call.ID)
	if f.session == nil {
		t.Fatal("no native session")
	}
	return f
}
func sipTestHeader(message, name string) string {
	for _, line := range strings.Split(message, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(name)+":") {
			return strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
	}
	return ""
}
func (f *sipDialogFixture) send(t *testing.T, method string, seq int, body, extra string) {
	t.Helper()
	request := fmt.Sprintf("%s sip:+12025550100@%s SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:%d;branch=z9hG4bK-%s-%d;rport\r\nMax-Forwards: 70\r\nFrom: <sip:+12025550101@127.0.0.1>;tag=caller\r\nTo: %s\r\nCall-ID: native-dialog@local\r\nCSeq: %d %s\r\nContact: <sip:caller@127.0.0.1:%d>\r\n%s", method, f.gateway.cfg.ListenAddress, f.localPort, method, seq, f.to, seq, method, f.localPort, extra)
	if body != "" {
		request += "Content-Type: application/sdp\r\n"
	}
	request += fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	if _, err := f.signaling.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
}
func (f *sipDialogFixture) answer(t *testing.T) string {
	t.Helper()
	if err := f.app.db().updateStatus(f.call.ID, "answering", ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.gateway.Answer(f.call) }()
	response := readSIPResponseContaining(t, f.signaling, "200 OK")
	f.to = sipTestHeader(response, "To")
	f.send(t, "ACK", 1, "", "")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answer did not finish after ACK")
	}
	if err := f.app.db().updateStatus(f.call.ID, "answered", ""); err != nil {
		t.Fatal(err)
	}
	return strings.SplitN(response, "\r\n\r\n", 2)[1]
}
func (f *sipDialogFixture) respond(t *testing.T, request string, body string) {
	t.Helper()
	message := "SIP/2.0 200 OK\r\n"
	for _, header := range []string{"Via", "From", "To", "Call-ID", "CSeq"} {
		message += header + ": " + sipTestHeader(request, header) + "\r\n"
	}
	message += fmt.Sprintf("Contact: <sip:caller@127.0.0.1:%d>\r\n", f.localPort)
	if body != "" {
		message += "Content-Type: application/sdp\r\nSession-Expires: 90;refresher=uac\r\n"
	}
	message += fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	if _, err := f.signaling.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
}
func (f *sipDialogFixture) waitEnded(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, _ := f.app.db().findCall(f.call.ID)
		if f.gateway.sessionCount() == 0 && row != nil && isTerminalStatus(row.Status) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("SIP call did not end")
}
func (f *sipDialogFixture) waitRefreshIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.session.refreshMu.TryLock() {
			f.session.refreshMu.Unlock()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("SIP refresh handler did not finish")
}
func TestSIPAnsweredDialogRefreshAndBidirectionalMedia(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprint("SRTP=", secure), func(t *testing.T) {
			f := newSIPDialogFixture(t, secure, 2*time.Second)
			answer := f.answer(t)
			endpoint, err := parseSIPMediaOffer([]byte(answer), f.gateway.cfg)
			if err != nil {
				t.Fatal(err)
			}
			coreConnected := make(chan net.Conn, 1)
			coreServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, _, err := upgradeBuffered(w, r)
				if err != nil {
					return
				}
				coreConnected <- conn
			}))
			defer coreServer.Close()
			_, err = f.app.db().db.Exec(`UPDATE calls SET audio_bridge_url=?,peer_kind='realtime' WHERE id=?`, strings.Replace(coreServer.URL, "http:", "ws:", 1), f.call.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.gateway.StartMedia(f.call); err != nil {
				t.Fatal(err)
			}
			var core net.Conn
			select {
			case core = <-coreConnected:
			case <-time.After(2 * time.Second):
				t.Fatal("core media not connected")
			}
			defer core.Close()
			var encrypt, decrypt *srtp.Context
			if secure {
				encrypt, err = srtp.CreateContext(f.key[:16], f.key[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
				if err != nil {
					t.Fatal(err)
				}
				decrypt, err = srtp.CreateContext(endpoint.RemoteKey[:16], endpoint.RemoteKey[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
				if err != nil {
					t.Fatal(err)
				}
			}
			target := net.UDPAddrFromAddrPort(netip.AddrPortFrom(endpoint.RemoteAddress, uint16(endpoint.RemotePort)))
			for i := 1; i <= 3; i++ {
				packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 42}, Payload: bytes.Repeat([]byte{0x81}, 160)}
				raw, _ := packet.Marshal()
				if secure {
					raw, err = encrypt.EncryptRTP(nil, raw, nil)
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err = f.media.WriteToUDP(raw, target); err != nil {
					t.Fatal(err)
				}
			}
			core.SetReadDeadline(time.Now().Add(2 * time.Second))
			audio, _, err := wsutil.ReadClientData(core)
			if err != nil || (len(audio) < 800 || len(audio) > 960 || bytes.Equal(audio, make([]byte, len(audio)))) {
				t.Fatalf("inbound audio len=%d err=%v", len(audio), err)
			}
			if err = wsutil.WriteServerBinary(core, bytes.Repeat([]byte{0x10, 0x03}, 480)); err != nil {
				t.Fatal(err)
			}
			f.media.SetReadDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 4096)
			n, _, err := f.media.ReadFromUDP(buf)
			if err != nil {
				t.Fatal(err)
			}
			raw := buf[:n]
			if secure {
				raw, err = decrypt.DecryptRTP(nil, raw, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			var outgoing rtp.Packet
			if err = outgoing.Unmarshal(raw); err != nil || len(outgoing.Payload) != 160 {
				t.Fatalf("outbound audio: %+v %v", outgoing, err)
			}
			f.send(t, "INVITE", 2, f.body, "Supported: timer\r\nSession-Expires: 180;refresher=uac\r\n")
			refresh := readSIPResponseContaining(t, f.signaling, "200 OK")
			if !strings.Contains(refresh, "Session-Expires: 180;refresher=uac") {
				t.Fatal("refresh timer not negotiated")
			}
			f.send(t, "ACK", 2, "", "")
			// A response can reach this socket before the handler releases its
			// offer/answer lock. Keep these sequential checks free of glare.
			f.waitRefreshIdle(t)
			f.send(t, "UPDATE", 3, "", "")
			readSIPResponseContaining(t, f.signaling, "200 OK")
			f.waitRefreshIdle(t)
			f.send(t, "UPDATE", 2, "", "")
			readSIPResponseContaining(t, f.signaling, "500 Invalid CSeq")
			f.waitRefreshIdle(t)
			f.send(t, "INVITE", 4, strings.Replace(f.body, "m=audio ", "m=audio 1", 1), "")
			readSIPResponseContaining(t, f.signaling, "488 Media Change Not Supported")
			f.send(t, "BYE", 5, "", "")
			readSIPResponseContaining(t, f.signaling, "200 OK")
			f.waitEnded(t)
		})
	}
}
func TestSIPMediaStartupFailureSendsBYE(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	f.answer(t)
	_, err := f.app.db().db.Exec(`UPDATE calls SET audio_bridge_url='invalid://bridge',peer_kind='realtime' WHERE id=?`, f.call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.gateway.StartMedia(f.call); err != nil {
		t.Fatal(err)
	}
	bye := readSIPResponseContaining(t, f.signaling, "BYE sip:")
	f.respond(t, bye, "")
	f.waitEnded(t)
}
func TestSIPLocallyInitiatedSessionRefresh(t *testing.T) {
	f := newSIPDialogFixture(t, true, 2*time.Second)
	f.answer(t)
	done := make(chan error, 1)
	go func() { done <- f.session.sendSessionRefresh(sipSessionTimer{interval: 90 * time.Second, local: true}) }()
	invite := readSIPResponseContaining(t, f.signaling, "INVITE sip:")
	f.respond(t, invite, f.body)
	readSIPResponseContaining(t, f.signaling, "ACK sip:")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("local refresh stalled")
	}
	f.send(t, "BYE", 2, "", "")
	readSIPResponseContaining(t, f.signaling, "200 OK")
	f.waitEnded(t)
}
func TestSIPMissingInitialACKEndsCall(t *testing.T) {
	f := newSIPDialogFixture(t, false, 80*time.Millisecond)
	f.app.db().updateStatus(f.call.ID, "answering", "")
	done := make(chan error, 1)
	go func() { done <- f.gateway.Answer(f.call) }()
	readSIPResponseContaining(t, f.signaling, "200 OK")
	bye := readSIPResponseContaining(t, f.signaling, "BYE sip:")
	f.respond(t, bye, "")
	f.waitEnded(t)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("missing ACK left answer goroutine alive")
	}
}
func TestSIPTLSCertificateReloadKeepsLastGood(t *testing.T) {
	dir := t.TempDir()
	writeSIPTestCertificate(t, dir, "sip.example.test")
	cfg := directSIPTestConfig()
	cfg.TLSCertFile = filepath.Join(dir, "fullchain.pem")
	cfg.TLSKeyFile = filepath.Join(dir, "privkey.pem")
	config, err := cfg.tlsConfig()
	if err != nil {
		t.Fatal(err)
	}
	first, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeSIPTestCertificate(t, dir, "sip.example.test")
	second, err := config.GetCertificate(nil)
	if err != nil || bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatalf("certificate did not reload: %v", err)
	}
	if err = os.WriteFile(cfg.TLSKeyFile, []byte("partial renewal"), 0600); err != nil {
		t.Fatal(err)
	}
	kept, err := config.GetCertificate(nil)
	if err != nil || kept != second {
		t.Fatalf("lost last good certificate: %v", err)
	}
}
func TestSIPAdmissionCapacityIsAtomic(t *testing.T) {
	g := &sipGateway{cfg: sipGatewayConfig{MaxSessions: 3}, byProviderCall: map[string]*sipSession{}, byCall: map[string]*sipSession{}}
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if g.reserveSession(fmt.Sprint(i)) == 0 {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatalf("accepted %d", accepted.Load())
	}
	for id := range g.reserved {
		g.releaseReservation(id)
	}
	if g.reserveSession("replacement") != 0 {
		t.Fatal("reservation leaked")
	}
}
func TestSIPSessionTimerValidation(t *testing.T) {
	for _, tc := range []struct {
		value  string
		status int
		local  bool
	}{{"89", 422, false}, {"90;refresher=uac", 0, false}, {"180;refresher=uas", 0, true}, {"bad", 400, false}, {"90;refresher=bad", 400, false}} {
		req := sip.NewRequest(sip.INVITE, sip.Uri{})
		req.AppendHeader(sip.NewHeader("Session-Expires", tc.value))
		req.AppendHeader(sip.NewHeader("Supported", "timer"))
		timer, status, _ := parseSIPSessionTimer(req, sipSessionTimer{})
		if status != tc.status || (status == 0 && timer.local != tc.local) {
			t.Fatalf("%s: %+v %d", tc.value, timer, status)
		}
	}
}
func TestSIPSRTPReplayWindowSurvivesSequenceWrap(t *testing.T) {
	offer, key := sipAuditSecureOffer(t)
	security, err := newSIPMediaSecurity(offer)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := srtp.CreateContext(key[:16], key[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	if err != nil {
		t.Fatal(err)
	}
	media := sipRTPMedia{offer: offer, security: security}
	var prior []byte
	for _, seq := range []uint16{65534, 65535, 0, 1} {
		p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: seq, SSRC: 1}, Payload: bytes.Repeat([]byte{0xff}, 160)}
		raw, _ := p.Marshal()
		raw, err = sender.EncryptRTP(nil, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = media.decodePacket(raw); err != nil {
			t.Fatal(err)
		}
		if seq == 65535 {
			prior = raw
		}
	}
	if _, err = media.decodePacket(prior); err == nil {
		t.Fatal("pre-wrap replay accepted")
	}
}
func TestSIPJitterQueueKeepsRecentSpeech(t *testing.T) {
	b := newRTPJitterBuffer()
	for i := 0; i < 40; i++ {
		b.push(uint16(i), bytes.Repeat([]byte{byte(i)}, 160))
	}
	frame, ok := b.pop()
	if !ok || len(frame) == 0 || frame[0] < 34 {
		t.Fatalf("kept stale speech: %v %v", frame, ok)
	}
	if b.buffered > 960 || b.droppedSamples == 0 {
		t.Fatal("unbounded backlog or missing diagnostics")
	}
}

func TestSIPRefreshMissingACKAndExpirySendBYE(t *testing.T) {
	for _, missingACK := range []bool{true, false} {
		t.Run(fmt.Sprint("missingACK=", missingACK), func(t *testing.T) {
			f := newSIPDialogFixture(t, false, 500*time.Millisecond)
			f.answer(t)
			if missingACK {
				f.send(t, "INVITE", 2, f.body, "")
				readSIPResponseContaining(t, f.signaling, "200 OK")
			} else {
				f.session.answerMu.Lock()
				f.session.timerUpdated = time.Now().Add(-91 * time.Second)
				f.session.answerMu.Unlock()
			}
			bye := readSIPResponseContaining(t, f.signaling, "BYE sip:")
			f.respond(t, bye, "")
			f.waitEnded(t)
		})
	}
}
func TestSIPForeignDialogAndUnmatchedCancelCannotEndCall(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	f.answer(t)
	correct := f.to
	f.to = strings.Replace(correct, "tag=", "tag=wrong-", 1)
	f.send(t, "UPDATE", 2, "", "")
	readSIPResponseContaining(t, f.signaling, "481 Call Does Not Exist")
	f.to = correct
	f.send(t, "CANCEL", 99, "", "")
	readSIPResponseContaining(t, f.signaling, "481 No Matching Transaction")
	if f.session.ended.Load() {
		t.Fatal("unmatched request ended call")
	}
	f.send(t, "BYE", 2, "", "")
	readSIPResponseContaining(t, f.signaling, "200 OK")
	f.waitEnded(t)
}
func TestSIPSDPStrictNegotiation(t *testing.T) {
	base := "v=0\r\no=- 1 1 IN IP4 203.0.113.20\r\ns=test\r\nc=IN IP4 203.0.113.20\r\nt=0 0\r\n"
	for _, tc := range []struct {
		description string
		valid       bool
	}{
		{"m=audio 4000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000/1\r\n", true},
		{"m=audio 4000 RTP/AVP 0\r\na=rtpmap:0 PCMA/8000\r\n", false},
		{"m=audio 4000 RTP/AVP 96\r\na=rtpmap:96 PCMU/8000/2\r\n", false},
		{"a=inactive\r\nm=audio 4000 RTP/AVP 0\r\na=sendrecv\r\n", true},
		{"a=inactive\r\nm=audio 4000 RTP/AVP 0\r\n", false},
		{"m=audio 4000 RTP/AVP 0\r\na=sendonly\r\n", false},
	} {
		_, err := parseSIPMediaOffer([]byte(base+tc.description), directSIPTestConfig())
		if (err == nil) != tc.valid {
			t.Fatalf("offer %q: %v", tc.description, err)
		}
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 30))
	for _, extra := range []string{"|2^20", "|2^20|1:4", " UNENCRYPTED_SRTP"} {
		body := base + "m=audio 4000 RTP/SAVP 0\r\na=crypto:7 " + sipSDESCryptoSuite + " inline:" + key + extra + "\r\n"
		if _, err := parseSIPMediaOffer([]byte(body), directSIPTestConfig()); err == nil {
			t.Fatalf("unsupported SDES parameters accepted: %q", extra)
		}
	}
}
func TestSIPJitterResumesShortDTXPause(t *testing.T) {
	b := newRTPJitterBuffer()
	start := time.Now()
	for i := 0; i < 3; i++ {
		b.pushRTP(uint16(i+1), bytes.Repeat([]byte{1}, 160), uint32(160*(i+1)), start)
	}
	for i := 0; i < 8; i++ {
		b.pop()
	}
	b.pushRTP(4, bytes.Repeat([]byte{2}, 160), 1600, start.Add(140*time.Millisecond))
	for i := 0; i < 3; i++ {
		if frame, ready := b.pop(); ready && len(frame) > 0 && frame[0] == 2 {
			return
		}
	}
	t.Fatal("DTX resumed packet was mistaken for late audio")
}

func TestSIPRemoteHangupInterruptsPendingLocalRefresh(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	f.answer(t)
	done := make(chan error, 1)
	go func() { done <- f.session.sendSessionRefresh(sipSessionTimer{interval: 90 * time.Second, local: true}) }()
	readSIPResponseContaining(t, f.signaling, "INVITE sip:")
	f.send(t, "BYE", 2, "", "")
	readSIPResponseContaining(t, f.signaling, "200 OK")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unanswered refresh succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("hangup waited for local refresh timeout")
	}
	f.waitEnded(t)
}
