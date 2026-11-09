package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
	"github.com/mochi-mqtt/server/v2/packets"
)

type testCA struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pem  []byte
}

func makeTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MQTT test issuer"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{key: key, cert: cert, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}
func (ca *testCA) issue(t *testing.T, host string, serial int64, expires time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		template.DNSNames = nil
		template.IPAddresses = []net.IP{ip}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ca.pem...), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
}
func writeTestPair(t *testing.T, certPath, keyPath string, cert, key []byte) {
	t.Helper()
	for path, body := range map[string][]byte{certPath: cert, keyPath: key} {
		tmp := path + ".new"
		if err := os.WriteFile(tmp, body, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}
}
func startTransportBroker(t *testing.T, cfg map[string]string) (*App, *sdk.AppCtx, *testkit.EmitRecorder) {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", "")
	recorder := testkit.NewEmitRecorder()
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithConfig(cfg), testkit.WithEmitter(recorder))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- app.broker.Serve(run) }()
	go func() { done <- app.runPersistence(run) }()
	t.Cleanup(func() {
		cancel()
		_ = app.broker.Close()
		for range 2 {
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("broker shutdown timeout")
			}
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for !app.durable.ready.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !app.durable.ready.Load() {
		t.Fatal("recovery not ready")
	}
	return app, ctx, recorder
}
func filesConfig(t *testing.T, ca *testCA) map[string]string {
	t.Helper()
	dir := t.TempDir()
	cert, key := ca.issue(t, "broker.test", 2, time.Now().Add(90*24*time.Hour))
	certPath, keyPath := filepath.Join(dir, "chain.pem"), filepath.Join(dir, "key.pem")
	writeTestPair(t, certPath, keyPath, cert, key)
	return map[string]string{"transport_mode": "tls", "tls_provider": "files", "tls_listen_port": "0", "advertised_host": "broker.test", "advertised_port": "443", "bind_interface": "127.0.0.1", "tls_cert_file": certPath, "tls_key_file": keyPath, "tls_reload_seconds": "1", "ha_discovery_enabled": "false"}
}
func secureDial(t *testing.T, app *App, ca *testCA) *tls.Conn {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.pem)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(app.broker.Port())), &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: "broker.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
func mqttAuthenticate(t *testing.T, conn net.Conn, user *MQTTUser, id string, protocol byte) {
	t.Helper()
	wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Connect}, ProtocolVersion: protocol, Connect: packets.ConnectParams{ProtocolName: []byte("MQTT"), ClientIdentifier: id, Clean: true, Keepalive: 60, UsernameFlag: true, PasswordFlag: true, Username: []byte(user.Username), Password: []byte("test-password")}})
	typ, body, err := wireRead(conn)
	if err != nil || typ != packets.Connack || len(body) < 2 || body[1] != 0 {
		t.Fatalf("authentication: %d %x %v", typ, body, err)
	}
}

func TestTLSVerifiedEnrollmentAndHotRenewal(t *testing.T) {
	ca := makeTestCA(t)
	cfg := filesConfig(t, ca)
	app, ctx, _ := startTransportBroker(t, cfg)
	if _, ok := app.broker.server.Listeners.Get("tcp1"); ok {
		t.Fatal("TLS started a plaintext listener")
	}
	endpoint := app.snapshotStatus(ctx.WithProject("a"))
	if endpoint["endpoint"] != "mqtts://broker.test:443" || endpoint["tls"] != true || endpoint["advertised_port"] != 443 || endpoint["port"] == 443 {
		t.Fatalf("wrong public enrollment: %#v", endpoint)
	}
	u := scopedUser(t, app, "a", "sensor", []string{scopeTopicRoot("a") + "#"}, []string{})
	first := secureDial(t, app, ca)
	mqttAuthenticate(t, first, u, "tls-first", 5)
	if len(first.ConnectionState().VerifiedChains) == 0 {
		t.Fatal("client did not verify certificate chain")
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(app.broker.Port()))
	for _, clientCfg := range []*tls.Config{
		{ServerName: "broker.test", RootCAs: x509.NewCertPool()},
		{ServerName: "wrong.test", RootCAs: func() *x509.CertPool { p := x509.NewCertPool(); p.AppendCertsFromPEM(ca.pem); return p }()},
		{ServerName: "broker.test", MinVersion: tls.VersionTLS11, MaxVersion: tls.VersionTLS11},
	} {
		if c, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, clientCfg); err == nil {
			c.Close()
			t.Fatal("unverified or obsolete TLS connection accepted")
		}
	}
	plain, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	wireWrite(t, plain, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Connect}, ProtocolVersion: 4, Connect: packets.ConnectParams{ProtocolName: []byte("MQTT"), ClientIdentifier: "plaintext", Clean: true}})
	if typ, _, err := wireRead(plain); err == nil && typ == packets.Pingresp {
		t.Fatal("plaintext was accepted on TLS")
	}
	plain.Close()
	cert, key := ca.issue(t, "broker.test", 3, time.Now().Add(90*24*time.Hour))
	writeTestPair(t, cfg["tls_cert_file"], cfg["tls_key_file"], cert, key)
	run, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.broker.certificates.run(run) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("certificate watcher did not stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		app.broker.certificates.mu.RLock()
		serial := app.broker.certificates.cert.Leaf.SerialNumber.Int64()
		app.broker.certificates.mu.RUnlock()
		if serial == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	renewed := secureDial(t, app, ca)
	if renewed.ConnectionState().PeerCertificates[0].SerialNumber.Int64() != 3 {
		t.Fatal("renewed certificate was not hot loaded")
	}
	wireWrite(t, first, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Pingreq}, ProtocolVersion: 5})
	if typ, _, err := wireRead(first); err != nil || typ != packets.Pingresp {
		t.Fatal("renewal interrupted established session")
	}
	badCert, badKey := ca.issue(t, "wrong.test", 4, time.Now().Add(time.Hour))
	writeTestPair(t, cfg["tls_cert_file"], cfg["tls_key_file"], badCert, badKey)
	if err := app.broker.certificates.reload(); err == nil {
		t.Fatal("wrong-host renewal replaced live certificate")
	}
	if secureDial(t, app, ca).ConnectionState().PeerCertificates[0].SerialNumber.Int64() != 3 {
		t.Fatal("invalid renewal lost last valid certificate")
	}
	if app.broker.certificates.status()["last_error"] == "" {
		t.Fatal("renewal error hidden")
	}
	expired, expiredKey := ca.issue(t, "broker.test", 5, time.Now().Add(-time.Minute))
	writeTestPair(t, cfg["tls_cert_file"], cfg["tls_key_file"], expired, expiredKey)
	if err := app.broker.certificates.reload(); err == nil {
		t.Fatal("expired replacement accepted")
	}
	// Simulate the last usable certificate expiring: enrollment and handshakes
	// must fail closed, while the listener never downgrades to plaintext.
	pair, err := tls.X509KeyPair(expired, expiredKey)
	if err != nil {
		t.Fatal(err)
	}
	pair.Leaf, _ = x509.ParseCertificate(pair.Certificate[0])
	app.broker.certificates.mu.Lock()
	app.broker.certificates.cert = &pair
	app.broker.certificates.mu.Unlock()
	if status := app.broker.endpointStatus(); status["ready"] != false || status["endpoint"] != "" {
		t.Fatal("expired certificate remained enrollable", status)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.pem)
	if c, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, &tls.Config{ServerName: "broker.test", RootCAs: pool}); err == nil {
		c.Close()
		t.Fatal("expired certificate allowed a new connection")
	}
}

func TestTLSBoundsUnauthenticatedConnectionsAndTimeout(t *testing.T) {
	ca := makeTestCA(t)
	cfg := filesConfig(t, ca)
	cfg["tls_max_pending_handshakes"] = "1"
	cfg["tls_handshake_timeout_seconds"] = "3"
	app, _, _ := startTransportBroker(t, cfg)
	u := scopedUser(t, app, "a", "pending-auth", []string{}, []string{})
	secure := app.broker.listener.Listener.(*mqttTLSListener)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(app.broker.Port()))
	raw, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	deadline := time.Now().Add(time.Second)
	for len(secure.slots) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.pem)
	if c, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, &tls.Config{ServerName: "broker.test", RootCAs: pool}); err == nil {
		c.Close()
		t.Fatal("pending TLS capacity was not enforced")
	}
	raw.SetReadDeadline(time.Now().Add(4 * time.Second))
	var data [1]byte
	if _, err := raw.Read(data[:]); err == nil {
		t.Fatal("unauthenticated peer did not time out")
	}
	deadline = time.Now().Add(time.Second)
	for len(secure.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	conn := secureDial(t, app, ca)
	// A completed handshake without MQTT authentication must still occupy the
	// pre-auth slot; completing TLS cannot bypass the resource bound.
	if c, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, &tls.Config{ServerName: "broker.test", RootCAs: pool}); err == nil {
		c.Close()
		t.Fatal("TLS-only connection bypassed pre-auth capacity")
	}
	mqttAuthenticate(t, conn, u, "authenticated", 5)
	// The pending-auth bound must not accidentally cap authenticated sessions.
	secureDial(t, app, ca).Close()
	conn.Close()
}

func TestTLSOverloadDisconnectHasNoPublishAcknowledgment(t *testing.T) {
	ca := makeTestCA(t)
	cfg := filesConfig(t, ca)
	cfg["max_publish_per_second"] = "1"
	cfg["max_publish_burst"] = "1"
	app, _, _ := startTransportBroker(t, cfg)
	root := scopeTopicRoot("a")
	u := scopedUser(t, app, "a", "sensor", []string{root + "#"}, []string{})
	conn := secureDial(t, app, ca)
	mqttAuthenticate(t, conn, u, "tls-overload", 5)
	wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish}, ProtocolVersion: 5, TopicName: root + "first", Payload: []byte("one")})
	wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Pingreq}, ProtocolVersion: 5})
	if typ, _, err := wireRead(conn); err != nil || typ != packets.Pingresp {
		t.Fatal(typ, err)
	}
	wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1}, ProtocolVersion: 5, PacketID: 1, TopicName: root + "rejected", Payload: []byte("two")})
	if typ, body, err := wireRead(conn); err != nil || typ != packets.Disconnect || len(body) == 0 || body[0] != packets.ErrQuotaExceeded.Code {
		t.Fatal("TLS publish was acknowledged after overload", typ, body, err)
	}
}

func TestRetainedCleanupDoesNotDeleteOnRejectedPublish(t *testing.T) {
	app, ctx, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "retain_max_count": "1", "max_publish_per_scope_per_second": "1", "max_publish_per_scope_burst": "1", "ha_discovery_enabled": "false"})
	root := scopeTopicRoot("a")
	if err := app.broker.Publish(root+"traffic", []byte("accepted"), false, 0); err != nil {
		t.Fatal(err)
	}
	app.admission.mu.Lock()
	bucket := app.admission.scopes["a"]
	app.admission.mu.Unlock()
	bucket.mu.Lock()
	bucket.rate = 0.001
	bucket.mu.Unlock()
	for _, name := range []string{"old", "new"} {
		putRetainedFixture(t, app, packets.Packet{TopicName: root + name, Payload: []byte("keep")})
	}
	app.sweepOnce()
	stored, err := listRetainedMessages(ctx.AppDB(), "a", "", 10)
	if err != nil || len(stored) != 2 {
		t.Fatal("rejected cleanup deleted retained data", stored, err)
	}
}

func TestPayloadAndInvalidAdmissionPoliciesRejectExplicitly(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-app-token")
	app, ctx, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "max_payload_bytes": "1024", "ha_discovery_enabled": "false"})
	response := appRequest(t, app, "POST", "/test_publish", `{"topic":"`+scopeTopicRoot("a")+`state","payload":"`+strings.Repeat("x", 1025)+`"}`, "a")
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("oversize HTTP payload not rejected", response.Code, response.Body.String())
	}
	if _, err := app.toolPublish(ctx.WithProject("a"), map[string]any{"topic": scopeTopicRoot("a") + "state", "payload": strings.Repeat("x", 1025)}); err == nil {
		t.Fatal("oversize MCP publish accepted")
	}
	for _, name := range []string{"max_clients", "max_payload_bytes", "max_publish_per_second", "max_publish_per_scope_per_second", "max_pending_writes_per_client"} {
		t.Run(name, func(t *testing.T) {
			ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithConfig(map[string]string{name: "invalid"}))
			if err := (&App{ctx: ctx}).initAdmission(); err == nil {
				t.Fatal("invalid limit silently defaulted")
			}
		})
	}
}

func TestTransportConfigurationFailsClosed(t *testing.T) {
	ca := makeTestCA(t)
	cert, key := ca.issue(t, "broker.test", 8, time.Now().Add(time.Hour))
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	pair.Leaf = leaf
	if err := validCertificate(&pair, "broker.test", time.Now()); err == nil {
		t.Fatal("client-only certificate advertised as a server certificate")
	}
	for name, cfg := range map[string]map[string]string{
		"missing TLS config":        {},
		"missing certificate":       {"advertised_host": "broker.test"},
		"public plaintext":          {"transport_mode": "local", "bind_interface": "0.0.0.0", "advertised_host": "8.8.8.8"},
		"public plaintext hostname": {"transport_mode": "local", "advertised_host": "broker.test"},
		"scheme in hostname":        {"advertised_host": "mqtts://broker.test"},
		"wrong ACME port":           {"advertised_host": "broker.test", "tls_provider": "acme", "advertised_port": "8883", "acme_accept_terms": "true"},
		"unaccepted ACME terms":     {"advertised_host": "broker.test", "tls_provider": "acme", "advertised_port": "443"},
		"insecure directory":        {"advertised_host": "broker.test", "tls_provider": "acme", "advertised_port": "443", "acme_accept_terms": "true", "acme_directory_url": "http://issuer.test/directory"},
		"invalid bind":              {"transport_mode": "local", "bind_interface": "en0"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithConfig(cfg))
			if _, err := parseTransport(ctx); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	for _, host := range []string{"broker.example.com", "127.0.0.1", "2001:db8::1"} {
		if _, err := validatePublicHost(host); err != nil {
			t.Fatal(host, err)
		}
	}
	for _, host := range []string{"0.0.0.0", "::", "host:8883", "*.example.com", "host/path", "foo\x00bar"} {
		if _, err := validatePublicHost(host); err == nil {
			t.Fatal("invalid hostname accepted", host)
		}
	}
}

func TestOverloadRejectsBeforeAckOrRetentionAcrossProtocolsAndQoS(t *testing.T) {
	for _, protocol := range []byte{4, 5} {
		for _, qos := range []byte{0, 1, 2} {
			t.Run(fmt.Sprintf("MQTT%d-QoS%d", protocol, qos), func(t *testing.T) {
				app, ctx, recorder := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "max_publish_per_second": "1", "max_publish_burst": "1", "ha_discovery_enabled": "false"})
				root := scopeTopicRoot("a")
				u := scopedUser(t, app, "a", "sensor", []string{root + "#"}, []string{})
				conn, code := wireConnect(t, app, u, "overload", protocol, "")
				if code != 0 {
					t.Fatal(code)
				}
				wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish}, ProtocolVersion: protocol, TopicName: root + "accepted", Payload: []byte("one")})
				wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Pingreq}, ProtocolVersion: protocol})
				if typ, _, err := wireRead(conn); err != nil || typ != packets.Pingresp {
					t.Fatal("first publish not processed", typ, err)
				}
				wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: qos, Retain: true}, ProtocolVersion: protocol, PacketID: 1, TopicName: root + "rejected", Payload: []byte("never retained")})
				typ, body, err := wireRead(conn)
				if protocol == 5 {
					if err != nil || typ != packets.Disconnect || len(body) == 0 || body[0] != packets.ErrQuotaExceeded.Code {
						t.Fatalf("missing explicit quota disconnect: %d %x %v", typ, body, err)
					}
				} else if err == nil {
					t.Fatalf("MQTT 3 received a packet after rejection: %d %x", typ, body)
				}
				if _, err := getRetainedMessage(ctx.AppDB(), "a", root+"rejected"); err == nil {
					t.Fatal("rejected publish retained")
				}
				for _, event := range recorder.EventsByTopic("mqtt.message") {
					if event.Data.(map[string]any)["topic"] == root+"rejected" {
						t.Fatal("rejected publish delivered")
					}
				}
				if app.counters("a").rateLimited.Load() != 1 {
					t.Fatal("rejection metric missing")
				}
			})
		}
	}
}

func TestScopeLimitCoversHTTPMCPAndReconnects(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-app-token")
	app, ctx, _ := startTransportBroker(t, map[string]string{"transport_mode": "local", "listen_port": "0", "max_publish_per_scope_per_second": "1", "max_publish_per_scope_burst": "1", "max_publish_per_second": "0", "ha_discovery_enabled": "false"})
	root := scopeTopicRoot("a")
	first := appRequest(t, app, "POST", "/test_publish", `{"topic":"`+root+`state","payload":"one"}`, "a")
	if first.Code != http.StatusOK {
		t.Fatal(first.Code, first.Body.String())
	}
	// Hold the admitted scope bucket below one token while bcrypt runs under
	// -race, so this checks shared admission rather than wall-clock speed.
	app.admission.mu.Lock()
	bucket := app.admission.scopes["a"]
	app.admission.mu.Unlock()
	bucket.mu.Lock()
	bucket.rate = 0.001
	bucket.mu.Unlock()
	second := appRequest(t, app, "POST", "/test_publish", `{"topic":"`+root+`state","payload":"two"}`, "a")
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "1" {
		t.Fatal("missing HTTP backpressure", second.Code, second.Body.String())
	}
	if _, err := app.toolPublish(ctx.WithProject("a"), map[string]any{"topic": root + "state", "payload": "three"}); err == nil || !strings.Contains(err.Error(), "capacity exhausted") {
		t.Fatal("MCP returned success on overload", err)
	}
	if _, err := app.toolPublish(ctx.WithProject("b"), map[string]any{"topic": scopeTopicRoot("b") + "state", "payload": "independent"}); err != nil {
		t.Fatal("one scope blocked another", err)
	}
	u := scopedUser(t, app, "a", "new-client", []string{root + "#"}, []string{})
	conn, code := wireConnect(t, app, u, "fresh-connection", 5, "")
	if code != 0 {
		t.Fatal(code)
	}
	wireWrite(t, conn, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: 1}, ProtocolVersion: 5, PacketID: 1, TopicName: root + "state", Payload: []byte("cannot bypass")})
	typ, body, err := wireRead(conn)
	if err != nil || typ != packets.Disconnect || len(body) == 0 || body[0] != packets.ErrQuotaExceeded.Code {
		t.Fatal("reconnect bypassed scope limit", typ, body, err)
	}
}

func TestReadinessTracksSelectedListener(t *testing.T) {
	configs := map[string]map[string]string{
		"local": {"transport_mode": "local", "listen_port": "0", "ha_discovery_enabled": "false"},
		"tls":   filesConfig(t, makeTestCA(t)),
	}
	for mode, config := range configs {
		t.Run(mode, func(t *testing.T) {
			app := &App{}
			ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithConfig(config))
			if err := app.OnMount(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.broker.Close() })
			check := func(want int) {
				t.Helper()
				response := appRequest(t, app, http.MethodGet, "/ready", "", "")
				if response.Code != want {
					t.Fatalf("readiness status = %d, want %d", response.Code, want)
				}
			}
			check(http.StatusServiceUnavailable)
			run, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- app.broker.Serve(run) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if appRequest(t, app, http.MethodGet, "/ready", "", "").Code == http.StatusOK {
					break
				}
				time.Sleep(time.Millisecond)
			}
			check(http.StatusOK)
			if err := app.broker.Close(); err != nil {
				t.Fatal(err)
			}
			check(http.StatusServiceUnavailable)
		})
	}
}
