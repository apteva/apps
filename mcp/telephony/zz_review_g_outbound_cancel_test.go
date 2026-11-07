package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// A 200 OK that crosses our CANCEL (local hangup while ringing) must be
// ACKed and then ended with BYE, never left answered at the carrier.
func TestReviewGOutboundCancelCrossingAnswerIsAckedAndHungUp(t *testing.T) {
	app, appCtx := withTelephonyTestContext(t, &answerPlatform{})
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
	media, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	remoteKey := make([]byte, 30)
	_, _ = rand.Read(remoteKey)
	cfg := directSIPTestConfig()
	cfg.PublicIP = netip.MustParseAddr("127.0.0.1")
	cfg.RTPBindIP = netip.MustParseAddr("127.0.0.1")
	cfg.RTPPortMin, cfg.RTPPortMax = 34200, 34300
	cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	ringing := make(chan struct{}, 1)
	ackSeen := make(chan struct{}, 1)
	byeSeen := make(chan struct{}, 1)
	var mu sync.Mutex
	var inviteTx sip.ServerTransaction
	var inviteReq *sip.Request
	server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		if req.GetHeader("Authorization") == nil {
			challenge := sip.NewResponseFromRequest(req, sip.StatusUnauthorized, "Unauthorized", nil)
			challenge.AppendHeader(sip.NewHeader("WWW-Authenticate", `Digest realm="didww.com", nonce="test-nonce", algorithm=MD5, qop="auth"`))
			_ = tx.Respond(challenge)
			return
		}
		mu.Lock()
		inviteTx, inviteReq = tx, req
		mu.Unlock()
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusRinging, "Ringing", nil))
		select {
		case ringing <- struct{}{}:
		default:
		}
	})
	server.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
		mu.Lock()
		originalTx, originalReq := inviteTx, inviteReq
		mu.Unlock()
		// The callee answered just before the CANCEL arrived.
		body, _ := buildSIPMediaAnswer(cfg, sipMediaOffer{Secure: true, PayloadType: 0, Codec: "PCMU", CryptoTag: "1"},
			media.LocalAddr().(*net.UDPAddr).Port, &sipMediaSecurity{LocalKey: remoteKey})
		answer := sip.NewSDPResponseFromRequest(originalReq, body)
		answer.AppendHeader(&sip.ContactHeader{Address: originalReq.Recipient})
		_ = originalTx.Respond(answer)
	})
	server.OnAck(func(_ *sip.Request, _ sip.ServerTransaction) {
		select {
		case ackSeen <- struct{}{}:
		default:
		}
	})
	server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
		select {
		case byeSeen <- struct{}{}:
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
	row := callRow{ID: "review-g-cross", ThreadID: "review-g-cross", Direction: "outbound", CarrierSlug: "didww",
		CarrierSID: "review-g-cross", IngressPath: inboundTransportSIPDirect, ToNumber: "+33123456789", FromNumber: "+33123456780",
		Status: "initiated", ProjectID: "project-a", PeerKind: peerKindRealtime, PlacedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	settings := didwwSIPSettings{host: "127.0.0.1", port: port, username: "test-user", password: "test-password", secure: true, scheme: "sip", codecs: []string{"PCMU"}}
	if err := g.startSIPOutbound(&row, settings, 5, 60); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ringing:
	case <-time.After(5 * time.Second):
		t.Fatal("call never rang")
	}
	stored, err := app.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = hangupOutboundSIP(app, stored)
	for name, seen := range map[string]chan struct{}{"ACK": ackSeen, "BYE": byeSeen} {
		select {
		case <-seen:
		case <-time.After(5 * time.Second):
			t.Fatalf("answer that crossed CANCEL never got %s", name)
		}
	}
}
