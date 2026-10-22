package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

type dnsFixture struct {
	testkit.BasePlatformClient
	sdk.AppContextClient
	mu         sync.Mutex
	records    []dnsRecord
	calls      []string
	failDelete bool
}

func (f *dnsFixture) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"domains": float64(3)}}, nil
}
func (f *dnsFixture) CallAppResultContext(ctx context.Context, app, tool string, args map[string]any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.CallAppResult(app, tool, args, out)
}
func (f *dnsFixture) CallAppResult(app, tool string, args map[string]any, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app != "domains" || args["_project_id"] != "project-a" {
		return errors.New("wrong app/project")
	}
	f.calls = append(f.calls, tool)
	var result any = map[string]any{"ok": true}
	switch tool {
	case "domain_records_list":
		var records []dnsRecord
		for _, r := range f.records {
			if r.Name == args["name"] {
				records = append(records, r)
			}
		}
		result = map[string]any{"records": records, "capabilities": map[string]any{"write_types": []string{"A", "TXT"}, "delete_types": []string{"A", "TXT"}}}
	case "domain_records_set":
		if args["mode"] != "create" {
			if args["record_id"] == nil || args["expected_record"] == nil {
				return errors.New("destructive DNS mutation")
			}
			for i, r := range f.records {
				if r.ID == args["record_id"] {
					f.records = append(f.records[:i], f.records[i+1:]...)
					break
				}
			}
		}
		f.records = append(f.records, dnsRecord{ID: time.Now().String(), Name: args["name"].(string), Type: args["type"].(string), Value: args["value"].(string), TTL: 600})
	case "domain_records_delete":
		if f.failDelete {
			return errors.New("provider unavailable")
		}
		if args["record_id"] == nil || args["expected_record"] == nil {
			return errors.New("unsafe delete")
		}
		for i, r := range f.records {
			if r.ID == args["record_id"] {
				f.records = append(f.records[:i], f.records[i+1:]...)
				break
			}
		}
	default:
		return errors.New("unknown tool")
	}
	b, _ := json.Marshal(result)
	return json.Unmarshal(b, out)
}
func testTLSManager(t *testing.T) (*managedTLS, *dnsFixture) {
	t.Helper()
	a := testApp(t)
	a.cfg.tlsMode = "managed"
	a.cfg.dnsZone = "example.com"
	a.cfg.acmeTerms = true
	a.cfg.storageKey = strings.Repeat("storage-key-", 4)
	db, err := sql.Open("sqlite", t.TempDir()+"/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`CREATE TABLE turn_managed_tls(namespace TEXT PRIMARY KEY,sealed BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	f := &dnsFixture{}
	m := &managedTLS{app: a, db: db, platform: f, project: "project-a", namespace: "turn.example.com|test", wake: make(chan struct{}, 1), done: make(chan struct{}), phase: "pending", issue: issueManagedCertificate, waitDNS: func(context.Context, string, string, string) error { return nil }}
	a.tlsManager = m
	return m, f
}
func TestManagedTLSConfig(t *testing.T) {
	values := validConfig()
	values["tls_mode"] = "managed"
	values["dns_zone"] = "example.com"
	values["tls_storage_key"] = strings.Repeat("s", 40)
	values["acme_accept_terms"] = "true"
	values["tls_certificate_pem"] = "old manual cert ignored"
	if _, err := parseConfig(values); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"public_host", "8.8.8.8"}, {"public_host", "example.com"}, {"dns_zone", "other.com"}, {"tls_storage_key", "short"}, {"acme_accept_terms", "false"}, {"tls_mode", "wrong"}} {
		copy := sdk.Config{}
		for k, v := range values {
			copy[k] = v
		}
		copy[pair[0]] = pair[1]
		if _, err := parseConfig(copy); err == nil {
			t.Errorf("accepted invalid %v", pair)
		}
	}
}
func TestEncryptedTLSStateAndWrongKey(t *testing.T) {
	state := tlsState{AccountKey: "private account", Certificate: "public cert", PrivateKey: "private leaf", PendingTXT: "owned TXT"}
	sealed, err := sealTLS("key", "namespace", state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "private") {
		t.Fatal("plaintext key")
	}
	out, err := openTLS("key", "namespace", sealed)
	if err != nil || out != state {
		t.Fatal("round trip failed", err)
	}
	for _, v := range [][2]string{{"other", "namespace"}, {"key", "other"}} {
		if _, err = openTLS(v[0], v[1], sealed); err == nil {
			t.Fatal("wrong key or namespace accepted")
		}
	}
}
func TestManagedDNSPreservesOtherRecordsAndRecoversCleanup(t *testing.T) {
	m, f := testTLSManager(t)
	f.records = []dnsRecord{{ID: "other", Name: "_acme-challenge.turn", Type: "TXT", Value: "unrelated challenge", TTL: 600}}
	if err := m.presentTXT(context.Background(), "owned-token"); err != nil {
		t.Fatal(err)
	}
	f.failDelete = true
	if m.cleanupTXT(context.Background()) == nil {
		t.Fatal("cleanup failure ignored")
	}
	if m.state.PendingTXT != "owned-token" {
		t.Fatal("lost cleanup receipt")
	}
	f.failDelete = false
	if err := m.cleanupTXT(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.records) != 1 || f.records[0].ID != "other" {
		t.Fatal("unrelated TXT deleted")
	}
	var sealed []byte
	if err := m.db.QueryRow(`SELECT sealed FROM turn_managed_tls WHERE namespace=?`, m.namespace).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	state, err := openTLS(m.app.cfg.storageKey, m.namespace, sealed)
	if err != nil || state.PendingTXT != "" {
		t.Fatal("cleanup not persisted")
	}
	f.records = []dnsRecord{{ID: "existing", Name: "turn", Type: "A", Value: "1.1.1.1"}}
	if m.ensureAddress(context.Background()) == nil {
		t.Fatal("conflicting record accepted")
	}
	if len(f.records) != 1 {
		t.Fatal("conflicting DNS mutated")
	}
	m.app.mu.Lock()
	m.app.state = "standby"
	m.app.mu.Unlock()
	f.records = nil
	if m.ensureAddress(context.Background()) == nil {
		t.Fatal("passive instance wrote DNS")
	}
}
func TestACMEDNSIssuancePersistenceAndRenewal(t *testing.T) {
	m, f := testTLSManager(t)
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour)}
	caDER, _ := x509.CreateCertificate(rand.Reader, root, root, &caKey.PublicKey, caKey)
	root, _ = x509.ParseCertificate(caDER)
	var host string
	var accepted, finalized bool
	var chain string
	orders := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", "dGVzdC1ub25jZQ")
		w.Header().Set("Content-Type", "application/json")
		send := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/directory":
			send(200, map[string]any{"newNonce": host + "/nonce", "newAccount": host + "/account", "newOrder": host + "/new-order"})
		case "/nonce":
			w.WriteHeader(204)
		case "/account":
			w.Header().Set("Location", host+"/account/1")
			status := 201
			if orders > 0 {
				status = 200
			}
			send(status, map[string]any{"status": "valid"})
		case "/new-order":
			orders++
			accepted = false
			finalized = false
			w.Header().Set("Location", host+"/order/1")
			send(201, map[string]any{"status": "pending", "authorizations": []string{host + "/auth/1"}, "finalize": host + "/finalize"})
		case "/auth/1":
			status := "pending"
			if accepted {
				status = "valid"
			}
			send(200, map[string]any{"status": status, "identifier": map[string]string{"type": "dns", "value": "turn.example.com"}, "challenges": []map[string]string{{"type": "dns-01", "url": host + "/challenge", "token": "test-challenge"}}})
		case "/challenge":
			accepted = true
			send(200, map[string]any{"status": "valid", "type": "dns-01", "url": host + "/challenge", "token": "test-challenge"})
		case "/order/1":
			status := "ready"
			if finalized {
				status = "valid"
			}
			send(200, map[string]any{"status": status, "authorizations": []string{host + "/auth/1"}, "finalize": host + "/finalize", "certificate": host + "/cert"})
		case "/finalize":
			var jws struct {
				Payload string `json:"payload"`
			}
			_ = json.NewDecoder(r.Body).Decode(&jws)
			raw, _ := base64.RawURLEncoding.DecodeString(jws.Payload)
			var payload struct {
				CSR string `json:"csr"`
			}
			_ = json.Unmarshal(raw, &payload)
			der, _ := base64.RawURLEncoding.DecodeString(payload.CSR)
			csr, err := x509.ParseCertificateRequest(der)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(orders + 1)), DNSNames: csr.DNSNames, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			signed, _ := x509.CreateCertificate(rand.Reader, leaf, root, csr.PublicKey, caKey)
			chain = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: signed})) + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
			finalized = true
			send(200, map[string]any{"status": "valid", "certificate": host + "/cert"})
		case "/cert":
			w.Header().Set("Content-Type", "application/pem-certificate-chain")
			_, _ = w.Write([]byte(chain))
		default:
			t.Errorf("unexpected ACME path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	host = server.URL
	m.directory = host + "/directory"
	m.renew(context.Background())
	if m.app.certificate.Load() == nil {
		t.Fatalf("issuance failed: %v", m.app.managedTLSStatus())
	}
	if orders != 1 {
		t.Fatalf("orders %d", orders)
	}
	for _, r := range f.records {
		if r.Type == "TXT" {
			t.Fatal("challenge leaked")
		}
	}
	var sealed []byte
	_ = m.db.QueryRow(`SELECT sealed FROM turn_managed_tls WHERE namespace=?`, m.namespace).Scan(&sealed)
	state, err := openTLS(m.app.cfg.storageKey, m.namespace, sealed)
	if err != nil || state.PrivateKey == "" || state.AccountKey == "" {
		t.Fatal("certificate/account not persisted", err)
	}
	original := m.app.certificate.Load()
	m.nextAttempt = time.Time{}
	m.renew(context.Background())
	if orders != 1 {
		t.Fatal("unnecessary renewal")
	}
	// A near-expiry cached leaf runs the same automated path and swaps atomically.
	near := *original
	leaf := *original.Leaf
	leaf.NotAfter = time.Now().Add(time.Hour)
	near.Leaf = &leaf
	m.app.certificate.Store(&near)
	m.nextAttempt = time.Time{}
	m.renew(context.Background())
	if orders != 2 || m.app.certificate.Load() == &near {
		t.Fatalf("renewal failed: %v", m.app.managedTLSStatus())
	}
	m.nextAttempt = time.Time{}
	m.app.certificate.Store(&near)
	m.issue = func(context.Context, *managedTLS) (string, string, error) {
		return "", "", errors.New("test CA unavailable")
	}
	m.renew(context.Background())
	if m.app.certificate.Load() != &near {
		t.Fatal("failed renewal replaced working leaf")
	}
	if m.nextAttempt.Before(time.Now().Add(50 * time.Minute)) {
		t.Fatal("missing retry backoff")
	}
}

func TestManagedTLSRestoresEncryptedStateWithoutIssuance(t *testing.T) {
	m, f := testTLSManager(t)
	m.namespace = m.app.cfg.publicHost + "|" + acmeDirectory
	chain, key, _ := testCertificate(t, m.app.cfg.publicHost, time.Now().Add(-time.Minute), time.Now().Add(90*24*time.Hour))
	m.state = tlsState{AccountKey: "persisted-account", Certificate: chain, PrivateKey: key}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	manifest := m.app.Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, m.db, nil, f, nil).WithProject("project-a")
	restored := &App{}
	if err := restored.initialize(m.app.cfg, "project-a"); err != nil {
		t.Fatal(err)
	}
	if err := restored.initManagedTLS(ctx); err != nil {
		t.Fatal(err)
	}
	if restored.certificate.Load() == nil || string(restored.certificate.Load().Certificate[0]) == "" {
		t.Fatal("cached certificate missing")
	}
	if restored.tlsManager.state.AccountKey != "persisted-account" || len(f.calls) != 0 {
		t.Fatal("restore issued or touched DNS")
	}
	restored.cfg.storageKey = "wrong-key-with-more-than-thirty-two-characters"
	if err := restored.initManagedTLS(ctx); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestOwnedDNSAddressFollowsDeploymentWithoutReplacingOtherRecords(t *testing.T) {
	m, f := testTLSManager(t)
	if err := m.ensureAddress(context.Background()); err != nil {
		t.Fatal(err)
	}
	oldID := m.state.AddressID
	if oldID == "" {
		t.Fatal("missing address receipt")
	}
	m.app.cfg.publicIP = netip.MustParseAddr("1.1.1.1")
	if err := m.ensureAddress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.records) != 1 || f.records[0].Value != "1.1.1.1" {
		t.Fatal("owned address not moved")
	}
	// A third-party replacement must not inherit this installation's ownership.
	f.records[0].ID = "third-party"
	f.records[0].Value = "8.8.4.4"
	if err := m.ensureAddress(context.Background()); err == nil {
		t.Fatal("third-party record overwritten")
	}
	if f.records[0].Value != "8.8.4.4" {
		t.Fatal("unrelated address changed")
	}
}
