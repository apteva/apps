package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Tenant statuses. Kept in lockstep with the CHECK constraint in
// migrations/001_init.sql + 002_setup_token.sql (which broadens the
// allowed set to include setup_pending).
const (
	StatusStarting     = "starting"
	StatusSetupPending = "setup_pending" // server up, no admin registered yet; api_key not captured
	StatusActive       = "active"
	StatusSuspended    = "suspended"
	StatusStopped      = "stopped"
	StatusDisconnected = "disconnected"
	StatusFailed       = "failed"
	StatusDeleted      = "deleted"
)

// Tenant kinds. local: fleet supervises the apteva child process;
// remote: registered via tenant_connect, fleet only observes.
const (
	KindLocal  = "local"
	KindRemote = "remote"
)

type Tenant struct {
	ID             string     `json:"id"`
	Slug           string     `json:"slug"`
	Kind           string     `json:"kind"`
	BaseURL        string     `json:"base_url"`
	ConfigDir      string     `json:"config_dir,omitempty"`
	OwnerEmail     string     `json:"owner_email"`
	OwnerUserID    string     `json:"owner_user_id,omitempty"`
	CurrentVersion string     `json:"current_version,omitempty"`
	TargetVersion  string     `json:"target_version,omitempty"`
	Status         string     `json:"status"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
	LastHealth     any        `json:"last_health,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	// Domain link — populated by tenant_attach_domain via the optional
	// Domains/Certs/Routes apps. domain_record_id encodes "<apex>|<type>"
	// so detach can target the same registrar record.
	Domain           string     `json:"domain,omitempty"`
	DomainRecordID   string     `json:"domain_record_id,omitempty"`
	DomainAttachedAt *time.Time `json:"domain_attached_at,omitempty"`

	// Respawn bookkeeping — set by the auto-respawn worker when a
	// local tenant's port goes empty. Reset on a successful health
	// probe. Capped in code (see localproc.go).
	RespawnAttempts int        `json:"respawn_attempts,omitempty"`
	LastRespawnAt   *time.Time `json:"last_respawn_at,omitempty"`

	// InstanceID picks the host:
	//   0  = parent (local process — existing behavior)
	//   >0 = row id in the Instances app's table; tenant runs as an
	//        apteva-server on that VPS, driven via instance_run_command.
	InstanceID int64 `json:"instance_id"`
}

// IsHosted returns true when this tenant runs on a remote instance
// (managed via the Instances app), not on the parent.
func (t *Tenant) IsHosted() bool { return t.InstanceID > 0 }

type Event struct {
	ID        int64     `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Kind      string    `json:"kind"`
	Actor     string    `json:"actor,omitempty"`
	Payload   any       `json:"payload,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type store struct{ db *sql.DB }

func newID() string {
	b := make([]byte, 13)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return "tnt_" + hex.EncodeToString(b)
}

// insert persists a new tenant row. setupTokenEnc is the sealed
// setup token for setup_pending tenants; pass nil for tenants that
// already have an api_key (remote connect, etc.).
func (s *store) insert(t *Tenant, apiKeyEnc, setupTokenEnc []byte) error {
	if t.ID == "" {
		t.ID = newID()
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	if t.Status == "" {
		t.Status = StatusActive
	}
	if t.Kind == "" {
		t.Kind = KindRemote
	}
	var stTok any
	if len(setupTokenEnc) > 0 {
		stTok = setupTokenEnc
	}
	_, err := s.db.Exec(`
		INSERT INTO fleet_tenants (id, slug, kind, base_url, config_dir, api_key_enc, setup_token_enc, owner_email, owner_user_id, current_version, target_version, status, last_seen_at, last_health, created_at, updated_at, instance_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, t.ID, t.Slug, t.Kind, t.BaseURL, nullStr(t.ConfigDir), apiKeyEnc, stTok, t.OwnerEmail, nullStr(t.OwnerUserID),
		nullStr(t.CurrentVersion), nullStr(t.TargetVersion), t.Status,
		nil, nil, t.CreatedAt, t.UpdatedAt, t.InstanceID)
	return err
}

// getSetupToken returns the sealed setup_token_enc for a tenant, or
// nil if none was stored (post-attach, or tenants that never had one).
func (s *store) getSetupToken(id string) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRow(`SELECT setup_token_enc FROM fleet_tenants WHERE id = ?`, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return blob, err
}

// attachAPIKey replaces the sentinel api_key_enc with the real key,
// clears the setup_token, and flips the row to active in one step.
func (s *store) attachAPIKey(id string, apiKeyEnc []byte) error {
	_, err := s.db.Exec(
		`UPDATE fleet_tenants SET api_key_enc = ?, setup_token_enc = NULL, status = ?, updated_at = ? WHERE id = ?`,
		apiKeyEnc, StatusActive, time.Now().UTC(), id,
	)
	return err
}

func (s *store) get(id string) (*Tenant, []byte, error) {
	row := s.db.QueryRow(`
		SELECT id, slug, kind, base_url, config_dir, api_key_enc, owner_email, owner_user_id, current_version, target_version, status, last_seen_at, last_health, created_at, updated_at, domain, domain_record_id, domain_attached_at, respawn_attempts, last_respawn_at, instance_id
		FROM fleet_tenants WHERE id = ?
	`, id)
	return scanTenant(row)
}

func (s *store) getBySlug(slug string) (*Tenant, []byte, error) {
	row := s.db.QueryRow(`
		SELECT id, slug, kind, base_url, config_dir, api_key_enc, owner_email, owner_user_id, current_version, target_version, status, last_seen_at, last_health, created_at, updated_at, domain, domain_record_id, domain_attached_at, respawn_attempts, last_respawn_at, instance_id
		FROM fleet_tenants WHERE slug = ?
	`, slug)
	return scanTenant(row)
}

func (s *store) list(filter map[string]string) ([]*Tenant, error) {
	q := strings.Builder{}
	// api_key_enc is intentionally elided from list results.
	q.WriteString(`SELECT id, slug, kind, base_url, config_dir, X'00' AS api_key_enc, owner_email, owner_user_id, current_version, target_version, status, last_seen_at, last_health, created_at, updated_at, domain, domain_record_id, domain_attached_at, respawn_attempts, last_respawn_at, instance_id FROM fleet_tenants WHERE 1=1`)
	args := []any{}
	cols := map[string]string{
		"status":      "status",
		"owner_email": "owner_email",
		"version":     "current_version",
		"kind":        "kind",
	}
	for _, k := range []string{"status", "owner_email", "version", "kind"} {
		v := filter[k]
		if v == "" {
			continue
		}
		q.WriteString(fmt.Sprintf(" AND %s = ?", cols[k]))
		args = append(args, v)
	}
	q.WriteString(" ORDER BY created_at DESC")
	rows, err := s.db.Query(q.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tenant
	for rows.Next() {
		t, _, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *store) setStatus(id, newStatus, actor string) error {
	prev, _, err := s.get(id)
	if err != nil {
		return err
	}
	if prev.Status == newStatus {
		return nil
	}
	_, err = s.db.Exec(`UPDATE fleet_tenants SET status = ?, updated_at = ? WHERE id = ?`, newStatus, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	return s.recordEvent(id, "status_changed", actor, map[string]any{"from": prev.Status, "to": newStatus})
}

func (s *store) updateHealth(id string, ok bool, version string, payload []byte) error {
	now := time.Now().UTC()
	if ok {
		_, err := s.db.Exec(`UPDATE fleet_tenants SET last_seen_at = ?, last_health = ?, current_version = COALESCE(NULLIF(?, ''), current_version), updated_at = ? WHERE id = ?`,
			now, string(payload), version, now, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE fleet_tenants SET last_health = ?, updated_at = ? WHERE id = ?`, string(payload), now, id)
	return err
}

func (s *store) recordEvent(tenantID, kind, actor string, payload any) error {
	var pj sql.NullString
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		pj = sql.NullString{String: string(b), Valid: true}
	}
	_, err := s.db.Exec(`INSERT INTO fleet_events (tenant_id, kind, actor, payload) VALUES (?, ?, ?, ?)`, tenantID, kind, nullStr(actor), pj)
	return err
}

func (s *store) recentEvents(tenantID string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, tenant_id, kind, actor, payload, created_at FROM fleet_events WHERE tenant_id = ? ORDER BY id DESC LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var actor sql.NullString
		var payload sql.NullString
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Kind, &actor, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		if actor.Valid {
			e.Actor = actor.String
		}
		if payload.Valid {
			_ = json.Unmarshal([]byte(payload.String), &e.Payload)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *store) hardDelete(id string) error {
	_, err := s.db.Exec(`DELETE FROM fleet_tenants WHERE id = ?`, id)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTenant(r rowScanner) (*Tenant, []byte, error) {
	var (
		t             Tenant
		configDir     sql.NullString
		ownerUID      sql.NullString
		curVer        sql.NullString
		tgtVer        sql.NullString
		lastSeen      sql.NullTime
		lastHealthRaw sql.NullString
		apiKeyEnc     []byte
		domain        sql.NullString
		domainRec     sql.NullString
		domainAt      sql.NullTime
		lastRespawn   sql.NullTime
	)
	err := r.Scan(
		&t.ID, &t.Slug, &t.Kind, &t.BaseURL, &configDir, &apiKeyEnc,
		&t.OwnerEmail, &ownerUID, &curVer, &tgtVer, &t.Status,
		&lastSeen, &lastHealthRaw, &t.CreatedAt, &t.UpdatedAt,
		&domain, &domainRec, &domainAt, &t.RespawnAttempts, &lastRespawn,
		&t.InstanceID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	if configDir.Valid {
		t.ConfigDir = configDir.String
	}
	if ownerUID.Valid {
		t.OwnerUserID = ownerUID.String
	}
	if curVer.Valid {
		t.CurrentVersion = curVer.String
	}
	if tgtVer.Valid {
		t.TargetVersion = tgtVer.String
	}
	if lastSeen.Valid {
		ls := lastSeen.Time
		t.LastSeenAt = &ls
	}
	if lastHealthRaw.Valid && lastHealthRaw.String != "" {
		_ = json.Unmarshal([]byte(lastHealthRaw.String), &t.LastHealth)
	}
	if domain.Valid {
		t.Domain = domain.String
	}
	if domainRec.Valid {
		t.DomainRecordID = domainRec.String
	}
	if domainAt.Valid {
		da := domainAt.Time
		t.DomainAttachedAt = &da
	}
	if lastRespawn.Valid {
		lr := lastRespawn.Time
		t.LastRespawnAt = &lr
	}
	return &t, apiKeyEnc, nil
}

// setDomain stamps the domain link triple atomically. Called by
// attachDomain after the DNS write succeeds.
func (s *store) setDomain(id, fqdn, recordID string, at time.Time) error {
	_, err := s.db.Exec(
		`UPDATE fleet_tenants SET domain = ?, domain_record_id = ?, domain_attached_at = ?, updated_at = ? WHERE id = ?`,
		nullStr(fqdn), nullStr(recordID), at, time.Now().UTC(), id,
	)
	return err
}

// clearDomain undoes setDomain — called from detachDomain after the
// registrar delete. We clear the row even if the remote delete failed
// since the operator's recourse is the registrar UI, not retrying
// here against a phantom local record.
func (s *store) clearDomain(id string) error {
	_, err := s.db.Exec(
		`UPDATE fleet_tenants SET domain = NULL, domain_record_id = NULL, domain_attached_at = NULL, updated_at = ? WHERE id = ?`,
		time.Now().UTC(), id,
	)
	return err
}

// setTargetVersion records the operator's desired apteva version. The
// auto-update worker (not in v0.3.0) reads it; for v0.3.0 it just lets
// the panel show "update available" when current != target.
func (s *store) setTargetVersion(id, version string) error {
	_, err := s.db.Exec(
		`UPDATE fleet_tenants SET target_version = ?, updated_at = ? WHERE id = ?`,
		nullStr(version), time.Now().UTC(), id,
	)
	return err
}

// bumpRespawn records an auto-respawn attempt. The counter caps in code
// (see tryRespawn); this is just the persistence half.
func (s *store) bumpRespawn(id string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE fleet_tenants SET respawn_attempts = respawn_attempts + 1, last_respawn_at = ?, updated_at = ? WHERE id = ?`,
		now, now, id,
	)
	return err
}

// resetRespawn clears the respawn counter — called when a tenant is
// observed healthy after a respawn so the next blip starts fresh.
func (s *store) resetRespawn(id string) error {
	_, err := s.db.Exec(
		`UPDATE fleet_tenants SET respawn_attempts = 0, updated_at = ? WHERE id = ? AND respawn_attempts > 0`,
		time.Now().UTC(), id,
	)
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

var ErrNotFound = errors.New("tenant not found")
