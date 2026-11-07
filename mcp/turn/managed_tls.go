package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"golang.org/x/crypto/acme"
)

const acmeDirectory = "https://acme-v02.api.letsencrypt.org/directory"
const renewBefore = 30 * 24 * time.Hour

type tlsState struct {
	AccountKey   string `json:"account_key"`
	Certificate  string `json:"certificate,omitempty"`
	PrivateKey   string `json:"private_key,omitempty"`
	PendingTXT   string `json:"pending_txt,omitempty"`
	AddressID    string `json:"address_id,omitempty"`
	AddressValue string `json:"address_value,omitempty"`
}
type managedTLS struct {
	app                                   *App
	db                                    *sql.DB
	platform                              sdk.PlatformClient
	namespace, project                    string
	mu                                    sync.Mutex
	phase, message                        string
	lastAttempt, lastSuccess, nextAttempt time.Time
	state                                 tlsState // only the single renewal goroutine accesses state
	wake                                  chan struct{}
	cancel                                context.CancelFunc
	done                                  chan struct{}
	// Injectable only by in-process tests, never install configuration.
	directory string
	waitDNS   func(context.Context, string, string, string) error
	issue     func(context.Context, *managedTLS) (string, string, error)
}

func sealTLS(key, namespace string, value tlsState) ([]byte, error) {
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plain, []byte(namespace)), nil
}
func openTLS(key, namespace string, sealed []byte) (tlsState, error) {
	var out tlsState
	sum := sha256.Sum256([]byte(key))
	block, _ := aes.NewCipher(sum[:])
	g, _ := cipher.NewGCM(block)
	if len(sealed) < g.NonceSize() {
		return out, errors.New("invalid managed TLS state")
	}
	plain, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], []byte(namespace))
	if err != nil {
		return out, errors.New("managed TLS storage key does not unlock the saved state")
	}
	err = json.Unmarshal(plain, &out)
	return out, err
}
func (m *managedTLS) save() error {
	sealed, err := sealTLS(m.app.cfg.storageKey, m.namespace, m.state)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(`INSERT INTO turn_managed_tls(namespace,sealed) VALUES(?,?) ON CONFLICT(namespace) DO UPDATE SET sealed=excluded.sealed`, m.namespace, sealed)
	return err
}
func (a *App) initManagedTLS(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil || ctx.PlatformAPI() == nil {
		return errors.New("managed TLS requires the app database and platform API")
	}
	if !domainsBound(ctx) {
		return errors.New("bind the Domains app before enabling managed TLS")
	}
	project := a.project
	if project == "" {
		project = a.cfg.dnsProject
	}
	if project == "" {
		return errors.New("set dns_project_id for a global TURN installation")
	}
	if a.project != "" && a.cfg.dnsProject != "" && a.cfg.dnsProject != a.project {
		return errors.New("DNS project must match this TURN installation")
	}
	m := &managedTLS{app: a, db: ctx.AppDB(), platform: ctx.PlatformAPI(), project: project, namespace: a.cfg.publicHost + "|" + acmeDirectory, phase: "pending", wake: make(chan struct{}, 1), done: make(chan struct{}), directory: acmeDirectory, waitDNS: waitManagedDNS, issue: issueManagedCertificate}
	var sealed []byte
	err := m.db.QueryRow(`SELECT sealed FROM turn_managed_tls WHERE namespace=?`, m.namespace).Scan(&sealed)
	if err != nil && err != sql.ErrNoRows {
		return errors.New("cannot read managed TLS state")
	}
	if err == nil {
		if m.state, err = openTLS(a.cfg.storageKey, m.namespace, sealed); err != nil {
			return err
		}
	}
	if m.state.Certificate != "" {
		cert, err := parseCertificate(m.state.Certificate, m.state.PrivateKey, a.cfg.publicHost, time.Now())
		if err == nil {
			a.certificate.Store(cert)
			m.phase = "live"
		} // An expired leaf must be renewable after restart.
	}
	a.tlsManager = m
	return nil
}
func (a *App) managedTLSStatus() map[string]any {
	if a.tlsManager == nil {
		return map[string]any{"mode": a.cfg.tlsMode, "automatic": false, "storage_key_configured": a.cfg.storageKey != ""}
	}
	m := a.tlsManager
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]any{"mode": "managed", "automatic": true, "phase": m.phase, "message": m.message, "last_attempt": unixTime(m.lastAttempt), "last_success": unixTime(m.lastSuccess), "next_check": unixTime(m.nextAttempt), "storage_key_configured": true, "authority": "Let's Encrypt", "dns_zone": a.cfg.dnsZone}
}
func unixTime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
func (m *managedTLS) setPhase(phase, message string) {
	m.mu.Lock()
	m.phase, m.message = phase, message
	m.mu.Unlock()
}
func (m *managedTLS) start() {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
			case <-ticker.C:
			}
			m.app.mu.Lock()
			active := m.app.admittingLocked(time.Now())
			m.app.mu.Unlock()
			if !active {
				continue
			}
			m.renew(ctx)
		}
	}()
	m.wake <- struct{}{}
}
func (m *managedTLS) stop() {
	if m.cancel != nil {
		m.cancel()
		<-m.done
	}
}
func (m *managedTLS) renew(parent context.Context) {
	now := time.Now()
	m.mu.Lock()
	next := m.nextAttempt
	m.mu.Unlock()
	if now.Before(next) {
		return
	}
	m.mu.Lock()
	m.lastAttempt = now
	m.nextAttempt = now.Add(time.Hour)
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	m.setPhase("issuing", "Preparing DNS validation")
	if err := m.cleanupTXT(ctx); err != nil {
		m.setPhase("error", "Cannot remove this installation's previous DNS challenge; retry after checking Domains")
		return
	}
	if err := m.ensureAddress(ctx); err != nil {
		m.setPhase("error", err.Error())
		return
	}
	cert := m.app.certificate.Load()
	if cert != nil && cert.Leaf.NotAfter.Sub(time.Now()) > renewBefore {
		m.setPhase("live", "Automatic certificate is active")
		return
	}
	chain, key, err := m.issue(ctx, m)
	if err != nil {
		m.setPhase("error", err.Error())
		return
	}
	parsed, err := parseCertificate(chain, key, m.app.cfg.publicHost, time.Now())
	if err != nil {
		m.setPhase("error", "Certificate authority returned an invalid server certificate")
		return
	}
	previous := m.state
	m.state.Certificate, m.state.PrivateKey = chain, key
	if err = m.save(); err != nil {
		m.state = previous
		m.setPhase("error", "Cannot persist the renewed certificate; previous certificate retained")
		return
	}
	m.app.certificate.Store(parsed)
	m.mu.Lock()
	m.phase = "live"
	m.message = "Automatic certificate is active"
	m.lastSuccess = time.Now()
	m.nextAttempt = time.Now().Add(time.Hour)
	m.mu.Unlock()
}
func (m *managedTLS) domains(ctx context.Context, tool string, args map[string]any, out any) error {
	if tool == "domain_records_set" {
		m.app.mu.Lock()
		active := m.app.admittingLocked(time.Now())
		m.app.mu.Unlock()
		if !active {
			return errors.New("Only the active TURN execution can manage DNS")
		}
	}
	args["_project_id"] = m.project
	if err := sdk.CallAppResultContext(ctx, m.platform, "domains", tool, args, out); err != nil {
		return fmt.Errorf("Domains %s failed", tool)
	}
	return nil
}

type dnsRecord struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	TTL   int    `json:"ttl"`
	Prio  int    `json:"prio"`
}

func (m *managedTLS) records(ctx context.Context, name string) ([]dnsRecord, error) {
	var out struct {
		Records      []dnsRecord `json:"records"`
		Capabilities *struct {
			WriteTypes  []string `json:"write_types"`
			DeleteTypes []string `json:"delete_types"`
		} `json:"capabilities"`
	}
	err := m.domains(ctx, "domain_records_list", map[string]any{"domain": m.app.cfg.dnsZone, "name": name}, &out)
	if err == nil {
		if out.Capabilities == nil {
			return nil, errors.New("Managed TLS requires Domains 1.2.0 or newer; upgrade the bound app")
		}
		has := func(values []string, want string) bool {
			for _, v := range values {
				if v == want {
					return true
				}
			}
			return false
		}
		if !has(out.Capabilities.WriteTypes, "A") || !has(out.Capabilities.WriteTypes, "TXT") || !has(out.Capabilities.DeleteTypes, "TXT") {
			return nil, errors.New("DNS provider must support A records and exact TXT creation/deletion")
		}
	}
	return out.Records, err
}
func (m *managedTLS) relayName() string {
	return strings.TrimSuffix(m.app.cfg.publicHost, "."+m.app.cfg.dnsZone)
}
func (m *managedTLS) ensureAddress(ctx context.Context) error {
	records, err := m.records(ctx, m.relayName())
	if err != nil {
		return err
	}
	found := false
	var previous *dnsRecord
	for i, r := range records {
		if r.Type == "CNAME" || r.Type == "AAAA" {
			return errors.New("Relay hostname has conflicting DNS records; existing records were preserved")
		}
		if r.Type != "A" {
			continue
		}
		if r.Value == m.app.cfg.publicIP.String() {
			found = true
			continue
		}
		if r.ID == m.state.AddressID && r.Value == m.state.AddressValue && r.ID != "" {
			v := records[i]
			previous = &v
			continue
		}
		return errors.New("Relay hostname has conflicting DNS records; existing records were preserved")
	}
	if found && previous == nil {
		return nil
	}
	args := map[string]any{"domain": m.app.cfg.dnsZone, "name": m.relayName(), "type": "A", "value": m.app.cfg.publicIP.String(), "ttl": 600, "mode": "create"}
	if previous != nil {
		args["mode"] = "upsert"
		args["record_id"] = previous.ID
		args["expected_record"] = map[string]any{"value": previous.Value, "ttl": previous.TTL, "prio": previous.Prio}
	}
	var out map[string]any
	if err = m.domains(ctx, "domain_records_set", args, &out); err != nil {
		return err
	}
	records, err = m.records(ctx, m.relayName())
	if err != nil {
		return err
	}
	var id string
	for _, r := range records {
		if r.Type == "A" {
			if r.Value != m.app.cfg.publicIP.String() || id != "" {
				return errors.New("Relay DNS changed concurrently; inspect the hostname before retrying")
			}
			id = r.ID
		}
	}
	if id == "" {
		return errors.New("DNS provider did not return the relay record ID")
	}
	m.state.AddressID, m.state.AddressValue = id, m.app.cfg.publicIP.String()
	return m.save()
}
func (m *managedTLS) presentTXT(ctx context.Context, value string) error {
	name := "_acme-challenge." + m.relayName()
	records, err := m.records(ctx, name)
	if err != nil {
		return err
	}
	// Never follow an existing challenge delegation or replace any TXT RRset.
	for _, r := range records {
		if r.Type == "TXT" && strings.Trim(r.Value, "\"") == value {
			return errors.New("An identical DNS validation record already exists; it was preserved")
		}
		if r.Type == "CNAME" || r.Type == "NS" {
			return errors.New("DNS challenge has an existing delegation; it was preserved")
		}
	}
	m.state.PendingTXT = value
	if err = m.save(); err != nil {
		return errors.New("Cannot persist DNS challenge ownership")
	}
	var out map[string]any
	return m.domains(ctx, "domain_records_set", map[string]any{"domain": m.app.cfg.dnsZone, "name": name, "type": "TXT", "value": value, "ttl": 600, "mode": "create"}, &out)
}
func (m *managedTLS) cleanupTXT(ctx context.Context) error {
	if m.state.PendingTXT == "" {
		return nil
	}
	name := "_acme-challenge." + m.relayName()
	records, err := m.records(ctx, name)
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Type != "TXT" || strings.Trim(r.Value, "\"") != m.state.PendingTXT {
			continue
		}
		if r.ID == "" {
			return errors.New("DNS provider did not supply an exact record ID")
		}
		var out map[string]any
		if err = m.domains(ctx, "domain_records_delete", map[string]any{"domain": m.app.cfg.dnsZone, "name": name, "type": "TXT", "record_id": r.ID, "expected_record": map[string]any{"value": r.Value, "ttl": r.TTL, "prio": r.Prio}}, &out); err != nil {
			return err
		}
	}
	m.state.PendingTXT = ""
	return m.save()
}
func waitManagedDNS(ctx context.Context, host, ip, txt string) error {
	// Both independent public resolvers must see the requested values before
	// asking the CA to validate; a local split-horizon DNS result is insufficient.
	for {
		ready := true
		for _, server := range []string{"1.1.1.1:53", "8.8.8.8:53"} {
			r := &net.Resolver{PreferGo: true, Dial: func(c context.Context, n, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(c, n, server)
			}}
			q, cancel := context.WithTimeout(ctx, 5*time.Second)
			values, e := r.LookupTXT(q, "_acme-challenge."+host)
			cancel()
			seen := false
			for _, v := range values {
				seen = seen || v == txt
			}
			if e != nil || !seen {
				ready = false
			}
			q, cancel = context.WithTimeout(ctx, 5*time.Second)
			ips, e := r.LookupHost(q, host)
			cancel()
			if e != nil || len(ips) != 1 || ips[0] != ip {
				ready = false
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("Public DNS validation timed out; check the relay A record and Domains connection")
		case <-time.After(5 * time.Second):
		}
	}
}
func issueManagedCertificate(ctx context.Context, m *managedTLS) (string, string, error) {
	fail := func(message string) (string, string, error) { return "", "", errors.New(message) }
	if err := m.ensureAddress(ctx); err != nil {
		return "", "", err
	}
	if m.state.AccountKey == "" {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return fail("Cannot create ACME account key")
		}
		der, _ := x509.MarshalPKCS8PrivateKey(key)
		m.state.AccountKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		if m.save() != nil {
			return fail("Cannot persist ACME account key")
		}
	}
	block, _ := pem.Decode([]byte(m.state.AccountKey))
	if block == nil {
		return fail("Saved ACME account key is invalid")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return fail("Saved ACME account key is invalid")
	}
	signer, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return fail("Saved ACME account key is invalid")
	}
	client := &acme.Client{Key: signer, DirectoryURL: m.directory, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
	acct := &acme.Account{}
	if m.app.cfg.acmeEmail != "" {
		acct.Contact = []string{"mailto:" + m.app.cfg.acmeEmail}
	}
	if _, err = client.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return fail("ACME account registration failed; retry later")
	}
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(m.app.cfg.publicHost))
	if err != nil {
		return fail("ACME certificate order failed; retry later")
	}
	for _, authURL := range order.AuthzURLs {
		auth, err := client.GetAuthorization(ctx, authURL)
		if err != nil {
			return fail("Cannot read ACME authorization")
		}
		if auth.Status == acme.StatusValid {
			continue
		}
		var challenge *acme.Challenge
		for _, c := range auth.Challenges {
			if c.Type == "dns-01" {
				challenge = c
				break
			}
		}
		if challenge == nil {
			return fail("Certificate authority did not offer DNS validation")
		}
		value, err := client.DNS01ChallengeRecord(challenge.Token)
		if err != nil {
			return fail("Cannot construct DNS validation")
		}
		if err = m.presentTXT(ctx, value); err != nil {
			return "", "", err
		}
		// Use a fresh bounded context for cleanup even if issuance was cancelled.
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = m.cleanupTXT(c)
		}()
		m.setPhase("dns", "Waiting for public DNS propagation")
		if err = m.waitDNS(ctx, m.app.cfg.publicHost, m.app.cfg.publicIP.String(), value); err != nil {
			return "", "", err
		}
		m.setPhase("issuing", "Certificate authority is validating the hostname")
		if _, err = client.Accept(ctx, challenge); err != nil {
			return fail("Certificate authority rejected the DNS challenge")
		}
		if _, err = client.WaitAuthorization(ctx, auth.URI); err != nil {
			return fail("Certificate authority could not validate public DNS")
		}
		if err = m.cleanupTXT(ctx); err != nil {
			return fail("Cannot remove this installation's DNS validation record")
		}
	}
	order, err = client.WaitOrder(ctx, order.URI)
	if err != nil {
		return fail("Certificate order did not become ready")
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fail("Cannot generate certificate key")
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{m.app.cfg.publicHost}}, leafKey)
	if err != nil {
		return fail("Cannot generate certificate request")
	}
	chain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		var oe *acme.OrderError
		if !errors.As(err, &oe) || (oe.Status != acme.StatusReady && oe.Status != acme.StatusProcessing) {
			return fail("Certificate finalization failed")
		}
		for {
			order, err = client.GetOrder(ctx, order.URI)
			if err != nil {
				return fail("Cannot read finalized certificate order")
			}
			if order.Status == acme.StatusValid {
				chain, err = client.FetchCert(ctx, order.CertURL, true)
				break
			}
			if order.Status == acme.StatusInvalid {
				return fail("Certificate authority rejected the certificate request")
			}
			select {
			case <-ctx.Done():
				return fail("Certificate finalization timed out")
			case <-time.After(2 * time.Second):
			}
		}
	}
	if err != nil || len(chain) == 0 {
		return fail("Cannot download issued certificate")
	}
	var cert strings.Builder
	for _, der := range chain {
		_ = pem.Encode(&cert, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	der, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return fail("Cannot encode certificate key")
	}
	return cert.String(), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}
func (a *App) handleTLSDomains(w http.ResponseWriter, r *http.Request) {
	if a.appCtx == nil || a.appCtx.PlatformAPI() == nil {
		http.Error(w, "platform unavailable", 503)
		return
	}
	project := a.project
	if project == "" {
		project = a.cfg.dnsProject
	}
	if project == "" {
		project = r.URL.Query().Get("project_id")
	}
	if project == "" {
		http.Error(w, "select a DNS project first", 400)
		return
	}
	var out struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	if err := sdk.CallAppResultContext(r.Context(), a.appCtx.PlatformAPI(), "domains", "domain_list", map[string]any{"_project_id": project}, &out); err != nil {
		http.Error(w, "Bind Domains and select the project containing the DNS zone", 400)
		return
	}
	writeJSON(w, 200, out)
}
func (a *App) handleTLSRenew(w http.ResponseWriter, r *http.Request) {
	m := a.tlsManager
	if m == nil {
		http.Error(w, "enable managed TLS in Settings first", 409)
		return
	}
	a.mu.Lock()
	active := a.admittingLocked(time.Now())
	a.mu.Unlock()
	if !active {
		http.Error(w, "only the active TURN execution can request a certificate", 409)
		return
	}
	// Wake the scheduled check without allowing clients to force unnecessary CA
	// orders or bypass the one-hour retry backoff.
	select {
	case m.wake <- struct{}{}:
	default:
	}
	writeJSON(w, 202, map[string]any{"queued": true})
}

// requires.apps provides one Domains picker and its exact install binding.
func domainsBound(ctx *sdk.AppCtx) bool {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return false
	}
	id, err := ctx.PlatformAPI().WhoAmI()
	if err != nil || id == nil {
		return false
	}
	switch v := id.Bindings["domains"].(type) {
	case float64:
		return v > 0
	case int:
		return v > 0
	case int64:
		return v > 0
	}
	return false
}
