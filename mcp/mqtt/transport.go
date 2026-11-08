package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

type transportConfig struct {
	mode, host, bind, provider, certFile, keyFile, cacheDir, email, directory   string
	port, publicPort, reloadSeconds, renewDays, handshakeSeconds, maxHandshakes int
}

func strictInt(ctx *sdk.AppCtx, name string, def, min, max int) (int, error) {
	raw := configString(ctx, name, strconv.Itoa(def))
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, min, max)
	}
	return n, nil
}

func parseTransport(ctx *sdk.AppCtx) (transportConfig, error) {
	c := transportConfig{mode: configString(ctx, "transport_mode", "tls"), provider: configString(ctx, "tls_provider", "files"), host: strings.TrimSpace(configString(ctx, "advertised_host", "")), bind: configString(ctx, "bind_interface", "")}
	var err error
	if c.mode != "tls" && c.mode != "local" {
		return c, errors.New("transport_mode must be tls or local")
	}
	if c.bind != "" && net.ParseIP(c.bind) == nil {
		return c, errors.New("bind_interface must be an IP address")
	}
	portName, defaultPort := "tls_listen_port", 8883
	if c.mode == "local" {
		portName, defaultPort = "listen_port", 1883
		if c.bind == "" {
			c.bind = "127.0.0.1"
		}
		if !localIP(c.bind) {
			return c, errors.New("local transport must bind a loopback or private IP")
		}
		if c.host == "" {
			c.host = c.bind
		}
		if c.host != "localhost" && !localIP(c.host) {
			return c, errors.New("local transport can advertise only localhost or a private IP; use TLS for public endpoints")
		}
	}
	c.host, err = validatePublicHost(c.host)
	if err != nil {
		return c, err
	}
	c.port, err = strictInt(ctx, portName, defaultPort, 0, 65535)
	if err != nil {
		return c, err
	}
	if c.port != 0 && c.port != defaultPort {
		return c, fmt.Errorf("%s must be %d for managed installs or 0 for tests", portName, defaultPort)
	}
	advertisedDefault := defaultPort
	if c.port == 0 {
		advertisedDefault = 0
	}
	c.publicPort, err = strictInt(ctx, "advertised_port", advertisedDefault, 0, 65535)
	if err != nil {
		return c, err
	}
	if c.publicPort == 0 && c.port != 0 {
		return c, errors.New("advertised_port 0 is only valid with a test listener port 0")
	}
	c.handshakeSeconds, err = strictInt(ctx, "tls_handshake_timeout_seconds", 15, 1, 120)
	if err != nil {
		return c, err
	}
	c.maxHandshakes, err = strictInt(ctx, "tls_max_pending_handshakes", 128, 1, 4096)
	if err != nil {
		return c, err
	}
	if c.mode == "local" {
		return c, nil
	}
	c.reloadSeconds, err = strictInt(ctx, "tls_reload_seconds", 30, 1, 3600)
	if err != nil {
		return c, err
	}
	c.renewDays, err = strictInt(ctx, "tls_renew_before_days", 30, 1, 30)
	if err != nil {
		return c, err
	}
	switch c.provider {
	case "files":
		c.certFile = configString(ctx, "tls_cert_file", "")
		c.keyFile = configString(ctx, "tls_key_file", "")
		if c.certFile == "" || c.keyFile == "" {
			return c, errors.New("TLS requires tls_cert_file and tls_key_file, or tls_provider=acme")
		}
	case "acme":
		if net.ParseIP(c.host) != nil || !strings.Contains(c.host, ".") || strings.HasSuffix(c.host, ".local") {
			return c, errors.New("ACME requires a public DNS hostname")
		}
		if c.publicPort != 443 {
			return c, errors.New("ACME TLS-ALPN-01 requires advertised_port=443 and public TCP 443 forwarding to the TLS listener")
		}
		if configString(ctx, "acme_accept_terms", "false") != "true" {
			return c, errors.New("ACME requires explicit acme_accept_terms=true")
		}
		c.email = configString(ctx, "acme_email", "")
		c.directory = configString(ctx, "acme_directory_url", acme.LetsEncryptURL)
		u, e := url.Parse(c.directory)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return c, errors.New("acme_directory_url must be an HTTPS directory URL")
		}
		c.cacheDir = configString(ctx, "acme_cache_dir", "")
		if c.cacheDir == "" {
			if ctx.DataDir() == "" {
				return c, errors.New("ACME requires the app's persistent data directory or an explicit acme_cache_dir")
			}
			c.cacheDir = filepath.Join(ctx.DataDir(), "mqtt-acme")
		}
	default:
		return c, errors.New("tls_provider must be files or acme")
	}
	return c, nil
}

func localIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func validatePublicHost(host string) (string, error) {
	if host == "" {
		return "", errors.New("advertised_host is required for TLS enrollment")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() {
			return "", errors.New("advertised_host cannot be a wildcard or multicast address")
		}
		return ip.String(), nil
	}
	if len(host) > 253 || strings.ContainsAny(host, ":/@?#[]\\") || strings.ContainsRune(host, 0) || strings.TrimSpace(host) != host {
		return "", errors.New("advertised_host must be a DNS hostname or IP without a scheme or port")
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid advertised_host DNS label")
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return "", errors.New("advertised_host must use ASCII DNS labels (punycode for international names)")
			}
		}
	}
	return host, nil
}

type certificateService struct {
	cfg        transportConfig
	mu         sync.RWMutex
	cert       *tls.Certificate
	lastError  string
	lastLoaded time.Time
	manager    *autocert.Manager
	ctx        context.Context
	cancel     context.CancelFunc
}

func newCertificateService(cfg transportConfig) (*certificateService, *tls.Config, error) {
	s := &certificateService{cfg: cfg}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	config := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"mqtt"}, GetCertificate: s.getCertificate}
	if cfg.provider == "files" {
		if err := s.reload(); err != nil {
			return nil, nil, fmt.Errorf("initial MQTT TLS certificate: %w", err)
		}
	} else {
		if err := os.MkdirAll(cfg.cacheDir, 0700); err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(cfg.cacheDir)
		if err != nil {
			return nil, nil, err
		}
		if info.Mode().Perm()&0077 != 0 {
			return nil, nil, errors.New("ACME cache directory must be private (mode 0700)")
		}
		s.manager = &autocert.Manager{Prompt: autocert.AcceptTOS, Cache: autocert.DirCache(cfg.cacheDir), HostPolicy: autocert.HostWhitelist(cfg.host), Email: cfg.email, RenewBefore: time.Duration(cfg.renewDays) * 24 * time.Hour, Client: &acme.Client{DirectoryURL: cfg.directory, HTTPClient: &http.Client{Timeout: 20 * time.Second, Transport: cancelTransport{ctx: s.ctx, base: http.DefaultTransport}}}}
		config.NextProtos = append(config.NextProtos, acme.ALPNProto)
	}
	return s, config, nil
}

func validCertificate(cert *tls.Certificate, host string, now time.Time) error {
	if cert == nil || len(cert.Certificate) == 0 {
		return errors.New("certificate unavailable")
	}
	leaf := cert.Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return err
		}
		leaf = parsed
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return errors.New("certificate is expired or not yet valid")
	}
	if len(leaf.ExtKeyUsage) > 0 {
		server := false
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
				server = true
			}
		}
		if !server {
			return errors.New("certificate does not permit TLS server authentication")
		}
	}
	if err := leaf.VerifyHostname(host); err != nil {
		return errors.New("certificate does not match advertised_host")
	}
	return nil
}

func (s *certificateService) reload() error {
	cert, err := tls.LoadX509KeyPair(s.cfg.certFile, s.cfg.keyFile)
	if err == nil {
		err = validCertificate(&cert, s.cfg.host, time.Now())
		if err == nil {
			cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			s.lastError = "certificate files are unreadable"
		} else {
			s.lastError = "certificate reload failed: " + err.Error()
		}
		return err
	}
	s.cert = &cert
	s.lastError = ""
	s.lastLoaded = time.Now().UTC()
	return nil
}

func (s *certificateService) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if hello.ServerName != "" && !strings.EqualFold(strings.TrimSuffix(hello.ServerName, "."), s.cfg.host) {
		return nil, errors.New("unconfigured MQTT TLS hostname")
	}
	if s.manager != nil && len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == acme.ALPNProto {
		return s.manager.GetCertificate(hello)
	}
	// Ordinary handshakes never wait for issuance or a CA network request.
	// The worker provisions certificates before enrollment becomes ready.
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := validCertificate(s.cert, s.cfg.host, time.Now()); err != nil {
		return nil, err
	}
	return s.cert, nil
}

func (s *certificateService) status() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ready := s.cert != nil && validCertificate(s.cert, s.cfg.host, time.Now()) == nil
	out := map[string]any{"provider": s.cfg.provider, "ready": ready, "last_error": s.lastError, "renew_before_days": s.cfg.renewDays}
	if s.cert != nil {
		out["expires_at"] = s.cert.Leaf.NotAfter.UTC().Format(time.RFC3339)
		out["loaded_at"] = s.lastLoaded.Format(time.RFC3339)
		out["renewal_due"] = time.Until(s.cert.Leaf.NotAfter) < time.Duration(s.cfg.renewDays)*24*time.Hour
	}
	return out
}

func (s *certificateService) run(ctx context.Context) error {
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	defer s.cancel()
	tick := time.NewTicker(time.Duration(s.cfg.reloadSeconds) * time.Second)
	defer tick.Stop()
	refresh := func() {
		if s.manager == nil {
			_ = s.reload()
		} else {
			_ = s.refreshACME()
		}
	}
	// ACME proactively provisions/refreshes enrollment metadata; its TLS listener
	// must already be serving so the CA can perform TLS-ALPN-01 validation.
	if s.manager != nil {
		refresh()
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			refresh()
		}
	}
}

func certificateHello(host string) *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{ServerName: host, SupportedProtos: []string{"mqtt"}, CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}, SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256}, SupportedCurves: []tls.CurveID{tls.CurveP256}}
}

type cancelTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t cancelTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	if t.ctx.Err() != nil {
		stop()
		cancel()
		return nil, t.ctx.Err()
	}
	response, err := t.base.RoundTrip(r.Clone(ctx))
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	response.Body = &cancelBody{ReadCloser: response.Body, cancel: cancel, stop: stop}
	return response, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	stop   func() bool
}

func (b *cancelBody) Close() error { defer b.cancel(); defer b.stop(); return b.ReadCloser.Close() }

func (s *certificateService) refreshACME() error {
	cert, err := s.manager.GetCertificate(certificateHello(s.cfg.host))
	if err == nil {
		err = validCertificate(cert, s.cfg.host, time.Now())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastError = "ACME certificate unavailable or renewal failed"
		return err
	}
	copy := *cert
	copy.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	s.cert = &copy
	s.lastLoaded = time.Now().UTC()
	s.lastError = ""
	return nil
}
