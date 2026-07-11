package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// newPublicHTTPClient resolves and dials only globally routable addresses.
// Validation is repeated for redirects and in DialContext so a hostname cannot
// pass a preflight lookup and then rebind to a private address.
func newPublicHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("resolve %s: no addresses", host)
		}
		for _, resolved := range ips {
			if !isPublicIP(resolved.IP) {
				return nil, fmt.Errorf("refusing non-public address for %s", host)
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return validatePublicHTTPURL(req.URL)
		},
	}
}

func validatePublicHTTPURL(u *url.URL) error {
	if u == nil || u.Hostname() == "" {
		return errors.New("URL must include a host")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("URL must use http or https")
	}
	if u.User != nil {
		return errors.New("URL credentials are not allowed")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !isPublicIP(ip) {
		return errors.New("URL must not target a private or local address")
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() &&
		!ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}

func validSNSURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return nil, errors.New("invalid SNS URL")
	}
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return nil, errors.New("SNS URL must be a plain HTTPS URL")
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasPrefix(host, "sns.") ||
		(!strings.HasSuffix(host, ".amazonaws.com") && !strings.HasSuffix(host, ".amazonaws.com.cn")) {
		return nil, errors.New("SNS URL host is not an AWS SNS endpoint")
	}
	return u, nil
}
