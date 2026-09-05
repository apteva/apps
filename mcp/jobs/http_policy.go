package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var publicTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, errors.New("destination has no addresses")
		}
		for _, a := range addresses {
			if !publicIP(a.IP) {
				return nil, errors.New("private HTTP destinations are disabled")
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		var last error
		// Connect to a validated literal address, preventing DNS rebinding between
		// validation and connect. HTTP retains the original Host and TLS name.
		for _, a := range addresses {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
	return t
}()

func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96"} {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
func targetHTTPClient(cfg sdk.Config) *http.Client {
	base := *getDispatchClient()
	if strings.EqualFold(cfg.Get("allow_private_http"), "false") {
		base.Transport = publicTransport
	}
	// Arbitrary secrets can be present in custom headers and the body. Never
	// replay them to a different origin through 307/308 or custom redirects.
	base.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && (req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host) {
			return errors.New("cross-origin HTTP redirect rejected")
		}
		return nil
	}
	return &base
}
