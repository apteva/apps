package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Each fallback pool has one operator/provider configured certificate identity.
// The destination remains the dialog's route/contact IP; only TLS SNI and name
// verification use the configured hostname. No global ServerName override and
// no InsecureSkipVerify, including when the original connection disappeared.
type sipDialogTransports struct {
	mu     sync.Mutex
	closed bool
	peers  map[string]*sipgo.UserAgent
}

type sipDialogRequester struct{ gateway *sipGateway }

func (r sipDialogRequester) Request(ctx context.Context, req *sip.Request) (sip.ClientTransaction, error) {
	g := r.gateway
	ua := g.ua
	if strings.EqualFold(req.Transport(), "tls") && req.To() != nil && req.To().Params.Has("tag") {
		var source, peerName string
		if s := g.sessionByProviderCall(sipCallID(req)); s != nil {
			source = s.dialog.InviteRequest.Source()
			peerName = g.cfg.TLSPeerNames[s.call.CarrierSlug]
		} else if s := g.outboundSessionByProviderCall(sipCallID(req)); s != nil {
			peerName = s.settings.host
		}
		// A route set is authoritative. Only an unrouted dialog can reuse the
		// incoming socket irrespective of a differently advertised Contact port.
		if req.Route() == nil && source != "" {
			if conn, err := ua.TransportLayer().GetConnection("tls", source); err == nil {
				req.SetDestination(source)
				defer conn.TryClose()
				return requestSIPOnUA(ctx, ua, req)
			}
		}
		if conn, err := ua.TransportLayer().GetConnection("tls", req.Destination()); err == nil {
			defer conn.TryClose()
			return requestSIPOnUA(ctx, ua, req)
		}
		host, _, err := net.SplitHostPort(req.Destination())
		if err == nil && net.ParseIP(host) != nil && peerName != "" {
			if !g.cfg.sourceAllowed(req.Destination()) {
				return nil, errors.New("SIP dialog TLS destination outside carrier networks")
			}
			ua, err = g.tlsDialogPeer(peerName)
			if err != nil {
				return nil, err
			}
		}
	}
	return requestSIPOnUA(ctx, ua, req)
}
func requestSIPOnUA(ctx context.Context, ua *sipgo.UserAgent, req *sip.Request) (sip.ClientTransaction, error) {
	if req.IsAck() {
		return nil, ua.TransportLayer().WriteMsg(req)
	}
	return ua.TransactionLayer().Request(ctx, req)
}
func (g *sipGateway) tlsDialogPeer(name string) (*sipgo.UserAgent, error) {
	p := &g.dialogTransports
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("SIP gateway stopping")
	}
	if ua := p.peers[name]; ua != nil {
		return ua, nil
	}
	if len(p.peers) >= 32 {
		return nil, errors.New("SIP TLS identity pool full")
	}
	cfg, err := g.cfg.tlsConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errors.New("SIP TLS unavailable")
	}
	cfg = cfg.Clone()
	cfg.ServerName = name
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("Apteva-Telephony"), sipgo.WithUserAgentHostname(g.cfg.PublicHost), sipgo.WithUserAgenTLSConfig(cfg))
	if err != nil {
		return nil, err
	}
	server, err := sipgo.NewServer(ua)
	if err != nil {
		ua.Close()
		return nil, err
	}
	g.registerServerHandlers(server)
	if p.peers == nil {
		p.peers = map[string]*sipgo.UserAgent{}
	}
	p.peers[name] = ua
	return ua, nil
}
func (p *sipDialogTransports) close() {
	p.mu.Lock()
	p.closed = true
	peers := p.peers
	p.peers = nil
	p.mu.Unlock()
	for _, ua := range peers {
		_ = ua.Close()
	}
}
