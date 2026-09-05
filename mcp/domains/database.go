package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── DB helpers ────────────────────────────────────────────────────

const domainSelectCols = `id, project_id, name,
	COALESCE(registrar_slug,''), COALESCE(dns_provider_slug,''),
	COALESCE(connection_id, 0), connection_mode,
	COALESCE(expires_at,''), COALESCE(notes,''),
	COALESCE(created_at,''), COALESCE(updated_at,'')`

func scanDomain(s interface{ Scan(...any) error }) (*Domain, error) {
	d := &Domain{}
	err := s.Scan(&d.ID, &d.ProjectID, &d.Name,
		&d.RegistrarSlug, &d.DNSProviderSlug,
		&d.ConnectionID, &d.ConnectionMode,
		&d.ExpiresAt, &d.Notes, &d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

func upsertDomainInventory(ctx *sdk.AppCtx, pid, name, reg, dns, notes string, connID int64) (*Domain, error) {
	// SQLite NULLIF(?, 0) makes "no connection" NULL, so COALESCE
	// preserves an existing pin on re-add instead of clobbering it.
	_, err := ctx.AppDB().Exec(
		`INSERT INTO domains (project_id, name, registrar_slug, dns_provider_slug, notes, connection_id, connection_mode)
		 VALUES (?, ?, ?, ?, ?, NULLIF(?, 0), CASE WHEN ? > 0 THEN 'pinned' ELSE 'unmanaged' END)
		 ON CONFLICT(project_id, name) WHERE deleted_at IS NULL
		 DO UPDATE SET
		   registrar_slug    = COALESCE(NULLIF(excluded.registrar_slug,''), domains.registrar_slug),
		   dns_provider_slug = COALESCE(NULLIF(excluded.dns_provider_slug,''), domains.dns_provider_slug),
		   connection_id     = COALESCE(excluded.connection_id, domains.connection_id),
 connection_mode = CASE WHEN excluded.connection_id IS NOT NULL THEN 'pinned' ELSE domains.connection_mode END,
		   notes             = COALESCE(NULLIF(excluded.notes,''), domains.notes),
		   updated_at        = CURRENT_TIMESTAMP`,
		pid, name, reg, dns, notes, connID, connID,
	)
	if err != nil {
		return nil, fmt.Errorf("insert domain: %w", err)
	}
	var id int64
	if err := ctx.AppDB().QueryRow(
		`SELECT id FROM domains WHERE project_id = ? AND name = ? AND deleted_at IS NULL`,
		pid, name).Scan(&id); err != nil {
		return nil, fmt.Errorf("read domain after upsert: %w", err)
	}
	return dbDomainGet(ctx.AppDB(), pid, id)
}

func dbDomainList(db *sql.DB, pid string) ([]*Domain, error) {
	rows, err := db.Query(
		`SELECT `+domainSelectCols+`
		 FROM domains WHERE project_id = ? AND deleted_at IS NULL
		 ORDER BY name`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Domain{}
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		if d != nil {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

func dbDomainGet(db *sql.DB, pid string, id int64) (*Domain, error) {
	return scanDomain(db.QueryRow(
		`SELECT `+domainSelectCols+`
		 FROM domains WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid,
	))
}

func dbDomainGetByName(db *sql.DB, pid, name string) (*Domain, error) {
	return scanDomain(db.QueryRow(
		`SELECT `+domainSelectCols+`
		 FROM domains WHERE project_id = ? AND name = ? AND deleted_at IS NULL`,
		pid, name,
	))
}

func dbRegistrationIntentInsert(db *sql.DB, in *RegistrationIntent) error {

	_, err := db.Exec(`INSERT INTO registration_intents
		(token, project_id, domain, years, auto_renew, whois_privacy, coupon, notes,
		 provider_slug, connection_id, price, currency, status, expires_at, cost_cents)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'prepared', ?, ?)`,
		in.Token, in.ProjectID, in.Domain, in.Years, in.AutoRenew, in.WhoisPrivacy,
		in.Coupon, in.Notes, in.Provider, in.ConnectionID, in.Price, in.Currency,
		in.ExpiresAt.Format(time.RFC3339), in.CostCents)
	if err != nil {
		return fmt.Errorf("store registration intent: %w", err)
	}
	return nil
}

func dbRegistrationIntentGet(db *sql.DB, pid, token string) (*RegistrationIntent, error) {
	in := &RegistrationIntent{}
	var autoRenew, privacy int
	var raw, expires, result string
	err := db.QueryRow(`SELECT token, project_id, domain, years, auto_renew, whois_privacy,
		coupon, notes, provider_slug, connection_id, price, currency, status,
		response_json, error_message, expires_at, cost_cents, result_json, attempted_at, CAST(updated_at AS TEXT)
		FROM registration_intents WHERE project_id=? AND token=?`, pid, token).Scan(
		&in.Token, &in.ProjectID, &in.Domain, &in.Years, &autoRenew, &privacy,
		&in.Coupon, &in.Notes, &in.Provider, &in.ConnectionID, &in.Price, &in.Currency,
		&in.Status, &raw, &in.Error, &expires, &in.CostCents, &result, &in.AttemptedAt, &in.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	in.AutoRenew = autoRenew != 0
	in.WhoisPrivacy = privacy != 0
	in.Raw = json.RawMessage(raw)
	in.Result = json.RawMessage(result)
	in.ExpiresAt, err = time.Parse(time.RFC3339, expires)
	if err != nil {
		return nil, fmt.Errorf("parse registration intent expiry: %w", err)
	}
	return in, nil
}

func dbRegistrationIntentClaim(db *sql.DB, pid, token string) (bool, error) {
	res, err := db.Exec(`UPDATE registration_intents
		SET status='processing', updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND token=? AND status='prepared'`, pid, token)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func dbRegistrationIntentStatus(db *sql.DB, token, status string, raw json.RawMessage, message string) error {
	_, err := db.Exec(`UPDATE registration_intents
		SET status=?, response_json=?, error_message=?, updated_at=CURRENT_TIMESTAMP WHERE token=?`,
		status, string(raw), message, token)
	return err
}
