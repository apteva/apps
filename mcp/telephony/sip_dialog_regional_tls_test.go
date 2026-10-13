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
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func TestSIPDialogTelnyxRegionalTLS(t *testing.T) {
	for _, tc := range []struct {
		name, certificate, identity, provider string
		outbound, untrusted, blocked, ok      bool
	}{
		{name: "inbound EU fallback", certificate: "sip.telnyx.eu", identity: "sip.telnyx.com", provider: "telnyx", ok: true},
		{name: "outbound EU fallback", certificate: "sip.telnyx.eu", identity: "sip.telnyx.com", provider: "telnyx", outbound: true, ok: true},
		{name: "inbound global unchanged", certificate: "sip.telnyx.com", identity: "sip.telnyx.com", provider: "telnyx", ok: true},
		{name: "explicit EU identity", certificate: "sip.telnyx.eu", identity: "sip.telnyx.eu", provider: "telnyx", ok: true},
		{name: "global fallback from EU", certificate: "sip.telnyx.com", identity: "sip.telnyx.eu", provider: "telnyx", ok: true},
		{name: "untrusted EU certificate", certificate: "sip.telnyx.eu", identity: "sip.telnyx.com", provider: "telnyx", untrusted: true},
		{name: "unrelated certificate", certificate: "attacker.example", identity: "sip.telnyx.com", provider: "telnyx"},
		{name: "lookalike certificate", certificate: "sip.telnyx.eu.attacker.example", identity: "sip.telnyx.com", provider: "telnyx"},
		{name: "custom identity stays exact", certificate: "sip.telnyx.eu", identity: "custom.example", provider: "telnyx"},
		{name: "other provider stays exact", certificate: "sip.telnyx.eu", identity: "sip.telnyx.com", provider: "generic"},
		{name: "destination allowlist preserved", certificate: "sip.telnyx.eu", identity: "sip.telnyx.com", provider: "telnyx", blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSIPTestCertificate(t, dir, tc.certificate)
			cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem"))
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			if !tc.untrusted {
				pem, err := os.ReadFile(filepath.Join(dir, "fullchain.pem"))
				if err != nil || !roots.AppendCertsFromPEM(pem) {
					t.Fatal("test trust root", err)
				}
			}
			peer, err := sipgo.NewUA()
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			server, err := sipgo.NewServer(peer)
			if err != nil {
				t.Fatal(err)
			}
			var requests, handshakes atomic.Int32
			handler := func(req *sip.Request, tx sip.ServerTransaction) {
				requests.Add(1)
				if err := tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil)); err != nil {
					t.Error(err)
				}
			}
			server.OnBye(handler)
			server.OnInvite(handler)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			listener.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready := make(chan struct{})
			go server.ListenAndServeTLS(context.WithValue(ctx, sipgo.ListenReadyCtxKey, sipgo.ListenReadyCtxValue(ready)), "tls", address, &tls.Config{
				Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
				GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
					handshakes.Add(1)
					if hello.ServerName != tc.identity && hello.ServerName != "sip.telnyx.eu" && hello.ServerName != "sip.telnyx.com" {
						t.Error("unexpected TLS identity", hello.ServerName)
					}
					return nil, nil
				},
			})
			select {
			case <-ready:
			case <-time.After(3 * time.Second):
				t.Fatal("TLS listener startup")
			}
			cfg := directSIPTestConfig()
			cfg.TLSCertFile, cfg.TLSKeyFile = filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
			cfg.PublicHost = tc.certificate
			cfg.clientRoots = roots
			cfg.TLSPeerNames = map[string]string{tc.provider: tc.identity}
			if !tc.blocked {
				cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
			}
			base, err := sipgo.NewUA(sipgo.WithUserAgenTLSConfig(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}))
			if err != nil {
				t.Fatal(err)
			}
			defer base.Close()
			g := &sipGateway{ua: base, cfg: cfg, byProviderCall: map[string]*sipSession{}, outboundByProviderCall: map[string]*outboundSIPSession{}}
			defer g.dialogTransports.close()
			client, err := sipgo.NewClient(base)
			if err != nil {
				t.Fatal(err)
			}
			client.TxRequester = sipDialogRequester{g}
			host, port, _ := net.SplitHostPort(address)
			uri := sip.Uri{Scheme: "sip", Host: host, UriParams: sip.NewParams()}
			fmt.Sscan(port, &uri.Port)
			uri.UriParams.Add("transport", "tls")
			id := "regional-dialog"
			call := &callRow{CarrierSlug: tc.provider}
			if tc.outbound {
				g.outboundByProviderCall[id] = &outboundSIPSession{carrierSlug: tc.provider, settings: didwwSIPSettings{host: tc.identity}}
			} else {
				g.byProviderCall[id] = &sipSession{call: call, dialog: &sipgo.DialogServerSession{Dialog: sipgo.Dialog{InviteRequest: sip.NewRequest(sip.INVITE, uri)}}}
			}
			for i, method := range []sip.RequestMethod{sip.BYE, sip.INVITE} {
				req := sip.NewRequest(method, uri)
				cid := sip.CallIDHeader(id)
				req.AppendHeader(&cid)
				to := sip.ToHeader{Address: uri, Params: sip.NewParams()}
				to.Params.Add("tag", "remote")
				req.AppendHeader(&to)
				requestCtx, stop := context.WithTimeout(t.Context(), 2*time.Second)
				response, err := client.Do(requestCtx, req)
				stop()
				if !tc.ok {
					if err == nil || requests.Load() != 0 || handshakes.Load() > 2 {
						t.Fatalf("unsafe request: response=%v err=%v requests=%d handshakes=%d", response, err, requests.Load(), handshakes.Load())
					}
					break
				}
				if err != nil || response.StatusCode != 200 || requests.Load() != int32(i+1) {
					t.Fatalf("%s: response=%v err=%v dispatched=%d", method, response, err, requests.Load())
				}
				if req.Destination() != address || req.Recipient.Host != host {
					t.Fatal("dialog routing changed")
				}
				expected := int32(1)
				if tc.identity != tc.certificate {
					expected = 2
				}
				if handshakes.Load() != expected {
					t.Fatalf("handshakes=%d want=%d; verified regional socket must be reused", handshakes.Load(), expected)
				}
			}
		})
	}
}
