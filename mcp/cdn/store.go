package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Zone is one (hostname → origin) mapping. status is driven by
// route_status — "active" means the route leg landed; dns/cert
// failures don't gate liveness because the local-dev path runs
// without those legs at all. The per-leg fields tell you which
// pieces actually fired.
type Zone struct {
	ID           int64  `json:"id"`
	ProjectID    string `json:"project_id,omitempty"`
	Hostname     string `json:"hostname"`
	OriginURL    string `json:"origin_url"`
	RecordType   string `json:"record_type"`
	RecordValue  string `json:"record_value"`
	AllowHTTP    bool   `json:"allow_http"`
	Status       string `json:"status"`
	StatusDetail string `json:"status_detail,omitempty"`
	DNSStatus    string `json:"dns_status,omitempty"`
	CertStatus   string `json:"cert_status,omitempty"`
	RouteStatus  string `json:"route_status,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// ─── Validation ───────────────────────────────────────────────────

func validateHostname(h string) error {
	h = strings.TrimSpace(strings.ToLower(h))
	if h == "" {
		return errors.New("hostname required")
	}
	if strings.ContainsAny(h, " \t\r\n@/?#") {
		return errors.New("hostname must not contain whitespace, @, /, ?, or #")
	}
	if strings.Contains(h, "://") {
		return errors.New("hostname must not include a scheme")
	}
	if strings.Contains(h, ":") {
		return errors.New("hostname must not include a port")
	}
	if !strings.Contains(h, ".") {
		return errors.New("hostname must be an FQDN (contain at least one dot)")
	}
	if len(h) > 253 {
		return errors.New("hostname too long (>253 chars)")
	}
	return nil
}

func validateOriginURL(s string) error {
	if s == "" {
		return errors.New("origin_url required")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid origin_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("origin_url scheme must be http or https; got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("origin_url must include a host")
	}
	return nil
}

// splitApex splits "files.acme.com" into apex="acme.com" and sub="files".
// For an apex hostname ("acme.com"), sub is "".
//
// Naive on multi-label TLDs ("acme.co.uk" → apex "co.uk") — same
// limitation domains app's splitSLDTLD has. v0.1 accepts this; v0.2
// can integrate the public-suffix list if it bites.
func splitApex(hostname string) (apex, sub string) {
	parts := strings.Split(hostname, ".")
	if len(parts) <= 2 {
		return hostname, ""
	}
	apex = strings.Join(parts[len(parts)-2:], ".")
	sub = strings.Join(parts[:len(parts)-2], ".")
	return apex, sub
}

// ─── DB ops ────────────────────────────────────────────────────────

const zoneSelectCols = `id, project_id, hostname, origin_url, record_type,
		record_value, allow_http, status, status_detail, dns_status, cert_status, route_status,
		COALESCE(created_at,''), COALESCE(updated_at,'')`

func scanZone(s interface{ Scan(...any) error }) (*Zone, error) {
	z := &Zone{}
	var allow int
	err := s.Scan(&z.ID, &z.ProjectID, &z.Hostname, &z.OriginURL, &z.RecordType,
		&z.RecordValue, &allow, &z.Status, &z.StatusDetail, &z.DNSStatus, &z.CertStatus, &z.RouteStatus,
		&z.CreatedAt, &z.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	z.AllowHTTP = allow != 0
	return z, nil
}

func dbInsertZone(db *sql.DB, z *Zone) (int64, error) {
	allow := 0
	if z.AllowHTTP {
		allow = 1
	}
	res, err := db.Exec(
		`INSERT INTO zones (project_id, hostname, origin_url, record_type, record_value,
			allow_http, status, status_detail, dns_status, cert_status, route_status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		z.ProjectID, z.Hostname, z.OriginURL, z.RecordType, z.RecordValue,
		allow, z.Status, z.StatusDetail, z.DNSStatus, z.CertStatus, z.RouteStatus,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbUpdateZoneStatus(db *sql.DB, id int64, status, detail, dnsStatus, certStatus, routeStatus string) error {
	_, err := db.Exec(
		`UPDATE zones SET status = ?, status_detail = ?,
			dns_status = ?, cert_status = ?, route_status = ?,
			updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		status, detail, dnsStatus, certStatus, routeStatus, id,
	)
	return err
}

func dbGetZone(db *sql.DB, pid string, id int64) (*Zone, error) {
	return scanZone(db.QueryRow(
		`SELECT `+zoneSelectCols+` FROM zones WHERE id = ? AND project_id = ?`,
		id, pid,
	))
}

func dbGetZoneByHostname(db *sql.DB, pid, hostname string) (*Zone, error) {
	return scanZone(db.QueryRow(
		`SELECT `+zoneSelectCols+` FROM zones WHERE project_id = ? AND hostname = ?`,
		pid, hostname,
	))
}

func dbListZones(db *sql.DB, pid string) ([]*Zone, error) {
	rows, err := db.Query(
		`SELECT `+zoneSelectCols+` FROM zones WHERE project_id = ? ORDER BY hostname`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Zone{}
	for rows.Next() {
		z, err := scanZone(rows)
		if err == nil && z != nil {
			out = append(out, z)
		}
	}
	return out, nil
}

func dbDeleteZone(db *sql.DB, pid string, id int64) (bool, error) {
	res, err := db.Exec(`DELETE FROM zones WHERE id = ? AND project_id = ?`, id, pid)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
