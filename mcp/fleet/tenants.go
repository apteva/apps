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

// DomainGrant delegates a base domain to a tenant. It is separate
// from Tenant.Domain, which is the public hostname of the tenant's own
// dashboard.
type DomainGrant struct {
	ID               int64     `json:"id"`
	TenantID         string    `json:"tenant_id"`
	Domain           string    `json:"domain"`
	Wildcard         bool      `json:"wildcard"`
	Status           string    `json:"status"`
	DomainRecordID   string    `json:"domain_record_id,omitempty"`
	WildcardRecordID string    `json:"wildcard_record_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ProviderGrant exposes a parent integration connection as a
// tenant-local virtual connection. Apps in the tenant see a normal
// connection id; the tenant server proxies allowed calls back to the
// controller.
type ProviderGrant struct {
	ID                 int64     `json:"id"`
	TenantID           string    `json:"tenant_id"`
	GrantID            string    `json:"grant_id"`
	AppSlug            string    `json:"app_slug"`
	ParentConnectionID int64     `json:"parent_connection_id"`
	TenantConnectionID int64     `json:"tenant_connection_id"`
	TenantInstallID    int64     `json:"tenant_install_id,omitempty"`
	TenantRole         string    `json:"tenant_role,omitempty"`
	Status             string    `json:"status"`
	AllowedTools       []string  `json:"allowed_tools,omitempty"`
	AllowedDomains     []string  `json:"allowed_domains,omitempty"`
	AllowedFrom        []string  `json:"allowed_from,omitempty"`
	Metadata           any       `json:"metadata,omitempty"`
	TokenHash          string    `json:"-"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Event struct {
	ID        int64     `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Kind      string    `json:"kind"`
	Actor     string    `json:"actor,omitempty"`
	Payload   any       `json:"payload,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// RetainedSource records a stopped source data directory left behind by a
// successful cross-host migration. It can only be removed through the
// explicit tenant_migrate_finalize confirmation path.
type RetainedSource struct {
	TenantID         string    `json:"tenant_id"`
	SourceInstanceID int64     `json:"source_instance_id"`
	SourceConfigDir  string    `json:"source_config_dir"`
	SourceSlug       string    `json:"source_slug"`
	CreatedAt        time.Time `json:"created_at"`
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

// setLocation moves a tenant between hosts: updates instance_id (0 =
// local parent, >0 = a row in the Instances app), base_url, and
// config_dir atomically. Used by tenant_migrate after the data dir has
// been transferred and the new apteva-server is confirmed healthy.
func (s *store) setLocation(id string, instanceID int64, baseURL, configDir string) error {
	_, err := s.db.Exec(
		`UPDATE fleet_tenants SET instance_id = ?, base_url = ?, config_dir = ?, updated_at = ? WHERE id = ?`,
		instanceID, baseURL, configDir, time.Now().UTC(), id,
	)
	return err
}

func (s *store) createRetainedSource(r *RetainedSource) error {
	if r == nil || strings.TrimSpace(r.TenantID) == "" {
		return errors.New("retained source tenant_id required")
	}
	r.CreatedAt = time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO fleet_retained_sources
			(tenant_id, source_instance_id, source_config_dir, source_slug, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		r.TenantID, r.SourceInstanceID, r.SourceConfigDir, r.SourceSlug, r.CreatedAt,
	)
	return err
}

func (s *store) getRetainedSource(tenantID string) (*RetainedSource, error) {
	r := &RetainedSource{}
	err := s.db.QueryRow(`
		SELECT tenant_id, source_instance_id, source_config_dir, source_slug, created_at
		FROM fleet_retained_sources WHERE tenant_id = ?`, tenantID,
	).Scan(&r.TenantID, &r.SourceInstanceID, &r.SourceConfigDir, &r.SourceSlug, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *store) deleteRetainedSource(tenantID string) error {
	_, err := s.db.Exec(`DELETE FROM fleet_retained_sources WHERE tenant_id = ?`, tenantID)
	return err
}

func (s *store) upsertDomainGrant(g *DomainGrant) error {
	if g == nil {
		return errors.New("domain grant required")
	}
	now := time.Now().UTC()
	status := strings.TrimSpace(g.Status)
	if status == "" {
		status = "active"
	}
	wildcard := 0
	if g.Wildcard {
		wildcard = 1
	}
	res, err := s.db.Exec(
		`INSERT INTO fleet_domain_grants
			(tenant_id, domain, wildcard, status, domain_record_id, wildcard_record_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id, domain) DO UPDATE SET
		   wildcard = excluded.wildcard,
		   status = excluded.status,
		   domain_record_id = excluded.domain_record_id,
		   wildcard_record_id = excluded.wildcard_record_id,
		   updated_at = excluded.updated_at`,
		g.TenantID, g.Domain, wildcard, status, nullStr(g.DomainRecordID), nullStr(g.WildcardRecordID), now, now,
	)
	if err != nil {
		return err
	}
	if g.ID == 0 {
		if id, _ := res.LastInsertId(); id != 0 {
			g.ID = id
		}
		if g.ID == 0 {
			_ = s.db.QueryRow(
				`SELECT id FROM fleet_domain_grants WHERE tenant_id = ? AND domain = ?`,
				g.TenantID, g.Domain,
			).Scan(&g.ID)
		}
	}
	g.Status = status
	g.CreatedAt = now
	g.UpdatedAt = now
	return nil
}

const domainGrantCols = `id, tenant_id, domain, wildcard, status,
	domain_record_id, wildcard_record_id, created_at, updated_at`

func scanDomainGrant(s interface{ Scan(...any) error }) (*DomainGrant, error) {
	var (
		g             DomainGrant
		wildcard      int
		recordID      sql.NullString
		wildcardRecID sql.NullString
	)
	err := s.Scan(&g.ID, &g.TenantID, &g.Domain, &wildcard, &g.Status,
		&recordID, &wildcardRecID, &g.CreatedAt, &g.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g.Wildcard = wildcard != 0
	if recordID.Valid {
		g.DomainRecordID = recordID.String
	}
	if wildcardRecID.Valid {
		g.WildcardRecordID = wildcardRecID.String
	}
	return &g, nil
}

func (s *store) listDomainGrants(tenantID string) ([]*DomainGrant, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if tenantID != "" {
		rows, err = s.db.Query(
			`SELECT `+domainGrantCols+`
			   FROM fleet_domain_grants
			  WHERE tenant_id = ?
			  ORDER BY domain`,
			tenantID,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT ` + domainGrantCols + `
			   FROM fleet_domain_grants
			  ORDER BY tenant_id, domain`,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*DomainGrant{}
	for rows.Next() {
		g, err := scanDomainGrant(rows)
		if err != nil {
			return nil, err
		}
		if g != nil {
			out = append(out, g)
		}
	}
	return out, rows.Err()
}

func (s *store) getDomainGrant(tenantID, domain string) (*DomainGrant, error) {
	return scanDomainGrant(s.db.QueryRow(
		`SELECT `+domainGrantCols+`
		   FROM fleet_domain_grants
		  WHERE tenant_id = ? AND domain = ?`,
		tenantID, domain,
	))
}

func (s *store) deleteDomainGrant(tenantID, domain string) error {
	_, err := s.db.Exec(
		`DELETE FROM fleet_domain_grants WHERE tenant_id = ? AND domain = ?`,
		tenantID, domain,
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
