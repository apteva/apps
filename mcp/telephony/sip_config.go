package main

import (
	"context"
	"crypto/tls"
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
)

type sipRuntimeHolder struct {
	startMu sync.Mutex
	mu      sync.RWMutex
	gateway *sipGateway
}

type sipGatewayConfig struct {
	Enabled       bool
	Transport     string
	ListenAddress string
	PublicHost    string
	PublicIP      netip.Addr
	TLSCertFile   string
	TLSKeyFile    string
	AllowedCIDRs  []netip.Prefix
	RTPBindIP     netip.Addr
	RTPPortMin    int
	RTPPortMax    int
	SRTPMode      string
	MaxSessions   int
	certificate   *sipTLSCertificate
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

var defaultSIPCarrierCIDRs = []string{
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
	cfg.AllowedCIDRs, err = parseSIPAllowedCIDRs(allowedCIDRs)
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
	return &tls.Config{GetCertificate: loader.getCertificate, MinVersion: tls.VersionTLS12}, nil
}

func (c sipGatewayConfig) sourceAllowed(address string) bool {
	host := address
	if parsedHost, _, err := net.SplitHostPort(address); err == nil {
		host = parsedHost
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range c.AllowedCIDRs {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
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
	var out []netip.Prefix
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !strings.Contains(value, "/") {
			value += "/32"
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil || !prefix.Addr().Is4() {
			return nil, fmt.Errorf("invalid IPv4 carrier CIDR %q", value)
		}
		if prefix.Bits() == 0 {
			return nil, errors.New("SIP allowed CIDRs cannot allow the entire internet")
		}
		out = append(out, prefix.Masked())
	}
	return out, nil
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
