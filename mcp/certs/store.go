package main

import (
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Cert is the public-shape row. cert_pem and key_pem are intentionally
// excluded from JSON — only cert_material returns them, and only to
// privileged callers.
type Cert struct {
	ID            int64  `json:"id"`
	ProjectID     string `json:"project_id,omitempty"`
	FQDN          string `json:"fqdn"`
	Status        string `json:"status"`
	Serial        string `json:"serial,omitempty"`
	IssuedAt      string `json:"issued_at,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	LastRenewedAt string `json:"last_renewed_at,omitempty"`
	LastAttemptAt string `json:"last_attempt_at,omitempty"`
	Error         string `json:"error,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type CertMaterial struct {
	FQDN     string
	CertPEM  []byte
	KeyPEM   []byte
	Issued   time.Time
	Expires  time.Time
	Status   string
}

// ─── Cert CRUD ─────────────────────────────────────────────────────

func dbInsertOrTouchCert(db *sql.DB, projectID, fqdn string) (*Cert, error) {
	if strings.TrimSpace(fqdn) == "" {
		return nil, errors.New("fqdn required")
	}
	_, err := db.Exec(
		`INSERT INTO certs (project_id, fqdn, status, last_attempt_at)
		 VALUES (?, ?, 'pending', ?)
		 ON CONFLICT(project_id, fqdn) DO UPDATE SET
		   last_attempt_at = excluded.last_attempt_at,
		   updated_at      = CURRENT_TIMESTAMP`,
		projectID, fqdn, nowUTC(),
	)
	if err != nil {
		return nil, err
	}
	return dbGetCertByFQDN(db, projectID, fqdn)
}

func dbSetCertStatus(db *sql.DB, id int64, status, errMsg string) error {
	_, err := db.Exec(
		`UPDATE certs SET status = ?, error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, errMsg, id,
	)
	return err
}

func dbSetCertIssued(db *sql.DB, id int64, certPEM, keyPEM []byte, serial string, issuedAt, expiresAt time.Time) error {
	_, err := db.Exec(
		`UPDATE certs
		   SET status = 'live', cert_pem = ?, key_pem = ?,
		       serial = ?, issued_at = ?, expires_at = ?,
		       last_renewed_at = ?,
		       error = '', updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		certPEM, keyPEM, serial,
		issuedAt.UTC().Format(time.RFC3339),
		expiresAt.UTC().Format(time.RFC3339),
		issuedAt.UTC().Format(time.RFC3339),
		id,
	)
	return err
}

func dbGetCert(db *sql.DB, id int64) (*Cert, error) {
	row := db.QueryRow(
		`SELECT `+certColumns+` FROM certs WHERE id = ?`, id)
	return scanCert(row)
}

func dbGetCertByFQDN(db *sql.DB, projectID, fqdn string) (*Cert, error) {
	row := db.QueryRow(
		`SELECT `+certColumns+` FROM certs WHERE project_id = ? AND fqdn = ?`,
		projectID, fqdn)
	c, err := scanCert(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func dbListCerts(db *sql.DB, projectID string, includeRevoked bool) ([]Cert, error) {
	q := `SELECT ` + certColumns + ` FROM certs WHERE project_id = ?`
	if !includeRevoked {
		q += ` AND status != 'revoked'`
	}
	q += ` ORDER BY id DESC`
	rows, err := db.Query(q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Cert{}
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func dbCertMaterial(db *sql.DB, projectID, fqdn string) (*CertMaterial, error) {
	row := db.QueryRow(
		`SELECT fqdn, cert_pem, key_pem, status,
		        COALESCE(issued_at,''), COALESCE(expires_at,'')
		   FROM certs
		  WHERE project_id = ? AND fqdn = ? AND status = 'live'`,
		projectID, fqdn)
	var m CertMaterial
	var issuedStr, expiresStr string
	if err := row.Scan(&m.FQDN, &m.CertPEM, &m.KeyPEM, &m.Status, &issuedStr, &expiresStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	m.Issued, _ = time.Parse(time.RFC3339, issuedStr)
	m.Expires, _ = time.Parse(time.RFC3339, expiresStr)
	return &m, nil
}

// dbDueForRenewal lists live certs whose expiry is within `window`.
// Failed certs are not retried by the renewal worker — operators
// should re-issue them explicitly.
func dbDueForRenewal(db *sql.DB, window time.Duration) ([]Cert, error) {
	cutoff := time.Now().Add(window).UTC().Format(time.RFC3339)
	rows, err := db.Query(
		`SELECT `+certColumns+`
		   FROM certs
		  WHERE status = 'live' AND expires_at IS NOT NULL AND expires_at <= ?`,
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Cert{}
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

const certColumns = `id, project_id, fqdn, status, serial,
		COALESCE(issued_at,''), COALESCE(expires_at,''),
		COALESCE(last_renewed_at,''), COALESCE(last_attempt_at,''),
		error, created_at, updated_at`

type rowScanner interface{ Scan(...any) error }

func scanCert(r rowScanner) (*Cert, error) {
	var c Cert
	if err := r.Scan(
		&c.ID, &c.ProjectID, &c.FQDN, &c.Status, &c.Serial,
		&c.IssuedAt, &c.ExpiresAt,
		&c.LastRenewedAt, &c.LastAttemptAt,
		&c.Error, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

// ─── ACME accounts ─────────────────────────────────────────────────

// dbGetOrCreateAccountKey returns the PEM-encoded private key + LE
// account URL for (directoryURL, email). Caller is responsible for
// registering the account with ACME on first use and persisting the
// account URL via dbSetAccountURL.
func dbGetAccountRow(db *sql.DB, directoryURL, email string) (keyPEM []byte, accountURL string, exists bool, err error) {
	row := db.QueryRow(
		`SELECT account_key, account_url
		   FROM acme_accounts
		  WHERE directory_url = ? AND email = ?`,
		directoryURL, email)
	err = row.Scan(&keyPEM, &accountURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	return keyPEM, accountURL, true, nil
}

func dbInsertAccount(db *sql.DB, directoryURL, email string, keyPEM []byte, accountURL string) error {
	_, err := db.Exec(
		`INSERT INTO acme_accounts (directory_url, email, account_key, account_url)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(directory_url, email) DO UPDATE SET
		   account_key = excluded.account_key,
		   account_url = excluded.account_url`,
		directoryURL, email, keyPEM, accountURL,
	)
	return err
}

// ─── PEM helpers ───────────────────────────────────────────────────

func encodePrivateKeyPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func encodeCertChainPEM(chain [][]byte) []byte {
	var buf strings.Builder
	for _, der := range chain {
		buf.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	return []byte(buf.String())
}

func parseLeafCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("cert PEM has no block")
	}
	return x509.ParseCertificate(block.Bytes)
}
