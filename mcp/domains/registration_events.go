package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"time"
)

func claimRegistration(ctx *sdk.AppCtx, in *RegistrationIntent, attempted string) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE registration_intents SET status='processing',attempted_at=?,updated_at=? WHERE project_id=? AND token=? AND status=? AND updated_at=?`, attempted, time.Now().UTC().Format(time.RFC3339), in.ProjectID, in.Token, in.Status, in.UpdatedAt)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return apiError(409, "purchase already submitted or changed")
	}
	if err = queueDomainEvent(tx, in.ProjectID, in.Domain, "domain.registration.submitted", "submission:"+in.Token, map[string]any{"provider": in.Provider, "connection_id": in.ConnectionID, "status": "processing", "years": in.Years, "auto_renew": in.AutoRenew, "whois_privacy": in.WhoisPrivacy}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	deliverDomainEvents(ctx, in.ProjectID)
	return nil
}
func registrationTransition(ctx *sdk.AppCtx, in *RegistrationIntent, status string, raw json.RawMessage, message string) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `UPDATE registration_intents SET status=?,response_json=?,error_message=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND token=?`
	if status == "cancelled" || status == "expired" {
		query += ` AND status='prepared'`
	}
	if status == "failed" || status == "unknown" {
		query += ` AND status NOT IN ('succeeded','cancelled','expired')`
	}
	res, err := tx.Exec(query, status, string(raw), message, in.ProjectID, in.Token)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("purchase state changed; transition not permitted")
	}
	topic := "domain.registration." + status
	if err = queueDomainEvent(tx, in.ProjectID, in.Domain, topic, "registration:"+in.Token+":"+status, map[string]any{"provider": in.Provider, "connection_id": in.ConnectionID, "status": status}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	deliverDomainEvents(ctx, in.ProjectID)
	return nil
}

func queueRegistrationPrepared(tx *sql.Tx, in *RegistrationIntent) error {
	data := map[string]any{"provider": in.Provider, "connection_id": in.ConnectionID, "status": "prepared", "years": in.Years, "auto_renew": in.AutoRenew, "whois_privacy": in.WhoisPrivacy, "billing_mode": in.BillingMode, "expires_at": in.ExpiresAt.UTC().Format(time.RFC3339)}
	if in.Price != "" {
		data["quoted_price"] = map[string]any{"amount": in.Price, "currency": in.Currency}
	}
	return queueDomainEvent(tx, in.ProjectID, in.Domain, "domain.registration.prepared", "prepared:"+in.Token, data)
}
