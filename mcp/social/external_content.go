package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func callbackNonce() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func publicAvatarAddress(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast() && ip.IsGlobalUnicast()
}

var avatarHTTPClient = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
	Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
	DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, addr := range ips {
			if !publicAvatarAddress(addr.IP) {
				return nil, fmt.Errorf("avatar destination is not public")
			}
		}
		var last error
		for _, addr := range ips {
			conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		if last == nil {
			last = fmt.Errorf("avatar host has no addresses")
		}
		return nil, last
	},
}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("too many avatar redirects")
	}
	return validateAvatarURL(req.URL)
}}

func validateAvatarURL(u *url.URL) error {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("invalid avatar URL")
	}
	return nil
}
func redactedMediaURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid URL"
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}
func mediaValidationError(message string) bool {
	for _, fragment := range []string{"choose a valid", "accepts one media", "cannot mix", "privacy_level"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func (a *App) avatarClientForRequest() *http.Client {
	if a.avatarClient != nil {
		return a.avatarClient
	}
	return avatarHTTPClient
}
