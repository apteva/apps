package main

// Provider-neutral reference data. External schemas are normalized here so
// strategies, accounting, backtests, and the UI never depend on Alpaca field
// names. Original payloads and digests are retained for auditability.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

const (
	alpacaTradingSlug       = "alpaca-trading"
	referenceProviderAlpaca = "alpaca"
	referenceUniverseAlpaca = "ALPACA_TRADABLE_US"
)

type Security struct {
	ID              string `json:"id"`
	AssetClass      string `json:"asset_class"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	PrimaryCurrency string `json:"primary_currency,omitempty"`
	Source          string `json:"source"`
	UpdatedAt       string `json:"updated_at"`
}

type SecurityListing struct {
	ID              int64  `json:"id"`
	SecurityID      string `json:"security_id"`
	ProviderAssetID string `json:"provider_asset_id,omitempty"`
	Venue           string `json:"venue"`
	Symbol          string `json:"symbol"`
	Currency        string `json:"currency,omitempty"`
	ValidFrom       string `json:"valid_from,omitempty"`
	ValidTo         string `json:"valid_to,omitempty"`
	Active          bool   `json:"active"`
	Source          string `json:"source"`
}

type CorporateAction struct {
	Provider          string  `json:"provider"`
	ProviderEventID   string  `json:"provider_event_id"`
	Revision          int     `json:"revision"`
	ActionType        string  `json:"action_type"`
	Status            string  `json:"status"`
	SecurityID        string  `json:"security_id,omitempty"`
	RelatedSecurityID string  `json:"related_security_id,omitempty"`
	Symbol            string  `json:"symbol,omitempty"`
	NewSymbol         string  `json:"new_symbol,omitempty"`
	CUSIP             string  `json:"cusip,omitempty"`
	ISIN              string  `json:"isin,omitempty"`
	AnnouncementDate  string  `json:"announcement_date,omitempty"`
	ExDate            string  `json:"ex_date,omitempty"`
	RecordDate        string  `json:"record_date,omitempty"`
	PayableDate       string  `json:"payable_date,omitempty"`
	EffectiveDate     string  `json:"effective_date,omitempty"`
	ProcessDate       string  `json:"process_date,omitempty"`
	RatioNumerator    float64 `json:"ratio_numerator,omitempty"`
	RatioDenominator  float64 `json:"ratio_denominator,omitempty"`
	CashAmount        float64 `json:"cash_amount,omitempty"`
	Currency          string  `json:"currency,omitempty"`
	DataQuality       string  `json:"data_quality"`
	RawJSON           string  `json:"-"`
	PayloadSHA256     string  `json:"payload_sha256"`
	IngestedAt        string  `json:"ingested_at,omitempty"`
	UpdatedAt         string  `json:"updated_at,omitempty"`
}

type ExchangeSession struct {
	Venue       string `json:"venue"`
	SessionDate string `json:"session_date"`
	SessionType string `json:"session_type"`
	OpenAt      string `json:"open_at,omitempty"`
	CloseAt     string `json:"close_at,omitempty"`
	Status      string `json:"status"`
	Source      string `json:"source"`
	Revision    int    `json:"revision"`
}

type ReferenceDataIssue struct {
	ID         int64  `json:"id"`
	Provider   string `json:"provider"`
	IssueKey   string `json:"issue_key"`
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	Message    string `json:"message"`
	Status     string `json:"status"`
	FirstSeen  string `json:"first_seen_at"`
	LastSeen   string `json:"last_seen_at"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}

type CorporateActionPosting struct {
	ID                int64   `json:"id"`
	ProjectID         string  `json:"project_id"`
	PortfolioID       int64   `json:"portfolio_id"`
	Provider          string  `json:"provider"`
	ProviderEventID   string  `json:"provider_event_id"`
	ProviderRevision  int     `json:"provider_revision"`
	EffectType        string  `json:"effect_type"`
	SecurityID        string  `json:"security_id,omitempty"`
	RelatedSecurityID string  `json:"related_security_id,omitempty"`
	Symbol            string  `json:"symbol,omitempty"`
	RelatedSymbol     string  `json:"related_symbol,omitempty"`
	QuantityDelta     float64 `json:"quantity_delta"`
	CashDelta         float64 `json:"cash_delta"`
	CostBasisDelta    float64 `json:"cost_basis_delta"`
	Status            string  `json:"status"`
	AppliedAt         string  `json:"applied_at"`
}

// referenceDataAdapter is the provider boundary for the canonical security,
// corporate-action, calendar, universe, and broker-effect stores. Providers may
// implement any subset of capabilities; consumers only read normalized tables.
type referenceDataAdapter interface {
	Provider() string
	Sync(context.Context, *sdk.AppCtx, *referenceSyncResult)
}

type alpacaReferenceDataAdapter struct{}

func (alpacaReferenceDataAdapter) Provider() string { return referenceProviderAlpaca }

var referenceDataAdapters = []referenceDataAdapter{alpacaReferenceDataAdapter{}}

type referenceSyncResult struct {
	Provider         string `json:"provider"`
	Securities       int    `json:"securities"`
	Listings         int    `json:"listings"`
	CorporateActions int    `json:"corporate_actions"`
	Sessions         int    `json:"sessions"`
	Activities       int    `json:"activities"`
	Issues           int    `json:"issues"`
	StartedAt        string `json:"started_at"`
	CompletedAt      string `json:"completed_at,omitempty"`
	Degraded         bool   `json:"degraded"`
}

func canonicalSecurityID(provider, providerAssetID, isin, cusip, venue, symbol string) string {
	key := strings.TrimSpace(isin)
	if key != "" {
		key = "isin:" + strings.ToUpper(key)
	} else if strings.TrimSpace(cusip) != "" {
		key = "cusip:" + strings.ToUpper(strings.TrimSpace(cusip))
	} else if strings.TrimSpace(providerAssetID) != "" {
		key = provider + ":asset:" + strings.TrimSpace(providerAssetID)
	} else {
		key = provider + ":listing:" + strings.ToUpper(strings.TrimSpace(venue)) + ":" + canonicalSymbol(symbol)
	}
	return "sec-" + uuid.NewSHA1(uuid.NameSpaceURL, []byte(key)).String()
}

func resolveSecurityID(db *sql.DB, provider, providerAssetID, isin, cusip, venue, symbol string) (string, error) {
	for _, identifier := range []struct{ kind, value string }{
		{"isin", isin}, {"cusip", cusip}, {provider + "_asset_id", providerAssetID},
	} {
		if strings.TrimSpace(identifier.value) == "" {
			continue
		}
		var id string
		err := db.QueryRow(`SELECT security_id FROM security_identifiers WHERE identifier_type = ? AND identifier_value = ? ORDER BY valid_from DESC LIMIT 1`,
			identifier.kind, strings.ToUpper(strings.TrimSpace(identifier.value))).Scan(&id)
		if err == nil {
			return id, nil
		}
		if err != sql.ErrNoRows {
			return "", err
		}
	}
	if symbol = canonicalSymbol(symbol); symbol != "" {
		var id string
		err := db.QueryRow(`SELECT security_id FROM security_listings WHERE symbol = ? AND (? = '' OR venue = ?) ORDER BY active DESC, updated_at DESC LIMIT 1`, symbol, venue, venue).Scan(&id)
		if err == nil {
			return id, nil
		}
		if err != sql.ErrNoRows {
			return "", err
		}
		if venue != "" {
			err = db.QueryRow(`SELECT security_id FROM security_listings WHERE symbol=? ORDER BY active DESC,updated_at DESC LIMIT 1`, symbol).Scan(&id)
			if err == nil {
				return id, nil
			}
			if err != sql.ErrNoRows {
				return "", err
			}
		}
	}
	return canonicalSecurityID(provider, providerAssetID, isin, cusip, venue, symbol), nil
}

func dbUpsertSecurity(db *sql.DB, security *Security, listing *SecurityListing, identifiers map[string]string) error {
	if security == nil || security.ID == "" {
		return fmt.Errorf("security id required")
	}
	if security.Status == "" {
		security.Status = "active"
	}
	_, err := db.Exec(`INSERT INTO securities (id, asset_class, name, status, primary_currency, source)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET asset_class=excluded.asset_class, name=CASE WHEN excluded.name='' THEN securities.name ELSE excluded.name END,
		status=excluded.status, primary_currency=CASE WHEN excluded.primary_currency='' THEN securities.primary_currency ELSE excluded.primary_currency END,
		source=excluded.source, updated_at=CURRENT_TIMESTAMP`,
		security.ID, security.AssetClass, security.Name, security.Status, security.PrimaryCurrency, security.Source)
	if err != nil {
		return err
	}
	for kind, value := range identifiers {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, err := db.Exec(`INSERT INTO security_identifiers (security_id, identifier_type, identifier_value, source)
			VALUES (?, ?, ?, ?) ON CONFLICT(identifier_type, identifier_value, valid_from, source)
			DO UPDATE SET security_id=excluded.security_id`, security.ID, kind, value, security.Source); err != nil {
			return err
		}
	}
	if listing == nil || listing.Symbol == "" {
		return nil
	}
	listing.SecurityID = security.ID
	_, err = db.Exec(`INSERT INTO security_listings
		(security_id, provider_asset_id, venue, symbol, currency, valid_from, valid_to, active, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(venue, symbol, valid_from, source) DO UPDATE SET security_id=excluded.security_id,
		provider_asset_id=excluded.provider_asset_id, currency=excluded.currency, valid_to=excluded.valid_to,
		active=excluded.active, updated_at=CURRENT_TIMESTAMP`, listing.SecurityID, listing.ProviderAssetID,
		listing.Venue, canonicalSymbol(listing.Symbol), listing.Currency, listing.ValidFrom, listing.ValidTo,
		boolInt(listing.Active), listing.Source)
	return err
}

func dbUpsertCorporateAction(db *sql.DB, action *CorporateAction) (bool, bool, error) {
	if action == nil || action.Provider == "" || action.ProviderEventID == "" {
		return false, false, fmt.Errorf("provider and event id required")
	}
	var revision int
	var digest string
	err := db.QueryRow(`SELECT revision, payload_sha256 FROM corporate_actions WHERE provider=? AND provider_event_id=? ORDER BY revision DESC LIMIT 1`,
		action.Provider, action.ProviderEventID).Scan(&revision, &digest)
	if err != nil && err != sql.ErrNoRows {
		return false, false, err
	}
	if err == nil && digest == action.PayloadSHA256 {
		return false, false, nil
	}
	corrected := revision > 0
	action.Revision = revision + 1
	if action.Status == "" {
		action.Status = "confirmed"
	}
	if action.DataQuality == "" {
		action.DataQuality = "complete"
	}
	_, err = db.Exec(`INSERT INTO corporate_actions
		(provider,provider_event_id,revision,action_type,status,security_id,related_security_id,symbol,new_symbol,cusip,isin,
		 announcement_date,ex_date,record_date,payable_date,effective_date,process_date,ratio_numerator,ratio_denominator,cash_amount,currency,data_quality,raw_json,payload_sha256)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		action.Provider, action.ProviderEventID, action.Revision, action.ActionType, action.Status, nullableText(action.SecurityID), nullableText(action.RelatedSecurityID),
		action.Symbol, action.NewSymbol, action.CUSIP, action.ISIN, action.AnnouncementDate, action.ExDate, action.RecordDate, action.PayableDate,
		action.EffectiveDate, action.ProcessDate, action.RatioNumerator, action.RatioDenominator, action.CashAmount, action.Currency,
		action.DataQuality, action.RawJSON, action.PayloadSHA256)
	return err == nil, corrected, err
}

func nullableText(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func dbListCorporateActions(db *sql.DB, symbol, actionType, since, until string, limit int) ([]*CorporateAction, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT a.provider,a.provider_event_id,a.revision,a.action_type,a.status,COALESCE(a.security_id,''),COALESCE(a.related_security_id,''),
		a.symbol,a.new_symbol,a.cusip,a.isin,a.announcement_date,a.ex_date,a.record_date,a.payable_date,a.effective_date,a.process_date,
		a.ratio_numerator,a.ratio_denominator,a.cash_amount,a.currency,a.data_quality,a.payload_sha256,a.ingested_at,a.updated_at
		FROM corporate_actions a
		JOIN (SELECT provider,provider_event_id,MAX(revision) revision FROM corporate_actions GROUP BY provider,provider_event_id) latest
		ON latest.provider=a.provider AND latest.provider_event_id=a.provider_event_id AND latest.revision=a.revision WHERE 1=1`
	args := []any{}
	if symbol = canonicalSymbol(symbol); symbol != "" {
		query += ` AND (a.symbol=? OR a.new_symbol=?)`
		args = append(args, symbol, symbol)
	}
	if actionType = strings.ToLower(strings.TrimSpace(actionType)); actionType != "" {
		query += ` AND a.action_type=?`
		args = append(args, actionType)
	}
	if since != "" {
		query += ` AND COALESCE(NULLIF(a.effective_date,''),NULLIF(a.ex_date,''),a.process_date)>=?`
		args = append(args, since)
	}
	if until != "" {
		query += ` AND COALESCE(NULLIF(a.effective_date,''),NULLIF(a.ex_date,''),a.process_date)<=?`
		args = append(args, until)
	}
	query += ` ORDER BY COALESCE(NULLIF(a.effective_date,''),NULLIF(a.ex_date,''),a.process_date) DESC, a.provider_event_id LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CorporateAction{}
	for rows.Next() {
		a := &CorporateAction{}
		if err := rows.Scan(&a.Provider, &a.ProviderEventID, &a.Revision, &a.ActionType, &a.Status, &a.SecurityID, &a.RelatedSecurityID,
			&a.Symbol, &a.NewSymbol, &a.CUSIP, &a.ISIN, &a.AnnouncementDate, &a.ExDate, &a.RecordDate, &a.PayableDate, &a.EffectiveDate, &a.ProcessDate,
			&a.RatioNumerator, &a.RatioDenominator, &a.CashAmount, &a.Currency, &a.DataQuality, &a.PayloadSHA256, &a.IngestedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func dbListSecurities(db *sql.DB, query string, asOf string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	q := `SELECT s.id,s.asset_class,s.name,s.status,s.primary_currency,s.source,s.updated_at,
		l.id,l.provider_asset_id,l.venue,l.symbol,l.currency,l.valid_from,l.valid_to,l.active,l.source
		FROM securities s JOIN security_listings l ON l.security_id=s.id WHERE 1=1`
	args := []any{}
	if query = strings.ToUpper(strings.TrimSpace(query)); query != "" {
		q += ` AND (l.symbol LIKE ? OR UPPER(s.name) LIKE ?)`
		args = append(args, "%"+query+"%", "%"+query+"%")
	}
	if asOf != "" {
		q += ` AND (l.valid_from='' OR l.valid_from<=?) AND (l.valid_to='' OR l.valid_to>?)`
		args = append(args, asOf, asOf)
	}
	q += ` ORDER BY l.active DESC,l.symbol LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var s Security
		var l SecurityListing
		var active int
		if err := rows.Scan(&s.ID, &s.AssetClass, &s.Name, &s.Status, &s.PrimaryCurrency, &s.Source, &s.UpdatedAt,
			&l.ID, &l.ProviderAssetID, &l.Venue, &l.Symbol, &l.Currency, &l.ValidFrom, &l.ValidTo, &active, &l.Source); err != nil {
			return nil, err
		}
		l.SecurityID = s.ID
		l.Active = active != 0
		out = append(out, map[string]any{"security": s, "listing": l})
	}
	return out, rows.Err()
}

func dbListExchangeSessions(db *sql.DB, venue, start, end string, limit int) ([]*ExchangeSession, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	q := `SELECT venue,session_date,session_type,open_at,close_at,status,source,revision FROM exchange_sessions WHERE 1=1`
	args := []any{}
	if venue != "" {
		q += ` AND venue=?`
		args = append(args, strings.ToUpper(venue))
	}
	if start != "" {
		q += ` AND session_date>=?`
		args = append(args, start)
	}
	if end != "" {
		q += ` AND session_date<=?`
		args = append(args, end)
	}
	q += ` ORDER BY session_date LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ExchangeSession{}
	for rows.Next() {
		s := &ExchangeSession{}
		if err := rows.Scan(&s.Venue, &s.SessionDate, &s.SessionType, &s.OpenAt, &s.CloseAt, &s.Status, &s.Source, &s.Revision); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func dbRecordReferenceIssue(db *sql.DB, provider, key, severity, category, message string, payload any) error {
	raw, _ := json.Marshal(payload)
	_, err := db.Exec(`INSERT INTO reference_data_issues(provider,issue_key,severity,category,message,payload_json)
		VALUES(?,?,?,?,?,?) ON CONFLICT(provider,issue_key) DO UPDATE SET severity=excluded.severity,category=excluded.category,
		message=excluded.message,payload_json=excluded.payload_json,status='open',last_seen_at=CURRENT_TIMESTAMP,resolved_at=NULL`,
		provider, key, severity, category, message, string(raw))
	if err == nil {
		emit("data_quality.issue.changed", map[string]any{"schema_version": 1, "provider": provider, "issue_key": key, "severity": severity, "category": category, "status": "open"})
	}
	return err
}

func dbListReferenceIssues(db *sql.DB, status string, limit int) ([]*ReferenceDataIssue, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT id,provider,issue_key,severity,category,message,status,first_seen_at,last_seen_at,COALESCE(resolved_at,'') FROM reference_data_issues WHERE 1=1`
	args := []any{}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'error' THEN 1 WHEN 'warning' THEN 2 ELSE 3 END,last_seen_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ReferenceDataIssue{}
	for rows.Next() {
		i := &ReferenceDataIssue{}
		if err := rows.Scan(&i.ID, &i.Provider, &i.IssueKey, &i.Severity, &i.Category, &i.Message, &i.Status, &i.FirstSeen, &i.LastSeen, &i.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func dbSetReferenceCheckpoint(db *sql.DB, provider, stream, cursor, watermark, lastErr string, rows int) error {
	okAt, errAt := any(nil), any(nil)
	if lastErr == "" {
		okAt = "now"
	} else {
		errAt = "now"
	}
	_, err := db.Exec(`INSERT INTO reference_data_checkpoints(provider,stream,cursor,watermark,last_ok_at,last_error_at,last_error,rows_ingested)
		VALUES(?,?,?,?,CASE WHEN ?='now' THEN CURRENT_TIMESTAMP ELSE NULL END,CASE WHEN ?='now' THEN CURRENT_TIMESTAMP ELSE NULL END,?,?)
		ON CONFLICT(provider,stream) DO UPDATE SET cursor=excluded.cursor,watermark=excluded.watermark,
		last_ok_at=CASE WHEN ?='now' THEN CURRENT_TIMESTAMP ELSE reference_data_checkpoints.last_ok_at END,
		last_error_at=CASE WHEN ?='now' THEN CURRENT_TIMESTAMP ELSE reference_data_checkpoints.last_error_at END,last_error=excluded.last_error,
		rows_ingested=reference_data_checkpoints.rows_ingested+excluded.rows_ingested,updated_at=CURRENT_TIMESTAMP`,
		provider, stream, cursor, watermark, okAt, errAt, lastErr, rows, okAt, errAt)
	return err
}

var supportedCorporateActionTypes = map[string]bool{
	"reverse_split": true, "forward_split": true, "unit_split": true, "cash_dividend": true,
	"stock_dividend": true, "spin_off": true, "cash_merger": true, "stock_merger": true,
	"stock_and_cash_merger": true, "redemption": true, "name_change": true,
	"worthless_removal": true, "rights_distribution": true, "partial_call": true,
	"reorganization": true, "capital_gains_distribution": true,
}

func normalizeCorporateActionType(container string, item map[string]any) string {
	if value := refString(item, "type", "ca_type", "action_type"); value != "" {
		value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", "_"))
		if supportedCorporateActionTypes[value] {
			return value
		}
	}
	value := strings.ToLower(strings.TrimSpace(container))
	value = strings.TrimSuffix(value, "_corporate_actions")
	value = strings.TrimSuffix(value, "_actions")
	value = strings.TrimSuffix(value, "s")
	// plurals whose singular form is not a simple trailing-s removal.
	replacements := map[string]string{"cash_dividende": "cash_dividend", "stock_dividende": "stock_dividend", "reorganizatione": "reorganization", "partial_calle": "partial_call", "capital_gains_distributione": "capital_gains_distribution"}
	if fixed := replacements[value]; fixed != "" {
		value = fixed
	}
	return value
}

func parseAlpacaCorporateActions(raw json.RawMessage) ([]*CorporateAction, string, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, "", fmt.Errorf("decode corporate actions: %w", err)
	}
	next := refString(root, "next_page_token", "nextPageToken")
	container := root
	for _, key := range []string{"corporate_actions", "corporateActions", "data"} {
		if nested, ok := root[key].(map[string]any); ok {
			container = nested
			break
		}
	}
	out := []*CorporateAction{}
	for key, value := range container {
		items, ok := value.([]any)
		if !ok {
			continue
		}
		for _, rawItem := range items {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			typ := normalizeCorporateActionType(key, item)
			if !supportedCorporateActionTypes[typ] {
				continue
			}
			canonicalRaw, _ := json.Marshal(item)
			digest := fmt.Sprintf("%x", sha256.Sum256(canonicalRaw))
			numerator := refFloat(item, "new_rate", "newRate", "ratio_numerator", "rate", "new_quantity", "new_qty")
			denominator := refFloat(item, "old_rate", "oldRate", "ratio_denominator", "old_quantity", "old_qty")
			if denominator == 0 && numerator > 0 {
				denominator = 1
			}
			a := &CorporateAction{
				Provider: referenceProviderAlpaca, ProviderEventID: refString(item, "id", "corporate_action_id", "ca_id"), ActionType: typ,
				Status: strings.ToLower(refString(item, "status")), Symbol: canonicalSymbol(refString(item, "symbol", "initiating_symbol", "old_symbol")),
				NewSymbol: canonicalSymbol(refString(item, "new_symbol", "resulting_symbol", "payable_symbol")), CUSIP: refString(item, "cusip", "initiating_cusip"),
				ISIN: refString(item, "isin", "initiating_isin"), AnnouncementDate: dateOnly(refString(item, "announcement_date", "declaration_date")),
				ExDate: dateOnly(refString(item, "ex_date")), RecordDate: dateOnly(refString(item, "record_date")), PayableDate: dateOnly(refString(item, "payable_date", "payment_date")),
				EffectiveDate: dateOnly(refString(item, "effective_date", "execution_date")), ProcessDate: dateOnly(refString(item, "process_date")),
				RatioNumerator: numerator, RatioDenominator: denominator, CashAmount: refFloat(item, "cash", "cash_amount", "rate", "amount", "per_share_amount"),
				Currency: strings.ToUpper(refString(item, "currency")), DataQuality: refString(item, "data_quality"), RawJSON: string(canonicalRaw), PayloadSHA256: digest,
			}
			if a.Status == "" {
				a.Status = "confirmed"
			}
			if a.DataQuality == "" {
				a.DataQuality = "complete"
			}
			if a.ProviderEventID == "" {
				a.ProviderEventID = "derived-" + digest[:24]
			}
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderEventID < out[j].ProviderEventID })
	return out, next, nil
}

func refString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := m[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		case json.Number:
			return value.String()
		case float64:
			return strconv.FormatFloat(value, 'f', -1, 64)
		}
	}
	return ""
}

func refFloat(m map[string]any, keys ...string) float64 {
	for _, key := range keys {
		switch value := m[key].(type) {
		case float64:
			return value
		case json.Number:
			n, _ := value.Float64()
			return n
		case string:
			n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err == nil {
				return n
			}
		}
	}
	return 0
}

func dateOnly(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 10 {
		return value[:10]
	}
	return value
}

func referenceHistoryStart(ctx *sdk.AppCtx) string {
	if ctx != nil {
		if configured := strings.TrimSpace(ctx.Config().Get("reference_history_start")); configured != "" {
			return configured
		}
	}
	return "2000-01-01"
}

func findActiveConnection(platform sdk.PlatformClient, slug string) (int64, bool) {
	if platform == nil {
		return 0, false
	}
	connections, err := platform.ListConnections(sdk.ConnectionFilter{AppSlug: slug})
	if err != nil {
		return 0, false
	}
	for _, connection := range connections {
		if connection.Status == "" || connection.Status == "active" || connection.Status == "connected" {
			return connection.ID, true
		}
	}
	return 0, false
}

func syncReferenceData(syncCtx context.Context, ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return nil
	}
	if syncCtx == nil {
		syncCtx = context.Background()
	}
	for _, adapter := range referenceDataAdapters {
		result := &referenceSyncResult{Provider: adapter.Provider(), StartedAt: time.Now().UTC().Format(time.RFC3339)}
		adapter.Sync(syncCtx, ctx, result)
		result.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		emit("reference.sync.completed", map[string]any{"schema_version": 1, "result": result})
	}
	if err := applyDueCorporateActions(syncCtx, ctx); err != nil {
		_ = dbRecordReferenceIssue(ctx.AppDB(), "projector", "action-projector", "error", "accounting", err.Error(), nil)
		return err
	}
	return nil
}

func (alpacaReferenceDataAdapter) Sync(_ context.Context, ctx *sdk.AppCtx, result *referenceSyncResult) {
	marketConn, marketOK := findActiveConnection(ctx.PlatformAPI(), alpacaMarketDataSlug)
	tradingConn, tradingOK := findActiveConnection(ctx.PlatformAPI(), alpacaTradingSlug)
	if marketOK {
		if err := syncAlpacaCorporateActions(ctx, marketConn, result); err != nil {
			result.Degraded = true
			result.Issues++
			_ = dbRecordReferenceIssue(ctx.AppDB(), referenceProviderAlpaca, "corporate-actions-sync", "error", "ingestion", err.Error(), nil)
		}
	} else {
		result.Degraded = true
		_ = dbRecordReferenceIssue(ctx.AppDB(), referenceProviderAlpaca, "market-data-unbound", "warning", "connection", "Bind Alpaca Market Data to ingest corporate actions.", nil)
	}
	if tradingOK {
		if err := syncAlpacaAssets(ctx, tradingConn, result); err != nil {
			result.Degraded = true
			result.Issues++
			_ = dbRecordReferenceIssue(ctx.AppDB(), referenceProviderAlpaca, "assets-sync", "error", "ingestion", err.Error(), nil)
		}
		if err := syncAlpacaCalendar(ctx, tradingConn, result); err != nil {
			result.Degraded = true
			result.Issues++
			_ = dbRecordReferenceIssue(ctx.AppDB(), referenceProviderAlpaca, "calendar-sync", "error", "ingestion", err.Error(), nil)
		}
		if err := syncAlpacaAccountActivities(ctx, tradingConn, result); err != nil {
			result.Degraded = true
			result.Issues++
			_ = dbRecordReferenceIssue(ctx.AppDB(), referenceProviderAlpaca, "activities-sync", "warning", "ingestion", err.Error(), nil)
		}
	} else {
		result.Degraded = true
		_ = dbRecordReferenceIssue(ctx.AppDB(), referenceProviderAlpaca, "trading-unbound", "warning", "connection", "Bind Alpaca Trading to ingest the asset master, authoritative sessions, and account activities.", nil)
	}
}

func syncReferenceActivities(_ context.Context, ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return nil
	}
	connectionID, ok := findActiveConnection(ctx.PlatformAPI(), alpacaTradingSlug)
	if !ok {
		return nil
	}
	result := &referenceSyncResult{Provider: referenceProviderAlpaca, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := syncAlpacaAccountActivities(ctx, connectionID, result); err != nil {
		_ = dbSetReferenceCheckpoint(ctx.AppDB(), referenceProviderAlpaca, "account_activities", "", "", err.Error(), 0)
		return err
	}
	return nil
}

func syncAlpacaCorporateActions(ctx *sdk.AppCtx, connectionID int64, result *referenceSyncResult) error {
	start := referenceHistoryStart(ctx)
	var page, watermark string
	_ = ctx.AppDB().QueryRow(`SELECT cursor,watermark FROM reference_data_checkpoints WHERE provider=? AND stream='corporate_actions'`, referenceProviderAlpaca).Scan(&page, &watermark)
	if page != "" && watermark != "" {
		start = watermark
	} else if watermark != "" {
		if parsed, err := time.Parse("2006-01-02", dateOnly(watermark)); err == nil {
			start = parsed.AddDate(0, 0, -30).Format("2006-01-02")
		}
	}
	end := time.Now().UTC().AddDate(1, 0, 0).Format("2006-01-02")
	total := 0
	completed := false
	for pages := 0; pages < 500; pages++ {
		args := map[string]any{"start": start, "end": end, "region": "all", "data_quality": "all", "limit": 1000, "sort": "asc"}
		if page != "" {
			args["page_token"] = page
		}
		res, err := executeAlpacaToolWithRetry(context.Background(), ctx.PlatformAPI(), connectionID, "corporate_actions", args)
		if err != nil {
			return err
		}
		if res == nil || !res.Success {
			return fmt.Errorf("corporate_actions failed: %s", string(safeBytes(res)))
		}
		actions, next, err := parseAlpacaCorporateActions(res.Data)
		if err != nil {
			return err
		}
		for _, action := range actions {
			securityID, resolveErr := resolveSecurityID(ctx.AppDB(), referenceProviderAlpaca, "", action.ISIN, action.CUSIP, "", action.Symbol)
			if resolveErr != nil {
				return resolveErr
			}
			action.SecurityID = securityID
			if err := ensurePlaceholderSecurity(ctx.AppDB(), action); err != nil {
				return err
			}
			if issue := validateCorporateAction(action); issue != "" {
				result.Issues++
				_ = dbRecordReferenceIssue(ctx.AppDB(), referenceProviderAlpaca, "action:"+action.ProviderEventID, "warning", "corporate_action", issue, action)
				action.DataQuality = "incomplete"
			}
			inserted, corrected, err := dbUpsertCorporateAction(ctx.AppDB(), action)
			if err != nil {
				return err
			}
			if inserted {
				total++
				topic := "corporate_action.received"
				if corrected {
					topic = "corporate_action.corrected"
				}
				emit(topic, map[string]any{"schema_version": 1, "provider": action.Provider, "provider_event_id": action.ProviderEventID, "revision": action.Revision, "action_type": action.ActionType, "symbol": action.Symbol})
			}
		}
		if next == "" || next == page {
			page = ""
			completed = true
			break
		}
		page = next
		_ = dbSetReferenceCheckpoint(ctx.AppDB(), referenceProviderAlpaca, "corporate_actions", page, start, "", 0)
	}
	if !completed {
		return fmt.Errorf("corporate_actions pagination exceeded 500 pages; checkpoint retained for resume")
	}
	result.CorporateActions += total
	return dbSetReferenceCheckpoint(ctx.AppDB(), referenceProviderAlpaca, "corporate_actions", page, time.Now().UTC().Format("2006-01-02"), "", total)
}

func ensurePlaceholderSecurity(db *sql.DB, action *CorporateAction) error {
	if action.SecurityID == "" {
		return nil
	}
	class := inferAssetClass(action.Symbol)
	if class != "equity" && class != "etf" {
		class = "equity"
	}
	return dbUpsertSecurity(db, &Security{ID: action.SecurityID, AssetClass: class, Status: "active", PrimaryCurrency: action.Currency, Source: action.Provider},
		&SecurityListing{SecurityID: action.SecurityID, Venue: "UNKNOWN", Symbol: action.Symbol, Currency: action.Currency, Active: true, Source: action.Provider},
		map[string]string{"isin": action.ISIN, "cusip": action.CUSIP})
}

func validateCorporateAction(action *CorporateAction) string {
	if !supportedCorporateActionTypes[action.ActionType] {
		return "unsupported corporate-action type"
	}
	if action.Symbol == "" && action.CUSIP == "" && action.ISIN == "" {
		return "action has no resolvable symbol or identifier"
	}
	if strings.Contains(action.ActionType, "split") && (action.RatioNumerator <= 0 || action.RatioDenominator <= 0) {
		return "split is missing a positive old/new ratio"
	}
	if (action.ActionType == "cash_dividend" || action.ActionType == "capital_gains_distribution") && action.CashAmount <= 0 {
		return "cash distribution is missing a positive per-share amount"
	}
	if action.EffectiveDate == "" && action.ExDate == "" && action.PayableDate == "" && action.ProcessDate == "" {
		return "action has no effective, ex, payable, or process date"
	}
	return ""
}

func syncAlpacaAssets(ctx *sdk.AppCtx, connectionID int64, result *referenceSyncResult) error {
	today := time.Now().UTC().Format("2006-01-02")
	seenActive := map[string]bool{}
	for _, status := range []string{"active", "inactive"} {
		res, err := executeAlpacaToolWithRetry(context.Background(), ctx.PlatformAPI(), connectionID, "list_assets", map[string]any{"status": status, "asset_class": "us_equity"})
		if err != nil {
			return err
		}
		if res == nil || !res.Success {
			return fmt.Errorf("list_assets(%s) failed: %s", status, string(safeBytes(res)))
		}
		assets, err := parseObjectList(res.Data, "assets")
		if err != nil {
			return err
		}
		for _, asset := range assets {
			symbol := canonicalSymbol(refString(asset, "symbol"))
			if symbol == "" {
				continue
			}
			assetID := refString(asset, "id", "asset_id")
			venue := strings.ToUpper(refString(asset, "exchange"))
			if venue == "" {
				venue = "ALPACA_US"
			}
			isin := refString(asset, "isin")
			cusip := refString(asset, "cusip")
			securityID, err := resolveSecurityID(ctx.AppDB(), referenceProviderAlpaca, assetID, isin, cusip, venue, symbol)
			if err != nil {
				return err
			}
			active := status == "active" && refBoolDefault(asset, true, "tradable", "active")
			validTo := ""
			if !active {
				validTo = today
			} else {
				seenActive[securityID] = true
			}
			class := normalizeAssetClass(refString(asset, "class", "asset_class"), symbol)
			security := &Security{ID: securityID, AssetClass: class, Name: refString(asset, "name"), Status: status, PrimaryCurrency: strings.ToUpper(refString(asset, "currency")), Source: referenceProviderAlpaca}
			listing := &SecurityListing{ProviderAssetID: assetID, Venue: venue, Symbol: symbol, Currency: security.PrimaryCurrency, ValidTo: validTo, Active: active, Source: referenceProviderAlpaca}
			if err := dbUpsertSecurity(ctx.AppDB(), security, listing, map[string]string{"alpaca_asset_id": assetID, "isin": isin, "cusip": cusip}); err != nil {
				return err
			}
			result.Securities++
			result.Listings++
			if active {
				_, _ = ctx.AppDB().Exec(`INSERT INTO universe_memberships(universe_id,security_id,valid_from,source)
					SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM universe_memberships WHERE universe_id=? AND security_id=? AND source=? AND valid_to='')`, referenceUniverseAlpaca, securityID, today, referenceProviderAlpaca, referenceUniverseAlpaca, securityID, referenceProviderAlpaca)
			}
		}
	}
	// Snapshot-derived membership is truthful only from the first observed day.
	rows, err := ctx.AppDB().Query(`SELECT security_id FROM universe_memberships WHERE universe_id=? AND valid_to=''`, referenceUniverseAlpaca)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil && !seenActive[id] {
				_, _ = ctx.AppDB().Exec(`UPDATE universe_memberships SET valid_to=?,updated_at=CURRENT_TIMESTAMP WHERE universe_id=? AND security_id=? AND valid_to=''`, today, referenceUniverseAlpaca, id)
			}
		}
	}
	_ = dbRecordReferenceIssue(ctx.AppDB(), referenceProviderAlpaca, "historical-universe-limited", "warning", "survivorship", "Alpaca asset snapshots provide point-in-time universe coverage only from the first successful sync; earlier backtests remain explicit-symbol and are marked survivorship-unverified.", map[string]any{"coverage_starts": today})
	attachStableSecurityIDs(ctx.AppDB())
	emit("reference.security.changed", map[string]any{"schema_version": 1, "provider": referenceProviderAlpaca, "securities": result.Securities, "universe": referenceUniverseAlpaca})
	return dbSetReferenceCheckpoint(ctx.AppDB(), referenceProviderAlpaca, "assets", "", today, "", result.Securities)
}

func attachStableSecurityIDs(db *sql.DB) {
	for _, table := range []string{"positions", "orders", "marks", "watchlist", "backtest_market_bars"} {
		// Table names are a fixed internal allow-list, never user input.
		_, _ = db.Exec(`UPDATE ` + table + ` SET security_id=(SELECT l.security_id FROM security_listings l WHERE l.symbol=` + table + `.symbol ORDER BY l.active DESC,l.updated_at DESC LIMIT 1) WHERE security_id IS NULL`)
	}
}

func normalizeAssetClass(value, symbol string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "us_equity", "equity":
		if inferAssetClass(symbol) == "etf" {
			return "etf"
		}
		return "equity"
	case "crypto":
		return "crypto"
	case "us_option", "option":
		return "option"
	}
	return inferAssetClass(symbol)
}

func refBoolDefault(m map[string]any, def bool, keys ...string) bool {
	for _, key := range keys {
		if value, ok := m[key].(bool); ok {
			return value
		}
	}
	return def
}

func parseObjectList(raw json.RawMessage, keys ...string) ([]map[string]any, error) {
	var direct []map[string]any
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, nil
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	for _, key := range keys {
		if values, ok := root[key].([]any); ok {
			out := []map[string]any{}
			for _, value := range values {
				if row, ok := value.(map[string]any); ok {
					out = append(out, row)
				}
			}
			return out, nil
		}
	}
	return nil, nil
}

func syncAlpacaCalendar(ctx *sdk.AppCtx, connectionID int64, result *referenceSyncResult) error {
	start := referenceHistoryStart(ctx)
	var prior string
	if ctx.AppDB().QueryRow(`SELECT watermark FROM reference_data_checkpoints WHERE provider=? AND stream='calendar'`, referenceProviderAlpaca).Scan(&prior) == nil && prior != "" {
		start = time.Now().UTC().AddDate(-1, 0, 0).Format("2006-01-02")
	}
	end := time.Now().UTC().AddDate(2, 0, 0).Format("2006-01-02")
	res, err := executeAlpacaToolWithRetry(context.Background(), ctx.PlatformAPI(), connectionID, "get_calendar", map[string]any{"start": start, "end": end, "date_type": "TRADING"})
	if err != nil {
		return err
	}
	if res == nil || !res.Success {
		return fmt.Errorf("get_calendar failed: %s", string(safeBytes(res)))
	}
	rows, err := parseObjectList(res.Data, "calendar", "sessions")
	if err != nil {
		return err
	}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return err
	}
	count := 0
	observed := map[string]bool{}
	for _, row := range rows {
		date := dateOnly(refString(row, "date", "session_date"))
		if date == "" {
			continue
		}
		observed[date] = true
		open := refString(row, "open", "session_open", "open_at")
		closeValue := refString(row, "close", "session_close", "close_at")
		openAt := calendarTimestamp(date, open, location)
		closeAt := calendarTimestamp(date, closeValue, location)
		status := "open"
		if openAt == "" || closeAt == "" {
			status = "closed"
		}
		_, err := ctx.AppDB().Exec(`INSERT INTO exchange_sessions(venue,session_date,session_type,open_at,close_at,status,source) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(venue,session_date,session_type,source) DO UPDATE SET open_at=excluded.open_at,close_at=excluded.close_at,status=excluded.status,revision=exchange_sessions.revision+CASE WHEN exchange_sessions.open_at<>excluded.open_at OR exchange_sessions.close_at<>excluded.close_at OR exchange_sessions.status<>excluded.status THEN 1 ELSE 0 END,updated_at=CURRENT_TIMESTAMP`,
			"US_EQUITY", date, "regular", openAt, closeAt, status, referenceProviderAlpaca)
		if err != nil {
			return err
		}
		count++
	}
	startDate, startErr := time.Parse("2006-01-02", start)
	endDate, endErr := time.Parse("2006-01-02", end)
	if startErr == nil && endErr == nil {
		for day := startDate; !day.After(endDate); day = day.AddDate(0, 0, 1) {
			date := day.Format("2006-01-02")
			if observed[date] {
				continue
			}
			_, err := ctx.AppDB().Exec(`INSERT INTO exchange_sessions(venue,session_date,session_type,status,source) VALUES('US_EQUITY',?,'regular','closed',?) ON CONFLICT(venue,session_date,session_type,source) DO UPDATE SET open_at='',close_at='',status='closed',updated_at=CURRENT_TIMESTAMP`, date, referenceProviderAlpaca)
			if err != nil {
				return err
			}
			count++
		}
	}
	result.Sessions += count
	emit("calendar.session.changed", map[string]any{"schema_version": 1, "venue": "US_EQUITY", "sessions": count, "through": end})
	return dbSetReferenceCheckpoint(ctx.AppDB(), referenceProviderAlpaca, "calendar", "", end, "", count)
}

func calendarTimestamp(date, clock string, location *time.Location) string {
	clock = strings.TrimSpace(clock)
	if clock == "" {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339, clock); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, date+" "+clock, location); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func syncAlpacaAccountActivities(ctx *sdk.AppCtx, connectionID int64, result *referenceSyncResult) error {
	activityTypes := "DIV,CGD,MA,NC,SC,SSO,SSP,REORG"
	after := time.Now().UTC().AddDate(0, 0, -14).Format("2006-01-02")
	activities := []map[string]any{}
	page := ""
	for pages := 0; pages < 100; pages++ {
		args := map[string]any{"activity_types": activityTypes, "after": after, "direction": "asc", "page_size": 100}
		if page != "" {
			args["page_token"] = page
		}
		res, err := executeAlpacaToolWithRetry(context.Background(), ctx.PlatformAPI(), connectionID, "list_activities", args)
		if err != nil {
			return err
		}
		if res == nil || !res.Success {
			return fmt.Errorf("list_activities failed: %s", string(safeBytes(res)))
		}
		batch, err := parseObjectList(res.Data, "activities")
		if err != nil {
			return err
		}
		activities = append(activities, batch...)
		if len(batch) < 100 {
			break
		}
		next := refString(batch[len(batch)-1], "id", "activity_id")
		if next == "" || next == page {
			break
		}
		page = next
	}
	portfolios, err := dbAllPortfolios(ctx.AppDB())
	if err != nil {
		return err
	}
	count := 0
	for _, activity := range activities {
		id := refString(activity, "id", "activity_id")
		if id == "" {
			continue
		}
		typ := strings.ToUpper(refString(activity, "activity_type", "type", "entry_type"))
		symbol := canonicalSymbol(refString(activity, "symbol"))
		cash := refFloat(activity, "net_amount", "cash_amount")
		qty := refFloat(activity, "qty", "quantity")
		for _, portfolio := range portfolios {
			if portfolio.Mode != "live" || portfolio.BrokerSlug != alpacaTradingSlug {
				continue
			}
			effect := "broker_activity:" + strings.ToLower(typ)
			details, _ := json.Marshal(activity)
			res, err := ctx.AppDB().Exec(`INSERT INTO corporate_action_postings(project_id,portfolio_id,provider,provider_event_id,provider_revision,effect_type,symbol,quantity_delta,cash_delta,status,details_json)
				VALUES(?,?,?,?,?,?,?,?,?,'observed',?) ON CONFLICT(portfolio_id,provider,provider_event_id,provider_revision,effect_type,symbol,related_symbol) DO NOTHING`, portfolio.ProjectID, portfolio.ID, referenceProviderAlpaca, id, 1, effect, symbol, qty, cash, string(details))
			if err != nil {
				return err
			}
			if affected, _ := res.RowsAffected(); affected > 0 {
				count++
				emit("corporate_action.applied", map[string]any{"schema_version": 1, "portfolio_id": portfolio.ID, "provider_event_id": id, "effect_type": effect, "status": "observed"})
			}
		}
	}
	result.Activities += count
	return dbSetReferenceCheckpoint(ctx.AppDB(), referenceProviderAlpaca, "account_activities", "", time.Now().UTC().Format(time.RFC3339), "", count)
}

func applyDueCorporateActions(_ context.Context, ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return nil
	}
	today := time.Now().UTC().Format("2006-01-02")
	projectorStart := ""
	err := ctx.AppDB().QueryRow(`SELECT watermark FROM reference_data_checkpoints WHERE provider='trading' AND stream='corporate_action_projector'`).Scan(&projectorStart)
	if err == sql.ErrNoRows || projectorStart == "" {
		projectorStart = today
		if err := dbSetReferenceCheckpoint(ctx.AppDB(), "trading", "corporate_action_projector", "", projectorStart, "", 0); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	portfolios, err := dbAllPortfolios(ctx.AppDB())
	if err != nil {
		return err
	}
	for _, portfolio := range portfolios {
		if portfolio.Mode != "paper" {
			continue
		} // broker accounts are authoritative.
		positions, err := dbListPositions(ctx.AppDB(), portfolio.ID)
		if err != nil {
			return err
		}
		for _, position := range positions {
			actions, err := dbListCorporateActions(ctx.AppDB(), position.Symbol, "", projectorStart, today, 1000)
			if err != nil {
				return err
			}
			for _, action := range actions {
				if action.Status == "cancelled" || action.Status == "deleted" || action.DataQuality == "incomplete" {
					continue
				}
				if err := applyCorporateActionToPortfolio(ctx.AppDB(), portfolio, action, today); err != nil {
					_ = dbRecordReferenceIssue(ctx.AppDB(), action.Provider, "apply:"+action.ProviderEventID+":"+strconv.FormatInt(portfolio.ID, 10), "error", "accounting", err.Error(), map[string]any{"portfolio_id": portfolio.ID, "revision": action.Revision})
				}
			}
		}
		if err := applyPendingCashEntitlements(ctx.AppDB(), portfolio, today); err != nil {
			return err
		}
	}
	return nil
}

func applyPendingCashEntitlements(db *sql.DB, portfolio *Portfolio, today string) error {
	rows, err := db.Query(`SELECT provider,provider_event_id,provider_revision FROM corporate_action_entitlements WHERE portfolio_id=?`, portfolio.ID)
	if err != nil {
		return err
	}
	type entitlementKey struct {
		provider, id string
		revision     int
	}
	keys := []entitlementKey{}
	for rows.Next() {
		var key entitlementKey
		if err := rows.Scan(&key.provider, &key.id, &key.revision); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, key := range keys {
		action, err := dbGetCorporateActionRevision(db, key.provider, key.id, key.revision)
		if err != nil {
			return err
		}
		if action.PayableDate != "" && action.PayableDate <= today {
			if err := applyCashDistribution(db, portfolio, action, today); err != nil {
				return err
			}
		}
	}
	return nil
}

func dbGetCorporateActionRevision(db *sql.DB, provider, id string, revision int) (*CorporateAction, error) {
	row := db.QueryRow(`SELECT provider,provider_event_id,revision,action_type,status,COALESCE(security_id,''),COALESCE(related_security_id,''),symbol,new_symbol,cusip,isin,announcement_date,ex_date,record_date,payable_date,effective_date,process_date,ratio_numerator,ratio_denominator,cash_amount,currency,data_quality,payload_sha256,ingested_at,updated_at FROM corporate_actions WHERE provider=? AND provider_event_id=? AND revision=?`, provider, id, revision)
	a := &CorporateAction{}
	err := row.Scan(&a.Provider, &a.ProviderEventID, &a.Revision, &a.ActionType, &a.Status, &a.SecurityID, &a.RelatedSecurityID, &a.Symbol, &a.NewSymbol, &a.CUSIP, &a.ISIN, &a.AnnouncementDate, &a.ExDate, &a.RecordDate, &a.PayableDate, &a.EffectiveDate, &a.ProcessDate, &a.RatioNumerator, &a.RatioDenominator, &a.CashAmount, &a.Currency, &a.DataQuality, &a.PayloadSHA256, &a.IngestedAt, &a.UpdatedAt)
	return a, err
}

func applyCorporateActionToPortfolio(db *sql.DB, portfolio *Portfolio, action *CorporateAction, today string) error {
	if portfolio == nil || action == nil || action.Symbol == "" {
		return nil
	}
	var earlier int
	if err := db.QueryRow(`SELECT COUNT(*) FROM corporate_action_postings WHERE portfolio_id=? AND provider=? AND provider_event_id=? AND provider_revision<>? AND status='applied'`, portfolio.ID, action.Provider, action.ProviderEventID, action.Revision).Scan(&earlier); err != nil {
		return err
	}
	if earlier > 0 {
		return fmt.Errorf("corporate action revision changed after application; manual reconciliation required")
	}
	switch action.ActionType {
	case "cash_dividend", "capital_gains_distribution":
		return applyCashDistribution(db, portfolio, action, today)
	case "forward_split", "reverse_split", "unit_split":
		if dueDate(action) > today {
			return nil
		}
		return applySplit(db, portfolio, action)
	case "name_change":
		if action.NewSymbol == "" || dueDate(action) > today {
			return nil
		}
		return applySymbolChange(db, portfolio, action)
	case "worthless_removal":
		if dueDate(action) > today {
			return nil
		}
		return applyWorthlessRemoval(db, portfolio, action)
	default:
		// Mergers, spinoffs, rights, redemptions, and reorganizations can
		// contain several legs and tax-basis allocations. They remain visible
		// and auditable, but are not guessed from incomplete terms.
		return nil
	}
}

func dueDate(action *CorporateAction) string {
	for _, value := range []string{action.EffectiveDate, action.PayableDate, action.ExDate, action.ProcessDate} {
		if value != "" {
			return value
		}
	}
	return "9999-12-31"
}

func applyCashDistribution(db *sql.DB, portfolio *Portfolio, action *CorporateAction, today string) error {
	if action.ExDate == "" || action.ExDate > today || action.CashAmount <= 0 {
		return nil
	}
	var entitled float64
	err := db.QueryRow(`SELECT entitled_quantity FROM corporate_action_entitlements WHERE portfolio_id=? AND provider=? AND provider_event_id=? AND provider_revision=? AND symbol=?`, portfolio.ID, action.Provider, action.ProviderEventID, action.Revision, action.Symbol).Scan(&entitled)
	if err == sql.ErrNoRows {
		if action.ExDate < today {
			_ = dbRecordReferenceIssue(db, action.Provider, "missed-entitlement:"+action.ProviderEventID+":"+strconv.FormatInt(portfolio.ID, 10), "warning", "accounting", "Distribution arrived after its ex-date; historical entitlement was not inferred from current holdings.", map[string]any{"portfolio_id": portfolio.ID, "symbol": action.Symbol, "ex_date": action.ExDate})
			return nil
		} // Never infer a historical entitlement from today's holdings.
		_ = db.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM positions WHERE portfolio_id=? AND symbol=?`, portfolio.ID, action.Symbol).Scan(&entitled)
		if entitled <= 0 {
			return nil
		}
		_, err = db.Exec(`INSERT INTO corporate_action_entitlements(portfolio_id,provider,provider_event_id,provider_revision,symbol,entitled_quantity) VALUES(?,?,?,?,?,?)`, portfolio.ID, action.Provider, action.ProviderEventID, action.Revision, action.Symbol, entitled)
	}
	if err != nil {
		return err
	}
	payable := action.PayableDate
	if payable == "" {
		payable = action.ExDate
	}
	if payable > today {
		return nil
	}
	cash := entitled * action.CashAmount
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inserted, err := insertPostingTx(tx, portfolio, action, "cash_distribution", action.Symbol, "", 0, cash, 0, map[string]any{"entitled_quantity": entitled, "per_share": action.CashAmount, "currency": action.Currency})
	if err != nil {
		return err
	}
	if !inserted {
		return nil
	}
	if _, err = tx.Exec(`UPDATE portfolios SET cash=cash+?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, cash, portfolio.ID); err != nil {
		return err
	}
	if err = dbInsertJournalTx(tx, portfolio.ProjectID, portfolio.ID, "corporate_action", fmt.Sprintf("%s credited %.2f %s for %s", strings.ReplaceAll(action.ActionType, "_", " "), cash, action.Currency, action.Symbol), map[string]any{"provider_event_id": action.ProviderEventID, "revision": action.Revision, "cash_delta": cash}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	emitCorporateActionApplied(portfolio, action, "cash_distribution", cash, 0)
	return nil
}

func applySplit(db *sql.DB, portfolio *Portfolio, action *CorporateAction) error {
	if action.RatioNumerator <= 0 || action.RatioDenominator <= 0 {
		return fmt.Errorf("invalid split ratio")
	}
	ratio := action.RatioNumerator / action.RatioDenominator
	var qty, avg float64
	err := db.QueryRow(`SELECT qty,avg_cost FROM positions WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, portfolio.ID, action.Symbol).Scan(&qty, &avg)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	newQty := qty * ratio
	newAvg := avg / ratio
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inserted, err := insertPostingTx(tx, portfolio, action, "split", action.Symbol, "", newQty-qty, 0, 0, map[string]any{"old_quantity": qty, "new_quantity": newQty, "old_avg_cost": avg, "new_avg_cost": newAvg, "ratio": ratio})
	if err != nil {
		return err
	}
	if !inserted {
		return nil
	}
	if _, err = tx.Exec(`UPDATE positions SET qty=?,avg_cost=?,updated_at=CURRENT_TIMESTAMP WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, newQty, newAvg, portfolio.ID, action.Symbol); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE orders SET status='cancelled',rejection_code='corporate_action',rejection_detail='cancelled for split',resolved_at=CURRENT_TIMESTAMP WHERE portfolio_id=? AND symbol=? AND status='working'`, portfolio.ID, action.Symbol); err != nil {
		return err
	}
	if err = dbInsertJournalTx(tx, portfolio.ProjectID, portfolio.ID, "corporate_action", fmt.Sprintf("Applied %g:%g split to %s", action.RatioNumerator, action.RatioDenominator, action.Symbol), map[string]any{"provider_event_id": action.ProviderEventID, "revision": action.Revision, "old_quantity": qty, "new_quantity": newQty}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	emitCorporateActionApplied(portfolio, action, "split", 0, newQty-qty)
	return nil
}

func applySymbolChange(db *sql.DB, portfolio *Portfolio, action *CorporateAction) error {
	if action.NewSymbol == "" || action.NewSymbol == action.Symbol {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldQty, oldAvg, oldRealized float64
	scanErr := tx.QueryRow(`SELECT qty,avg_cost,realized_pnl FROM positions WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, portfolio.ID, action.Symbol).Scan(&oldQty, &oldAvg, &oldRealized)
	if scanErr != nil && scanErr != sql.ErrNoRows {
		return scanErr
	}
	if scanErr == sql.ErrNoRows {
		return nil
	}
	inserted, err := insertPostingTx(tx, portfolio, action, "symbol_change", action.Symbol, action.NewSymbol, 0, 0, 0, map[string]any{"old_symbol": action.Symbol, "new_symbol": action.NewSymbol})
	if err != nil {
		return err
	}
	if !inserted {
		return nil
	}
	var newQty, newAvg, newRealized float64
	newErr := tx.QueryRow(`SELECT qty,avg_cost,realized_pnl FROM positions WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, portfolio.ID, action.NewSymbol).Scan(&newQty, &newAvg, &newRealized)
	if newErr == nil {
		combined := oldQty + newQty
		weighted := (oldQty*oldAvg + newQty*newAvg) / combined
		if _, err = tx.Exec(`UPDATE positions SET qty=?,avg_cost=?,realized_pnl=?,security_id=COALESCE(security_id,?),updated_at=CURRENT_TIMESTAMP WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, combined, weighted, oldRealized+newRealized, nullableText(action.SecurityID), portfolio.ID, action.NewSymbol); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM positions WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, portfolio.ID, action.Symbol)
	} else if newErr == sql.ErrNoRows {
		_, err = tx.Exec(`UPDATE positions SET symbol=?,security_id=COALESCE(security_id,?),updated_at=CURRENT_TIMESTAMP WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, action.NewSymbol, nullableText(action.SecurityID), portfolio.ID, action.Symbol)
	} else {
		return newErr
	}
	if err != nil {
		return err
	}
	_, _ = tx.Exec(`UPDATE orders SET status='cancelled',rejection_code='corporate_action',rejection_detail='cancelled for symbol change',resolved_at=CURRENT_TIMESTAMP WHERE portfolio_id=? AND symbol=? AND status='working'`, portfolio.ID, action.Symbol)
	effective := dueDate(action)
	_, _ = tx.Exec(`UPDATE security_listings SET valid_to=?,active=0,updated_at=CURRENT_TIMESTAMP WHERE security_id=? AND symbol=? AND valid_to=''`, effective, action.SecurityID, action.Symbol)
	_, _ = tx.Exec(`INSERT INTO security_listings(security_id,provider_asset_id,venue,symbol,currency,valid_from,active,source)
		SELECT security_id,provider_asset_id,venue,?,currency,?,1,source FROM security_listings WHERE security_id=? AND symbol=? ORDER BY id DESC LIMIT 1
		ON CONFLICT(venue,symbol,valid_from,source) DO UPDATE SET security_id=excluded.security_id,active=1,updated_at=CURRENT_TIMESTAMP`, action.NewSymbol, effective, action.SecurityID, action.Symbol)
	_, _ = tx.Exec(`INSERT INTO watchlist(project_id,portfolio_id,symbol,security_id) SELECT project_id,portfolio_id,?,COALESCE(security_id,?) FROM watchlist WHERE portfolio_id=? AND symbol=? ON CONFLICT(portfolio_id,symbol) DO NOTHING`, action.NewSymbol, nullableText(action.SecurityID), portfolio.ID, action.Symbol)
	_, _ = tx.Exec(`DELETE FROM watchlist WHERE portfolio_id=? AND symbol=?`, portfolio.ID, action.Symbol)
	if err = dbInsertJournalTx(tx, portfolio.ProjectID, portfolio.ID, "corporate_action", fmt.Sprintf("Symbol changed from %s to %s", action.Symbol, action.NewSymbol), map[string]any{"provider_event_id": action.ProviderEventID, "revision": action.Revision}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	emitCorporateActionApplied(portfolio, action, "symbol_change", 0, 0)
	return nil
}

func applyWorthlessRemoval(db *sql.DB, portfolio *Portfolio, action *CorporateAction) error {
	var qty, avg float64
	err := db.QueryRow(`SELECT qty,avg_cost FROM positions WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, portfolio.ID, action.Symbol).Scan(&qty, &avg)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inserted, err := insertPostingTx(tx, portfolio, action, "worthless_removal", action.Symbol, "", -qty, 0, -qty*avg, map[string]any{"quantity_removed": qty, "cost_basis_removed": qty * avg})
	if err != nil {
		return err
	}
	if !inserted {
		return nil
	}
	if _, err = tx.Exec(`DELETE FROM positions WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, portfolio.ID, action.Symbol); err != nil {
		return err
	}
	_, _ = tx.Exec(`UPDATE orders SET status='cancelled',rejection_code='corporate_action',rejection_detail='security removed',resolved_at=CURRENT_TIMESTAMP WHERE portfolio_id=? AND symbol=? AND status='working'`, portfolio.ID, action.Symbol)
	if err = dbInsertJournalTx(tx, portfolio.ProjectID, portfolio.ID, "corporate_action", fmt.Sprintf("Removed worthless security %s", action.Symbol), map[string]any{"provider_event_id": action.ProviderEventID, "revision": action.Revision, "cost_basis_removed": qty * avg}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	emitCorporateActionApplied(portfolio, action, "worthless_removal", 0, -qty)
	return nil
}

func insertPostingTx(tx *sql.Tx, portfolio *Portfolio, action *CorporateAction, effect, symbol, related string, qty, cash, basis float64, details map[string]any) (bool, error) {
	raw, _ := json.Marshal(details)
	result, err := tx.Exec(`INSERT INTO corporate_action_postings(project_id,portfolio_id,provider,provider_event_id,provider_revision,effect_type,security_id,related_security_id,symbol,related_symbol,quantity_delta,cash_delta,cost_basis_delta,status,details_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'applied',?) ON CONFLICT(portfolio_id,provider,provider_event_id,provider_revision,effect_type,symbol,related_symbol) DO NOTHING`, portfolio.ProjectID, portfolio.ID, action.Provider, action.ProviderEventID, action.Revision, effect, nullableText(action.SecurityID), nullableText(action.RelatedSecurityID), symbol, related, qty, cash, basis, string(raw))
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

func emitCorporateActionApplied(portfolio *Portfolio, action *CorporateAction, effect string, cash, qty float64) {
	emit("corporate_action.applied", map[string]any{"schema_version": 1, "portfolio_id": portfolio.ID, "provider": action.Provider, "provider_event_id": action.ProviderEventID, "revision": action.Revision, "action_type": action.ActionType, "effect_type": effect, "symbol": action.Symbol, "new_symbol": action.NewSymbol, "cash_delta": cash, "quantity_delta": qty})
	emit("position.changed", map[string]any{"schema_version": 1, "portfolio_id": portfolio.ID, "symbol": action.Symbol, "source": "corporate_action"})
}

func dbListCorporateActionPostings(db *sql.DB, portfolioID int64, limit int) ([]*CorporateActionPosting, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := db.Query(`SELECT id,project_id,portfolio_id,provider,provider_event_id,provider_revision,effect_type,COALESCE(security_id,''),COALESCE(related_security_id,''),symbol,related_symbol,quantity_delta,cash_delta,cost_basis_delta,status,applied_at FROM corporate_action_postings WHERE portfolio_id=? ORDER BY applied_at DESC,id DESC LIMIT ?`, portfolioID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CorporateActionPosting{}
	for rows.Next() {
		p := &CorporateActionPosting{}
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.PortfolioID, &p.Provider, &p.ProviderEventID, &p.ProviderRevision, &p.EffectType, &p.SecurityID, &p.RelatedSecurityID, &p.Symbol, &p.RelatedSymbol, &p.QuantityDelta, &p.CashDelta, &p.CostBasisDelta, &p.Status, &p.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func referenceDataStatus(db *sql.DB) map[string]any {
	out := map[string]any{"provider": referenceProviderAlpaca, "survivorship": "forward_snapshot_only"}
	counts := map[string]string{
		"securities":        `SELECT COUNT(*) FROM securities`,
		"listings":          `SELECT COUNT(*) FROM security_listings`,
		"corporate_actions": `SELECT COUNT(*) FROM (SELECT provider,provider_event_id FROM corporate_actions GROUP BY provider,provider_event_id)`,
		"sessions":          `SELECT COUNT(*) FROM exchange_sessions`,
		"open_issues":       `SELECT COUNT(*) FROM reference_data_issues WHERE status='open'`,
	}
	for key, query := range counts {
		var count int
		if err := db.QueryRow(query).Scan(&count); err == nil {
			out[key] = count
		}
	}
	checkpoints := []map[string]any{}
	rows, err := db.Query(`SELECT provider,stream,cursor,watermark,COALESCE(last_ok_at,''),COALESCE(last_error_at,''),last_error,rows_ingested,updated_at FROM reference_data_checkpoints ORDER BY provider,stream`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var provider, stream, cursor, watermark, lastOK, lastErrorAt, lastError, updated string
			var ingested int
			if rows.Scan(&provider, &stream, &cursor, &watermark, &lastOK, &lastErrorAt, &lastError, &ingested, &updated) == nil {
				checkpoints = append(checkpoints, map[string]any{"provider": provider, "stream": stream, "cursor": cursor, "watermark": watermark, "last_ok_at": lastOK, "last_error_at": lastErrorAt, "last_error": lastError, "rows_ingested": ingested, "updated_at": updated})
			}
		}
	}
	out["checkpoints"] = checkpoints
	var coverage sql.NullString
	_ = db.QueryRow(`SELECT MIN(valid_from) FROM universe_memberships WHERE universe_id=?`, referenceUniverseAlpaca).Scan(&coverage)
	out["universe"] = map[string]any{"id": referenceUniverseAlpaca, "coverage_start": coverage.String, "historical_verified": false}
	return out
}

func referenceManifest(db *sql.DB, symbols []string, start, end, adjustment string) map[string]any {
	manifest := map[string]any{"captured_at": time.Now().UTC().Format(time.RFC3339Nano), "adjustment_mode": adjustment, "symbols": symbols, "range_start": start, "range_end": end}
	switch adjustment {
	case "raw":
		manifest["economic_continuity"] = false
		manifest["corporate_action_treatment"] = "unadjusted prices; events retained for audit but not injected into replay holdings"
		manifest["warning"] = "raw prices may contain corporate-action discontinuities"
	case "split_adjusted", "price_return":
		manifest["economic_continuity"] = true
		manifest["corporate_action_treatment"] = "provider split-adjusted prices; cash distributions excluded from return"
	case "total_return", "provider_adjusted":
		manifest["economic_continuity"] = true
		manifest["corporate_action_treatment"] = "provider-adjusted prices including supported splits and distributions"
	default:
		manifest["economic_continuity"] = false
		manifest["corporate_action_treatment"] = "unknown"
	}
	var actions, sessions int
	_ = db.QueryRow(`SELECT COUNT(*) FROM corporate_actions WHERE COALESCE(NULLIF(effective_date,''),NULLIF(ex_date,''),process_date) BETWEEN ? AND ?`, start, end).Scan(&actions)
	_ = db.QueryRow(`SELECT COUNT(*) FROM exchange_sessions WHERE session_date BETWEEN ? AND ?`, start, end).Scan(&sessions)
	manifest["corporate_action_records"] = actions
	manifest["session_records"] = sessions
	status := referenceDataStatus(db)
	manifest["checkpoints"] = status["checkpoints"]
	coverageStart := ""
	if universe, ok := status["universe"].(map[string]any); ok {
		coverageStart, _ = universe["coverage_start"].(string)
	}
	manifest["survivorship_safe"] = coverageStart != "" && start >= coverageStart
	manifest["universe_id"] = referenceUniverseAlpaca
	raw, _ := json.Marshal(manifest)
	manifest["manifest_sha256"] = fmt.Sprintf("%x", sha256.Sum256(raw))
	return manifest
}
