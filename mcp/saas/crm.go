package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const crmCustomerRetryAfter = "-6 hours"

type crmContactUpsertResponse struct {
	Contact struct {
		ID int64 `json:"id"`
	} `json:"contact"`
	WasCreated bool `json:"was_created"`
}

// syncCustomerCRM best-effort links one SaaS customer to the optional CRM app.
// CRM absence or transient failures never block customer creation or checkout;
// durable status on saas_customers lets the recovery worker retry later.
func (a *App) syncCustomerCRM(ctx *sdk.AppCtx, pid string, customer *Customer) bool {
	if ctx == nil || customer == nil || strings.TrimSpace(customer.Email) == "" {
		return false
	}
	if customer.CRMContactID != nil && *customer.CRMContactID > 0 {
		return true
	}

	var out crmContactUpsertResponse
	err := ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_upsert_by_channel", map[string]any{
		"_project_id": pid,
		"kind":        "email",
		"value":       customer.Email,
		"defaults": map[string]any{
			"display_name": firstNonEmpty(customer.Name, customer.Email),
			"tags":         []any{"saas-customer"},
		},
		"source": "saas",
	}, &out)
	if err == nil && out.Contact.ID <= 0 {
		err = errors.New("contacts_upsert_by_channel returned no contact id")
	}
	if err != nil {
		if updateErr := dbCustomerSetCRMSyncFailure(ctx.AppDB(), pid, customer.ID, err.Error()); updateErr != nil {
			ctx.Logger().Warn("record CRM customer sync failure", "customer_id", customer.ID, "err", updateErr)
		}
		ctx.Logger().Warn("CRM customer sync failed", "customer_id", customer.ID, "err", err)
		return false
	}
	if err := dbCustomerSetCRMSyncSuccess(ctx.AppDB(), pid, customer.ID, out.Contact.ID); err != nil {
		ctx.Logger().Warn("record CRM customer sync success", "customer_id", customer.ID, "crm_contact_id", out.Contact.ID, "err", err)
		return false
	}
	customer.CRMContactID = &out.Contact.ID
	customer.CRMSyncStatus = "synced"
	customer.CRMSyncError = ""
	return true
}

func (a *App) retryPendingCRMCustomers(ctx *sdk.AppCtx) error {
	pid := projectID(ctx, nil)
	customers, err := dbCustomersPendingCRMSync(ctx.AppDB(), pid, 100)
	if err != nil {
		return err
	}
	for _, customer := range customers {
		a.syncCustomerCRM(ctx, customer.ProjectID, customer)
	}
	return nil
}

func dbCustomerSetCRMSyncSuccess(db *sql.DB, pid string, customerID, contactID int64) error {
	if contactID <= 0 {
		return errors.New("CRM contact id required")
	}
	res, err := db.Exec(`UPDATE saas_customers
		SET crm_contact_id=?, crm_sync_status='synced', crm_sync_error='',
			crm_sync_attempted_at=CURRENT_TIMESTAMP, crm_synced_at=CURRENT_TIMESTAMP,
			updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, contactID, pid, customerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("customer not found")
	}
	return nil
}

func dbCustomerSetCRMSyncFailure(db *sql.DB, pid string, customerID int64, message string) error {
	if len(message) > 1000 {
		message = message[:1000]
	}
	res, err := db.Exec(`UPDATE saas_customers
		SET crm_sync_status='failed', crm_sync_error=?, crm_sync_attempted_at=CURRENT_TIMESTAMP,
			updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, message, pid, customerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("customer not found")
	}
	return nil
}

func dbCustomersPendingCRMSync(db *sql.DB, pid string, limit int) ([]*Customer, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id, project_id FROM saas_customers
		WHERE crm_contact_id IS NULL
			AND (crm_sync_attempted_at IS NULL OR crm_sync_attempted_at <= datetime('now', ?))`
	args := []any{crmCustomerRetryAfter}
	if pid != "" {
		query += ` AND project_id=?`
		args = append(args, pid)
	}
	query += ` ORDER BY project_id, id LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type customerKey struct {
		ID        int64
		ProjectID string
	}
	keys := make([]customerKey, 0)
	for rows.Next() {
		var key customerKey
		if err := rows.Scan(&key.ID, &key.ProjectID); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	customers := make([]*Customer, 0, len(keys))
	for _, key := range keys {
		customer, err := dbCustomerGet(db, key.ProjectID, key.ID)
		if err != nil {
			return nil, fmt.Errorf("load CRM sync customer %s/%d: %w", key.ProjectID, key.ID, err)
		}
		if customer != nil {
			customers = append(customers, customer)
		}
	}
	return customers, nil
}
