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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/gobwas/ws"
	"github.com/pion/sdp/v3"
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
	if _, err := carrier.Place(nil, request); err == nil || !strings.Contains(err.Error(), "recording") {
		t.Fatalf("provider-cloud recording should fail: %v", err)
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
	t.Helper()
	app, appCtx := withTelephonyTestContext(t, &answerPlatform{})
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err == nil {
			defer conn.Close()
			<-r.Context().Done()
		}
	}))
	defer audio.Close()

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
		sdpBody, buildErr := buildSIPMediaAnswer(cfg, sipMediaOffer{Secure: secureAnswer, PayloadType: 0,
			Codec: "PCMU", CryptoTag: "1"}, udpMedia.LocalAddr().(*net.UDPAddr).Port,
			&sipMediaSecurity{LocalKey: remoteKey})
		if buildErr != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusInternalServerError, "SDP Error", nil))
			return
		}
		_ = tx.Respond(sip.NewSDPResponseFromRequest(req, sdpBody))
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
		Direction: "outbound", CarrierSlug: "didww", CarrierSID: "didww-outbound-loopback",
		ToNumber: "+33123456789", FromNumber: "+33123456780", Status: "initiated", ProjectID: "project-a",
		PeerKind: peerKindRealtime, AudioBridgeURL: "ws" + strings.TrimPrefix(audio.URL, "http"),
		PlacedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	if err := g.startDIDWWOutbound(&row, didwwSIPSettings{host: "127.0.0.1", port: port,
		username: "test-user", password: "test-password", secure: true}, 5, 60); err != nil {
		t.Fatal(err)
	}
	select {
	case invite := <-inviteSeen:
		if invite.From() == nil || invite.From().Address.User != "33123456780" ||
			invite.Recipient.User != "33123456789" || !bytes.Contains(invite.Body(), []byte("RTP/SAVP")) {
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
		if err := (&didwwCarrier{app: app}).Hangup(appCtx, stored); err != nil {
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
	if err := (&didwwCarrier{app: app}).Hangup(appCtx, stored); err != nil {
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
}
