package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type PaymentMethod struct {
	ID                      int64           `json:"id"`
	ProjectID               string          `json:"project_id,omitempty"`
	CustomerID              int64           `json:"customer_id"`
	CustomerName            string          `json:"customer_name,omitempty"`
	CustomerEmail           string          `json:"customer_email,omitempty"`
	Provider                string          `json:"provider"`
	ProviderCustomerID      string          `json:"provider_customer_id,omitempty"`
	ProviderPaymentMethodID string          `json:"provider_payment_method_id"`
	ProviderMandateID       string          `json:"provider_mandate_id,omitempty"`
	Type                    string          `json:"type"`
	Status                  string          `json:"status"`
	IsDefault               bool            `json:"is_default"`
	Reusable                bool            `json:"reusable"`
	DelayedNotification     bool            `json:"delayed_notification"`
	DisplayBrand            string          `json:"display_brand,omitempty"`
	DisplayLast4            string          `json:"display_last4,omitempty"`
	ExpMonth                int             `json:"exp_month,omitempty"`
	ExpYear                 int             `json:"exp_year,omitempty"`
	Country                 string          `json:"country,omitempty"`
	Currency                string          `json:"currency,omitempty"`
	Metadata                json.RawMessage `json:"metadata,omitempty"`
	CreatedAt               string          `json:"created_at,omitempty"`
	UpdatedAt               string          `json:"updated_at,omitempty"`
	DetachedAt              string          `json:"detached_at,omitempty"`
}

type SetupSession struct {
	ID                    int64           `json:"id"`
	ProjectID             string          `json:"project_id,omitempty"`
	CustomerID            int64           `json:"customer_id"`
	Provider              string          `json:"provider"`
	ProviderCustomerID    string          `json:"provider_customer_id,omitempty"`
	ProviderSessionID     string          `json:"provider_session_id"`
	ProviderSetupIntentID string          `json:"provider_setup_intent_id,omitempty"`
	Status                string          `json:"status"`
	SuccessURL            string          `json:"success_url,omitempty"`
	CancelURL             string          `json:"cancel_url,omitempty"`
	URL                   string          `json:"url,omitempty"`
	PaymentMethodTypes    json.RawMessage `json:"payment_method_types,omitempty"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
	CreatedAt             string          `json:"created_at,omitempty"`
	CompletedAt           string          `json:"completed_at,omitempty"`
	ExpiresAt             string          `json:"expires_at,omitempty"`
}

type paymentMethodFilters struct {
	customerID int64
	status     string
	pmType     string
	limit      int
}

func (a *App) toolPaymentMethodsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out, err := dbPaymentMethodsList(ctx.AppDB(), pid, paymentMethodFilters{
		customerID: int64Arg(args, "customer_id"),
		status:     strArg(args, "status"),
		pmType:     strArg(args, "type"),
		limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"payment_methods": out, "count": len(out)}, nil
}

func (a *App) toolPaymentMethodSetupCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	customerID := int64Arg(args, "customer_id")
	if customerID == 0 {
		return nil, errors.New("customer_id required")
	}
	cust, err := dbCustomerGetByID(ctx.AppDB(), pid, customerID)
	if err != nil {
		return nil, err
	}
	if cust == nil {
		return nil, fmt.Errorf("customer %d not found", customerID)
	}
	if strings.TrimSpace(cust.Email) == "" {
		return nil, errors.New("customer has no email; set one before creating a setup session")
	}
	paymentTypes := stringSliceArg(args, "payment_method_types")
	if len(paymentTypes) == 0 {
		paymentTypes = []string{"card"}
	}
	for i, typ := range paymentTypes {
		paymentTypes[i] = strings.ToLower(strings.TrimSpace(typ))
		if paymentTypes[i] == "" {
			return nil, errors.New("payment_method_types cannot contain empty values")
		}
	}
	setDefault := true
	if _, ok := args["set_default"]; ok {
		setDefault = boolFromArg(args, "set_default")
	}
	meta := mapFromAny(args["metadata"])
	setup, err := a.createStripeSetupSession(ctx, pid, cust, setupSessionRequest{
		PaymentMethodTypes: paymentTypes,
		SuccessURL:         strArg(args, "success_url"),
		CancelURL:          strArg(args, "cancel_url"),
		SetDefault:         setDefault,
		Metadata:           meta,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"setup_session": setup,
		"url":           setup.URL,
	}, nil
}

func (a *App) toolPaymentMethodDefaultSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	pm, err := dbPaymentMethodSetDefault(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	emitPaymentMethod(ctx, "payment_method.default_changed", pm)
	return map[string]any{"payment_method": pm}, nil
}

func (a *App) toolPaymentMethodDetach(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	pm, err := dbPaymentMethodGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if pm == nil {
		return nil, fmt.Errorf("payment method %d not found", id)
	}
	if pm.Provider == "stripe" && pm.ProviderPaymentMethodID != "" {
		bound, err := requireProcessor(ctx)
		if err != nil {
			return nil, err
		}
		if err := requireMethodConnection(ctx, bound, pm); err != nil {
			return nil, err
		}
		if err := executeStripe(ctx, bound, "detach_payment_method", map[string]any{
			"payment_method_id": pm.ProviderPaymentMethodID,
		}, nil); err != nil {
			return nil, err
		}
	}
	pm, err = dbPaymentMethodDetach(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	emitPaymentMethod(ctx, "payment_method.detached", pm)
	return map[string]any{"payment_method": pm}, nil
}

func (a *App) handleHTTPPaymentMethodsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPPaymentMethodsList(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPPaymentMethodItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/payment-methods/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	switch parts[1] {
	case "default":
		if r.Method == http.MethodPost {
			a.handleHTTPPaymentMethodSetDefault(w, r, parts[0])
			return
		}
	case "detach":
		if r.Method == http.MethodPost {
			a.handleHTTPPaymentMethodDetach(w, r, parts[0])
			return
		}
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleHTTPSetupSessionsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		a.handleHTTPSetupSessionCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPPaymentMethodsList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cid, _ := strconv.ParseInt(q.Get("customer_id"), 10, 64)
	out, err := dbPaymentMethodsList(ctx.AppDB(), pid, paymentMethodFilters{
		customerID: cid,
		status:     q.Get("status"),
		pmType:     q.Get("type"),
		limit:      limit,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"payment_methods": out, "count": len(out)})
}

func (a *App) handleHTTPSetupSessionCreate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	body["_project_id"] = pid
	out, err := a.toolPaymentMethodSetupCreate(ctx, body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPPaymentMethodSetDefault(w http.ResponseWriter, r *http.Request, idPart string) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := strconv.ParseInt(idPart, 10, 64)
	pm, err := dbPaymentMethodSetDefault(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emitPaymentMethod(ctx, "payment_method.default_changed", pm)
	httpJSON(w, map[string]any{"payment_method": pm})
}

func (a *App) handleHTTPPaymentMethodDetach(w http.ResponseWriter, r *http.Request, idPart string) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := strconv.ParseInt(idPart, 10, 64)
	out, err := a.toolPaymentMethodDetach(ctx, map[string]any{"_project_id": pid, "id": id})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func emitPaymentMethod(ctx *sdk.AppCtx, topic string, pm *PaymentMethod) {
	if ctx == nil || pm == nil {
		return
	}
	ctx.EmitWithProject(topic, pm.ProjectID, map[string]any{
		"id":                         pm.ID,
		"customer_id":                pm.CustomerID,
		"provider":                   pm.Provider,
		"provider_payment_method_id": pm.ProviderPaymentMethodID,
		"type":                       pm.Type,
		"status":                     pm.Status,
		"is_default":                 pm.IsDefault,
		"metadata":                   mapFromAny(pm.Metadata),
	})
}

func dbPaymentMethodsList(db *sql.DB, pid string, f paymentMethodFilters) ([]*PaymentMethod, error) {
	where := []string{"pm.project_id = ?"}
	args := []any{pid}
	if f.customerID != 0 {
		where = append(where, "pm.customer_id = ?")
		args = append(args, f.customerID)
	}
	if strings.TrimSpace(f.status) != "" {
		where = append(where, "pm.status = ?")
		args = append(args, strings.ToLower(strings.TrimSpace(f.status)))
	}
	if strings.TrimSpace(f.pmType) != "" {
		where = append(where, "pm.type = ?")
		args = append(args, strings.ToLower(strings.TrimSpace(f.pmType)))
	}
	limit := f.limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT pm.id, pm.project_id, pm.customer_id,
		        COALESCE(c.name, ''), COALESCE(c.email, ''),
		        pm.provider, pm.provider_customer_id, pm.provider_payment_method_id,
		        pm.provider_mandate_id, pm.type, pm.status, pm.is_default,
		        pm.reusable, pm.delayed_notification, pm.display_brand,
		        pm.display_last4, pm.exp_month, pm.exp_year, pm.country,
		        pm.currency, pm.metadata, pm.created_at, pm.updated_at, pm.detached_at
		 FROM billing_payment_methods pm
		 LEFT JOIN customers c ON c.id = pm.customer_id AND c.project_id = pm.project_id
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY pm.is_default DESC, pm.updated_at DESC, pm.id DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PaymentMethod
	for rows.Next() {
		pm, err := scanPaymentMethod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	return out, rows.Err()
}

func dbDefaultPaymentMethod(db *sql.DB, pid string, customerID int64) (*PaymentMethod, error) {
	methods, err := dbPaymentMethodsList(db, pid, paymentMethodFilters{
		customerID: customerID,
		status:     "active",
		limit:      100,
	})
	if err != nil {
		return nil, err
	}
	for _, pm := range methods {
		if pm.IsDefault && pm.Reusable {
			return pm, nil
		}
	}
	for _, pm := range methods {
		if pm.Reusable {
			return pm, nil
		}
	}
	return nil, nil
}

func dbPaymentMethodGet(db *sql.DB, pid string, id int64) (*PaymentMethod, error) {
	row := db.QueryRow(
		`SELECT pm.id, pm.project_id, pm.customer_id,
		        COALESCE(c.name, ''), COALESCE(c.email, ''),
		        pm.provider, pm.provider_customer_id, pm.provider_payment_method_id,
		        pm.provider_mandate_id, pm.type, pm.status, pm.is_default,
		        pm.reusable, pm.delayed_notification, pm.display_brand,
		        pm.display_last4, pm.exp_month, pm.exp_year, pm.country,
		        pm.currency, pm.metadata, pm.created_at, pm.updated_at, pm.detached_at
		 FROM billing_payment_methods pm
		 LEFT JOIN customers c ON c.id = pm.customer_id AND c.project_id = pm.project_id
		 WHERE pm.id = ? AND pm.project_id = ?`, id, pid)
	pm, err := scanPaymentMethod(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return pm, err
}

func dbPaymentMethodByProviderID(db *sql.DB, provider, providerPMID string) (*PaymentMethod, error) {
	row := db.QueryRow(
		`SELECT pm.id, pm.project_id, pm.customer_id,
		        COALESCE(c.name, ''), COALESCE(c.email, ''),
		        pm.provider, pm.provider_customer_id, pm.provider_payment_method_id,
		        pm.provider_mandate_id, pm.type, pm.status, pm.is_default,
		        pm.reusable, pm.delayed_notification, pm.display_brand,
		        pm.display_last4, pm.exp_month, pm.exp_year, pm.country,
		        pm.currency, pm.metadata, pm.created_at, pm.updated_at, pm.detached_at
		 FROM billing_payment_methods pm
		 LEFT JOIN customers c ON c.id = pm.customer_id AND c.project_id = pm.project_id
		 WHERE pm.provider = ? AND pm.provider_payment_method_id = ?
		   AND pm.detached_at IS NULL
		 LIMIT 1`, provider, providerPMID)
	pm, err := scanPaymentMethod(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return pm, err
}

func dbPaymentMethodUpsert(db *sql.DB, pm *PaymentMethod) (*PaymentMethod, error) {
	if pm == nil {
		return nil, errors.New("payment method required")
	}
	if pm.ProjectID == "" || pm.CustomerID == 0 || pm.ProviderPaymentMethodID == "" {
		return nil, errors.New("project_id, customer_id, and provider_payment_method_id required")
	}
	if pm.Provider == "" {
		pm.Provider = "stripe"
	}
	if pm.Type == "" {
		pm.Type = "unknown"
	}
	if pm.Status == "" {
		pm.Status = "active"
	}
	if pm.Metadata == nil {
		pm.Metadata = json.RawMessage(`{}`)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var tombstone int
	if err = tx.QueryRow("SELECT count(*) FROM billing_detached_methods WHERE provider_id=?", pm.ProviderPaymentMethodID).Scan(&tombstone); err != nil {
		return nil, err
	}
	if tombstone != 0 {
		return nil, errors.New("detached payment method cannot be reactivated by replay")
	}
	var owner int
	if err = tx.QueryRow("SELECT count(*) FROM customers WHERE id=? AND project_id=? AND deleted_at IS NULL", pm.CustomerID, pm.ProjectID).Scan(&owner); err != nil {
		return nil, err
	}
	if owner != 1 {
		return nil, errors.New("customer not found in project")
	}
	existing, err := dbPaymentMethodByProviderIDTx(tx, pm.Provider, pm.ProviderPaymentMethodID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.ProjectID != pm.ProjectID || existing.CustomerID != pm.CustomerID || existing.ProviderCustomerID != pm.ProviderCustomerID {
			return nil, errors.New("payment method belongs to a different customer")
		}
		if existing.DetachedAt != "" || existing.Status == "detached" {
			return nil, errors.New("detached payment method cannot be reactivated by replay")
		}
		// An attachment replay must not overwrite a later default selection.
		tx.Rollback()
		return dbPaymentMethodGet(db, pm.ProjectID, existing.ID)
	}
	if pm.IsDefault {
		if _, err := tx.Exec(
			`UPDATE billing_payment_methods
			 SET is_default = 0, updated_at = CURRENT_TIMESTAMP
			 WHERE project_id = ? AND customer_id = ? AND status = 'active'`,
			pm.ProjectID, pm.CustomerID); err != nil {
			return nil, err
		}
	}
	var id int64
	if existing != nil {
		id = existing.ID
		_, err = tx.Exec(
			`UPDATE billing_payment_methods
			 SET project_id = ?, customer_id = ?, provider_customer_id = ?,
			     provider_mandate_id = ?, type = ?, status = ?, is_default = ?,
			     reusable = ?, delayed_notification = ?, display_brand = ?,
			     display_last4 = ?, exp_month = ?, exp_year = ?, country = ?,
			     currency = ?, metadata = ?, updated_at = CURRENT_TIMESTAMP,
			     detached_at = NULL
			 WHERE id = ?`,
			pm.ProjectID, pm.CustomerID, nullStr(pm.ProviderCustomerID),
			nullStr(pm.ProviderMandateID), pm.Type, pm.Status, boolInt(pm.IsDefault),
			boolInt(pm.Reusable), boolInt(pm.DelayedNotification),
			nullStr(pm.DisplayBrand), nullStr(pm.DisplayLast4),
			nullableInt(pm.ExpMonth), nullableInt(pm.ExpYear),
			nullStr(pm.Country), nullStr(pm.Currency), jsonOrEmpty(pm.Metadata, "{}"), id)
	} else {
		res, execErr := tx.Exec(
			`INSERT INTO billing_payment_methods
			    (project_id, customer_id, provider, provider_customer_id,
			     provider_payment_method_id, provider_mandate_id, type, status,
			     is_default, reusable, delayed_notification, display_brand,
			     display_last4, exp_month, exp_year, country, currency, metadata,
			     created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			pm.ProjectID, pm.CustomerID, pm.Provider, nullStr(pm.ProviderCustomerID),
			pm.ProviderPaymentMethodID, nullStr(pm.ProviderMandateID), pm.Type, pm.Status,
			boolInt(pm.IsDefault), boolInt(pm.Reusable), boolInt(pm.DelayedNotification),
			nullStr(pm.DisplayBrand), nullStr(pm.DisplayLast4),
			nullableInt(pm.ExpMonth), nullableInt(pm.ExpYear),
			nullStr(pm.Country), nullStr(pm.Currency), jsonOrEmpty(pm.Metadata, "{}"),
			nowRFC3339(), nowRFC3339())
		err = execErr
		if err == nil {
			id, _ = res.LastInsertId()
		}
	}
	if err != nil {
		return nil, err
	}
	if !pm.IsDefault {
		var activeDefaults int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM billing_payment_methods
			 WHERE project_id = ? AND customer_id = ? AND status = 'active' AND is_default = 1`,
			pm.ProjectID, pm.CustomerID).Scan(&activeDefaults); err != nil {
			return nil, err
		}
		if activeDefaults == 0 && pm.Status == "active" {
			if _, err := tx.Exec(
				`UPDATE billing_payment_methods
				 SET is_default = 1, updated_at = CURRENT_TIMESTAMP
				 WHERE id = ?`, id); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbPaymentMethodGet(db, pm.ProjectID, id)
}

func dbPaymentMethodByProviderIDTx(tx *sql.Tx, provider, providerPMID string) (*PaymentMethod, error) {
	row := tx.QueryRow(
		`SELECT pm.id, pm.project_id, pm.customer_id, '', '',
		        pm.provider, pm.provider_customer_id, pm.provider_payment_method_id,
		        pm.provider_mandate_id, pm.type, pm.status, pm.is_default,
		        pm.reusable, pm.delayed_notification, pm.display_brand,
		        pm.display_last4, pm.exp_month, pm.exp_year, pm.country,
		        pm.currency, pm.metadata, pm.created_at, pm.updated_at, pm.detached_at
		 FROM billing_payment_methods pm
		 WHERE pm.provider = ? AND pm.provider_payment_method_id = ?
		 LIMIT 1`, provider, providerPMID)
	pm, err := scanPaymentMethod(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return pm, err
}

func dbPaymentMethodSetDefault(db *sql.DB, pid string, id int64) (*PaymentMethod, error) {
	pm, err := dbPaymentMethodGet(db, pid, id)
	if err != nil {
		return nil, err
	}
	if pm == nil {
		return nil, fmt.Errorf("payment method %d not found", id)
	}
	if pm.Status != "active" || pm.DetachedAt != "" {
		return nil, errors.New("only active payment methods can be default")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE billing_payment_methods
		 SET is_default = 0, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND customer_id = ?`,
		pid, pm.CustomerID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE billing_payment_methods
		 SET is_default = 1, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbPaymentMethodGet(db, pid, id)
}

func dbPaymentMethodDetach(db *sql.DB, pid string, id int64) (*PaymentMethod, error) {
	pm, err := dbPaymentMethodGet(db, pid, id)
	if err != nil {
		return nil, err
	}
	if pm == nil {
		return nil, fmt.Errorf("payment method %d not found", id)
	}
	now := nowRFC3339()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE billing_payment_methods
		 SET status = 'detached', is_default = 0, detached_at = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`,
		now, id, pid); err != nil {
		return nil, err
	}
	if pm.IsDefault {
		if _, err := tx.Exec(
			`UPDATE billing_payment_methods
			 SET is_default = 1, updated_at = CURRENT_TIMESTAMP
			 WHERE id = (
			   SELECT id FROM billing_payment_methods
			   WHERE project_id = ? AND customer_id = ? AND status = 'active'
			   ORDER BY updated_at DESC, id DESC LIMIT 1
			 )`,
			pid, pm.CustomerID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbPaymentMethodGet(db, pid, id)
}

func dbSetupSessionCreate(db *sql.DB, s *SetupSession) (*SetupSession, error) {
	if s == nil {
		return nil, errors.New("setup session required")
	}
	if s.ProjectID == "" || s.CustomerID == 0 || s.ProviderSessionID == "" {
		return nil, errors.New("project_id, customer_id, and provider_session_id required")
	}
	if s.Provider == "" {
		s.Provider = "stripe"
	}
	if s.Status == "" {
		s.Status = "pending"
	}
	if s.PaymentMethodTypes == nil {
		s.PaymentMethodTypes = json.RawMessage(`[]`)
	}
	if s.Metadata == nil {
		s.Metadata = json.RawMessage(`{}`)
	}
	res, err := db.Exec(
		`INSERT INTO billing_setup_sessions
		   (project_id, customer_id, provider, provider_customer_id,
		    provider_session_id, provider_setup_intent_id, status,
		    success_url, cancel_url, url, payment_method_types, metadata,
		    created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ProjectID, s.CustomerID, s.Provider, nullStr(s.ProviderCustomerID),
		s.ProviderSessionID, nullStr(s.ProviderSetupIntentID), s.Status,
		nullStr(s.SuccessURL), nullStr(s.CancelURL), nullStr(s.URL),
		jsonOrEmpty(s.PaymentMethodTypes, "[]"), jsonOrEmpty(s.Metadata, "{}"),
		nowRFC3339(), nullStr(s.ExpiresAt))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbSetupSessionGet(db, s.ProjectID, id)
}

func dbSetupSessionGet(db *sql.DB, pid string, id int64) (*SetupSession, error) {
	row := db.QueryRow(
		`SELECT id, project_id, customer_id, provider, provider_customer_id,
		        provider_session_id, provider_setup_intent_id, status,
		        success_url, cancel_url, url, payment_method_types, metadata,
		        created_at, completed_at, expires_at
		 FROM billing_setup_sessions
		 WHERE id = ? AND project_id = ?`, id, pid)
	ss, err := scanSetupSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ss, err
}

func dbSetupSessionCompleteByProviderSession(db *sql.DB, provider, providerSessionID, setupIntentID string) error {
	_, err := db.Exec(
		`UPDATE billing_setup_sessions
		 SET status = 'completed',
		     provider_setup_intent_id = COALESCE(NULLIF(?, ''), provider_setup_intent_id),
		     completed_at = COALESCE(completed_at, ?)
		 WHERE provider = ? AND provider_session_id = ?`,
		setupIntentID, nowRFC3339(), provider, providerSessionID)
	return err
}

func dbSetupSessionFailBySetupIntent(db *sql.DB, provider, setupIntentID string) error {
	_, err := db.Exec(
		`UPDATE billing_setup_sessions
		 SET status = 'failed'
		 WHERE provider = ? AND provider_setup_intent_id = ?`,
		provider, setupIntentID)
	return err
}

func scanPaymentMethod(s rowScanner) (*PaymentMethod, error) {
	var pm PaymentMethod
	var providerCustomer, mandate, brand, last4, country, currency, meta, detached sql.NullString
	var expMonth, expYear sql.NullInt64
	var isDefault, reusable, delayed int
	if err := s.Scan(
		&pm.ID, &pm.ProjectID, &pm.CustomerID, &pm.CustomerName, &pm.CustomerEmail,
		&pm.Provider, &providerCustomer, &pm.ProviderPaymentMethodID,
		&mandate, &pm.Type, &pm.Status, &isDefault,
		&reusable, &delayed, &brand, &last4, &expMonth, &expYear,
		&country, &currency, &meta, &pm.CreatedAt, &pm.UpdatedAt, &detached); err != nil {
		return nil, err
	}
	pm.ProviderCustomerID = providerCustomer.String
	pm.ProviderMandateID = mandate.String
	pm.IsDefault = isDefault == 1
	pm.Reusable = reusable == 1
	pm.DelayedNotification = delayed == 1
	pm.DisplayBrand = brand.String
	pm.DisplayLast4 = last4.String
	if expMonth.Valid {
		pm.ExpMonth = int(expMonth.Int64)
	}
	if expYear.Valid {
		pm.ExpYear = int(expYear.Int64)
	}
	pm.Country = country.String
	pm.Currency = currency.String
	if meta.Valid {
		pm.Metadata = json.RawMessage(meta.String)
	}
	pm.DetachedAt = detached.String
	return &pm, nil
}

func scanSetupSession(s rowScanner) (*SetupSession, error) {
	var ss SetupSession
	var providerCustomer, setupIntent, successURL, cancelURL, url sql.NullString
	var types, meta, completed, expires sql.NullString
	if err := s.Scan(
		&ss.ID, &ss.ProjectID, &ss.CustomerID, &ss.Provider, &providerCustomer,
		&ss.ProviderSessionID, &setupIntent, &ss.Status,
		&successURL, &cancelURL, &url, &types, &meta,
		&ss.CreatedAt, &completed, &expires); err != nil {
		return nil, err
	}
	ss.ProviderCustomerID = providerCustomer.String
	ss.ProviderSetupIntentID = setupIntent.String
	ss.SuccessURL = successURL.String
	ss.CancelURL = cancelURL.String
	ss.URL = url.String
	if types.Valid {
		ss.PaymentMethodTypes = json.RawMessage(types.String)
	}
	if meta.Valid {
		ss.Metadata = json.RawMessage(meta.String)
	}
	ss.CompletedAt = completed.String
	ss.ExpiresAt = expires.String
	return &ss, nil
}

func stringSliceArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	switch v := args[key].(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, toString(item))
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			out = append(out, strings.TrimSpace(part))
		}
		return out
	default:
		return nil
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableInt(v int) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(v), Valid: true}
}

func timestampFromUnix(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func firstNonEmpty(values []string, fallback string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return fallback
}

func firstString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func mapFromStringMap(in map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
