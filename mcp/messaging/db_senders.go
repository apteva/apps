package main

// db_senders.go — local CRUD for the senders table (migration 005).
//
// The provider (SES / Twilio) is still the source of truth for
// "can this identity actually send mail / sms" at execute time.
// This table mirrors provider state for fast reads + persists local-
// only state the provider has no concept of (per-project default
// sender, inbound bootstrap config, operator notes, compliance
// metadata).
//
// Reconciliation happens via senders_refresh — a full provider
// list + per-identity sync — or via TTL-on-read inside senders_list.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// senderRow mirrors the senders table 1:1. Pointers + sql.Null* for
// fields the DB allows NULL on so we can distinguish "unknown" from
// "empty string".
type senderRow struct {
	ID                  int64
	ProjectID           string
	Channel             string
	Address             string
	Kind                string
	DisplayName         string
	Provider            string
	ProviderIdentityID  string
	Verified            bool
	VerificationStatus  string
	SendingEnabled      bool
	DkimStatus          string
	InboundBootstrapped bool
	InboundConfig       string // JSON
	IsDefault           bool
	Notes               string
	Metadata            string // JSON
	// ParentIdentityID is the inheritance edge: a mailbox whose parent
	// domain is verified at SES gets this set on senders_create. Used
	// by refresh to keep inheritance mailboxes alive even though SES
	// doesn't list them, and by delete to skip the bogus SES call.
	ParentIdentityID *int64
	LastSyncedAt     *time.Time
	LastSyncError    string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

// senderUpsert is the input shape for upsertSender — leaves the
// id/timestamps/synced fields to the helper itself.
type senderUpsert struct {
	ProjectID           string
	Channel             string
	Address             string
	Kind                string
	DisplayName         string
	Provider            string
	ProviderIdentityID  string
	Verified            bool
	VerificationStatus  string
	SendingEnabled      bool
	DkimStatus          string
	InboundBootstrapped bool
	InboundConfig       string
	Metadata            string
	// ParentIdentityID points to the anchor identity this sender
	// inherits from (mailbox → email_domain in identities table).
	// Zero means standalone.
	ParentIdentityID int64
	// MarkSyncedNow toggles last_synced_at = CURRENT_TIMESTAMP on the
	// upsert — set true when the row comes from the provider; false
	// when it's a local-only edit (e.g. set_default).
	MarkSyncedNow bool
	SyncError     string
}

const senderColumns = `id, project_id, channel, address, kind,
	COALESCE(display_name,''),
	provider, COALESCE(provider_identity_id,''),
	verified, COALESCE(verification_status,''), sending_enabled,
	COALESCE(dkim_status,''),
	inbound_bootstrapped, COALESCE(inbound_config,''),
	is_default, COALESCE(notes,''), COALESCE(metadata,''),
	parent_identity_id,
	last_synced_at, COALESCE(last_sync_error,''),
	created_at, updated_at, deleted_at`

func scanSender(row interface {
	Scan(dest ...any) error
}) (*senderRow, error) {
	var s senderRow
	var lastSynced sql.NullTime
	var deletedAt sql.NullTime
	var parentIdentityID sql.NullInt64
	err := row.Scan(
		&s.ID, &s.ProjectID, &s.Channel, &s.Address, &s.Kind,
		&s.DisplayName,
		&s.Provider, &s.ProviderIdentityID,
		&s.Verified, &s.VerificationStatus, &s.SendingEnabled,
		&s.DkimStatus,
		&s.InboundBootstrapped, &s.InboundConfig,
		&s.IsDefault, &s.Notes, &s.Metadata,
		&parentIdentityID,
		&lastSynced, &s.LastSyncError,
		&s.CreatedAt, &s.UpdatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
	if parentIdentityID.Valid {
		s.ParentIdentityID = &parentIdentityID.Int64
	}
	if lastSynced.Valid {
		s.LastSyncedAt = &lastSynced.Time
	}
	if deletedAt.Valid {
		s.DeletedAt = &deletedAt.Time
	}
	return &s, nil
}

// dbUpsertSender inserts or updates a sender row by (project_id,
// channel, address). Returns the row id.
//
// Update strategy: provider-mirrored fields always overwrite (the
// caller is feeding the latest from SES/Twilio); local-only fields
// (is_default, notes, display_name when blank from provider) are
// preserved on update — pass them explicitly to overwrite.
func dbUpsertSender(db *sql.DB, u *senderUpsert) (int64, error) {
	if u.ProjectID == "" || u.Channel == "" || u.Address == "" {
		return 0, errors.New("project_id + channel + address required")
	}
	addr := strings.ToLower(strings.TrimSpace(u.Address))
	syncedClause := ""
	if u.MarkSyncedNow {
		syncedClause = ", last_synced_at = CURRENT_TIMESTAMP, last_sync_error = ?"
	}

	// NULLIF(?, 0) for parent_identity_id so a caller passing 0 (the
	// zero value of int64) means "no parent" without us tripping the
	// FK on a row that never had one.
	_, err := db.Exec(
		`INSERT INTO senders (
			project_id, channel, address, kind, display_name,
			provider, provider_identity_id,
			verified, verification_status, sending_enabled, dkim_status,
			inbound_bootstrapped, inbound_config,
			metadata,
			parent_identity_id,
			last_synced_at, last_sync_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0), `+
			conditionalTS(u.MarkSyncedNow)+`, ?)
		ON CONFLICT (project_id, channel, address) DO UPDATE SET
			kind = excluded.kind,
			display_name = COALESCE(NULLIF(excluded.display_name,''), display_name),
			provider = excluded.provider,
			provider_identity_id = excluded.provider_identity_id,
			verified = excluded.verified,
			verification_status = excluded.verification_status,
			sending_enabled = excluded.sending_enabled,
			dkim_status = excluded.dkim_status,
			inbound_bootstrapped = excluded.inbound_bootstrapped,
			inbound_config = COALESCE(NULLIF(excluded.inbound_config,''), inbound_config),
			metadata = COALESCE(NULLIF(excluded.metadata,''), metadata),
			parent_identity_id = COALESCE(excluded.parent_identity_id, parent_identity_id),
			last_synced_at = excluded.last_synced_at,
			last_sync_error = excluded.last_sync_error,
			updated_at = CURRENT_TIMESTAMP,
			deleted_at = NULL`,
		u.ProjectID, u.Channel, addr, u.Kind, u.DisplayName,
		u.Provider, u.ProviderIdentityID,
		boolInt(u.Verified), u.VerificationStatus, boolInt(u.SendingEnabled), u.DkimStatus,
		boolInt(u.InboundBootstrapped), u.InboundConfig,
		u.Metadata,
		u.ParentIdentityID,
		u.SyncError,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert sender: %w", err)
	}
	_ = syncedClause // already inlined via conditionalTS
	// Fetch the id (rowid changes between INSERT and UPDATE paths).
	var id int64
	err = db.QueryRow(`SELECT id FROM senders WHERE project_id = ? AND channel = ? AND address = ?`,
		u.ProjectID, u.Channel, addr,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert sender: fetch id: %w", err)
	}
	return id, nil
}

// conditionalTS returns a SQL fragment that's CURRENT_TIMESTAMP when
// markNow is true and NULL otherwise. Lets dbUpsertSender keep a
// single INSERT statement instead of branching at the Go level.
func conditionalTS(markNow bool) string {
	if markNow {
		return "CURRENT_TIMESTAMP"
	}
	return "NULL"
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func dbFindSender(db *sql.DB, projectID, channel, address string) (*senderRow, error) {
	addr := strings.ToLower(strings.TrimSpace(address))
	row := db.QueryRow(
		`SELECT `+senderColumns+` FROM senders
		 WHERE project_id = ? AND channel = ? AND address = ?`,
		projectID, channel, addr,
	)
	s, err := scanSender(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// dbListSenders returns active rows (deleted_at IS NULL) for a
// project, optionally filtered by channel and verified-only.
func dbListSenders(db *sql.DB, projectID, channel string, verifiedOnly bool) ([]*senderRow, error) {
	q := `SELECT ` + senderColumns + ` FROM senders WHERE project_id = ? AND deleted_at IS NULL`
	args := []any{projectID}
	if channel != "" {
		q += ` AND channel = ?`
		args = append(args, channel)
	}
	if verifiedOnly {
		q += ` AND verified = 1`
	}
	q += ` ORDER BY is_default DESC, channel, address`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*senderRow{}
	for rows.Next() {
		s, err := scanSender(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// dbSoftDeleteSender flips deleted_at = now. Idempotent — re-running
// against a soft-deleted row is a no-op (deleted_at not overwritten
// for the same reason a closed support ticket isn't re-closed).
// dbUpdateSenderLocal updates only fields the operator owns locally
// (no provider round-trip). Currently scoped to display_name + notes;
// add more fields here as inline-edit surfaces them. Empty strings
// preserve existing values (use NULL/explicit clear via a different
// path if you ever need real "blank it out" semantics).
func dbUpdateSenderLocal(db *sql.DB, projectID, channel, address, displayName, notes string) error {
	addr := strings.ToLower(strings.TrimSpace(address))
	_, err := db.Exec(
		`UPDATE senders
		 SET display_name = COALESCE(NULLIF(?, ''), display_name),
		     notes        = COALESCE(NULLIF(?, ''), notes),
		     updated_at   = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND channel = ? AND address = ?
		   AND deleted_at IS NULL`,
		displayName, notes, projectID, channel, addr,
	)
	return err
}

func dbSoftDeleteSender(db *sql.DB, projectID, channel, address string) error {
	addr := strings.ToLower(strings.TrimSpace(address))
	_, err := db.Exec(
		`UPDATE senders SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP, is_default = 0
		 WHERE project_id = ? AND channel = ? AND address = ? AND deleted_at IS NULL`,
		projectID, channel, addr,
	)
	return err
}

// dbSetDefaultSender atomically flips is_default = 1 on the named
// (project, channel, address) and clears it on every other row in
// the same cohort. Two statements in a tx; the partial unique index
// keeps two writers from both winning.
func dbSetDefaultSender(db *sql.DB, projectID, channel, address string) error {
	addr := strings.ToLower(strings.TrimSpace(address))
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE senders SET is_default = 0, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND channel = ? AND is_default = 1 AND deleted_at IS NULL`,
		projectID, channel,
	); err != nil {
		return err
	}
	res, err := tx.Exec(
		`UPDATE senders SET is_default = 1, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND channel = ? AND address = ? AND deleted_at IS NULL`,
		projectID, channel, addr,
	)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no active sender at (%s, %s, %s)", projectID, channel, address)
	}
	return tx.Commit()
}

// dbDefaultSender returns the (project, channel) default, or nil if
// none is set. Used by send_message when the caller leaves "from"
// blank.
func dbDefaultSender(db *sql.DB, projectID, channel string) (*senderRow, error) {
	row := db.QueryRow(
		`SELECT `+senderColumns+` FROM senders
		 WHERE project_id = ? AND channel = ? AND is_default = 1 AND deleted_at IS NULL`,
		projectID, channel,
	)
	s, err := scanSender(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// senderStaleThreshold is the read-side TTL: rows last synced more
// than this long ago trigger a background refresh from the provider.
const senderStaleThreshold = 5 * time.Minute

// dbHasStaleSenders returns true if any non-deleted row in the cohort
// is older than the staleness threshold (or never synced). Cheap —
// one SELECT with LIMIT 1.
func dbHasStaleSenders(db *sql.DB, projectID, channel string) (bool, error) {
	q := `SELECT 1 FROM senders
		WHERE project_id = ? AND deleted_at IS NULL
		AND (last_synced_at IS NULL OR last_synced_at < datetime('now', ?))`
	args := []any{projectID, fmt.Sprintf("-%d seconds", int(senderStaleThreshold.Seconds()))}
	if channel != "" {
		q += ` AND channel = ?`
		args = append(args, channel)
	}
	q += ` LIMIT 1`
	var probe int
	err := db.QueryRow(q, args...).Scan(&probe)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// senderRowToMap renders a senderRow as the panel-friendly JSON
// shape used by tools — pruning sql.Null wrappers and inlining
// inbound_config / metadata as nested objects when present.
func senderRowToMap(s *senderRow) map[string]any {
	out := map[string]any{
		"id":                   s.ID,
		"channel":              s.Channel,
		"address":              s.Address,
		"kind":                 s.Kind,
		"display_name":         s.DisplayName,
		"provider":             s.Provider,
		"provider_identity_id": s.ProviderIdentityID,
		"verified":             s.Verified,
		"verification_status":  s.VerificationStatus,
		"sending_enabled":      s.SendingEnabled,
		"dkim_status":          s.DkimStatus,
		"inbound_bootstrapped": s.InboundBootstrapped,
		"is_default":           s.IsDefault,
		"notes":                s.Notes,
	}
	if s.LastSyncedAt != nil {
		out["last_synced_at"] = s.LastSyncedAt.Format(time.RFC3339)
	}
	if s.LastSyncError != "" {
		out["last_sync_error"] = s.LastSyncError
	}
	if s.InboundConfig != "" {
		var inb map[string]any
		if json.Unmarshal([]byte(s.InboundConfig), &inb) == nil {
			out["inbound_config"] = inb
		}
	}
	if s.Metadata != "" {
		var meta map[string]any
		if json.Unmarshal([]byte(s.Metadata), &meta) == nil {
			out["metadata"] = meta
		}
	}
	return out
}
