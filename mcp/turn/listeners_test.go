package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

func TestListenerConfigAndTransportSelection(t *testing.T) {
	for _, patch := range []map[string]string{
		{"udp_port": "0"}, {"tcp_port": "65536"}, {"tls_ports": "443,443"}, {"tls_ports": "3478"},
		{"tls_ports": "443,"}, {"tls_ports": "443,no"}, {"tls_ports": "1,2,3,4,5,6,7,8,9"},
		{"udp_port": "49160"}, {"tcp_port": "49161"}, {"tls_ports": "49223"},
	} {
		values := validConfig()
		for key, value := range patch {
			values[key] = value
		}
		if _, err := parseConfig(values); err == nil {
			t.Fatalf("accepted invalid listener config %v", patch)
		}
	}
	t.Setenv("APTEVA_APP_PORT", "8443")
	values := validConfig()
	values["tls_ports"] = "443,8443"
	if _, err := parseConfig(values); err == nil {
		t.Fatal("HTTP control conflict accepted")
	}
	t.Setenv("APTEVA_APP_PORT", "")
	a := testApp(t)
	a.cfg.udpPort = 80
	a.cfg.tcpPort = 8081
	a.cfg.tlsPorts = []int{5349, 443, 8443}
	now := time.Now()
	chain, key, _ := testCertificate(t, a.cfg.publicHost, now.Add(-time.Hour), now.Add(time.Hour))
	cert, err := parseCertificate(chain, key, a.cfg.publicHost, now)
	if err != nil {
		t.Fatal(err)
	}
	a.certificate.Store(cert)
	for _, tt := range []struct {
		transport string
		port      int
		want      string
	}{
		{"udp", 0, "turn:turn.example.com:80?transport=udp"},
		{"tcp", 0, "turn:turn.example.com:8081?transport=tcp"},
		{"tls", 443, "turns:turn.example.com:443?transport=tcp"},
	} {
		result, err := a.issueCredentials(credentialRequest{ProjectID: "project-a", SessionID: "call", Transport: tt.transport, TLSPort: tt.port}, now)
		if err != nil || len(result.ICEServers) != 1 || len(result.ICEServers[0].URLs) != 1 || result.ICEServers[0].URLs[0] != tt.want {
			t.Fatalf("selection %v: %v %v", tt, result, err)
		}
	}
	urls, err := a.credentialURLs(credentialRequest{}, now)
	if err != nil || len(urls) != 5 {
		t.Fatalf("all endpoints: %v %v", urls, err)
	}
	for _, req := range []credentialRequest{{Transport: "bad"}, {Transport: "udp", TLSPort: 443}, {Transport: "tls", TLSPort: 444}} {
		if _, err := a.credentialURLs(req, now); err == nil {
			t.Fatalf("invalid selection accepted: %v", req)
		}
	}
	a.certificate.Store(nil)
	if _, err := a.credentialURLs(credentialRequest{Transport: "tls"}, now); err == nil {
		t.Fatal("TLS-only silently downgraded")
	}
	if a.status()["tcp_port"] != 8081 || a.cfg.publicSettings()["tls_ports"] != "5349,443,8443" {
		t.Fatal("custom ports not reported")
	}
}

func TestAdditionalTLSConflictReleasesEverySocket(t *testing.T) {
	a := testApp(t)
	a.cfg.bindIP = "127.0.0.1"
	conflict, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conflict.Close()
	if err := a.listenListeners("127.0.0.1:0", "127.0.0.1:0", []string{"127.0.0.1:0", conflict.Addr().String()}); err == nil {
		t.Fatal("occupied TLS port accepted")
	}
	if a.status()["ready"] == true {
		t.Fatal("failed listener startup is ready")
	}
	for _, addr := range []net.Addr{a.udp.LocalAddr(), a.tcp.Addr(), a.tlsListeners[0].Addr()} {
		if addr.Network() == "udp" {
			c, err := net.ListenPacket("udp4", addr.String())
			if err != nil {
				t.Fatal("UDP socket leaked", err)
			}
			c.Close()
		} else {
			c, err := net.Listen("tcp4", addr.String())
			if err != nil {
				t.Fatal("TCP/TLS socket leaked", err)
			}
			c.Close()
		}
	}
}

func TestSettingsRejectOccupiedNewTLSPort(t *testing.T) {
	a := testApp(t)
	a.cfg.bindIP = "127.0.0.1"
	conflict, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conflict.Close()
	port := conflict.Addr().(*net.TCPAddr).Port
	w := httptest.NewRecorder()
	a.handleValidateSettings(w, httptest.NewRequest("POST", "/settings/validate", strings.NewReader(fmt.Sprintf(`{"config":{"tls_ports":"5349,%d"}}`, port))))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "unavailable") {
		t.Fatalf("port conflict not reported: %d %s", w.Code, w.Body.String())
	}
}

func TestMultipleTLSListenersIncluding443(t *testing.T) {
	// Exact port is opt-in to avoid conflicting with a developer's HTTPS service.
	// Default test still exercises two concurrent TLS listeners and their cleanup.
	a := testApp(t)
	a.cfg.bindIP = "127.0.0.1"
	a.cfg.publicIP = netip.MustParseAddr("127.0.0.1")
	a.peerFilter = func(ip net.IP) bool { return ip.IsLoopback() }
	now := time.Now()
	chain, key, roots := testCertificate(t, a.cfg.publicHost, now.Add(-time.Hour), now.Add(time.Hour))
	cert, err := parseCertificate(chain, key, a.cfg.publicHost, now)
	if err != nil {
		t.Fatal(err)
	}
	a.certificate.Store(cert)
	first := "127.0.0.1:0"
	if os.Getenv("TURN_TEST_TLS_443") == "1" {
		first = "127.0.0.1:443"
	}
	if err = a.listenListeners("127.0.0.1:0", "127.0.0.1:0", []string{first, "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
	credential := issueTestCredential(t, a)
	for _, listener := range a.tlsListeners {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp4", listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: a.cfg.publicHost, MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatal(err)
		}
		factory := logging.NewDefaultLoggerFactory()
		factory.DefaultLogLevel = logging.LogLevelDisabled
		client, err := turn.NewClient(&turn.ClientConfig{TURNServerAddr: listener.Addr().String(), Conn: turn.NewSTUNConn(conn), Username: credential.Username, Password: credential.Credential, Realm: a.cfg.realm, LoggerFactory: factory, RTO: 10 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		if err = client.Listen(); err != nil {
			t.Fatal(err)
		}
		relay, err := client.Allocate()
		if err != nil {
			t.Fatal(err)
		}
		echoThroughRelay(t, relay)
		relay.Close()
		client.Close()
		conn.Close()
	}
	a.stopNetwork()
	for _, listener := range a.tlsListeners {
		socket, err := net.Listen("tcp4", listener.Addr().String())
		if err != nil {
			t.Fatal("TLS socket retained after stop", err)
		}
		socket.Close()
	}
}
