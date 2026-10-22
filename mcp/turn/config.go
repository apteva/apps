package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	turnPort     = 3478
	tlsPort      = 5349
	relayPortMin = 49160
	relayPortMax = 49223
)

type config struct {
	publicIP                                      netip.Addr
	publicHost, bindIP, realm, secret             string
	ttl                                           time.Duration
	maxAllocations, perCredential, bytesPerSecond int
	allowedPeers                                  []netip.Prefix
	certificate                                   *tls.Certificate
	tlsCertificatePEM, tlsPrivateKeyPEM           string
}

func parseConfig(values sdk.Config) (config, error) {
	var c config
	var err error
	c.publicIP, err = netip.ParseAddr(values.Get("public_ip"))
	if err != nil || !c.publicIP.Is4() || !publicPeer(c.publicIP) {
		return c, errors.New("public_ip must be a public IPv4 address of the relay instance")
	}
	c.publicHost = strings.TrimSpace(values.Get("public_host"))
	if c.publicHost == "" {
		c.publicHost = c.publicIP.String()
	}
	if !validHost(c.publicHost) {
		return c, errors.New("public_host must be an IPv4 address or DNS hostname without a scheme, path or port")
	}
	c.tlsCertificatePEM, c.tlsPrivateKeyPEM = values.Get("tls_certificate_pem"), values.Get("tls_private_key_pem")
	c.certificate, err = parseCertificate(c.tlsCertificatePEM, c.tlsPrivateKeyPEM, c.publicHost, time.Now())
	if err != nil {
		return c, err
	}
	c.bindIP = values.Get("bind_ip")
	if c.bindIP == "" {
		c.bindIP = "0.0.0.0"
	}
	bind, err := netip.ParseAddr(c.bindIP)
	if err != nil || !bind.Is4() || bind.IsMulticast() {
		return c, errors.New("bind_ip must be a local IPv4 address")
	}
	c.secret = values.Get("shared_secret")
	if len(c.secret) < 32 || len(c.secret) > 1024 {
		return c, errors.New("shared_secret must contain 32 to 1024 characters")
	}
	c.realm = values.Get("realm")
	if c.realm == "" {
		c.realm = "apteva"
	}
	if len(c.realm) > 128 || strings.ContainsAny(c.realm, "\r\n\x00") {
		return c, errors.New("invalid authentication realm")
	}
	seconds, err := configInt(values, "credential_ttl_seconds", 3600, 60, 86400)
	if err != nil {
		return c, err
	}
	c.ttl = time.Duration(seconds) * time.Second
	if c.maxAllocations, err = configInt(values, "max_allocations", 64, 1, relayPortMax-relayPortMin+1); err != nil {
		return c, err
	}
	if c.perCredential, err = configInt(values, "allocations_per_credential", 4, 1, 16); err != nil {
		return c, err
	}
	if c.bytesPerSecond, err = configInt(values, "bytes_per_second", 1048576, 65536, 104857600); err != nil {
		return c, err
	}
	if text := strings.TrimSpace(values.Get("allowed_peer_cidrs")); text != "" {
		parts := strings.Split(text, ",")
		if len(parts) > 32 {
			return c, errors.New("at most 32 allowed peer networks are supported")
		}
		for _, part := range parts {
			p, err := netip.ParsePrefix(strings.TrimSpace(part))
			if err != nil || !p.Addr().Is4() {
				return c, errors.New("allowed_peer_cidrs must contain IPv4 CIDRs")
			}
			c.allowedPeers = append(c.allowedPeers, p.Masked())
		}
	}
	return c, nil
}

func configInt(values sdk.Config, name string, fallback, minValue, maxValue int) (int, error) {
	text := values.Get(name)
	if text == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < minValue || n > maxValue {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minValue, maxValue)
	}
	return n, nil
}

func validHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return publicPeer(ip)
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// TURN is a public relay, never a route into the host's private network.
var deniedPeers = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("168.63.129.16/32"), // Azure host/metadata services.
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func publicPeer(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.Is4() || !ip.IsGlobalUnicast() {
		return false
	}
	for _, p := range deniedPeers {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func (c config) peerAllowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !publicPeer(ip) {
		return false
	}
	if len(c.allowedPeers) == 0 {
		return true
	}
	for _, p := range c.allowedPeers {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
