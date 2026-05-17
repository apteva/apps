package main

// db_identities.go — local CRUD for the identities table (migration
// 006). Mirrors db_senders.go's shape but for authentication anchors
// rather than sendable addresses.
//
// What lives here vs. senders:
//   - senders     → kind in (email_mailbox, phone, …). Always a valid
//                   From value. Has parent_identity_id pointing here.
//   - identities  → kind in (email_domain, whatsapp_business_account).
//                   Verify-once anchors. Hold per-anchor state (DKIM
//                   tokens, inbound bootstrap config) that has no
//                   analog on the sendable side. Never a From value.
//
// The cross-app lookup ("is alice@socialcast.dev's parent domain
// verified?") used to walk the senders table via string-suffix match;
// now it's a single indexed FK on senders.parent_identity_id.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type identityRow struct {
	ID                  int64
	ProjectID           string
	Kind                string
	Address             string
	Provider            string
	ProviderIdentityID  string
	Verified            bool
	VerificationStatus  string
	DkimStatus          string
	InboundBootstrapped bool
	InboundConfig       string // JSON
	Notes               string
	Metadata            string // JSON
	LastSyncedAt        *time.Time
	LastSyncError       string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	DeletedAt           *time.Time
}

type identityUpsert struct {
	ProjectID           string
	Kind                string
	Address             string
	Provider            string
	ProviderIdentityID  string
	Verified            bool
	VerificationStatus  string
	DkimStatus          string
	InboundBootstrapped bool
	InboundConfig       string
	Metadata            string
	MarkSyncedNow       bool
	SyncError           string
}

const identityColumns = `id, project_id, kind, address,
	provider, COALESCE(provider_identity_id,''),
	verified, COALESCE(verification_status,''),
	COALESCE(dkim_status,''),
	inbound_bootstrapped, COALESCE(inbound_config,''),
	COALESCE(notes,''), COALESCE(metadata,''),
	last_synced_at, COALESCE(last_sync_error,''),
	created_at, updated_at, deleted_at`

func scanIdentity(row interface {
	Scan(dest ...any) error
}) (*identityRow, error) {
	var i identityRow
	var lastSynced sql.NullTime
	var deletedAt sql.NullTime
	err := row.Scan(
		&i.ID, &i.ProjectID, &i.Kind, &i.Address,
		&i.Provider, &i.ProviderIdentityID,
		&i.Verified, &i.VerificationStatus,
		&i.DkimStatus,
		&i.InboundBootstrapped, &i.InboundConfig,
		&i.Notes, &i.Metadata,
		&lastSynced, &i.LastSyncError,
		&i.CreatedAt, &i.UpdatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
	if lastSynced.Valid {
		i.LastSyncedAt = &lastSynced.Time
	}
	if deletedAt.Valid {
		i.DeletedAt = &deletedAt.Time
	}
	return &i, nil
}

// dbUpsertIdentity inserts or updates by (project_id, kind, address).
// Provider-mirrored fields always overwrite on conflict; local-only
// (notes, metadata when blank from provider) preserved.
func dbUpsertIdentity(db *sql.DB, u *identityUpsert) (int64, error) {
	if u.ProjectID == "" || u.Kind == "" || u.Address == "" {
		return 0, errors.New("project_id + kind + address required")
	}
	addr := strings.ToLower(strings.TrimSpace(u.Address))
	_, err := db.Exec(
		`INSERT INTO identities (
			project_id, kind, address,
			provider, provider_identity_id,
			verified, verification_status, dkim_status,
			inbound_bootstrapped, inbound_config,
			metadata,
			last_synced_at, last_sync_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+
			conditionalTS(u.MarkSyncedNow)+`, ?)
		ON CONFLICT (project_id, kind, address) DO UPDATE SET
			provider = excluded.provider,
			provider_identity_id = excluded.provider_identity_id,
			verified = excluded.verified,
			verification_status = excluded.verification_status,
			dkim_status = excluded.dkim_status,
			inbound_bootstrapped = excluded.inbound_bootstrapped,
			inbound_config = COALESCE(NULLIF(excluded.inbound_config,''), inbound_config),
			metadata = COALESCE(NULLIF(excluded.metadata,''), metadata),
			last_synced_at = excluded.last_synced_at,
			last_sync_error = excluded.last_sync_error,
			updated_at = CURRENT_TIMESTAMP,
			deleted_at = NULL`,
		u.ProjectID, u.Kind, addr,
		u.Provider, u.ProviderIdentityID,
		boolInt(u.Verified), u.VerificationStatus, u.DkimStatus,
		boolInt(u.InboundBootstrapped), u.InboundConfig,
		u.Metadata,
		u.SyncError,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert identity: %w", err)
	}
	var id int64
	err = db.QueryRow(
		`SELECT id FROM identities WHERE project_id = ? AND kind = ? AND address = ?`,
		u.ProjectID, u.Kind, addr,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert identity: fetch id: %w", err)
	}
	return id, nil
}

func dbFindIdentity(db *sql.DB, projectID, kind, address string) (*identityRow, error) {
	addr := strings.ToLower(strings.TrimSpace(address))
	row := db.QueryRow(
		`SELECT `+identityColumns+` FROM identities
		 WHERE project_id = ? AND kind = ? AND address = ?`,
		projectID, kind, addr,
	)
	i, err := scanIdentity(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return i, nil
}

// dbGetIdentity is the by-id lookup — used to walk a sender's
// parent_identity_id FK.
func dbGetIdentity(db *sql.DB, id int64) (*identityRow, error) {
	row := db.QueryRow(`SELECT `+identityColumns+` FROM identities WHERE id = ?`, id)
	i, err := scanIdentity(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return i, nil
}

// dbListIdentities returns active rows for a project, optionally
// filtered by kind. Sorted by kind then address for stable display.
func dbListIdentities(db *sql.DB, projectID, kind string) ([]*identityRow, error) {
	q := `SELECT ` + identityColumns + ` FROM identities WHERE project_id = ? AND deleted_at IS NULL`
	args := []any{projectID}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY kind, address`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*identityRow{}
	for rows.Next() {
		i, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func dbSoftDeleteIdentity(db *sql.DB, projectID, kind, address string) error {
	addr := strings.ToLower(strings.TrimSpace(address))
	_, err := db.Exec(
		`UPDATE identities
		 SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND kind = ? AND address = ?
		   AND deleted_at IS NULL`,
		projectID, kind, addr,
	)
	return err
}

// dbCountSendersForIdentity returns how many active senders inherit
// from a given identity. Used by the soft-delete safety check: don't
// drop an identity if mailboxes still point at it.
func dbCountSendersForIdentity(db *sql.DB, identityID int64) (int, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM senders
		 WHERE parent_identity_id = ? AND deleted_at IS NULL`,
		identityID,
	).Scan(&n)
	return n, err
}
