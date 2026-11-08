package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

// A local HTTPS ACME v2 issuer exercises the real autocert manager, JWS client,
// TLS-ALPN challenge routing, certificate issuance, automatic renewal and cache
// recovery without contacting a public CA or using a real user's domain.
type acmeOrder struct {
	authorized  bool
	certificate []byte
}
type testACME struct {
	t          *testing.T
	ca         *testCA
	server     *httptest.Server
	mu         sync.Mutex
	orders     map[int]*acmeOrder
	address    string
	thumbprint string
	issued     atomic.Int64
	challenges atomic.Int64
	nonce      atomic.Int64
}

func newTestACME(t *testing.T) *testACME {
	ca := &testACME{t: t, ca: makeTestCA(t), orders: map[int]*acmeOrder{}}
	ca.server = httptest.NewTLSServer(http.HandlerFunc(ca.handle))
	t.Cleanup(ca.server.Close)
	return ca
}

func (ca *testACME) handle(w http.ResponseWriter, r *http.Request) {
	base := ca.server.URL
	w.Header().Set("Replay-Nonce", base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprint(ca.nonce.Add(1)))))
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/nonce" {
		w.WriteHeader(200)
		return
	}
	if r.URL.Path == "/directory" {
		json.NewEncoder(w).Encode(map[string]any{"newNonce": base + "/nonce", "newAccount": base + "/account", "newOrder": base + "/order", "meta": map[string]any{"termsOfService": base + "/terms"}})
		return
	}
	var envelope struct {
		Protected string `json:"protected"`
		Payload   string `json:"payload"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&envelope); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.URL.Path == "/account" {
		raw, _ := base64.RawURLEncoding.DecodeString(envelope.Protected)
		var header struct {
			JWK map[string]any `json:"jwk"`
		}
		_ = json.Unmarshal(raw, &header)
		canonical, _ := json.Marshal(header.JWK)
		sum := sha256.Sum256(canonical)
		ca.mu.Lock()
		ca.thumbprint = base64.RawURLEncoding.EncodeToString(sum[:])
		ca.mu.Unlock()
		w.Header().Set("Location", base+"/account/1")
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"status": "valid", "orders": base + "/orders"})
		return
	}
	if r.URL.Path == "/order" {
		ca.mu.Lock()
		id := len(ca.orders) + 1
		ca.orders[id] = &acmeOrder{}
		ca.mu.Unlock()
		w.Header().Set("Location", fmt.Sprintf("%s/orders/%d", base, id))
		w.WriteHeader(201)
		ca.orderJSON(w, id)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.Atoi(parts[1])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ca.mu.Lock()
	order, exists := ca.orders[id]
	ca.mu.Unlock()
	if !exists {
		http.NotFound(w, r)
		return
	}
	switch parts[0] {
	case "authz":
		ca.mu.Lock()
		status := "pending"
		if order.authorized {
			status = "valid"
		}
		ca.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"status": status, "identifier": map[string]string{"type": "dns", "value": "broker.test"}, "challenges": []any{map[string]any{"type": "tls-alpn-01", "url": fmt.Sprintf("%s/challenge/%d", base, id), "token": fmt.Sprintf("token-%d", id), "status": status}}})
	case "challenge":
		ca.mu.Lock()
		address, thumb := ca.address, ca.thumbprint
		ca.mu.Unlock()
		// ACME TLS-ALPN certificates are self-signed challenge proofs, not normal
		// MQTT identity certificates. Verify their SAN and critical acmeIdentifier
		// extension explicitly instead of applying public PKI trust to this probe.
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", address, &tls.Config{ServerName: "broker.test", NextProtos: []string{acme.ALPNProto}, InsecureSkipVerify: true})
		if err != nil {
			http.Error(w, "challenge handshake: "+err.Error(), 500)
			return
		}
		cert := conn.ConnectionState().PeerCertificates[0]
		protocol := conn.ConnectionState().NegotiatedProtocol
		conn.Close()
		expected := sha256.Sum256([]byte(fmt.Sprintf("token-%d.%s", id, thumb)))
		verified := false
		for _, extension := range cert.Extensions {
			if extension.Id.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 1, 31}) && extension.Critical {
				var value []byte
				if _, e := asn1.Unmarshal(extension.Value, &value); e == nil && string(value) == string(expected[:]) {
					verified = true
				}
			}
		}
		if protocol != acme.ALPNProto || cert.VerifyHostname("broker.test") != nil || !verified {
			http.Error(w, "invalid challenge proof", 400)
			return
		}
		ca.challenges.Add(1)
		ca.mu.Lock()
		order.authorized = true
		ca.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"type": "tls-alpn-01", "status": "valid", "url": base + r.URL.Path, "token": fmt.Sprintf("token-%d", id)})
	case "finalize":
		var body struct {
			CSR string `json:"csr"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		raw, _ := base64.RawURLEncoding.DecodeString(body.CSR)
		csr, err := x509.ParseCertificateRequest(raw)
		if err != nil || csr.CheckSignature() != nil || len(csr.DNSNames) != 1 || csr.DNSNames[0] != "broker.test" {
			http.Error(w, "invalid CSR", 400)
			return
		}
		serial := ca.issued.Add(1)
		expiry := time.Now().Add(90 * 24 * time.Hour)
		if serial == 1 {
			expiry = time.Now().Add(time.Hour)
		} // already inside renewal window; next cert gets normal lifetime.
		template := &x509.Certificate{SerialNumber: big.NewInt(100 + serial), Subject: pkix.Name{CommonName: "broker.test"}, DNSNames: csr.DNSNames, NotBefore: time.Now().Add(-time.Hour), NotAfter: expiry, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, template, ca.ca.cert, csr.PublicKey, ca.ca.key)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		ca.mu.Lock()
		order.certificate = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ca.ca.pem...)
		ca.mu.Unlock()
		w.Header().Set("Location", fmt.Sprintf("%s/orders/%d", base, id))
		ca.orderJSON(w, id)
	case "orders":
		ca.orderJSON(w, id)
	case "cert":
		ca.mu.Lock()
		certificate := append([]byte(nil), order.certificate...)
		ca.mu.Unlock()
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		w.Write(certificate)
	default:
		http.NotFound(w, r)
	}
}

func (ca *testACME) orderJSON(w http.ResponseWriter, id int) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	status := "pending"
	order := ca.orders[id]
	if order.authorized {
		status = "ready"
	}
	if len(order.certificate) > 0 {
		status = "valid"
	}
	out := map[string]any{"status": status, "identifiers": []any{map[string]string{"type": "dns", "value": "broker.test"}}, "authorizations": []string{fmt.Sprintf("%s/authz/%d", ca.server.URL, id)}, "finalize": fmt.Sprintf("%s/finalize/%d", ca.server.URL, id)}
	if len(order.certificate) > 0 {
		out["certificate"] = fmt.Sprintf("%s/cert/%d", ca.server.URL, id)
	}
	json.NewEncoder(w).Encode(out)
}

func TestACMEIssuesRenewsAndRestoresVerifiedCertificate(t *testing.T) {
	issuer := newTestACME(t)
	app, _, _ := startTransportBroker(t, map[string]string{"transport_mode": "tls", "tls_provider": "acme", "tls_listen_port": "0", "advertised_host": "broker.test", "advertised_port": "443", "bind_interface": "127.0.0.1", "acme_accept_terms": "true", "acme_directory_url": issuer.server.URL + "/directory", "acme_cache_dir": filepath.Join(t.TempDir(), "cache"), "tls_reload_seconds": "1", "ha_discovery_enabled": "false"})
	service := app.broker.certificates
	service.manager.Client.HTTPClient = &http.Client{Timeout: 5 * time.Second, Transport: cancelTransport{ctx: service.ctx, base: issuer.server.Client().Transport}}
	issuer.mu.Lock()
	issuer.address = net.JoinHostPort("127.0.0.1", strconv.Itoa(app.broker.Port()))
	issuer.mu.Unlock()
	if status := app.broker.endpointStatus(); status["endpoint"] != "" || status["ready"] != false {
		t.Fatal("unissued enrollment advertised as ready", status)
	}
	run, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.run(run) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("ACME worker did not stop")
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		service.mu.RLock()
		renewed := service.cert != nil && service.cert.Leaf.SerialNumber.Int64() >= 102
		service.mu.RUnlock()
		if renewed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if issuer.issued.Load() < 2 || issuer.challenges.Load() < 2 {
		t.Fatalf("issuance/renewal did not finish: issued=%d challenges=%d status=%#v", issuer.issued.Load(), issuer.challenges.Load(), service.status())
	}
	conn := secureDial(t, app, issuer.ca)
	if conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64() != 102 || len(conn.ConnectionState().VerifiedChains) == 0 {
		t.Fatal("renewed certificate not verified")
	}
	if status := app.broker.endpointStatus(); status["endpoint"] != "mqtts://broker.test:443" || status["ready"] != true {
		t.Fatal("wrong ACME enrollment", status)
	}
	restored, _, err := newCertificateService(service.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.cancel()
	before := issuer.issued.Load()
	if err := restored.refreshACME(); err != nil {
		t.Fatal(err)
	}
	cert, err := restored.getCertificate(certificateHello("broker.test"))
	if err != nil || cert.Leaf.SerialNumber.Int64() != 102 || issuer.issued.Load() != before {
		t.Fatal("durable certificate cache did not restore without issuing", err)
	}
}

func TestACMEWorkerCancelsPendingIssuerRequest(t *testing.T) {
	entered := make(chan struct{}, 1)
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer issuer.Close()
	app, _, _ := startTransportBroker(t, map[string]string{"transport_mode": "tls", "tls_provider": "acme", "tls_listen_port": "0", "advertised_host": "broker.test", "advertised_port": "443", "bind_interface": "127.0.0.1", "acme_accept_terms": "true", "acme_directory_url": issuer.URL + "/directory", "acme_cache_dir": filepath.Join(t.TempDir(), "cache"), "tls_reload_seconds": "1", "ha_discovery_enabled": "false"})
	s := app.broker.certificates
	s.manager.Client.HTTPClient = &http.Client{Timeout: 10 * time.Second, Transport: cancelTransport{ctx: s.ctx, base: issuer.Client().Transport}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("issuer request not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ACME worker ignored cancellation")
	}
	if status := s.status(); status["ready"] != false || status["last_error"] == "" {
		t.Fatal("issuer failure claimed successful enrollment", status)
	}
}
