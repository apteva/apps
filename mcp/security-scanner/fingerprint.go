package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ServiceInfo is what fingerprinting learned about one open port.
type ServiceInfo struct {
	Port     int            `json:"port"`
	Protocol string         `json:"protocol"`
	Service  string         `json:"service"`
	Product  string         `json:"product,omitempty"`
	Version  string         `json:"version,omitempty"`
	TLS      bool           `json:"tls"`
	Banner   string         `json:"banner,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

func (s *ServiceInfo) set(key string, value any) {
	if s.Details == nil {
		s.Details = map[string]any{}
	}
	s.Details[key] = value
}

// prober opens connections through the rate limiter so fingerprinting
// respects the same packets-per-second budget as port scanning.
type prober struct {
	timeout  time.Duration
	limiter  *rateLimiter
	hostname string
}

func (p *prober) dial(ctx context.Context, addr string, port int) (net.Conn, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: p.timeout}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
}

var wellKnownTCP = map[int]string{
	21: "ftp", 22: "ssh", 23: "telnet", 25: "smtp", 53: "dns", 80: "http", 110: "pop3", 111: "rpcbind", 135: "msrpc",
	139: "netbios-ssn", 143: "imap", 389: "ldap", 443: "https", 445: "smb", 465: "smtps", 587: "submission",
	636: "ldaps", 873: "rsync", 993: "imaps", 995: "pop3s", 1433: "mssql", 1521: "oracle", 1883: "mqtt",
	2049: "nfs", 2375: "docker", 2376: "docker-tls", 2379: "etcd", 3306: "mysql", 3389: "rdp", 5432: "postgresql",
	5672: "amqp", 5900: "vnc", 5984: "couchdb", 5985: "winrm", 5986: "winrm-tls", 6379: "redis", 6443: "kubernetes-api",
	8080: "http-alt", 8443: "https-alt", 9042: "cassandra", 9092: "kafka", 9200: "elasticsearch", 10250: "kubelet",
	11211: "memcached", 27017: "mongodb",
}

var tlsFirstPorts = map[int]bool{443: true, 465: true, 636: true, 993: true, 995: true, 2376: true, 5986: true, 6443: true, 8443: true, 9443: true, 10250: true, 4443: true}

// fingerprintTCP identifies the service on an open port with at most a few
// read-only probes. It never authenticates or sends anything that changes state.
func (p *prober) fingerprintTCP(ctx context.Context, addr string, port int) ServiceInfo {
	info := ServiceInfo{Port: port, Protocol: "tcp", Service: wellKnownTCP[port]}
	if info.Service == "" {
		info.Service = "unknown"
	}

	if tlsFirstPorts[port] {
		if p.probeTLS(ctx, addr, port, &info) {
			return info
		}
	}

	// 1. Passive: many services speak first.
	if banner, err := p.readBanner(ctx, addr, port); err == nil && len(banner) > 0 {
		parseBanner(banner, &info)
		return info
	}

	// 2. Port-specific probes for services that wait for the client.
	switch port {
	case 6379:
		if p.probeRedis(ctx, addr, port, &info) {
			return info
		}
	case 11211:
		if p.probeMemcached(ctx, addr, port, &info) {
			return info
		}
	case 5432:
		if p.probePostgres(ctx, addr, port, &info) {
			return info
		}
	}

	// 3. Generic: HTTP, then TLS (and HTTP over TLS).
	if p.probeHTTP(ctx, addr, port, false, &info) {
		return info
	}
	if !tlsFirstPorts[port] && p.probeTLS(ctx, addr, port, &info) {
		return info
	}
	// 4. Last resort for non-standard ports: harmless Redis PING and Memcached version.
	if port != 6379 && p.probeRedis(ctx, addr, port, &info) {
		return info
	}
	if port != 11211 {
		p.probeMemcached(ctx, addr, port, &info)
	}
	return info
}

func (p *prober) readBanner(ctx context.Context, addr string, port int) ([]byte, error) {
	conn, err := p.dial(ctx, addr, port)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(p.timeout + 1500*time.Millisecond))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if n > 0 {
		return buf[:n], nil
	}
	return nil, err
}

var (
	reSSH      = regexp.MustCompile(`^SSH-([\d.]+)-(\S+)`)
	reOpenSSH  = regexp.MustCompile(`(?i)OpenSSH_([\w.]+)`)
	reDropbear = regexp.MustCompile(`(?i)dropbear_([\w.]+)`)
	reVsftpd   = regexp.MustCompile(`(?i)vsFTPd ([\d.]+)`)
	reProFTPD  = regexp.MustCompile(`(?i)ProFTPD ([\d.]+\w*)`)
	reFileZ    = regexp.MustCompile(`(?i)FileZilla Server(?: version)? ([\d.]+\w*)`)
	reExim     = regexp.MustCompile(`(?i)Exim ([\d.]+)`)
	reSendmail = regexp.MustCompile(`(?i)Sendmail ([\d.]+)`)
	reRFB      = regexp.MustCompile(`^RFB (\d{3}\.\d{3})`)
	reHTTPProd = regexp.MustCompile(`^([A-Za-z][\w .-]*?)/(\d[\w.-]*)`)
	reTitle    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reRedisVer = regexp.MustCompile(`redis_version:([\d.]+)`)
)

func parseBanner(b []byte, info *ServiceInfo) {
	text := strings.TrimSpace(printable(b))
	info.Banner = truncate(text, 300)
	switch {
	case len(b) > 0 && b[0] == 0xFF:
		info.Service, info.Product = "telnet", "Telnet"
	case strings.HasPrefix(text, "SSH-"):
		info.Service = "ssh"
		if m := reOpenSSH.FindStringSubmatch(text); m != nil {
			info.Product, info.Version = "OpenSSH", m[1]
		} else if m := reDropbear.FindStringSubmatch(text); m != nil {
			info.Product, info.Version = "Dropbear SSH", m[1]
		} else if m := reSSH.FindStringSubmatch(text); m != nil {
			info.Product = m[2]
		}
		if m := reSSH.FindStringSubmatch(text); m != nil {
			info.set("protocol_version", m[1])
		}
	case reRFB.MatchString(text):
		info.Service, info.Product = "vnc", "VNC"
		info.Version = reRFB.FindStringSubmatch(text)[1]
	case mysqlHandshake(b, info):
	case strings.HasPrefix(text, "220"):
		lower := strings.ToLower(text)
		if strings.Contains(lower, "ftp") || info.Service == "ftp" {
			info.Service = "ftp"
			switch {
			case reVsftpd.MatchString(text):
				info.Product, info.Version = "vsftpd", reVsftpd.FindStringSubmatch(text)[1]
			case reProFTPD.MatchString(text):
				info.Product, info.Version = "ProFTPD", reProFTPD.FindStringSubmatch(text)[1]
			case reFileZ.MatchString(text):
				info.Product, info.Version = "FileZilla Server", reFileZ.FindStringSubmatch(text)[1]
			case strings.Contains(lower, "pure-ftpd"):
				info.Product = "Pure-FTPd"
			case strings.Contains(lower, "microsoft ftp"):
				info.Product = "Microsoft FTP Service"
			}
		} else {
			info.Service = "smtp"
			switch {
			case reExim.MatchString(text):
				info.Product, info.Version = "Exim", reExim.FindStringSubmatch(text)[1]
			case strings.Contains(lower, "postfix"):
				info.Product = "Postfix"
			case reSendmail.MatchString(text):
				info.Product, info.Version = "Sendmail", reSendmail.FindStringSubmatch(text)[1]
			case strings.Contains(lower, "microsoft esmtp"):
				info.Product = "Microsoft Exchange SMTP"
			}
		}
	case strings.HasPrefix(text, "+OK"):
		info.Service = "pop3"
		if strings.Contains(strings.ToLower(text), "dovecot") {
			info.Product = "Dovecot"
		}
	case strings.HasPrefix(text, "* OK"):
		info.Service = "imap"
		if strings.Contains(strings.ToLower(text), "dovecot") {
			info.Product = "Dovecot"
		}
	}
}

// mysqlHandshake parses the MySQL/MariaDB initial handshake packet.
func mysqlHandshake(b []byte, info *ServiceInfo) bool {
	if len(b) < 6 {
		return false
	}
	payload := b[4:]
	if payload[0] == 0xFF && len(payload) > 3 {
		// Error packet, e.g. "Host 'x' is not allowed to connect to this MySQL server".
		msg := printable(payload[3:])
		if strings.Contains(strings.ToLower(msg), "mysql") || strings.Contains(strings.ToLower(msg), "mariadb") {
			info.Service, info.Product = "mysql", "MySQL"
			info.set("error", truncate(msg, 200))
			return true
		}
		return false
	}
	if payload[0] != 10 {
		return false
	}
	end := bytes.IndexByte(payload[1:], 0)
	if end <= 0 || end > 60 {
		return false
	}
	ver := string(payload[1 : 1+end])
	if !regexp.MustCompile(`^\d+\.\d+\.\d+`).MatchString(ver) {
		return false
	}
	info.Service = "mysql"
	if i := strings.Index(ver, "-MariaDB"); i >= 0 {
		v := strings.TrimPrefix(ver[:i], "5.5.5-")
		info.Product, info.Version = "MariaDB", v
	} else {
		info.Product, info.Version = "MySQL", strings.SplitN(ver, "-", 2)[0]
	}
	info.Banner = ver
	return true
}

func (p *prober) probeHTTP(ctx context.Context, addr string, port int, overTLS bool, info *ServiceInfo) bool {
	conn, err := p.dial(ctx, addr, port)
	if err != nil {
		return false
	}
	defer conn.Close()
	if overTLS {
		tc := tls.Client(conn, p.tlsConfig(0))
		_ = tc.SetDeadline(time.Now().Add(p.timeout + 3*time.Second))
		if err := tc.Handshake(); err != nil {
			return false
		}
		conn = tc
	}
	host := addr
	if p.hostname != "" {
		host = p.hostname
	}
	_ = conn.SetDeadline(time.Now().Add(p.timeout + 3*time.Second))
	fmt.Fprintf(conn, "GET / HTTP/1.0\r\nHost: %s\r\nUser-Agent: apteva-security-scanner/0.1\r\nAccept: */*\r\nConnection: close\r\n\r\n", host)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if overTLS {
		info.Service = "https"
	} else {
		info.Service = "http"
	}
	info.set("http_status", resp.StatusCode)
	server := resp.Header.Get("Server")
	if server != "" {
		info.set("server_header", server)
		info.Banner = truncate("Server: "+server, 300)
		applyServerHeader(server, info)
	}
	if powered := resp.Header.Get("X-Powered-By"); powered != "" {
		info.set("x_powered_by", powered)
	}
	if m := reTitle.FindSubmatch(body); m != nil {
		info.set("title", truncate(strings.TrimSpace(printable(m[1])), 120))
	}
	missing := []string{}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options"} {
		if resp.Header.Get(h) == "" {
			missing = append(missing, h)
		}
	}
	if overTLS && resp.Header.Get("Strict-Transport-Security") == "" {
		missing = append(missing, "Strict-Transport-Security")
	}
	info.set("missing_headers", missing)

	// Recognise unauthenticated JSON APIs from the body.
	var doc map[string]any
	if json.Unmarshal(body, &doc) == nil {
		if tag, _ := doc["tagline"].(string); strings.Contains(tag, "You Know, for Search") {
			info.Service, info.Product = "elasticsearch", "Elasticsearch"
			if v, ok := doc["version"].(map[string]any); ok {
				info.Version, _ = v["number"].(string)
			}
			info.set("unauthenticated", resp.StatusCode == 200)
		}
		if _, ok := doc["couchdb"]; ok {
			info.Service, info.Product = "couchdb", "CouchDB"
			info.Version, _ = doc["version"].(string)
			info.set("unauthenticated", resp.StatusCode == 200)
		}
	}
	if strings.HasPrefix(server, "Docker/") || resp.Header.Get("Api-Version") != "" && resp.Header.Get("Docker-Experimental") != "" {
		info.Service, info.Product = "docker", "Docker Engine API"
		info.set("unauthenticated", !overTLS)
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		info.set("auth_required", true)
	}
	return true
}

func applyServerHeader(server string, info *ServiceInfo) {
	first := strings.Fields(server)
	if len(first) == 0 {
		return
	}
	m := reHTTPProd.FindStringSubmatch(server)
	if m == nil {
		if info.Product == "" {
			info.Product = first[0]
		}
		return
	}
	name, ver := m[1], m[2]
	switch strings.ToLower(name) {
	case "apache":
		info.Product = "Apache httpd"
	case "microsoft-iis":
		info.Product = "Microsoft IIS"
	default:
		info.Product = name
	}
	info.Version = ver
}

func (p *prober) tlsConfig(maxVersion uint16) *tls.Config {
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10} //nolint:gosec // we inspect, not trust
	if maxVersion != 0 {
		cfg.MaxVersion = maxVersion
	}
	if p.hostname != "" {
		cfg.ServerName = p.hostname
	}
	return cfg
}

func (p *prober) probeTLS(ctx context.Context, addr string, port int, info *ServiceInfo) bool {
	conn, err := p.dial(ctx, addr, port)
	if err != nil {
		return false
	}
	tc := tls.Client(conn, p.tlsConfig(0))
	_ = tc.SetDeadline(time.Now().Add(p.timeout + 3*time.Second))
	if err := tc.Handshake(); err != nil {
		conn.Close()
		return false
	}
	state := tc.ConnectionState()
	tc.Close()
	info.TLS = true
	info.set("tls_version", tls.VersionName(state.Version))
	info.set("tls_cipher", tls.CipherSuiteName(state.CipherSuite))
	if len(state.PeerCertificates) > 0 {
		recordCert(state.PeerCertificates[0], info)
	}
	// Does the server still accept TLS 1.0/1.1?
	if conn2, err := p.dial(ctx, addr, port); err == nil {
		old := tls.Client(conn2, p.tlsConfig(tls.VersionTLS11))
		_ = old.SetDeadline(time.Now().Add(p.timeout + 3*time.Second))
		if old.Handshake() == nil {
			info.set("legacy_tls", tls.VersionName(old.ConnectionState().Version))
		}
		old.Close()
	}
	if !p.probeHTTP(ctx, addr, port, true, info) {
		if info.Service == "unknown" || info.Service == "" {
			info.Service = "tls"
		}
	}
	return true
}

func recordCert(cert *x509.Certificate, info *ServiceInfo) {
	info.set("cert_subject", cert.Subject.CommonName)
	info.set("cert_issuer", cert.Issuer.CommonName)
	info.set("cert_not_after", cert.NotAfter.UTC().Format(time.RFC3339))
	info.set("cert_dns_names", cert.DNSNames)
	info.set("cert_self_signed", cert.Subject.String() == cert.Issuer.String() && cert.CheckSignatureFrom(cert) == nil)
}

func (p *prober) probeRedis(ctx context.Context, addr string, port int, info *ServiceInfo) bool {
	conn, err := p.dial(ctx, addr, port)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(p.timeout + 2*time.Second))
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		return false
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(line)
	switch {
	case line == "+PONG":
		info.Service, info.Product = "redis", "Redis"
		info.set("unauthenticated", true)
		// INFO server is read-only and reveals the version.
		if _, err := conn.Write([]byte("INFO server\r\n")); err == nil {
			buf := make([]byte, 8192)
			n, _ := io.ReadAtLeast(r, buf, 1)
			if m := reRedisVer.FindSubmatch(buf[:n]); m != nil {
				info.Version = string(m[1])
			}
		}
		return true
	case strings.HasPrefix(line, "-NOAUTH"), strings.HasPrefix(line, "-DENIED"), strings.HasPrefix(line, "-ERR"):
		info.Service, info.Product = "redis", "Redis"
		info.set("unauthenticated", false)
		info.Banner = truncate(line, 200)
		return true
	}
	return false
}

func (p *prober) probeMemcached(ctx context.Context, addr string, port int, info *ServiceInfo) bool {
	conn, err := p.dial(ctx, addr, port)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(p.timeout + 2*time.Second))
	if _, err := conn.Write([]byte("version\r\n")); err != nil {
		return false
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "VERSION ") {
		return false
	}
	info.Service, info.Product = "memcached", "Memcached"
	info.Version = strings.TrimSpace(strings.TrimPrefix(line, "VERSION "))
	info.set("unauthenticated", true)
	return true
}

func (p *prober) probePostgres(ctx context.Context, addr string, port int, info *ServiceInfo) bool {
	conn, err := p.dial(ctx, addr, port)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(p.timeout + 2*time.Second))
	// SSLRequest: length 8, code 80877103.
	if _, err := conn.Write([]byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}); err != nil {
		return false
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(conn, b); err != nil || (b[0] != 'S' && b[0] != 'N') {
		return false
	}
	info.Service, info.Product = "postgresql", "PostgreSQL"
	info.set("ssl_supported", b[0] == 'S')
	return true
}

func printable(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == '\n' || c == '\r' || c == '\t':
			sb.WriteByte(' ')
		case c >= 32 && c < 127:
			sb.WriteByte(c)
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
