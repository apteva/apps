package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type store struct {
	db *sql.DB
}

type tunnelConfig struct {
	BaseDomain string    `json:"base_domain"`
	ProjectID  string    `json:"project_id,omitempty"`
	DNS        dnsStatus `json:"dns"`
	CreatedAt  string    `json:"created_at"`
	UpdatedAt  string    `json:"updated_at"`
}

type dnsStatus struct {
	Managed bool   `json:"managed"`
	Domain  string `json:"domain,omitempty"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type,omitempty"`
	Value   string `json:"value,omitempty"`
	Error   string `json:"error,omitempty"`
}

type tunnel struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"project_id"`
	Name               string `json:"name"`
	Hostname           string `json:"hostname"`
	URL                string `json:"url"`
	Status             string `json:"status"`
	Connected          bool   `json:"connected"`
	RequestCount       int64  `json:"request_count"`
	BytesIn            int64  `json:"bytes_in"`
	BytesOut           int64  `json:"bytes_out"`
	LastConnectedAt    string `json:"last_connected_at,omitempty"`
	LastDisconnectedAt string `json:"last_disconnected_at,omitempty"`
	LastRequestAt      string `json:"last_request_at,omitempty"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
	TokenHash          string `json:"-"`
}

func (s *store) config() (*tunnelConfig, error) {
	var cfg tunnelConfig
	var managed int
	err := s.db.QueryRow(`
		SELECT base_domain, project_id, dns_managed, dns_domain, dns_name,
		       dns_type, dns_value, dns_error, created_at, updated_at
		FROM tunnel_config WHERE id = 1`).
		Scan(
			&cfg.BaseDomain, &cfg.ProjectID, &managed, &cfg.DNS.Domain,
			&cfg.DNS.Name, &cfg.DNS.Type, &cfg.DNS.Value, &cfg.DNS.Error,
			&cfg.CreatedAt, &cfg.UpdatedAt,
		)
	if err != nil {
		return nil, err
	}
	cfg.DNS.Managed = managed != 0
	return &cfg, nil
}

func (s *store) saveConfig(cfg *tunnelConfig) error {
	managed := 0
	if cfg.DNS.Managed {
		managed = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO tunnel_config
			(id, base_domain, project_id, dns_managed, dns_domain, dns_name,
			 dns_type, dns_value, dns_error, created_at, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			base_domain = excluded.base_domain,
			project_id = excluded.project_id,
			dns_managed = excluded.dns_managed,
			dns_domain = excluded.dns_domain,
			dns_name = excluded.dns_name,
			dns_type = excluded.dns_type,
			dns_value = excluded.dns_value,
			dns_error = excluded.dns_error,
			updated_at = excluded.updated_at`,
		cfg.BaseDomain, cfg.ProjectID, managed, cfg.DNS.Domain, cfg.DNS.Name,
		cfg.DNS.Type, cfg.DNS.Value, cfg.DNS.Error, cfg.CreatedAt, cfg.UpdatedAt,
	)
	return err
}

func (s *store) activeTunnelCount(projectID string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM tunnels WHERE project_id = ? AND status = 'active'`,
		projectID,
	).Scan(&count)
	return count, err
}

func (s *store) anyActiveTunnels() (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tunnels WHERE status = 'active'`).Scan(&count)
	return count > 0, err
}

func (s *store) createTunnel(projectID, name, hostname, tokenHash string) (*tunnel, error) {
	id, err := randomSecret("tun_", 16)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`
		INSERT INTO tunnels
			(id, project_id, name, hostname, token_hash, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'active', ?, ?)`,
		id, projectID, name, hostname, tokenHash, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create tunnel: %w", err)
	}
	return s.tunnelByID(id)
}

func (s *store) tunnelByID(id string) (*tunnel, error) {
	return scanTunnel(s.db.QueryRow(`
		SELECT id, project_id, name, hostname, token_hash, status, request_count,
		       bytes_in, bytes_out, last_connected_at, last_disconnected_at,
		       last_request_at, created_at, updated_at
		FROM tunnels WHERE id = ?`, id))
}

func (s *store) activeTunnelByHost(hostname string) (*tunnel, error) {
	return scanTunnel(s.db.QueryRow(`
		SELECT id, project_id, name, hostname, token_hash, status, request_count,
		       bytes_in, bytes_out, last_connected_at, last_disconnected_at,
		       last_request_at, created_at, updated_at
		FROM tunnels WHERE hostname = ? AND status = 'active'`, hostname))
}

func (s *store) activeTunnelByTokenHash(tokenHash string) (*tunnel, error) {
	return scanTunnel(s.db.QueryRow(`
		SELECT id, project_id, name, hostname, token_hash, status, request_count,
		       bytes_in, bytes_out, last_connected_at, last_disconnected_at,
		       last_request_at, created_at, updated_at
		FROM tunnels WHERE token_hash = ? AND status = 'active'`, tokenHash))
}

func (s *store) listTunnels(projectID string) ([]tunnel, error) {
	rows, err := s.db.Query(`
		SELECT id, project_id, name, hostname, token_hash, status, request_count,
		       bytes_in, bytes_out, last_connected_at, last_disconnected_at,
		       last_request_at, created_at, updated_at
		FROM tunnels
		WHERE project_id = ? AND status = 'active'
		ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tunnel{}
	for rows.Next() {
		item, err := scanTunnel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(...any) error
}

func scanTunnel(row rowScanner) (*tunnel, error) {
	var item tunnel
	var connectedAt, disconnectedAt, requestAt sql.NullString
	if err := row.Scan(
		&item.ID, &item.ProjectID, &item.Name, &item.Hostname, &item.TokenHash,
		&item.Status, &item.RequestCount, &item.BytesIn, &item.BytesOut,
		&connectedAt, &disconnectedAt, &requestAt, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	item.URL = "https://" + item.Hostname
	item.LastConnectedAt = connectedAt.String
	item.LastDisconnectedAt = disconnectedAt.String
	item.LastRequestAt = requestAt.String
	return &item, nil
}

func (s *store) rotateToken(id, projectID, tokenHash string) (*tunnel, error) {
	result, err := s.db.Exec(`
		UPDATE tunnels SET token_hash = ?, updated_at = ?
		WHERE id = ? AND project_id = ? AND status = 'active'`,
		tokenHash, time.Now().UTC().Format(time.RFC3339Nano), id, projectID,
	)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, sql.ErrNoRows
	}
	return s.tunnelByID(id)
}

func (s *store) revokeTunnel(id, projectID string) (*tunnel, error) {
	item, err := s.tunnelByID(id)
	if err != nil {
		return nil, err
	}
	if item.ProjectID != projectID || item.Status != "active" {
		return nil, sql.ErrNoRows
	}
	result, err := s.db.Exec(`
		UPDATE tunnels SET status = 'revoked', token_hash = ?, updated_at = ?
		WHERE id = ? AND project_id = ? AND status = 'active'`,
		"revoked:"+id, time.Now().UTC().Format(time.RFC3339Nano), id, projectID,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	item.Status = "revoked"
	return item, nil
}

func (s *store) markConnected(id string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.db.Exec(
		`UPDATE tunnels SET last_connected_at = ?, updated_at = ? WHERE id = ?`,
		now, now, id,
	)
}

func (s *store) markDisconnected(id string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.db.Exec(
		`UPDATE tunnels SET last_disconnected_at = ?, updated_at = ? WHERE id = ?`,
		now, now, id,
	)
}

func (s *store) stats(projectID string) (map[string]any, error) {
	var active, requests int64
	var bytesIn, bytesOut int64
	err := s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(request_count), 0),
		       COALESCE(SUM(bytes_in), 0), COALESCE(SUM(bytes_out), 0)
		FROM tunnels WHERE project_id = ? AND status = 'active'`, projectID).
		Scan(&active, &requests, &bytesIn, &bytesOut)
	return map[string]any{
		"active_tunnels": active,
		"request_count":  requests,
		"bytes_in":       bytesIn,
		"bytes_out":      bytesOut,
	}, err
}

func randomSecret(prefix string, size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func isNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
