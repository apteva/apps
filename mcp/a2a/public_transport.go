package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func allowPublicLoopback(app *sdk.AppCtx) bool {
	return strings.EqualFold(strings.TrimSpace(app.Config().Get("allow_loopback_public_agents")), "true")
}

// Resolve and validate inside DialContext, then connect to the validated IP.
// URL validation alone cannot protect against DNS changing between requests.
func publicIPAllowed(ip net.IP, allowLoopback bool) bool {
	if ip.IsLoopback() {
		return allowLoopback
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range publicReservedNetworks {
		if prefix.Contains(addr) {
			return false
		}
	}
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
func publicDialContext(allowLoopback bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("public agent host has no addresses")
		}
		for _, ip := range ips {
			if !publicIPAllowed(ip.IP, allowLoopback) {
				return nil, errors.New("public agent resolves to a private or reserved network")
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, e := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			lastErr = e
		}
		return nil, lastErr
	}
}
func newPublicTransport(allowLoopback bool) *http.Transport {
	return &http.Transport{DialContext: publicDialContext(allowLoopback), ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second}
}

var productionPublicTransport = newPublicTransport(false)
var developmentPublicTransport = newPublicTransport(true)

func (a *App) publicHTTPClient(app *sdk.AppCtx) *http.Client {
	transport := productionPublicTransport
	if allowPublicLoopback(app) {
		transport = developmentPublicTransport
	}
	return &http.Client{Transport: transport, Timeout: configDuration(app, "peer_timeout_seconds", defaultPeerTimeoutSeconds), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var publicReservedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}
