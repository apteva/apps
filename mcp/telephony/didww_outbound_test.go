package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/srtp/v3"
)

func didwwTestCredentials() map[string]string {
	return map[string]string{
		"sip_host": "fra.eu.out.didww.com", "sip_username": "outbound-user",
		"sip_password": "outbound-secret", "sip_transport": "tls", "sip_media_encryption": "srtp_sdes",
	}
}

func TestDIDWWOutboundSettingsFailClosed(t *testing.T) {
	fields := didwwTestCredentials()
	settings, err := didwwOutboundSettings(fields)
	if err != nil || settings.host != "fra.eu.out.didww.com" || settings.port != 5061 || !settings.secure {
		t.Fatalf("valid DIDWW settings: %+v, %v", settings, err)
	}
	for name, value := range map[string]string{
		"sip_host": "attacker.example.com", "sip_username": "", "sip_password": "",
		"sip_transport": "udp", "sip_media_encryption": "srtp_dtls", "sip_port": "0",
	} {
		invalid := didwwTestCredentials()
		invalid[name] = value
		if _, err := didwwOutboundSettings(invalid); err == nil {
			t.Fatalf("accepted unsafe %s=%q", name, value)
		}
	}
	fields["sip_media_encryption"] = "disabled"
	settings, err = didwwOutboundSettings(fields)
	if err != nil || settings.secure {
		t.Fatalf("explicit plain RTP configuration: %+v, %v", settings, err)
	}
}

func TestDIDWWOutboundPlaceDoesNotPretendRESTPlacesCalls(t *testing.T) {
	carrier := &didwwCarrier{fields: didwwTestCredentials()}
	request := carrierPlaceRequest{CallID: "local-1", To: "+33123456789", From: "+33123456780",
		TimeoutSec: 35, MaxDurationSec: 500}
	placed, err := carrier.Place(nil, request)
	if err != nil || placed.CarrierSID != "local-1" || carrier.timeoutSec != 35 || carrier.maxDurationSec != 500 {
		t.Fatalf("DIDWW placement preparation: %+v, %v", placed, err)
	}
	request.RecordingMode = recordingModeAlways
	if _, err := carrier.Place(nil, request); err != nil {
		t.Fatalf("local SIP recording should be supported: %v", err)
	}
	request.RecordingMode = recordingModeOff
	request.MachineDetection = "detect"
	if _, err := carrier.Place(nil, request); err == nil || !strings.Contains(err.Error(), "machine detection") {
		t.Fatalf("unsupported AMD should fail: %v", err)
	}
}

func TestDIDWWOutboundSDESOfferCanNegotiateSameKey(t *testing.T) {
	cfg := directSIPTestConfig()
	cfg.AllowedCIDRs = append(cfg.AllowedCIDRs, netip.MustParsePrefix("198.51.100.0/24"))
	localKey := make([]byte, 30)
	remoteKey := make([]byte, 30)
	if _, err := rand.Read(localKey); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(remoteKey); err != nil {
		t.Fatal(err)
	}
	localOffer := sipMediaOffer{Secure: true, PayloadType: 0, Codec: "PCMU", CryptoTag: "1"}
	body, err := buildSIPMediaAnswer(cfg, localOffer, 20000, &sipMediaSecurity{LocalKey: localKey})
	if err != nil {
		t.Fatal(err)
	}
	var description sdp.SessionDescription
	if err := description.Unmarshal(body); err != nil || len(description.MediaDescriptions) != 1 ||
		strings.Join(description.MediaDescriptions[0].MediaName.Protos, "/") != "RTP/SAVP" {
		t.Fatalf("DIDWW offer is not SDES SRTP: %v %s", err, body)
	}
	answer := sipMediaOffer{RemoteAddress: netip.MustParseAddr("203.0.113.5"), RemotePort: 30000,
		Secure: true, RemoteKey: remoteKey, PayloadType: 0, Codec: "PCMU", CryptoTag: "1"}
	security, err := newSIPMediaSecurityWithLocalKey(answer, localKey)
	if err != nil || !bytes.Equal(security.LocalKey, localKey) {
		t.Fatalf("outbound SRTP must use the key advertised in SDP: %v", err)
	}
	if _, err := newSIPMediaSecurityWithLocalKey(answer, []byte("wrong")); err == nil {
		t.Fatal("accepted an invalid local SRTP key")
	}
}

func TestDIDWWOutboundRejectsGatewaySecurityDowngradeBeforeSignaling(t *testing.T) {
	g := &sipGateway{cfg: directSIPTestConfig()}
	g.cfg.SRTPMode = sipSRTPRequired
	row := &callRow{ID: "call-1", CarrierSlug: "didww"}
	if err := g.startDIDWWOutbound(row, didwwSIPSettings{secure: false}, 30, 3600); err == nil {
		t.Fatal("plain RTP was accepted under required SRTP policy")
	}
}

func TestDIDWWOutboundSIPInviteAnswerAndHangup(t *testing.T) {
	runDIDWWOutboundSIPPeer(t, true, false)
}

func TestDIDWWOutboundRejectsCarrierMediaDowngrade(t *testing.T) {
	runDIDWWOutboundSIPPeer(t, false, false)
}

func TestDIDWWOutboundCancelsBeforeAnswer(t *testing.T) {
	runDIDWWOutboundSIPPeer(t, true, true)
}

func runDIDWWOutboundSIPPeer(t *testing.T, secureAnswer, leaveRinging bool) {
	runOutboundSIPPeer(t, "didww", secureAnswer, leaveRinging)
}
func TestGenericOutboundSIPTelnyxAndTwilio(t *testing.T) {
	for _, provider := range []string{"telnyx", "twilio"} {
		t.Run(provider+"/answer", func(t *testing.T) { runOutboundSIPPeer(t, provider, true, false) })
		t.Run(provider+"/cancel", func(t *testing.T) { runOutboundSIPPeer(t, provider, true, true) })
		t.Run(provider+"/downgrade", func(t *testing.T) { runOutboundSIPPeer(t, provider, false, false) })
	}
}
func runOutboundSIPPeer(t *testing.T, provider string, secureAnswer, leaveRinging bool) {
	runOutboundSIPPeerCodec(t, provider, "PCMU", secureAnswer, leaveRinging)
}
func TestGenericOutboundSIPAlternateCodecsAndAudio(t *testing.T) {
	for _, tc := range []struct{ provider, codec string }{{"telnyx", "G722"}, {"telnyx", "PCMA"}, {"twilio", "PCMA"}} {
		t.Run(tc.provider+"/"+tc.codec, func(t *testing.T) { runOutboundSIPPeerCodec(t, tc.provider, tc.codec, true, false) })
	}
}
func runOutboundSIPPeerCodec(t *testing.T, provider, codec string, secureAnswer, leaveRinging bool, humanModes ...bool) {
	t.Helper()
	app, appCtx := withTelephonyTestContext(t, &answerPlatform{})
	human := len(humanModes) > 0 && humanModes[0]
	coreConnected := make(chan net.Conn, 1)
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err == nil {
			coreConnected <- conn
		}
	}))
	defer audio.Close()
	if human {
		audio.Close()
		audio = softphoneTestServer(t, app)
		_, p, _ := net.SplitHostPort(strings.TrimPrefix(audio.URL, "http://"))
		t.Setenv("APTEVA_APP_PORT", p)
	}

	directory := t.TempDir()
	writeSIPTestCertificate(t, directory, "127.0.0.1")
	certificate, err := tls.LoadX509KeyPair(filepath.Join(directory, "fullchain.pem"), filepath.Join(directory, "privkey.pem"))
	if err != nil {
		t.Fatal(err)
	}
	serverUA, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	defer serverUA.Close()
	server, err := sipgo.NewServer(serverUA)
	if err != nil {
		t.Fatal(err)
	}
	udpMedia, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udpMedia.Close()
	remoteKey := make([]byte, 30)
	if _, err := rand.Read(remoteKey); err != nil {
		t.Fatal(err)
	}
	cfg := directSIPTestConfig()
	cfg.PublicIP = netip.MustParseAddr("127.0.0.1")
	cfg.RTPBindIP = netip.MustParseAddr("127.0.0.1")
	cfg.RTPPortMin, cfg.RTPPortMax = 34000, 34100
	cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	inviteSeen := make(chan *sip.Request, 1)
	authSeen := make(chan bool, 1)
	byeSeen := make(chan struct{}, 1)
	cancelSeen := make(chan struct{}, 1)
	var ringingMu sync.Mutex
	var ringingTx sip.ServerTransaction
	var ringingReq *sip.Request
	server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		select {
		case inviteSeen <- req:
		default:
		}
		if req.GetHeader("Authorization") == nil {
			challenge := sip.NewResponseFromRequest(req, sip.StatusUnauthorized, "Unauthorized", nil)
			challenge.AppendHeader(sip.NewHeader("WWW-Authenticate", `Digest realm="didww.com", nonce="test-nonce", algorithm=MD5, qop="auth"`))
			_ = tx.Respond(challenge)
			return
		}
		select {
		case authSeen <- true:
		default:
		}
		if leaveRinging {
			ringingMu.Lock()
			ringingTx, ringingReq = tx, req
			ringingMu.Unlock()
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusRinging, "Ringing", nil))
			return
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusRinging, "Ringing", nil))
		sdpBody, buildErr := buildSIPMediaAnswer(cfg, sipMediaOffer{Secure: secureAnswer, PayloadType: map[string]uint8{"PCMU": 0, "PCMA": 8, "G722": 9}[codec],
			Codec: codec, CryptoTag: "1"}, udpMedia.LocalAddr().(*net.UDPAddr).Port,
			&sipMediaSecurity{LocalKey: remoteKey})
		if buildErr != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusInternalServerError, "SDP Error", nil))
			return
		}
		response := sip.NewSDPResponseFromRequest(req, sdpBody)
		response.AppendHeader(&sip.ContactHeader{Address: req.Recipient})
		response.AppendHeader(sip.NewHeader("Session-Expires", "1800;refresher=uac"))
		_ = tx.Respond(response)
	})
	server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
		select {
		case byeSeen <- struct{}{}:
		default:
		}
	})
	server.OnAck(func(_ *sip.Request, _ sip.ServerTransaction) {})
	server.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
		ringingMu.Lock()
		originalTx, originalReq := ringingTx, ringingReq
		ringingMu.Unlock()
		if originalTx != nil && originalReq != nil {
			_ = originalTx.Respond(sip.NewResponseFromRequest(originalReq, sip.StatusRequestTerminated, "Request Terminated", nil))
		}
		select {
		case cancelSeen <- struct{}{}:
		default:
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	ready := make(chan struct{}, 1)
	serverCtx, stopServer := context.WithCancel(context.WithValue(t.Context(), sipgo.ListenReadyCtxKey, sipgo.ListenReadyCtxValue(ready)))
	defer stopServer()
	go func() {
		_ = server.ListenAndServeTLS(serverCtx, "tcp", address, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("mock SIP TLS peer did not start")
	}
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	clientUA, err := sipgo.NewUA(sipgo.WithUserAgentHostname("127.0.0.1"),
		sipgo.WithUserAgenTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})) // Test peer only.
	if err != nil {
		t.Fatal(err)
	}
	client, err := sipgo.NewClient(clientUA)
	if err != nil {
		_ = clientUA.Close()
		t.Fatal(err)
	}
	params := sip.NewParams()
	params.Add("transport", "tls")
	contact := sip.ContactHeader{Address: sip.Uri{Scheme: "sips", User: "apteva", Host: "127.0.0.1", UriParams: params}}
	gatewayCtx, stopGateway := context.WithCancel(t.Context())
	g := &sipGateway{app: app, appCtx: appCtx, cfg: cfg, ua: clientUA, client: client,
		outboundDialogs: sipgo.NewDialogClientCache(client, contact), ctx: gatewayCtx, cancel: stopGateway,
		outboundByCall: make(map[string]*outboundSIPSession), outboundByProviderCall: make(map[string]*outboundSIPSession)}
	app.sip.mu.Lock()
	app.sip.gateway = g
	app.sip.mu.Unlock()
	defer g.Stop()
	row := callRow{ID: "didww-outbound-loopback", ThreadID: "human-didww-outbound-loopback",
		Direction: "outbound", CarrierSlug: provider, CarrierSID: "didww-outbound-loopback", IngressPath: inboundTransportSIPDirect,
		ToNumber: "+33123456789", FromNumber: "+33123456780", Status: "initiated", ProjectID: "project-a",
		PeerKind: peerKindRealtime, RecordingMode: recordingModeAlways, AudioBridgeURL: "ws" + strings.TrimPrefix(audio.URL, "http"),
		PlacedAt: time.Now().UTC().Format(time.RFC3339)}
	if human {
		row.PeerKind = peerKindHuman
		row.PeerToken = "browser-secret"
		row.CallbackSecret = "carrier-secret"
	}
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	var browser net.Conn
	if human {
		browser = dialWS(t, audio.URL+"/softphone/media/"+row.ID+"/"+row.PeerToken)
		defer browser.Close()
		readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	}
	settings := didwwSIPSettings{host: "127.0.0.1", port: port, username: "test-user", password: "test-password", secure: true, e164: provider != "didww", scheme: "sip", codecs: []string{codec}}
	if err := g.startSIPOutbound(&row, settings, 5, 60); err != nil {
		t.Fatal(err)
	}
	select {
	case invite := <-inviteSeen:
		from, to := "33123456780", "33123456789"
		if provider != "didww" {
			from, to = "+"+from, "+"+to
		}
		if invite.From() == nil || invite.From().Address.User != from ||
			invite.Recipient.User != to || !bytes.Contains(invite.Body(), []byte("RTP/SAVP")) {
			t.Fatalf("DIDWW INVITE did not carry caller ID, destination and secure media: %s", invite)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no SIP INVITE reached the mocked carrier")
	}
	select {
	case <-authSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("DIDWW SIP Digest challenge was not answered")
	}
	if leaveRinging {
		stored, err := app.db().findCall(row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := hangupOutboundSIP(app, stored); err != nil {
			t.Fatal(err)
		}
		select {
		case <-cancelSeen:
		case <-time.After(5 * time.Second):
			t.Fatal("hangup before answer did not send SIP CANCEL")
		}
		current, err := app.db().findCall(row.ID)
		if err != nil || current.Status != "canceled" {
			t.Fatalf("early SIP hangup status: %+v %v", current, err)
		}
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stored, findErr := app.db().findCall(row.ID)
		if findErr != nil {
			t.Fatal(findErr)
		}
		if stored.Status == "in-progress" || (!secureAnswer && stored.Status == "failed") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stored, _ := app.db().findCall(row.ID)
	if !secureAnswer {
		if stored.Status != "failed" || !strings.Contains(stored.ErrorMessage, "media that was not offered") {
			t.Fatalf("insecure answer was not rejected: status=%s error=%s", stored.Status, stored.ErrorMessage)
		}
		select {
		case <-byeSeen:
		case <-time.After(5 * time.Second):
			t.Fatal("invalid media answer was not cleared with BYE")
		}
		return
	}
	if stored.Status != "in-progress" {
		t.Fatalf("SIP answer did not connect the call: status=%s error=%s", stored.Status, stored.ErrorMessage)
	}
	if stored.CarrierSID == "" || stored.CarrierSID == row.ID {
		t.Fatalf("SIP Call-ID was not persisted as provider identity: %q", stored.CarrierSID)
	}
	// Exercise both media directions through the actual outbound bridge, not
	// just signaling: carrier SRTP -> PCM bridge and PCM bridge -> carrier SRTP.
	var core net.Conn
	if human {
		core = browser
		readSoftphoneEventWithin(t, browser, "peer.connected", 3*time.Second)
	} else {
		select {
		case core = <-coreConnected:
		case <-time.After(3 * time.Second):
			t.Fatal("outbound Core bridge did not attach")
		}
	}
	defer core.Close()
	session := g.outboundSessionByCall(row.ID)
	encrypt, err := srtp.CreateContext(remoteKey[:16], remoteKey[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	if err != nil {
		t.Fatal(err)
	}
	decrypt, err := srtp.CreateContext(session.localKey[:16], session.localKey[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	if err != nil {
		t.Fatal(err)
	}
	encoder := &sipRTPMedia{offer: sipMediaOffer{Codec: codec}}
	pcm := make([]int16, encoder.sampleRate()/50)
	for i := range pcm {
		pcm[i] = int16((i%40 - 20) * 200)
	}
	for i := 1; i <= 6; i++ {
		packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: session.media.offer.PayloadType, SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 42}, Payload: encoder.encodeAudio(pcm)}
		raw, e := packet.Marshal()
		if e != nil {
			t.Fatal(e)
		}
		raw, e = encrypt.EncryptRTP(nil, raw, nil)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = udpMedia.WriteToUDP(raw, session.media.conn.LocalAddr().(*net.UDPAddr)); e != nil {
			t.Fatal(e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = core.SetReadDeadline(time.Now().Add(3 * time.Second))
	var incoming []byte
	if human {
		incoming = readBinaryWithin(t, browser, 3*time.Second)
	} else {
		var op ws.OpCode
		incoming, op, err = wsutil.ReadClientData(core)
		if err != nil || op != ws.OpBinary {
			t.Fatalf("carrier-to-Core audio failed: %v", err)
		}
	}
	if len(incoming) < 800 || bytes.Equal(incoming, make([]byte, len(incoming))) {
		t.Fatalf("received silent/incomplete audio: %d", len(incoming))
	}
	if human {
		err = wsutil.WriteClientBinary(browser, bytes.Repeat([]byte{0x10, 0x03}, 480))
	} else {
		err = wsutil.WriteServerBinary(core, bytes.Repeat([]byte{0x10, 0x03}, 480))
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = udpMedia.SetReadDeadline(time.Now().Add(3 * time.Second))
	raw := make([]byte, 4096)
	n, _, err := udpMedia.ReadFromUDP(raw)
	if err != nil {
		t.Fatal(err)
	}
	clear, err := decrypt.DecryptRTP(nil, raw[:n], nil)
	if err != nil {
		t.Fatal(err)
	}
	var packet rtp.Packet
	if err = packet.Unmarshal(clear); err != nil || packet.PayloadType != session.media.offer.PayloadType || len(packet.Payload) != 160 {
		t.Fatalf("Core-to-carrier RTP failed: %+v %v", packet, err)
	}
	decoded := encoder.decodeAudio(packet.Payload)
	if bytes.Equal(pcm16ToBytes(decoded), make([]byte, len(decoded)*2)) {
		t.Fatal("outbound audio became silence")
	}
	// The mock challenges every new re-INVITE, exercising the bounded
	// authentication retry with the established dialog and fresh CSeq.
	session.signalMu.Lock()
	err = session.sendSessionRefresh(sipSessionTimer{interval: 1800 * time.Second, local: true})
	session.signalMu.Unlock()
	if err != nil {
		t.Fatalf("authenticated outbound session refresh: %v", err)
	}
	session.timerMu.Lock()
	refreshed := session.timer.local && session.timer.interval == 1800*time.Second && time.Since(session.timerUpdated) < time.Second
	session.timerMu.Unlock()
	if !refreshed {
		t.Fatal("outbound session refresh did not update timer")
	}
	recorder := session.media.recorder.Load()
	if recorder == nil {
		t.Fatal("outbound SIP recording did not start")
	}
	control := &localSIPController{app: app}
	if err = control.PauseRecording(appCtx, stored, ""); err != nil || !recorder.paused.Load() {
		t.Fatalf("pause recording: %v", err)
	}
	if err = control.ResumeRecording(appCtx, stored, ""); err != nil || recorder.paused.Load() {
		t.Fatalf("resume recording: %v", err)
	}
	if err = g.Answer(stored); err == nil || session.ended.Load() {
		t.Fatal("answer operation ended an outbound dialog")
	}
	if err := g.Hangup(stored); err != nil {
		t.Fatal(err)
	}
	select {
	case <-byeSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("local hangup did not send SIP BYE")
	}
	if current, err := app.db().findCall(row.ID); err != nil || current.Status != "completed" {
		t.Fatalf("DIDWW hangup status: %+v %v", current, err)
	}
	select {
	case <-recorder.done:
	case <-time.After(3 * time.Second):
		t.Fatal("outbound recorder not drained")
	}
	var path string
	if err = app.db().db.QueryRow(`SELECT path FROM local_recording_files WHERE recording_id=?`, recorder.row.ID).Scan(&path); err != nil {
		t.Fatal(err)
	}
	rawWAV, err := os.ReadFile(path)
	if err != nil || len(rawWAV) <= 44 || string(rawWAV[:4]) != "RIFF" {
		t.Fatalf("local recorded WAV unavailable: %v", err)
	}

}

func TestGenericOutboundSIPHumanBrowserMedia(t *testing.T) {
	for _, provider := range []string{"telnyx", "twilio"} {
		t.Run(provider, func(t *testing.T) { runOutboundSIPPeerCodec(t, provider, "PCMU", true, false, true) })
	}
}
