package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// sqliteTime matches CURRENT_TIMESTAMP so stored times compare as text.
const sqliteTime = "2006-01-02 15:04:05"

var errOrderNotFound = errors.New("fabrication order not found")
var errQuoteNotFound = errors.New("fabrication quote not found")

type FabricationQuote struct {
	ID         int64           `json:"id"`
	DesignID   int64           `json:"design_id"`
	RevisionID int64           `json:"revision_id"`
	Request    json.RawMessage `json:"request"`
	Options    []QuoteOption   `json:"options"`
	Errors     []ProviderError `json:"errors"`
	ExpiresAt  string          `json:"expires_at,omitempty"`
	CreatedAt  string          `json:"created_at"`
}

type ProviderError struct {
	Provider     string `json:"provider"`
	ConnectionID int64  `json:"connection_id"`
	Error        string `json:"error"`
}

type FabricationOrder struct {
	ID              int64           `json:"id"`
	DesignID        int64           `json:"design_id"`
	RevisionID      int64           `json:"revision_id"`
	QuoteID         int64           `json:"quote_id"`
	OptionID        string          `json:"option_id"`
	Provider        string          `json:"provider"`
	ConnectionID    int64           `json:"connection_id"`
	Status          string          `json:"status"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Currency        string          `json:"currency"`
	QuotedTotal     float64         `json:"quoted_total"`
	DraftTotal      float64         `json:"draft_total"`
	AddressID       string          `json:"address_id"`
	Option          QuoteOption     `json:"option"`
	Draft           Draft           `json:"draft"`
	Policy          PolicyVerdict   `json:"policy"`
	Approval        json.RawMessage `json:"approval,omitempty"`
	ProviderOrderID string          `json:"provider_order_id,omitempty"`
	ProviderStatus  string          `json:"provider_status,omitempty"`
	Tracking        json.RawMessage `json:"tracking"`
	Error           string          `json:"error,omitempty"`
	CreatedBy       string          `json:"created_by,omitempty"`
	SubmittedAt     string          `json:"submitted_at,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

func (s *Store) GetFabricationSettings(project string) (FabricationSettings, error) {
	settings := defaultFabricationSettings()
	var raw string
	err := s.db.QueryRow(`SELECT settings_json FROM fabrication_settings WHERE project_id = ?`, project).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return settings, err
	}
	if settings.ProviderSettings == nil {
		settings.ProviderSettings = map[string]map[string]any{}
	}
	return settings, nil
}

func (s *Store) SaveFabricationSettings(project string, settings FabricationSettings, by string) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO fabrication_settings(project_id, settings_json, updated_by) VALUES(?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET settings_json = excluded.settings_json, updated_by = excluded.updated_by, updated_at = CURRENT_TIMESTAMP`,
		project, string(raw), by)
	return err
}

func (s *Store) SaveFabricationQuote(project string, q FabricationQuote) (*FabricationQuote, error) {
	options, _ := json.Marshal(q.Options)
	errs, _ := json.Marshal(q.Errors)
	var expires any
	if q.ExpiresAt != "" {
		expires = q.ExpiresAt
	}
	res, err := s.db.Exec(`INSERT INTO fabrication_quotes(project_id, design_id, revision_id, request_json, options_json, errors_json, expires_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, project, q.DesignID, q.RevisionID, string(q.Request), string(options), string(errs), expires)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetFabricationQuote(project, id)
}

const quoteColumns = `id, design_id, revision_id, request_json, options_json, errors_json, COALESCE(expires_at, ''), created_at`

func scanQuote(row interface{ Scan(...any) error }) (*FabricationQuote, error) {
	var q FabricationQuote
	var options, errs string
	if err := row.Scan(&q.ID, &q.DesignID, &q.RevisionID, rawJSONScanner{&q.Request}, &options, &errs, &q.ExpiresAt, &q.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(options), &q.Options)
	_ = json.Unmarshal([]byte(errs), &q.Errors)
	if q.Options == nil {
		q.Options = []QuoteOption{}
	}
	if q.Errors == nil {
		q.Errors = []ProviderError{}
	}
	return &q, nil
}

func (s *Store) GetFabricationQuote(project string, id int64) (*FabricationQuote, error) {
	q, err := scanQuote(s.db.QueryRow(`SELECT `+quoteColumns+` FROM fabrication_quotes WHERE project_id = ? AND id = ?`, project, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errQuoteNotFound
	}
	return q, err
}

func (s *Store) ListFabricationQuotes(project string, designID, revisionID int64, limit int) ([]FabricationQuote, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+quoteColumns+` FROM fabrication_quotes WHERE project_id = ? AND design_id = ?
		AND (? = 0 OR revision_id = ?) ORDER BY id DESC LIMIT ?`, project, designID, revisionID, revisionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FabricationQuote{}
	for rows.Next() {
		q, err := scanQuote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *q)
	}
	return out, rows.Err()
}

func (s *Store) CreateFabricationOrder(project string, o FabricationOrder) (*FabricationOrder, error) {
	option, _ := json.Marshal(o.Option)
	draft, _ := json.Marshal(o.Draft)
	policy, _ := json.Marshal(o.Policy)
	res, err := s.db.Exec(`INSERT INTO fabrication_orders(project_id, design_id, revision_id, quote_id, option_id, provider, connection_id, status,
		idempotency_key, currency, quoted_total, draft_total, address_id, option_json, draft_json, policy_json, created_by)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, project, o.DesignID, o.RevisionID, o.QuoteID, o.OptionID, o.Provider, o.ConnectionID,
		o.Status, o.IdempotencyKey, o.Currency, o.QuotedTotal, o.DraftTotal, o.AddressID, string(option), string(draft), string(policy), o.CreatedBy)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetFabricationOrder(project, id)
}

const orderColumns = `id, design_id, revision_id, quote_id, option_id, provider, connection_id, status, idempotency_key, currency, quoted_total,
	draft_total, address_id, option_json, draft_json, policy_json, approval_json, COALESCE(provider_order_id, ''), COALESCE(provider_status, ''),
	tracking_json, COALESCE(error_text, ''), created_by, COALESCE(submitted_at, ''), created_at, updated_at`

func scanOrder(row interface{ Scan(...any) error }) (*FabricationOrder, error) {
	var o FabricationOrder
	var option, draft, policy string
	if err := row.Scan(&o.ID, &o.DesignID, &o.RevisionID, &o.QuoteID, &o.OptionID, &o.Provider, &o.ConnectionID, &o.Status, &o.IdempotencyKey,
		&o.Currency, &o.QuotedTotal, &o.DraftTotal, &o.AddressID, &option, &draft, &policy, rawJSONScanner{&o.Approval},
		&o.ProviderOrderID, &o.ProviderStatus, rawJSONScanner{&o.Tracking}, &o.Error, &o.CreatedBy, &o.SubmittedAt, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(option), &o.Option)
	_ = json.Unmarshal([]byte(draft), &o.Draft)
	_ = json.Unmarshal([]byte(policy), &o.Policy)
	return &o, nil
}

func (s *Store) GetFabricationOrder(project string, id int64) (*FabricationOrder, error) {
	o, err := scanOrder(s.db.QueryRow(`SELECT `+orderColumns+` FROM fabrication_orders WHERE project_id = ? AND id = ?`, project, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errOrderNotFound
	}
	return o, err
}

func (s *Store) ListFabricationOrders(project string, designID int64, statuses []string, limit int) ([]FabricationOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + orderColumns + ` FROM fabrication_orders WHERE project_id = ? AND (? = 0 OR design_id = ?)`
	args := []any{project, designID, designID}
	if len(statuses) > 0 {
		query += ` AND status IN (?` + repeatString(",?", len(statuses)-1) + `)`
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FabricationOrder{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// ActiveOrderProjects lists projects with orders that still need status polling.
func (s *Store) ActiveOrderProjects() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT project_id FROM fabrication_orders WHERE status IN (?, ?, ?, ?)`,
		orderAwaitingPayment, orderSubmitted, orderInProduction, orderShipped)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func repeatString(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// TransitionOrder moves an order from one of `from` to `to` atomically.
// It returns false when the order was not in an allowed state, which is how
// concurrent or repeated submits are refused without a second provider call.
func (s *Store) TransitionOrder(project string, id int64, from []string, to string) (bool, error) {
	args := []any{to, project, id}
	for _, f := range from {
		args = append(args, f)
	}
	res, err := s.db.Exec(`UPDATE fabrication_orders SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND id = ? AND status IN (?`+repeatString(",?", len(from)-1)+`)`, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) UpdateOrderDraft(project string, id int64, status string, draft Draft, verdict PolicyVerdict) error {
	d, _ := json.Marshal(draft)
	p, _ := json.Marshal(verdict)
	_, err := s.db.Exec(`UPDATE fabrication_orders SET status = ?, draft_json = ?, draft_total = ?, policy_json = ?, updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND id = ?`, status, string(d), draft.Total, string(p), project, id)
	return err
}

func (s *Store) RecordApproval(project string, id int64, approval map[string]any, status string) error {
	raw, _ := json.Marshal(approval)
	_, err := s.db.Exec(`UPDATE fabrication_orders SET approval_json = ?, status = ?, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`,
		string(raw), status, project, id)
	return err
}

func (s *Store) RecordSubmit(project string, id int64, status string, ref OrderRef, errText string) error {
	tracking, _ := json.Marshal(map[string]any{"payment_url": ref.PaymentURL, "note": ref.Note})
	var submitted any
	if ref.ProviderOrderID != "" {
		submitted = time.Now().UTC().Format(sqliteTime)
	}
	_, err := s.db.Exec(`UPDATE fabrication_orders SET status = ?, provider_order_id = NULLIF(?, ''), tracking_json = ?, error_text = NULLIF(?, ''),
		submitted_at = COALESCE(?, submitted_at), updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`,
		status, ref.ProviderOrderID, string(tracking), errText, submitted, project, id)
	return err
}

func (s *Store) RecordStatus(project string, id int64, st OrderStatus) error {
	tracking, _ := json.Marshal(map[string]any{"tracking_number": st.TrackingNumber, "tracking_url": st.TrackingURL, "carrier": st.Carrier})
	_, err := s.db.Exec(`UPDATE fabrication_orders SET status = ?, provider_status = ?,
		tracking_json = CASE WHEN ? != '' OR ? != '' THEN ? ELSE tracking_json END, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`,
		st.Status, st.ProviderStatus, st.TrackingNumber, st.TrackingURL, string(tracking), project, id)
	return err
}

// MonthSpend sums totals of orders submitted since the start of the month
// in the given currency, including orders whose submit outcome is unknown.
func (s *Store) MonthSpend(project, currency string, since time.Time) (float64, error) {
	var total sql.NullFloat64
	err := s.db.QueryRow(`SELECT SUM(draft_total) FROM fabrication_orders WHERE project_id = ? AND currency = ?
		AND status IN (?, ?, ?, ?, ?, ?, ?) AND COALESCE(submitted_at, updated_at) >= ?`,
		project, currency, orderSubmitting, orderAwaitingPayment, orderSubmitted, orderInProduction, orderShipped, orderDelivered, orderUnknown,
		since.UTC().Format(sqliteTime)).Scan(&total)
	return total.Float64, err
}
