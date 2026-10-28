package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var sipHostnamePattern = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*$`)

const (
	sipSRTPRequired  = "required"
	sipSRTPPreferred = "preferred"
	sipSRTPDisabled  = "disabled"

	defaultSIPListenAddress = "0.0.0.0:5061"
	defaultSIPRTPPortMin    = 20000
	defaultSIPRTPPortMax    = 20199
	defaultSIPMaxSessions   = 100

	defaultDIDWWTLSPeerName = "sip.didww.com"
)

type sipCarrierPrefix struct {
	Carrier string
	Prefix  netip.Prefix
}

type sipRuntimeHolder struct {
	startMu sync.Mutex
	mu      sync.RWMutex
	gateway *sipGateway
}

type sipGatewayConfig struct {
	socketPolicy  func(*net.UDPConn) sdk.SocketPolicyResult
	clientRoots   *x509.CertPool // nil uses system roots; tests inject a private CA.
	TLSPeerNames  map[string]string
	Enabled       bool
	Transport     string
	ListenAddress string
	PublicHost    string
	PublicIP      netip.Addr
	TLSCertFile   string
	TLSKeyFile    string
	AllowedCIDRs  []netip.Prefix
	// Operator ranges labelled carrier=cidr in sip_allowed_cidrs. They are
	// attributed before the built-in carrier ranges.
	CarrierCIDRs []sipCarrierPrefix
	RTPBindIP    netip.Addr
	RTPPortMin   int
	RTPPortMax   int
	SRTPMode     string
	MaxSessions  int
	certificate  *sipTLSCertificate
}

type sipConfigOptions struct {
	ForceEnabled     bool
	PublicURL        string
	CertificateRoots []string
	LookupNetIP      func(context.Context, string, string) ([]net.IP, error)
}

var defaultSIPCertificateRoots = []string{
	"/var/lib/apteva/certs",
	"/etc/letsencrypt/live",
	"/etc/apteva/certs",
}

// Default carrier networks, grouped by the carrier that owns them. The groups
// attribute an INVITE's source to one carrier so a route can only be reached
// from its own carrier's network (these ranges are shared by every customer
// of that carrier, so attribution is necessary but not sufficient).
var defaultSIPCarrierCIDRs = append(append(append([]string(nil),
	twilioSIPCarrierCIDRs...), telnyxSIPCarrierCIDRs...), didwwSIPCarrierCIDRs...)

var defaultSIPCarrierPrefixes = mustParseDefaultSIPCarrierPrefixes()

var twilioSIPCarrierCIDRs = []string{
	// Twilio Elastic SIP Trunking signaling and global media networks.
	"54.172.60.0/30",
	"54.244.51.0/30",
	"54.171.127.192/30",
	"35.156.191.128/30",
	"54.65.63.192/30",
	"54.169.127.128/30",
	"54.252.254.64/30",
	"177.71.206.192/30",
	"168.86.128.0/18",
}

var telnyxSIPCarrierCIDRs = []string{
	// Telnyx SIP signaling pools and media networks.
	"192.76.120.0/22",
	"64.16.250.10/32",
	"5.172.39.10/32",
	"5.172.39.25/32",
	"193.108.220.10/32",
	"193.108.220.25/32",
	"103.135.104.10/32",
	"103.135.104.25/32",
	"36.255.198.128/25",
	"50.114.136.128/25",
	"50.114.144.0/21",
	"64.16.226.0/23",
	"64.16.228.0/22",
	"64.16.248.0/23",
	"103.115.244.128/25",
	"185.246.41.128/25",
}

var didwwSIPCarrierCIDRs = []string{
	// DIDWW outbound SIP signaling and RTP networks, published in its
	// outbound SIP information. Keep restrictive even for outbound answers.
	"46.19.208.0/21",
	"185.238.172.0/22",
}

func loadSIPGatewayConfig(config sdk.Config) (sipGatewayConfig, error) {
	return loadSIPGatewayConfigWithOptions(config, sipConfigOptions{})
}

func loadSIPGatewayConfigWithOptions(config sdk.Config, options sipConfigOptions) (sipGatewayConfig, error) {
	cfg := sipGatewayConfig{
		Enabled:       options.ForceEnabled || configBool(config, "sip_enabled", "TELEPHONY_SIP_ENABLED", false),
		Transport:     strings.ToLower(strings.TrimSpace(configValue(config, "sip_transport", "TELEPHONY_SIP_TRANSPORT", "tls"))),
		ListenAddress: strings.TrimSpace(configValue(config, "sip_listen", "TELEPHONY_SIP_LISTEN", defaultSIPListenAddress)),
		PublicHost:    strings.TrimSpace(configValue(config, "sip_public_host", "TELEPHONY_SIP_PUBLIC_HOST", "")),
		TLSCertFile:   strings.TrimSpace(configValue(config, "sip_tls_cert_file", "TELEPHONY_SIP_TLS_CERT_FILE", "")),
		TLSKeyFile:    strings.TrimSpace(configValue(config, "sip_tls_key_file", "TELEPHONY_SIP_TLS_KEY_FILE", "")),
		RTPPortMin:    configInt(config, "sip_rtp_port_min", "TELEPHONY_SIP_RTP_PORT_MIN", defaultSIPRTPPortMin),
		RTPPortMax:    configInt(config, "sip_rtp_port_max", "TELEPHONY_SIP_RTP_PORT_MAX", defaultSIPRTPPortMax),
		SRTPMode:      strings.ToLower(strings.TrimSpace(configValue(config, "sip_srtp", "TELEPHONY_SIP_SRTP", sipSRTPPreferred))),
		MaxSessions:   configInt(config, "sip_max_sessions", "TELEPHONY_SIP_MAX_SESSIONS", defaultSIPMaxSessions),
	}
	if !cfg.Enabled {
		return cfg, nil
	}

	var err error
	if cfg.PublicHost == "" {
		cfg.PublicHost, err = sipHostFromPublicURL(options.PublicURL)
		if err != nil {
			return cfg, err
		}
	}
	if len(cfg.PublicHost) > 253 || !sipHostnamePattern.MatchString(cfg.PublicHost) {
		return cfg, errors.New("SIP public host must be the certificate hostname without a scheme or path")
	}

	publicIP := strings.TrimSpace(configValue(config, "sip_public_ip", "TELEPHONY_SIP_PUBLIC_IP", ""))
	if publicIP == "" {
		cfg.PublicIP, err = resolveSIPPublicIPv4(cfg.PublicHost, options.LookupNetIP)
	} else {
		cfg.PublicIP, err = netip.ParseAddr(publicIP)
	}
	if err != nil {
		return cfg, fmt.Errorf("SIP public host %s must resolve to a public IPv4 address; use sip_public_ip only for unusual NAT: %w",
			cfg.PublicHost, err)
	}
	if !usableSIPPublicIPv4(cfg.PublicIP) {
		return cfg, fmt.Errorf("SIP public host %s resolved to non-public address %s; use sip_public_ip only for unusual NAT",
			cfg.PublicHost, cfg.PublicIP)
	}
	bindIP := strings.TrimSpace(configValue(config, "sip_rtp_bind_ip", "TELEPHONY_SIP_RTP_BIND_IP", "0.0.0.0"))
	cfg.RTPBindIP, err = netip.ParseAddr(bindIP)
	if err != nil || !cfg.RTPBindIP.Is4() {
		return cfg, errors.New("SIP RTP bind address must be an IPv4 address")
	}
	if _, _, err := net.SplitHostPort(cfg.ListenAddress); err != nil {
		return cfg, fmt.Errorf("SIP listen address must be host:port: %w", err)
	}
	switch cfg.Transport {
	case "tls":
		if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
			return cfg, errors.New("both SIP TLS certificate and key overrides must be provided together")
		}
		if cfg.TLSCertFile == "" {
			cfg.TLSCertFile, cfg.TLSKeyFile, err = discoverSIPCertificate(cfg.PublicHost, options.CertificateRoots)
			if err != nil {
				return cfg, err
			}
		}
	case "udp", "tcp":
		if !configBool(config, "sip_allow_insecure_signaling", "TELEPHONY_SIP_ALLOW_INSECURE_SIGNALING", false) {
			return cfg, errors.New("UDP/TCP SIP signaling requires sip_allow_insecure_signaling=true")
		}
	default:
		return cfg, errors.New("SIP transport must be tls, tcp, or udp")
	}
	if cfg.RTPPortMin < 1024 || cfg.RTPPortMax > 65535 || cfg.RTPPortMin >= cfg.RTPPortMax {
		return cfg, errors.New("direct SIP RTP port range must be between 1024 and 65535")
	}
	if cfg.RTPPortMin%2 != 0 {
		cfg.RTPPortMin++
	}
	if cfg.RTPPortMax-cfg.RTPPortMin < 2 {
		return cfg, errors.New("direct SIP RTP port range must contain at least two ports")
	}
	switch cfg.SRTPMode {
	case sipSRTPRequired, sipSRTPPreferred, sipSRTPDisabled:
	default:
		return cfg, errors.New("SIP media encryption must be required, preferred, or disabled")
	}
	if cfg.MaxSessions < 1 || cfg.MaxSessions > 10000 {
		return cfg, errors.New("SIP maximum sessions must be between 1 and 10000")
	}
	allowedCIDRs := configValue(config, "sip_allowed_cidrs", "TELEPHONY_SIP_ALLOWED_CIDRS", "")
	if strings.TrimSpace(allowedCIDRs) == "" {
		allowedCIDRs = strings.Join(defaultSIPCarrierCIDRs, ",")
	}
	// Twilio may advertise an IP Contact while presenting a DNS-only SIP
	// certificate. Keep dialog routing intact and verify against its carrier
	// identity when reconnecting, just as for Telnyx. Operator overrides below
	// remain authoritative; never derive a trusted identity from the Contact.
	// DIDWW delivers inbound calls from its sip.didww.com signaling pool; without
	// an identity a BYE after a dropped TLS connection could not reconnect.
	cfg.TLSPeerNames = map[string]string{"telnyx": "sip.telnyx.com", "twilio": "sip.twilio.com", "didww": defaultDIDWWTLSPeerName}
	if raw := strings.TrimSpace(config["sip_tls_peer_names"]); raw != "" {
		if len(raw) > 4096 || json.Unmarshal([]byte(raw), &cfg.TLSPeerNames) != nil {
			return cfg, errors.New("invalid SIP TLS peer names")
		}
	}
	for provider, name := range cfg.TLSPeerNames {
		if len(provider) > 64 || len(name) > 253 || !sipHostnamePattern.MatchString(name) || net.ParseIP(name) != nil || !strings.Contains(name, ".") {
			return cfg, errors.New("SIP TLS peer names must map carriers to certificate hostnames")
		}
	}
	cfg.AllowedCIDRs, cfg.CarrierCIDRs, err = parseSIPCarrierCIDRs(allowedCIDRs)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c sipGatewayConfig) tlsConfig() (*tls.Config, error) {
	if c.Transport != "tls" {
		return nil, nil
	}
	loader := c.certificate
	if loader == nil {
		loader = &sipTLSCertificate{cfg: c}
	}
	if _, err := loader.getCertificate(nil); err != nil {
		return nil, err
	}
	return &tls.Config{RootCAs: c.clientRoots, GetCertificate: loader.getCertificate, MinVersion: tls.VersionTLS12}, nil
}

func sipSourceAddr(address string) (netip.Addr, bool) {
	host := address
	if parsedHost, _, err := net.SplitHostPort(address); err == nil {
		host = parsedHost
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

func (c sipGatewayConfig) sourceAllowed(address string) bool {
	ip, ok := sipSourceAddr(address)
	if !ok {
		return false
	}
	for _, prefix := range c.AllowedCIDRs {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// sourceCarrier attributes an allowed signaling source to the carrier owning
// its network: operator-labelled ranges first, then the published defaults.
// "" means the source is allowed (an unlabelled operator range) but cannot be
// attributed to one carrier.
func (c sipGatewayConfig) sourceCarrier(address string) string {
	ip, ok := sipSourceAddr(address)
	if !ok {
		return ""
	}
	for _, ranges := range [][]sipCarrierPrefix{c.CarrierCIDRs, defaultSIPCarrierPrefixes} {
		for _, item := range ranges {
			if item.Prefix.Contains(ip) {
				return item.Carrier
			}
		}
	}
	return ""
}

func mustParseDefaultSIPCarrierPrefixes() []sipCarrierPrefix {
	var out []sipCarrierPrefix
	for carrier, ranges := range map[string][]string{
		"twilio": twilioSIPCarrierCIDRs, "telnyx": telnyxSIPCarrierCIDRs, "didww": didwwSIPCarrierCIDRs,
	} {
		for _, value := range ranges {
			out = append(out, sipCarrierPrefix{Carrier: carrier, Prefix: netip.MustParsePrefix(value).Masked()})
		}
	}
	return out
}

func (c sipGatewayConfig) endpointURI() string {
	_, port, _ := net.SplitHostPort(c.ListenAddress)
	uri := "sip:" + c.PublicHost
	if (c.Transport == "tls" && port != "5061") || (c.Transport != "tls" && port != "5060") {
		uri += ":" + port
	}
	return uri + ";transport=" + c.Transport
}

func sipHostFromPublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("configure the Apteva public URL before enabling direct SIP")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("Apteva public URL does not contain a valid hostname")
	}
	if parsed.Scheme != "https" {
		return "", errors.New("Apteva public URL must use HTTPS before enabling direct SIP")
	}
	return strings.TrimSuffix(strings.ToLower(parsed.Hostname()), "."), nil
}

func usableSIPPublicIPv4(address netip.Addr) bool {
	return address.Is4() && address.IsGlobalUnicast() && !address.IsPrivate() &&
		!address.IsLoopback() && !address.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(address)
}

func resolveSIPPublicIPv4(host string, lookup func(context.Context, string, string) ([]net.IP, error)) (netip.Addr, error) {
	if parsed, err := netip.ParseAddr(host); err == nil {
		return parsed.Unmap(), nil
	}
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIP
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addresses, err := lookup(ctx, "ip4", host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, address := range addresses {
		if parsed, ok := netip.AddrFromSlice(address); ok {
			parsed = parsed.Unmap()
			if usableSIPPublicIPv4(parsed) {
				return parsed, nil
			}
		}
	}
	return netip.Addr{}, fmt.Errorf("%s has no public IPv4 DNS record", host)
}

func discoverSIPCertificate(host string, roots []string) (string, string, error) {
	useManagedCache := len(roots) == 0
	if useManagedCache {
		roots = defaultSIPCertificateRoots
	}
	for _, root := range roots {
		certificate := filepath.Join(root, host, "fullchain.pem")
		privateKey := filepath.Join(root, host, "privkey.pem")
		if regularFile(certificate) && regularFile(privateKey) {
			return certificate, privateKey, nil
		}
	}
	if useManagedCache {
		for _, cacheDir := range sipAutocertCacheDirs() {
			for _, name := range []string{host, host + "+rsa"} {
				cacheFile := filepath.Join(cacheDir, name)
				if regularFile(cacheFile) {
					// autocert.DirCache stores the certificate chain and private
					// key in the same PEM file. tls.LoadX509KeyPair accepts the
					// same path for both inputs.
					return cacheFile, cacheFile, nil
				}
			}
		}
	}
	return "", "", fmt.Errorf(
		"no managed TLS certificate found for %s; expected the Apteva ingress cache or <cert-dir>/%s/fullchain.pem and privkey.pem",
		host, host)
}

func sipAutocertCacheDirs() []string {
	var directories []string
	if configured := strings.TrimSpace(os.Getenv("APTEVA_ACME_CACHE_DIR")); configured != "" {
		directories = append(directories, configured)
	}
	if home := strings.TrimSpace(os.Getenv("APTEVA_HOME")); home != "" {
		directories = append(directories, filepath.Join(home, "ingress-certs"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		directories = append(directories, filepath.Join(home, ".apteva", "ingress-certs"))
	}
	return directories
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func parseSIPAllowedCIDRs(raw string) ([]netip.Prefix, error) {
	out, _, err := parseSIPCarrierCIDRs(raw)
	return out, err
}

var sipCarrierLabelPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Entries are cidr or carrier=cidr (for example twilio=203.0.113.0/24). A
// label binds the range to that carrier's routes; unlabelled operator ranges
// stay allowed but are not attributed to a carrier.
func parseSIPCarrierCIDRs(raw string) ([]netip.Prefix, []sipCarrierPrefix, error) {
	var out []netip.Prefix
	var labelled []sipCarrierPrefix
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		carrier := ""
		if label, cidr, ok := strings.Cut(value, "="); ok {
			carrier = strings.ToLower(strings.TrimSpace(label))
			value = strings.TrimSpace(cidr)
			if !sipCarrierLabelPattern.MatchString(carrier) {
				return nil, nil, fmt.Errorf("invalid SIP carrier label %q", label)
			}
		}
		if !strings.Contains(value, "/") {
			value += "/32"
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil || !prefix.Addr().Is4() {
			return nil, nil, fmt.Errorf("invalid IPv4 carrier CIDR %q", value)
		}
		if prefix.Bits() == 0 {
			return nil, nil, errors.New("SIP allowed CIDRs cannot allow the entire internet")
		}
		out = append(out, prefix.Masked())
		if carrier != "" {
			labelled = append(labelled, sipCarrierPrefix{Carrier: carrier, Prefix: prefix.Masked()})
		}
	}
	return out, labelled, nil
}

func configValue(config sdk.Config, key, envName, fallback string) string {
	if config != nil {
		if value := strings.TrimSpace(config.Get(key)); value != "" {
			return value
		}
	}
	if value := os.Getenv(envName); value != "" {
		return value
	}
	return fallback
}

func configInt(config sdk.Config, key, envName string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(configValue(config, key, envName, "")))
	if err != nil {
		return fallback
	}
	return value
}

func configBool(config sdk.Config, key, envName string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(configValue(config, key, envName, "")))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
