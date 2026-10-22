package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

func testCertificate(t *testing.T, host string, before, after time.Time) (string, string, *x509.CertPool) {
	t.Helper()
	rootPub, rootKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "TURN test authority"}, NotBefore: before, NotAfter: after, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, rootPub, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err = x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: before, NotAfter: after, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, pub, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})) + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}))
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	roots := x509.NewCertPool()
	roots.AddCert(root)
	return chain, privateKey, roots
}

func TestTLSCertificateValidation(t *testing.T) {
	now := time.Now()
	chain, key, _ := testCertificate(t, "turn.example.com", now.Add(-time.Hour), now.Add(48*time.Hour))
	values := validConfig()
	values["tls_certificate_pem"] = chain
	values["tls_private_key_pem"] = key
	cfg, err := parseConfig(values)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.tlsValid(now) || cfg.certificateStatus(now)["configured"] != true {
		t.Fatal("valid TLS certificate rejected")
	}
	for _, test := range []struct{ cert, key, host string }{{chain, "", "turn.example.com"}, {"", key, "turn.example.com"}, {chain, key, "wrong.example.com"}, {"invalid", key, "turn.example.com"}, {chain, "invalid", "turn.example.com"}} {
		if _, err := parseCertificate(test.cert, test.key, test.host, now); err == nil {
			t.Fatal("invalid certificate material accepted")
		}
	}
	if _, err := parseCertificate(chain, key, "turn.example.com", now.Add(72*time.Hour)); err == nil {
		t.Fatal("expired certificate accepted")
	}
	if _, err := parseCertificate(chain, key, "turn.example.com", now.Add(-2*time.Hour)); err == nil {
		t.Fatal("future certificate accepted")
	}
	_, otherKey, _ := testCertificate(t, "turn.example.com", now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := parseCertificate(chain, otherKey, "turn.example.com", now); err == nil {
		t.Fatal("mismatched certificate key accepted")
	}
	if _, err := parseCertificate(base64.StdEncoding.EncodeToString([]byte(chain)), base64.StdEncoding.EncodeToString([]byte(key)), "turn.example.com", now); err != nil {
		t.Fatal("base64 installation configuration rejected")
	}
}

func TestRealTLSRelayWithVerifiedCertificate(t *testing.T) {
	a := testApp(t)
	now := time.Now()
	chain, key, roots := testCertificate(t, a.cfg.publicHost, now.Add(-time.Hour), now.Add(48*time.Hour))
	var err error
	a.cfg.certificate, err = parseCertificate(chain, key, a.cfg.publicHost, now)
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.tlsCertificatePEM = chain
	a.cfg.tlsPrivateKeyPEM = key
	a.cfg.publicIP = netip.MustParseAddr("127.0.0.1")
	a.cfg.bindIP = "127.0.0.1"
	a.peerFilter = func(ip net.IP) bool { return ip.IsLoopback() }
	if err = a.listenNetwork("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	credential := issueTestCredential(t, a)
	if len(credential.URLs) != 3 || credential.URLs[2] != "turns:turn.example.com:5349?transport=tcp" {
		t.Fatal("TLS ICE endpoint missing")
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp4", a.tls.Addr().String(), &tls.Config{MinVersion: tls.VersionTLS12, ServerName: a.cfg.publicHost, RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.ConnectionState().Version < tls.VersionTLS12 {
		t.Fatal("weak TLS version negotiated")
	}
	factory := logging.NewDefaultLoggerFactory()
	factory.DefaultLogLevel = logging.LogLevelDisabled
	client, err := turn.NewClient(&turn.ClientConfig{TURNServerAddr: a.tls.Addr().String(), STUNServerAddr: a.tls.Addr().String(), Conn: turn.NewSTUNConn(conn), Username: credential.Username, Password: credential.Credential, Realm: a.cfg.realm, LoggerFactory: factory, RTO: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.Listen(); err != nil {
		t.Fatal(err)
	}
	relay, err := client.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	echoThroughRelay(t, relay)
	if bad, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp4", a.tls.Addr().String(), &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "wrong.example.com", RootCAs: roots}); err == nil {
		bad.Close()
		t.Fatal("wrong TLS hostname was trusted")
	}
	status, _ := json.Marshal(a.status())
	if strings.Contains(string(status), key) || strings.Contains(string(status), "PRIVATE KEY") {
		t.Fatal("TLS private key exposed through status")
	}
	// Ownership loss must stop encrypted sessions and release the TLS port too.
	a.mu.Lock()
	a.state = "fenced"
	a.mu.Unlock()
	a.stopNetwork()
	if len(a.relays) != 0 {
		t.Fatal("TLS allocation survived fencing")
	}
	if c, err := net.Listen("tcp4", a.tls.Addr().String()); err != nil {
		t.Fatal("TLS listener leaked on stop")
	} else {
		c.Close()
	}
}

func TestUnconfiguredTLSRejectsHandshakeAndKeepsReservedPort(t *testing.T) {
	a := startTestRelay(t)
	if conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp4", a.tls.Addr().String(), &tls.Config{MinVersion: tls.VersionTLS12, ServerName: a.cfg.publicHost}); err == nil {
		conn.Close()
		t.Fatal("unconfigured TLS accepted a client")
	}
	if listener, err := net.Listen("tcp4", a.tls.Addr().String()); err == nil {
		listener.Close()
		t.Fatal("native deployment lost its reserved TLS listener")
	}
	if len(issueTestCredential(t, a).URLs) != 2 {
		t.Fatal("unconfigured TLS advertised to clients")
	}
}

func TestSettingsValidationDoesNotPersistOrRevealSecrets(t *testing.T) {
	a := testApp(t)
	chain, key, _ := testCertificate(t, a.cfg.publicHost, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	invoke := func(config map[string]string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"config": config})
		w := httptest.NewRecorder()
		a.handleValidateSettings(w, httptest.NewRequest("POST", "/settings/validate", strings.NewReader(string(body))))
		return w
	}
	response := invoke(map[string]string{"tls_certificate_pem": chain, "tls_private_key_pem": key})
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"configured":true`) {
		t.Fatal(response.Body.String())
	}
	if a.cfg.certificate != nil || strings.Contains(response.Body.String(), key) || strings.Contains(response.Body.String(), a.cfg.secret) {
		t.Fatal("validation changed runtime state or exposed a secret")
	}
	if response := invoke(map[string]string{"max_allocations": "1000"}); response.Code != 400 {
		t.Fatal("invalid capacity accepted")
	}
	if response := invoke(map[string]string{"unknown": "x"}); response.Code != 400 {
		t.Fatal("unknown settings accepted")
	}
}
