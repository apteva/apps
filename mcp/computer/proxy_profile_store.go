package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type ProxyProfile struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ProviderSlug   string `json:"provider_slug"`
	ConnectionID   int64  `json:"connection_id"`
	ExternalRef    string `json:"external_ref,omitempty"`
	PoolType       string `json:"pool_type"`
	Protocol       string `json:"protocol"`
	DefaultCountry string `json:"default_country,omitempty"`
	StickyScope    string `json:"sticky_scope"`
	Enabled        bool   `json:"enabled"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

var errProxyProfileNotFound = errors.New("computer proxy profile not found")

const proxyProfileCols = `id, name, provider_slug, connection_id, external_ref,
	pool_type, protocol, default_country, sticky_scope, enabled, created_at, updated_at`

func scanProxyProfile(s rowScanner) (*ProxyProfile, error) {
	var p ProxyProfile
	var enabled int
	if err := s.Scan(&p.ID, &p.Name, &p.ProviderSlug, &p.ConnectionID, &p.ExternalRef,
		&p.PoolType, &p.Protocol, &p.DefaultCountry, &p.StickyScope, &enabled,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	return &p, nil
}

func normalizeProxyProfile(p *ProxyProfile) error {
	if p == nil {
		return errors.New("proxy profile is required")
	}
	p.Name = strings.TrimSpace(p.Name)
	p.ProviderSlug = strings.ToLower(strings.TrimSpace(p.ProviderSlug))
	p.ExternalRef = strings.TrimSpace(p.ExternalRef)
	p.PoolType = strings.ToLower(strings.TrimSpace(p.PoolType))
	p.Protocol = strings.ToLower(strings.TrimSpace(p.Protocol))
	p.DefaultCountry = strings.ToUpper(strings.TrimSpace(p.DefaultCountry))
	p.StickyScope = strings.ToLower(strings.TrimSpace(p.StickyScope))
	if p.Name == "" {
		return errors.New("proxy profile name is required")
	}
	if p.ProviderSlug == "" {
		return errors.New("proxy provider is required")
	}
	if p.ConnectionID <= 0 {
		return errors.New("proxy provider connection is required")
	}
	if p.PoolType == "" {
		p.PoolType = "residential"
	}
	if p.Protocol == "" {
		p.Protocol = "http"
	}
	if p.Protocol != "http" && p.Protocol != "https" && p.Protocol != "socks5" {
		return errors.New("proxy protocol must be http, https, or socks5")
	}
	if p.StickyScope == "" {
		p.StickyScope = "session"
	}
	if p.StickyScope != "rotating" && p.StickyScope != "session" && p.StickyScope != "context" {
		return errors.New("proxy sticky_scope must be rotating, session, or context")
	}
	if p.DefaultCountry != "" && !validProxyCountry(p.DefaultCountry) {
		return errors.New("default_country must be a two-letter ISO country code")
	}
	return nil
}

func dbCreateProxyProfile(db *sql.DB, p ProxyProfile) (*ProxyProfile, error) {
	if p.ID == "" {
		p.ID = newProxyProfileID()
	}
	if err := normalizeProxyProfile(&p); err != nil {
		return nil, err
	}
	now := nowUTC()
	_, err := db.Exec(`INSERT INTO computer_proxy_profiles (
		id, name, provider_slug, connection_id, external_ref, pool_type,
		protocol, default_country, sticky_scope, enabled, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.ProviderSlug, p.ConnectionID, p.ExternalRef, p.PoolType,
		p.Protocol, p.DefaultCountry, p.StickyScope, boolInt(p.Enabled), now, now)
	if err != nil {
		return nil, err
	}
	return dbGetProxyProfile(db, p.ID)
}

func dbGetProxyProfile(db *sql.DB, id string) (*ProxyProfile, error) {
	p, err := scanProxyProfile(db.QueryRow(`SELECT `+proxyProfileCols+` FROM computer_proxy_profiles WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errProxyProfileNotFound
	}
	return p, err
}

func dbGetProxyProfileByName(db *sql.DB, name string) (*ProxyProfile, error) {
	p, err := scanProxyProfile(db.QueryRow(`SELECT `+proxyProfileCols+` FROM computer_proxy_profiles WHERE name = ?`, strings.TrimSpace(name)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errProxyProfileNotFound
	}
	return p, err
}

func dbListProxyProfiles(db *sql.DB, enabledOnly bool) ([]*ProxyProfile, error) {
	query := `SELECT ` + proxyProfileCols + ` FROM computer_proxy_profiles`
	if enabledOnly {
		query += ` WHERE enabled = 1`
	}
	query += ` ORDER BY enabled DESC, name ASC`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProxyProfile
	for rows.Next() {
		p, err := scanProxyProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func dbUpdateProxyProfile(db *sql.DB, id string, patch map[string]any) (*ProxyProfile, error) {
	current, err := dbGetProxyProfile(db, id)
	if err != nil {
		return nil, err
	}
	next := *current
	applyProxyProfilePatch(&next, patch)
	if err := normalizeProxyProfile(&next); err != nil {
		return nil, err
	}
	_, err = db.Exec(`UPDATE computer_proxy_profiles SET
		name=?, provider_slug=?, connection_id=?, external_ref=?, pool_type=?,
		protocol=?, default_country=?, sticky_scope=?, enabled=?, updated_at=?
		WHERE id=?`, next.Name, next.ProviderSlug, next.ConnectionID, next.ExternalRef,
		next.PoolType, next.Protocol, next.DefaultCountry, next.StickyScope,
		boolInt(next.Enabled), nowUTC(), id)
	if err != nil {
		return nil, err
	}
	return dbGetProxyProfile(db, id)
}

func applyProxyProfilePatch(next *ProxyProfile, patch map[string]any) {
	if v, ok := patch["name"].(string); ok {
		next.Name = v
	}
	if v, ok := patch["provider_slug"].(string); ok {
		next.ProviderSlug = v
	}
	if v, ok := int64Arg(patch, "connection_id"); ok {
		next.ConnectionID = v
	}
	if v, ok := patch["external_ref"].(string); ok {
		next.ExternalRef = v
	}
	if v, ok := patch["pool_type"].(string); ok {
		next.PoolType = v
	}
	if v, ok := patch["protocol"].(string); ok {
		next.Protocol = v
	}
	if v, ok := patch["default_country"].(string); ok {
		next.DefaultCountry = v
	}
	if v, ok := patch["sticky_scope"].(string); ok {
		next.StickyScope = v
	}
	if v, ok := boolArg(patch, "enabled"); ok {
		next.Enabled = v
	}
}

func dbDeleteProxyProfile(db *sql.DB, id string) error {
	res, err := db.Exec(`DELETE FROM computer_proxy_profiles WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errProxyProfileNotFound
	}
	return nil
}

func int64Arg(args map[string]any, key string) (int64, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), n == float64(int64(n))
	case jsonNumber:
		i, err := n.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

// jsonNumber is the small method surface shared by encoding/json.Number.
type jsonNumber interface{ Int64() (int64, error) }

func proxyProfileConflictError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Errorf("proxy profile name already exists")
	}
	return err
}
