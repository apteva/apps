package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Route is the on-the-wire shape of a host_routes row. Owner_kind is
// "deploy" / "code" / "manual" and lets the panel show the right
// affordances; the platform doesn't gate on it.
type Route struct {
	ID              int64  `json:"id"`
	Hostname        string `json:"hostname"`
	Target          string `json:"target"`
	OwnerInstallID  int64  `json:"owner_install_id"`
	OwnerKind       string `json:"owner_kind"`
	CertFQDN        string `json:"cert_fqdn,omitempty"`
	AllowHTTP       bool   `json:"allow_http,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

// RegisterInput is the canonical write shape. Hostname and target
// are required; everything else has a sensible default. owner_kind
// is set by the registration handler from the caller's manifest /
// header (we don't trust caller-supplied values).
type RegisterInput struct {
	Hostname       string
	Target         string
	OwnerInstallID int64
	OwnerKind      string
	CertFQDN       string
	AllowHTTP      bool
}

// ─── Validation ───────────────────────────────────────────────────

// validateHostname is intentionally permissive — the registry of
// what's a "real" hostname lives in the Domains app. We just reject
// the obvious garbage so the matcher can rely on a few invariants:
// no whitespace, no scheme, no path, no port, no empty.
func validateHostname(h string) error {
	if h == "" {
		return errors.New("hostname required")
	}
	if strings.ContainsAny(h, " \t\r\n") {
		return errors.New("hostname must not contain whitespace")
	}
	if strings.Contains(h, "://") {
		return errors.New("hostname must not include a scheme")
	}
	if strings.ContainsAny(h, "/?#") {
		return errors.New("hostname must not include a path, query, or fragment")
	}
	if strings.Contains(h, ":") {
		return errors.New("hostname must not include a port")
	}
	if len(h) > 253 {
		return errors.New("hostname too long (>253 chars)")
	}
	return nil
}

// validateTarget rejects targets the matcher couldn't proxy. Must be
// http:// or https:// with a host. Unix sockets and other schemes
// are out of scope for v0.1; reject early with a clear message.
func validateTarget(t string) error {
	if t == "" {
		return errors.New("target required")
	}
	u, err := url.Parse(t)
	if err != nil {
		return fmt.Errorf("invalid target URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("target scheme must be http or https; got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("target must include a host")
	}
	return nil
}

// ─── DB ops ────────────────────────────────────────────────────────

const routeCols = `id, hostname, target, owner_install_id, owner_kind,
		cert_fqdn, allow_http, created_at, updated_at`

func scanRoute(s rowScanner) (*Route, error) {
	var r Route
	var allow int
	if err := s.Scan(&r.ID, &r.Hostname, &r.Target, &r.OwnerInstallID,
		&r.OwnerKind, &r.CertFQDN, &allow, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.AllowHTTP = allow != 0
	return &r, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

// dbUpsertRoute inserts a new route or updates an existing one if
// the SAME owner re-registers it. Different-owner conflicts surface
// as ErrHostnameOwnedElsewhere so the tool layer can return a clean
// 409 / hostname_in_use_by_other_owner.
func dbUpsertRoute(db *sql.DB, in RegisterInput) (*Route, string, error) {
	if err := validateHostname(in.Hostname); err != nil {
		return nil, "", err
	}
	if err := validateTarget(in.Target); err != nil {
		return nil, "", err
	}
	existing, err := dbGetRouteByHostname(db, in.Hostname)
	if err != nil {
		return nil, "", err
	}
	allow := 0
	if in.AllowHTTP {
		allow = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if existing != nil {
		if existing.OwnerInstallID != in.OwnerInstallID {
			return nil, "", ErrHostnameOwnedElsewhere
		}
		_, err := db.Exec(`UPDATE host_routes
			SET target = ?, owner_kind = ?, cert_fqdn = ?, allow_http = ?, updated_at = ?
			WHERE id = ?`,
			in.Target, in.OwnerKind, in.CertFQDN, allow, now, existing.ID)
		if err != nil {
			return nil, "", err
		}
		updated, err := dbGetRouteByID(db, existing.ID)
		return updated, "updated", err
	}
	res, err := db.Exec(`INSERT INTO host_routes
		(hostname, target, owner_install_id, owner_kind, cert_fqdn, allow_http, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Hostname, in.Target, in.OwnerInstallID, in.OwnerKind, in.CertFQDN, allow, now, now)
	if err != nil {
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	created, err := dbGetRouteByID(db, id)
	return created, "created", err
}

// ErrHostnameOwnedElsewhere fires when one owner tries to claim a
// hostname another owner already holds. The tool layer maps this to
// 409 hostname_in_use_by_other_owner; the panel surfaces it as a
// "this hostname is taken by <owner>" inline error.
var ErrHostnameOwnedElsewhere = errors.New("hostname owned by a different install")

func dbGetRouteByHostname(db *sql.DB, hostname string) (*Route, error) {
	row := db.QueryRow(`SELECT `+routeCols+` FROM host_routes WHERE hostname = ?`, hostname)
	r, err := scanRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func dbGetRouteByID(db *sql.DB, id int64) (*Route, error) {
	row := db.QueryRow(`SELECT `+routeCols+` FROM host_routes WHERE id = ?`, id)
	return scanRoute(row)
}

// dbDeleteRouteByHostname removes a route. The caller's owner id is
// passed through so the handler can check (in DB, atomic with the
// delete) that they actually own it. Returns (true, nil) when a row
// was deleted, (false, ErrNotOwner) when the hostname exists but is
// owned by someone else, (false, nil) when there's nothing to delete.
func dbDeleteRouteByHostname(db *sql.DB, hostname string, ownerID int64) (bool, error) {
	existing, err := dbGetRouteByHostname(db, hostname)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	if existing.OwnerInstallID != ownerID {
		return false, ErrNotOwner
	}
	res, err := db.Exec(`DELETE FROM host_routes WHERE id = ?`, existing.ID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ErrNotOwner — the caller asked to delete a route they don't own.
// Surfaces as 403 in the REST handler.
var ErrNotOwner = errors.New("not the route's owner")

// dbDeleteRoutesForOwner clears every route owned by a given install.
// Used by the orphan sweeper when an install is uninstalled. Returns
// the hostnames that were removed so the caller can fan out
// routes.changed events.
func dbDeleteRoutesForOwner(db *sql.DB, ownerID int64) ([]string, error) {
	rows, err := db.Query(`SELECT hostname FROM host_routes WHERE owner_install_id = ?`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hostnames []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hostnames = append(hostnames, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(hostnames) == 0 {
		return nil, nil
	}
	_, err = db.Exec(`DELETE FROM host_routes WHERE owner_install_id = ?`, ownerID)
	return hostnames, err
}

// dbListRoutes returns every route, optionally filtered to one
// owner. Sort by hostname for stable panel rendering.
func dbListRoutes(db *sql.DB, ownerFilter *int64) ([]*Route, error) {
	q := `SELECT ` + routeCols + ` FROM host_routes`
	args := []any{}
	if ownerFilter != nil {
		q += ` WHERE owner_install_id = ?`
		args = append(args, *ownerFilter)
	}
	q += ` ORDER BY hostname ASC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Route
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
