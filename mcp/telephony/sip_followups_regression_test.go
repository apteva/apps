package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
)

func TestSIPDialogTLSContactAndCertificateIdentity(t *testing.T) {
	// A real TLS listener has a DNS-only certificate, while the dialog Contact
	// advertises an IP. Exercise routing and TLS validation without a carrier.
	dir := t.TempDir()
	writeSIPTestCertificate(t, dir, "carrier.example.test")
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	pem, _ := os.ReadFile(filepath.Join(dir, "fullchain.pem"))
	roots.AppendCertsFromPEM(pem)
	peerUA, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	defer peerUA.Close()
	server, _ := sipgo.NewServer(peerUA)
	requests := make(chan *sip.Request, 8)
	server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		requests <- req
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		requests <- req
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	go server.ListenAndServeTLS(context.WithValue(ctx, sipgo.ListenReadyCtxKey, sipgo.ListenReadyCtxValue(ready)), "tls", address, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("TLS peer startup")
	}
	cfg := directSIPTestConfig()
	cfg.PublicHost = "carrier.example.test"
	cfg.TLSCertFile = filepath.Join(dir, "fullchain.pem")
	cfg.TLSKeyFile = filepath.Join(dir, "privkey.pem")
	cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	cfg.clientRoots = roots
	baseUA, _ := sipgo.NewUA(sipgo.WithUserAgenTLSConfig(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}))
	defer baseUA.Close()
	g := &sipGateway{ua: baseUA, cfg: cfg, byProviderCall: map[string]*sipSession{}, outboundByProviderCall: map[string]*outboundSIPSession{}}
	defer g.dialogTransports.close()
	requester := sipDialogRequester{g}
	client, _ := sipgo.NewClient(baseUA)
	client.TxRequester = requester
	host, port, _ := net.SplitHostPort(address)
	uri := sip.Uri{Scheme: "sip", Host: host, UriParams: sip.NewParams()}
	fmt.Sscan(port, &uri.Port)
	uri.UriParams.Add("transport", "tls")
	for _, tc := range []struct {
		name, identity string
		method         sip.RequestMethod
		ok             bool
	}{
		{"IP SAN rejection remains without configured identity", "", sip.BYE, false},
		{"DNS-only carrier certificate verified for IP Contact", "carrier.example.test", sip.BYE, true},
		{"same identity supports refresh", "carrier.example.test", sip.INVITE, true},
		{"wrong certificate name rejected", "wrong.example.test", sip.BYE, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "dialog-" + strings.ReplaceAll(tc.name, " ", "-")
			g.byProviderCall[id] = &sipSession{call: &callRow{CarrierSlug: "generic"}, dialog: &sipgo.DialogServerSession{Dialog: sipgo.Dialog{InviteRequest: sip.NewRequest(sip.INVITE, uri)}}}
			g.cfg.TLSPeerNames = map[string]string{"generic": tc.identity}
			req := sip.NewRequest(tc.method, uri)
			cid := sip.CallIDHeader(id)
			req.AppendHeader(&cid)
			to := sip.ToHeader{Address: uri, Params: sip.NewParams()}
			to.Params.Add("tag", "remote")
			req.AppendHeader(&to)
			requestCtx, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			response, e := client.Do(requestCtx, req)
			if tc.ok {
				if e != nil || response.StatusCode != 200 {
					t.Fatalf("response=%v error=%v", response, e)
				}
				select {
				case got := <-requests:
					if got.Recipient.Host != host {
						t.Fatal("dialog target rewritten")
					}
				case <-time.After(time.Second):
					t.Fatal("no request")
				}
			} else if e == nil {
				t.Fatal("untrusted certificate accepted")
			}
		})
	}
	t.Run("reuse established TLS socket despite different IP Contact port", func(t *testing.T) {
		verified, err := g.tlsDialogPeer("carrier.example.test")
		if err != nil {
			t.Fatal(err)
		}
		g.ua = verified
		defer func() { g.ua = baseUA }()
		wrongContact := uri
		wrongContact.Port = 9
		invite := sip.NewRequest(sip.INVITE, wrongContact)
		invite.SetSource(address)
		id := "reuse-dialog"
		g.byProviderCall[id] = &sipSession{call: &callRow{CarrierSlug: "generic"}, dialog: &sipgo.DialogServerSession{Dialog: sipgo.Dialog{InviteRequest: invite}}}
		g.cfg.TLSPeerNames = map[string]string{"generic": "wrong.example.test"}
		req := sip.NewRequest(sip.BYE, wrongContact)
		cid := sip.CallIDHeader(id)
		req.AppendHeader(&cid)
		to := sip.ToHeader{Address: wrongContact, Params: sip.NewParams()}
		to.Params.Add("tag", "remote")
		req.AppendHeader(&to)
		requestCtx, stop := context.WithTimeout(t.Context(), time.Second)
		defer stop()
		response, e := client.Do(requestCtx, req)
		if e != nil || response.StatusCode != 200 {
			t.Fatalf("reuse failed: %v %v", response, e)
		}
		got := <-requests
		if got.Recipient.Port != 9 {
			t.Fatal("Contact changed instead of transport destination")
		}
	})
}

func TestSIPOutboundEarlyMediaDrainAndHandoff(t *testing.T) {
	for _, payloadType := range []uint8{0, 8, 9, 111} {
		t.Run(fmt.Sprint(payloadType), func(t *testing.T) {
			cfg := directSIPTestConfig()
			cfg.RTPBindIP = netip.MustParseAddr("127.0.0.1")
			offer := sipMediaOffer{RemoteAddress: cfg.RTPBindIP, RemotePort: 9, PayloadType: payloadType, Codec: "PCMU", PacketSamples: 160}
			m, err := openSIPRTPMedia(cfg, offer)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			m.startEarlyReceiver()
			sender, err := net.DialUDP("udp4", nil, m.conn.LocalAddr().(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			// 60 seconds of 20ms early RTP, delivered rapidly. No decoder/sequence
			// state may be primed by the pre-answer epoch (including encrypted bytes).
			for i := 0; i < 3000; i++ {
				p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: payloadType, SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 3}, Payload: make([]byte, 160)}
				raw, _ := p.Marshal()
				sender.Write(raw)
				if i%20 == 0 {
					time.Sleep(time.Millisecond)
				}
			}
			deadline := time.Now().Add(time.Second)
			for m.observations.earlyPackets.Load() < 3000 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			m.finishEarlyReceiver()
			snapshot := m.observations.snapshot(time.Now())
			if snapshot.EarlyPackets != 3000 || snapshot.Packets != 0 || snapshot.SequenceLoss != 0 {
				t.Fatalf("early contaminated conversation: %+v", snapshot)
			}
			sender.Write([]byte("fresh-final-answer"))
			buf := make([]byte, 4096)
			m.conn.SetReadDeadline(time.Now().Add(time.Second))
			n, _, err := m.conn.ReadFromUDP(buf)
			if err != nil || string(buf[:n]) != "fresh-final-answer" {
				t.Fatalf("stale media after handoff: %q %v", buf[:n], err)
			}
		})
	}
}
func TestSIPOutboundEarlyMediaCancellation(t *testing.T) {
	cfg := directSIPTestConfig()
	offer := sipMediaOffer{RemoteAddress: netip.MustParseAddr("127.0.0.1"), RemotePort: 9, PayloadType: 0, Codec: "PCMU", PacketSamples: 160}
	m, err := openSIPRTPMedia(cfg, offer)
	if err != nil {
		t.Fatal(err)
	}
	m.startEarlyReceiver()
	m.Close()
	select {
	case <-m.early.done:
	case <-time.After(time.Second):
		t.Fatal("early RTP owner leaked after cancellation")
	}
	m.finishEarlyReceiver()
	m.finishEarlyReceiver()
}

func TestSIPOutboundFreshAnswerReserveIsBoundedAndExpires(t *testing.T) {
	cfg := directSIPTestConfig()
	offer := sipMediaOffer{RemoteAddress: netip.MustParseAddr("127.0.0.1"), RemotePort: 9, PayloadType: 0, Codec: "PCMU", PacketSamples: 160}
	m, err := openSIPRTPMedia(cfg, offer)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.startEarlyReceiver()
	m.early.answered.Store(true)
	sender, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: m.localPort})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	for i := 0; i < 20; i++ {
		sender.Write([]byte{byte(i)})
	}
	until := time.Now().Add(time.Second)
	for m.observations.activationDropped.Load() < 12 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	m.finishEarlyReceiver()
	if len(m.early.pending) != 8 || m.observations.activationDropped.Load() != 12 {
		t.Fatal("activation not bounded")
	}
	buffer := make([]byte, 4096)
	for i := 12; i < 20; i++ {
		n, _, e := m.readPacket(buffer)
		if e != nil || n != 1 || buffer[0] != byte(i) {
			t.Fatalf("fresh reserve lost %d %v", i, e)
		}
	}
	m.early.pending <- sipEarlyPacket{data: []byte("stale"), at: time.Now().Add(-time.Second)}
	sender.Write([]byte("fresh"))
	m.conn.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := m.readPacket(buffer)
	if err != nil || string(buffer[:n]) != "fresh" || m.observations.activationDropped.Load() != 13 {
		t.Fatal("expired activation audio forwarded")
	}
}
