package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"golang.org/x/crypto/acme"
)

// issueCert runs the full ACME DNS-01 dance for one FQDN. Designed
// to be invoked from a goroutine: long-lived (DNS propagation can
// take a minute), uses its own context, persists status to the DB
// at every transition so the panel reflects progress.
//
// Concurrency: caller must hold withIssuanceLock(fqdn) so two
// goroutines don't race on the same _acme-challenge slot.
func (a *App) issueCert(ctx *sdk.AppCtx, projectID, fqdn string) error {
	row, err := dbGetCertByFQDN(ctx.AppDB(), projectID, fqdn)
	if err != nil {
		return err
	}
	if row == nil {
		row, err = dbInsertOrTouchCert(ctx.AppDB(), projectID, fqdn)
		if err != nil {
			return err
		}
	}
	if a.contactEmail == "" {
		_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed",
			"acme_email config not set; can't register an ACME account")
		return errors.New("acme_email not configured")
	}
	_ = dbSetCertStatus(ctx.AppDB(), row.ID, "issuing", "")
	emit("certs.issuance.started", map[string]any{"cert_id": row.ID, "fqdn": fqdn})

	challengeType := a.selectChallengeType(ctx)

	// resolveApex talks to the Domains app to find the apex under
	// which fqdn lives. Only dns-01 needs it (TXT-write target);
	// http-01 puts the challenge at /.well-known/acme-challenge/<token>
	// on the FQDN itself.
	var apex, sub string
	if challengeType == "dns-01" {
		apex, sub, err = resolveApex(ctx, fqdn)
		if err != nil {
			_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("resolve apex", err))
			return err
		}
	}

	parent, cancel := shortCtx()
	defer cancel()

	client, err := a.acmeClient(parent, ctx.AppDB())
	if err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("acme client", err))
		return err
	}

	order, err := client.AuthorizeOrder(parent, []acme.AuthzID{{Type: "dns", Value: fqdn}})
	if err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("authorize order", err))
		return err
	}

	// Each authorization corresponds to one identifier (we only sent
	// one — the FQDN). v0.2 SAN certs will iterate.
	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(parent, authzURL)
		if err != nil {
			_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("get authorization", err))
			return err
		}
		if authz.Status == acme.StatusValid {
			continue // already authorized (cached for this account)
		}
		var (
			chal    *acme.Challenge
			cleanup func()
			prepErr error
		)
		switch challengeType {
		case "dns-01":
			chal, cleanup, prepErr = a.prepareDNS01(ctx, parent, client, authz, apex, sub, fqdn, row.ID)
		case "http-01":
			chal, cleanup, prepErr = a.prepareHTTP01(client, authz, row.ID, ctx.AppDB())
		default:
			prepErr = fmt.Errorf("unsupported challenge_type %q", challengeType)
		}
		if prepErr != nil {
			return prepErr // helpers already wrote status=failed
		}
		defer cleanup()

		if _, err := client.Accept(parent, chal); err != nil {
			_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("accept challenge", err))
			return err
		}
		if _, err := client.WaitAuthorization(parent, authz.URI); err != nil {
			_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("wait authz", err))
			return err
		}
		cleanup()
	}

	// Order should be ready; finalize.
	order, err = client.WaitOrder(parent, order.URI)
	if err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("wait order", err))
		return err
	}

	certKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("genkey", err))
		return err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: fqdn},
		DNSNames: []string{fqdn},
	}, certKey)
	if err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("csr", err))
		return err
	}

	chainDER, _, err := client.CreateOrderCert(parent, order.FinalizeURL, csrDER, true)
	if err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("create order cert", err))
		return err
	}

	certPEM := encodeCertChainPEM(chainDER)
	keyDER, err := x509.MarshalPKCS8PrivateKey(certKey)
	if err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("marshal key", err))
		return err
	}
	keyPEM := encodePrivateKeyPEM(keyDER)

	leaf, err := parseLeafCert(certPEM)
	if err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("parse leaf", err))
		return err
	}

	if err := dbSetCertIssued(ctx.AppDB(), row.ID,
		certPEM, keyPEM, leaf.SerialNumber.Text(16),
		leaf.NotBefore, leaf.NotAfter,
	); err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), row.ID, "failed", fmtErr("persist", err))
		return err
	}

	emit("certs.issuance.live", map[string]any{
		"cert_id": row.ID, "fqdn": fqdn,
		"expires_at": leaf.NotAfter.UTC().Format(time.RFC3339),
	})
	return nil
}

// acmeClient builds (or rehydrates) a *acme.Client backed by the
// account key for (directoryURL, email). On first use it generates an
// account key and registers with the ACME server; subsequent calls
// reuse the stored key + account URL.
func (a *App) acmeClient(ctx context.Context, db *sql.DB) (*acme.Client, error) {
	keyPEM, accountURL, exists, err := dbGetAccountRow(db, a.directoryURL, a.contactEmail)
	if err != nil {
		return nil, fmt.Errorf("load account row: %w", err)
	}

	var key crypto.Signer
	if exists {
		k, err := parseAccountKey(keyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse account key: %w", err)
		}
		key = k
	} else {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, fmt.Errorf("generate account key: %w", err)
		}
		key = k
	}

	client := &acme.Client{
		Key:          key,
		DirectoryURL: a.directoryURL,
	}

	if !exists {
		account := &acme.Account{Contact: []string{"mailto:" + a.contactEmail}}
		registered, err := client.Register(ctx, account, acme.AcceptTOS)
		if err != nil {
			// "account exists" means we have a valid key but never
			// recorded the URL locally — get the URI and persist.
			if !strings.Contains(err.Error(), "registration") {
				return nil, fmt.Errorf("register acme account: %w", err)
			}
			registered = &acme.Account{}
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("marshal new key: %w", err)
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if err := dbInsertAccount(db, a.directoryURL, a.contactEmail, pemBytes, registered.URI); err != nil {
			return nil, fmt.Errorf("persist account: %w", err)
		}
	} else if accountURL == "" {
		// We have a key but no recorded URL. Re-do the discovery so
		// subsequent client calls work — Register again is idempotent
		// when the key already corresponds to a registered account.
		account := &acme.Account{Contact: []string{"mailto:" + a.contactEmail}}
		_, _ = client.Register(ctx, account, acme.AcceptTOS)
	}

	return client, nil
}

func parseAccountKey(pemBytes []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block in account key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("account key is not a crypto.Signer")
	}
	return signer, nil
}

// waitForTXT polls public resolvers for the _acme-challenge TXT until
// one returns the expected value (or until timeout). 8.8.8.8 and
// 1.1.1.1 in parallel — registrars often propagate to one before the
// other.
func waitForTXT(parent context.Context, name, want string, timeout time.Duration) error {
	resolvers := []string{"8.8.8.8:53", "1.1.1.1:53"}
	deadline := time.Now().Add(timeout)
	backoff := 2 * time.Second
	for time.Now().Before(deadline) {
		for _, r := range resolvers {
			vals, err := resolverLookupTXT(parent, r, name)
			if err != nil {
				continue
			}
			for _, v := range vals {
				if strings.TrimSpace(v) == want {
					return nil
				}
			}
		}
		select {
		case <-parent.Done():
			return parent.Err()
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff += 2 * time.Second
		}
	}
	return fmt.Errorf("TXT for %q not visible after %s", name, timeout)
}

// prepareDNS01 sets up the TXT record for a dns-01 challenge and
// waits for public resolvers to see it. Returns the challenge to
// Accept and a once-guarded cleanup that deletes the TXT. On failure
// the helper writes status=failed itself; caller just returns err.
func (a *App) prepareDNS01(
	ctx *sdk.AppCtx,
	parent context.Context,
	client *acme.Client,
	authz *acme.Authorization,
	apex, sub, fqdn string,
	rowID int64,
) (*acme.Challenge, func(), error) {
	var dnsChal *acme.Challenge
	for _, ch := range authz.Challenges {
		if ch.Type == "dns-01" {
			dnsChal = ch
			break
		}
	}
	if dnsChal == nil {
		err := errors.New("ACME server didn't offer a dns-01 challenge")
		_ = dbSetCertStatus(ctx.AppDB(), rowID, "failed", err.Error())
		return nil, func() {}, err
	}
	txtValue, err := client.DNS01ChallengeRecord(dnsChal.Token)
	if err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), rowID, "failed", fmtErr("dns01 record", err))
		return nil, func() {}, err
	}
	if err := setChallengeTXT(ctx, apex, sub, txtValue); err != nil {
		_ = dbSetCertStatus(ctx.AppDB(), rowID, "failed", fmtErr("set TXT", err))
		return nil, func() {}, err
	}
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true
		_ = deleteChallengeTXT(ctx, apex, sub)
	}
	fullName := "_acme-challenge." + fqdn
	if err := waitForTXT(parent, fullName, txtValue, a.dnsTimeout); err != nil {
		cleanup()
		_ = dbSetCertStatus(ctx.AppDB(), rowID, "failed", fmtErr("wait TXT", err))
		return nil, func() {}, err
	}
	return dnsChal, cleanup, nil
}

// prepareHTTP01 writes the keyAuth to the webroot at
// .well-known/acme-challenge/<token>. The operator's reverse proxy
// (Caddy / nginx / …) serves that directory. No propagation wait
// needed — local-disk writes are immediately visible to the proxy.
func (a *App) prepareHTTP01(
	client *acme.Client,
	authz *acme.Authorization,
	rowID int64,
	db *sql.DB,
) (*acme.Challenge, func(), error) {
	var httpChal *acme.Challenge
	for _, ch := range authz.Challenges {
		if ch.Type == "http-01" {
			httpChal = ch
			break
		}
	}
	if httpChal == nil {
		err := errors.New("ACME server didn't offer an http-01 challenge")
		_ = dbSetCertStatus(db, rowID, "failed", err.Error())
		return nil, func() {}, err
	}
	keyAuth, err := client.HTTP01ChallengeResponse(httpChal.Token)
	if err != nil {
		_ = dbSetCertStatus(db, rowID, "failed", fmtErr("http01 response", err))
		return nil, func() {}, err
	}
	if err := writeChallenge(a.webrootPath, httpChal.Token, keyAuth); err != nil {
		_ = dbSetCertStatus(db, rowID, "failed", fmtErr("write challenge", err))
		return nil, func() {}, err
	}
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true
		_ = deleteChallenge(a.webrootPath, httpChal.Token)
	}
	return httpChal, cleanup, nil
}

func resolverLookupTXT(parent context.Context, server, name string) ([]string, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, network, server)
		},
	}
	cctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	return r.LookupTXT(cctx, name)
}
