package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type store struct {
	db *sql.DB
}

type device struct {
	ID              string `json:"id"`
	TokenCiphertext string `json:"-"`
	Platform        string `json:"platform"`
	BundleID        string `json:"bundle_id"`
	Environment     string `json:"environment"`
	UserRef         string `json:"user_ref,omitempty"`
	AppVersion      string `json:"app_version,omitempty"`
	Status          string `json:"status"`
	LastSeenAt      string `json:"last_seen_at"`
	CreatedAt       string `json:"created_at"`
}

type grant struct {
	ID          string
	DeviceID    string
	InstanceRef string
	ExpiresAt   string
}

type delivery struct {
	ID             string `json:"id"`
	GrantID        string `json:"-"`
	DeviceID       string `json:"device_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Type           string `json:"type"`
	ItemID         string `json:"item_id,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
	Badge          *int   `json:"badge,omitempty"`
	Status         string `json:"status"`
	ProviderID     string `json:"provider_id,omitempty"`
	Error          string `json:"error,omitempty"`
	CreatedAt      string `json:"created_at"`
	SentAt         string `json:"sent_at,omitempty"`
}

type pushStats struct {
	ProviderReady bool       `json:"provider_ready"`
	ActiveDevices int        `json:"active_devices"`
	SentToday     int        `json:"sent_today"`
	FailedToday   int        `json:"failed_today"`
	Recent        []delivery `json:"recent"`
}

func (s *store) upsertDevice(tokenCiphertext, tokenHash, bundleID, environment, userRef, appVersion string) (*device, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id, err := randomValue("dev_", 16)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`
		INSERT INTO devices
			(id, token_ciphertext, token_hash, platform, bundle_id, environment, user_ref, app_version, status, last_seen_at, created_at)
		VALUES (?, ?, ?, 'ios', ?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET
			token_ciphertext = excluded.token_ciphertext,
			bundle_id = excluded.bundle_id,
			environment = excluded.environment,
			user_ref = excluded.user_ref,
			app_version = excluded.app_version,
			status = 'active',
			last_seen_at = excluded.last_seen_at`,
		id, tokenCiphertext, tokenHash, bundleID, environment, userRef, appVersion, now, now)
	if err != nil {
		return nil, fmt.Errorf("upsert device: %w", err)
	}
	return s.deviceByTokenHash(tokenHash)
}

func (s *store) deviceByTokenHash(tokenHash string) (*device, error) {
	return scanDevice(s.db.QueryRow(`
		SELECT id, token_ciphertext, platform, bundle_id, environment, user_ref, app_version, status, last_seen_at, created_at
		FROM devices WHERE token_hash = ?`, tokenHash))
}

func (s *store) deviceByID(id string) (*device, error) {
	return scanDevice(s.db.QueryRow(`
		SELECT id, token_ciphertext, platform, bundle_id, environment, user_ref, app_version, status, last_seen_at, created_at
		FROM devices WHERE id = ?`, id))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDevice(row rowScanner) (*device, error) {
	var d device
	if err := row.Scan(&d.ID, &d.TokenCiphertext, &d.Platform, &d.BundleID, &d.Environment, &d.UserRef, &d.AppVersion, &d.Status, &d.LastSeenAt, &d.CreatedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *store) backfillDeviceRouting(bundleID, environment string) error {
	if bundleID == "" || !validEnvironment(environment) {
		return nil
	}
	_, err := s.db.Exec(`
		UPDATE devices SET bundle_id = ?, environment = ?
		WHERE bundle_id = '' OR environment = ''`, bundleID, environment)
	return err
}

func (s *store) createGrant(deviceID, instanceRef, secretHash string, expires time.Time) (*grant, error) {
	id, err := randomValue("grt_", 16)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	g := &grant{
		ID:          id,
		DeviceID:    deviceID,
		InstanceRef: instanceRef,
		ExpiresAt:   expires.UTC().Format(time.RFC3339Nano),
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin grant rotation: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE grants SET revoked_at = ?
		WHERE device_id = ? AND instance_ref = ? AND revoked_at IS NULL`,
		now, g.DeviceID, g.InstanceRef,
	); err != nil {
		return nil, fmt.Errorf("revoke previous grant: %w", err)
	}
	_, err = tx.Exec(`
		INSERT INTO grants (id, device_id, instance_ref, secret_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		g.ID, g.DeviceID, g.InstanceRef, secretHash, g.ExpiresAt, now)
	if err != nil {
		return nil, fmt.Errorf("create grant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit grant rotation: %w", err)
	}
	return g, nil
}

func (s *store) authorizeGrant(secret string) (*grant, error) {
	var g grant
	var revoked sql.NullString
	err := s.db.QueryRow(`
		SELECT id, device_id, instance_ref, expires_at, revoked_at
		FROM grants WHERE secret_hash = ?`, digest(secret)).
		Scan(&g.ID, &g.DeviceID, &g.InstanceRef, &g.ExpiresAt, &revoked)
	if err != nil {
		return nil, err
	}
	expires, err := time.Parse(time.RFC3339Nano, g.ExpiresAt)
	if err != nil || revoked.Valid || !expires.After(time.Now().UTC()) {
		return nil, errors.New("grant is expired or revoked")
	}
	return &g, nil
}

func (s *store) revokeGrant(id string) error {
	result, err := s.db.Exec(
		`UPDATE grants SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *store) revokeDevice(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE devices SET status = 'revoked' WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(`UPDATE grants SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) markDeviceInvalid(id string) {
	_, _ = s.db.Exec(`UPDATE devices SET status = 'invalid' WHERE id = ?`, id)
}

func (s *store) createDelivery(
	g *grant,
	typ, itemID, projectID string,
	badge *int,
	idempotencyKey string,
) (*delivery, bool, error) {
	id, err := randomValue("del_", 16)
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`
		INSERT INTO deliveries
			(id, grant_id, device_id, idempotency_key, type, item_id, project_id, badge, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
		id, g.ID, g.DeviceID, idempotencyKey, typ, itemID, projectID, badge, now)
	if err != nil {
		existing, getErr := s.deliveryByIdempotency(g.ID, idempotencyKey)
		if getErr == nil {
			return existing, true, nil
		}
		return nil, false, fmt.Errorf("create delivery: %w", err)
	}
	d, err := s.deliveryByID(id)
	return d, false, err
}

func (s *store) deliveryByIdempotency(grantID, key string) (*delivery, error) {
	return scanDelivery(s.db.QueryRow(`
		SELECT id, grant_id, device_id, idempotency_key, type, item_id, project_id, badge,
		       status, provider_id, error, created_at, sent_at
		FROM deliveries WHERE grant_id = ? AND idempotency_key = ?`, grantID, key))
}

func (s *store) deliveryByID(id string) (*delivery, error) {
	return scanDelivery(s.db.QueryRow(`
		SELECT id, grant_id, device_id, idempotency_key, type, item_id, project_id, badge,
		       status, provider_id, error, created_at, sent_at
		FROM deliveries WHERE id = ?`, id))
}

func scanDelivery(row rowScanner) (*delivery, error) {
	var d delivery
	var badge sql.NullInt64
	var sent sql.NullString
	if err := row.Scan(
		&d.ID, &d.GrantID, &d.DeviceID, &d.IdempotencyKey, &d.Type, &d.ItemID, &d.ProjectID, &badge,
		&d.Status, &d.ProviderID, &d.Error, &d.CreatedAt, &sent,
	); err != nil {
		return nil, err
	}
	if badge.Valid {
		value := int(badge.Int64)
		d.Badge = &value
	}
	if sent.Valid {
		d.SentAt = sent.String
	}
	return &d, nil
}

func (s *store) finishDelivery(id, status, providerID, message string) (*delivery, error) {
	var sent any
	if status == "sent" {
		sent = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		UPDATE deliveries SET status = ?, provider_id = ?, error = ?, sent_at = ?
		WHERE id = ?`, status, providerID, message, sent, id)
	if err != nil {
		return nil, err
	}
	return s.deliveryByID(id)
}

func (s *store) listDevices(limit int) ([]device, error) {
	rows, err := s.db.Query(`
		SELECT id, token_ciphertext, platform, bundle_id, environment, user_ref, app_version, status, last_seen_at, created_at
		FROM devices ORDER BY last_seen_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]device, 0)
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *store) listDeliveries(limit int) ([]delivery, error) {
	rows, err := s.db.Query(`
		SELECT id, grant_id, device_id, idempotency_key, type, item_id, project_id, badge,
		       status, provider_id, error, created_at, sent_at
		FROM deliveries ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]delivery, 0)
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *store) stats(providerReady bool) (*pushStats, error) {
	var stats pushStats
	stats.ProviderReady = providerReady
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE status = 'active'`).Scan(&stats.ActiveDevices); err != nil {
		return nil, err
	}
	start := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339Nano)
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE status = 'sent' AND created_at >= ?`, start).Scan(&stats.SentToday); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE status = 'failed' AND created_at >= ?`, start).Scan(&stats.FailedToday); err != nil {
		return nil, err
	}
	recent, err := s.listDeliveries(8)
	if err != nil {
		return nil, err
	}
	stats.Recent = recent
	return &stats, nil
}
