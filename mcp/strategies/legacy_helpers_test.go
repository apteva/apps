package main

// Test-only copies of Trading execution helpers. Strategies no longer executes
// or attributes fills itself, but its legacy fixtures replay old Trading
// behaviour (exec_legacy_test.go, store_legacy_test.go, trading_fixture_test.go).

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func readResearchRows(db researchQueryer, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, key := range columns {
			value := values[i]
			if b, ok := value.([]byte); ok {
				value = string(b)
			}
			row[key] = value
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// dbGetPortfolioAnyProject is for the engine (which doesn't carry a
// project context) — every other read uses the project-scoped variant.
func dbGetPortfolioAnyProject(db *sql.DB, id int64) (*Portfolio, error) {
	row := db.QueryRow(`
		SELECT id, project_id, name, COALESCE(agent_id, ''), mandate, allowed_classes,
		       starting_cash, cash, available_cash, status, mode, COALESCE(broker_slug, ''),
		       execution_environment, live_armed, created_at, updated_at
		FROM portfolios WHERE id = ?`, id)
	return scanPortfolio(row)
}

// dbAccrueStrategyFillTx folds one fill into the strategy's own lot book.
// A buy extends the lot at weighted-average cost; a sell realizes against
// that average. Costs accrue whichever way the fill goes.
func dbAccrueStrategyFillTx(
	tx *sql.Tx, portfolioID, strategyID int64,
	symbol, outcome, side string,
	qty, price, fee, spreadCost, slippageCost float64,
) error {
	if qty <= 0 {
		return nil
	}
	var openQty, avgCost float64
	row := tx.QueryRow(`
		SELECT open_qty, avg_cost FROM strategy_position_accounting
		WHERE portfolio_id = ? AND strategy_id = ? AND symbol = ? AND outcome = ?`,
		portfolioID, strategyID, symbol, outcome)
	if err := row.Scan(&openQty, &avgCost); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	realized := 0.0
	if isBuySide(side) {
		newQty := openQty + qty
		if newQty > 0 {
			avgCost = (openQty*avgCost + qty*price) / newQty
		}
		openQty = newQty
	} else {

		closedQty := qty
		if closedQty > openQty {
			closedQty = openQty
		}
		if closedQty > 0 {
			realized = (price - avgCost) * closedQty
			openQty -= closedQty
		}
		if openQty < strategyLotEpsilon {
			openQty, avgCost = 0, 0
		}
	}

	_, err := tx.Exec(`
		INSERT INTO strategy_position_accounting
			(portfolio_id, strategy_id, symbol, outcome, open_qty, avg_cost,
			 gross_realized_pnl, fees_paid, spread_cost, slippage_cost,
			 notional_traded, fill_count, first_fill_at, last_fill_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(portfolio_id, strategy_id, symbol, outcome) DO UPDATE SET
			open_qty = excluded.open_qty,
			avg_cost = excluded.avg_cost,
			gross_realized_pnl = strategy_position_accounting.gross_realized_pnl + excluded.gross_realized_pnl,
			fees_paid = strategy_position_accounting.fees_paid + excluded.fees_paid,
			spread_cost = strategy_position_accounting.spread_cost + excluded.spread_cost,
			slippage_cost = strategy_position_accounting.slippage_cost + excluded.slippage_cost,
			notional_traded = strategy_position_accounting.notional_traded + excluded.notional_traded,
			fill_count = strategy_position_accounting.fill_count + 1,
			last_fill_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP`,
		portfolioID, strategyID, symbol, outcome, openQty, avgCost,
		realized, fee, spreadCost, slippageCost, qty*price)
	return err
}

// accrueStrategyExecutionCost books a fill's execution cost against the
// originating strategy, alongside the portfolio-level accrual. The outcome key
// matches dbApplyFill so the cost lands on the same row as the fill it came
// from.
//
// Pass exactly the costs written to the fills row: dbRebuildStrategyAttribution
// replays that ledger, so anything recorded here and not there (or the reverse)
// would make a restart silently restate the numbers.
func accrueStrategyExecutionCost(
	tx *sql.Tx, portfolioID int64, o *Order, fee, spreadCost, slippageCost float64,
) error {
	if fee == 0 && spreadCost == 0 && slippageCost == 0 {
		return nil
	}
	strategyID, err := dbOrderStrategyID(tx, o.ID)
	if err != nil {
		return err
	}
	outcome := ""
	if o.AssetClass == "polymarket" {
		outcome = polyOutcome(o)
	}
	return dbAccrueStrategyCostTx(tx, portfolioID, strategyID, o.Symbol, outcome, fee, spreadCost, slippageCost)
}

// dbOrderStrategyID resolves the strategy that originated an order. Orders
// placed by hand or by an agent carry 0 and land in the unattributed bucket.
func dbOrderStrategyID(tx *sql.Tx, orderID string) (int64, error) {
	var strategyID int64
	err := tx.QueryRow(`SELECT COALESCE(strategy_id, 0) FROM orders WHERE id = ?`, orderID).Scan(&strategyID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return strategyID, err
}

type researchQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

// dbAccrueStrategyCostTx books execution cost that arrives separately from the
// fill itself, mirroring how dbAccruePositionAccountingTx is called for fees.
func dbAccrueStrategyCostTx(
	tx *sql.Tx, portfolioID, strategyID int64, symbol, outcome string,
	fee, spreadCost, slippageCost float64,
) error {
	if fee == 0 && spreadCost == 0 && slippageCost == 0 {
		return nil
	}
	_, err := tx.Exec(`
		INSERT INTO strategy_position_accounting
			(portfolio_id, strategy_id, symbol, outcome, fees_paid, spread_cost, slippage_cost)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(portfolio_id, strategy_id, symbol, outcome) DO UPDATE SET
			fees_paid = strategy_position_accounting.fees_paid + excluded.fees_paid,
			spread_cost = strategy_position_accounting.spread_cost + excluded.spread_cost,
			slippage_cost = strategy_position_accounting.slippage_cost + excluded.slippage_cost,
			updated_at = CURRENT_TIMESTAMP`,
		portfolioID, strategyID, symbol, outcome, fee, spreadCost, slippageCost)
	return err
}

const priceTolerance = 1e-9

func (e *engine) bumpFillCounter() {
	e.mu.Lock()
	e.fillsThisRun++
	e.fillsPending++
	e.mu.Unlock()
}

// polyOutcome — small helper so dbGetPosition can find a polymarket
// position via its YES/NO leg. Empty string for non-poly orders.
func polyOutcome(o *Order) string {
	if o.AssetClass == "polymarket" {
		if outcome := strings.ToUpper(strings.TrimSpace(o.Outcome)); outcome != "" {
			return outcome
		}
		if o.Side == "yes" || o.Side == "no" {
			return strings.ToUpper(o.Side)
		}
	}
	return ""
}

func dbGetPortfolioUniversePolicy(db *sql.DB, portfolio *Portfolio) (*PortfolioUniversePolicy, error) {
	p := defaultUniversePolicy(portfolio)
	var includes, excludes string
	var requireActive, enabled int
	err := db.QueryRow(`SELECT selection_mode,include_symbols,exclude_symbols,reference_universe_id,require_active_listing,enforcement_enabled,updated_at
		FROM portfolio_universe_policies WHERE portfolio_id=? AND project_id=?`, portfolio.ID, portfolio.ProjectID).
		Scan(&p.SelectionMode, &includes, &excludes, &p.ReferenceUniverseID, &requireActive, &enabled, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(includes), &p.IncludeSymbols)
	_ = json.Unmarshal([]byte(excludes), &p.ExcludeSymbols)
	p.RequireActiveListing, p.EnforcementEnabled = requireActive != 0, enabled != 0
	return p, nil
}

func portfolioUniverseViolation(db *sql.DB, portfolio *Portfolio, symbol string) (*UniverseViolation, error) {
	policy, err := dbGetPortfolioUniversePolicy(db, portfolio)
	if err != nil || !policy.EnforcementEnabled {
		return nil, err
	}
	symbol = canonicalSymbol(symbol)
	if contains(policy.ExcludeSymbols, symbol) {
		return &UniverseViolation{Code: "universe_symbol_excluded", Detail: fmt.Sprintf("%s is explicitly excluded by the portfolio universe policy", symbol)}, nil
	}
	included := contains(policy.IncludeSymbols, symbol)
	switch policy.SelectionMode {
	case "symbol_allowlist":
		if !included {
			return &UniverseViolation{Code: "universe_not_allowed", Detail: fmt.Sprintf("%s is not in the portfolio symbol allowlist", symbol)}, nil
		}
	case "reference_universe":
		member, err := referenceUniverseContainsSymbol(db, policy.ReferenceUniverseID, symbol, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if !member && !included {
			return &UniverseViolation{Code: "universe_not_member", Detail: fmt.Sprintf("%s is not an active member of reference universe %s", symbol, policy.ReferenceUniverseID)}, nil
		}
	}
	if policy.RequireActiveListing {
		active, err := activeListingExists(db, symbol, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if !active {
			return &UniverseViolation{Code: "universe_inactive_listing", Detail: fmt.Sprintf("%s has no active security-master listing", symbol)}, nil
		}
	}
	return nil, nil
}

func workingPortfolioOrders(db *sql.DB, id int64) ([]*Order, error) {

	return dbListOrdersInternal(db, id, "working", -1)
}

func defaultInstrument(symbol, assetClass, source string, now time.Time) *Instrument {
	symbol = canonicalSymbol(symbol)
	assetClass = strings.ToLower(strings.TrimSpace(assetClass))
	i := &Instrument{
		Symbol:         symbol,
		ProviderSymbol: symbol,
		AssetClass:     assetClass,
		Active:         true,
		Source:         source,
		UpdatedAt:      now.UTC().Format(time.RFC3339),
	}
	switch assetClass {
	case "crypto":
		i.Exchange = "BINANCE"
		i.ExchangeTimezone = "UTC"
		i.Calendar = calendarAlwaysOpen
		i.QuoteCurrency = "USD"
		i.BaseCurrency = strings.TrimSuffix(symbol, "-USD")
		i.VolumeUnit = "quote_currency"
	case "polymarket":
		i.Exchange = "POLYMARKET"
		i.ExchangeTimezone = "UTC"
		i.Calendar = calendarAlwaysOpen
		i.QuoteCurrency = "USD"
		i.VolumeUnit = "quote_currency"
	case "equity", "etf":
		i.Exchange = "US"
		i.ExchangeTimezone = "America/New_York"
		i.Calendar = calendarUSEquity
		i.QuoteCurrency = "USD"
		i.VolumeUnit = "shares"
	default:
		i.Exchange = "UNKNOWN"
		i.ExchangeTimezone = "UTC"
		i.Calendar = calendarAlwaysOpen
		i.VolumeUnit = "units"
	}
	return i
}

func ptr(v float64) *float64 { return &v }

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

func decodeResearchValue(value any, out any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func dbGetPortfolioRiskPolicy(db *sql.DB, portfolio *Portfolio) (*PortfolioRiskPolicy, error) {
	if portfolio == nil {
		return nil, errors.New("portfolio required")
	}
	p := legacyRiskPolicy(portfolio)
	p.PortfolioID, p.ProjectID = portfolio.ID, portfolio.ProjectID
	err := db.QueryRow(`SELECT risk_level,max_daily_loss_pct,max_drawdown_pct,max_position_pct,max_gross_exposure_pct,max_order_pct,updated_at
		FROM portfolio_risk_policies WHERE portfolio_id=? AND project_id=?`, portfolio.ID, portfolio.ProjectID).
		Scan(&p.RiskLevel, &p.MaxDailyLossPct, &p.MaxDrawdownPct, &p.MaxPositionPct, &p.MaxGrossExposurePct, &p.MaxOrderPct, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return p, nil
	}
	return p, err
}

func dbPortfolioConfig(db *sql.DB, id int64) (map[string]any, error) {
	var raw string
	if err := db.QueryRow(`SELECT config_json FROM portfolios WHERE id = ?`, id).Scan(&raw); err != nil {
		return nil, err
	}
	cfg := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	return cfg, nil
}

func dbPortfolioExecutionSettings(db *sql.DB, id int64) PortfolioExecutionSettings {
	cfg, err := dbPortfolioConfig(db, id)
	if err != nil {
		return PortfolioExecutionSettings{FeeBps: defaultFeeBps, SlippageBps: defaultSlippageBps}
	}
	fee := anyFloat(cfg["fee_bps"])
	if fee < 0 {
		fee = 0
	}
	slippage := anyFloat(cfg["slippage_bps"])
	if slippage < 0 {
		slippage = defaultSlippageBps
	}
	if _, ok := cfg["slippage_bps"]; !ok {
		slippage = defaultSlippageBps
	}
	return PortfolioExecutionSettings{FeeBps: fee, SlippageBps: slippage}
}

func dbGetPortfolio(db *sql.DB, projectID string, id int64) (*Portfolio, error) {
	row := db.QueryRow(`
		SELECT id, project_id, name, COALESCE(agent_id, ''), mandate, allowed_classes,
		       starting_cash, cash, available_cash, status, mode, COALESCE(broker_slug, ''),
		       execution_environment, live_armed, created_at, updated_at
		FROM portfolios WHERE id = ? AND project_id = ?`, id, projectID)
	return scanPortfolio(row)
}

func dbListPortfolios(db *sql.DB, projectID string) ([]*Portfolio, error) {
	rows, err := db.Query(`
		SELECT id, project_id, name, COALESCE(agent_id, ''), mandate, allowed_classes,
		       starting_cash, cash, available_cash, status, mode, COALESCE(broker_slug, ''),
		       execution_environment, live_armed, created_at, updated_at
		FROM portfolios WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Portfolio
	for rows.Next() {
		p, err := scanPortfolioRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPortfolio(row *sql.Row) (*Portfolio, error) {
	var p Portfolio
	var classesJSON string
	var availableCash sql.NullFloat64
	if err := row.Scan(&p.ID, &p.ProjectID, &p.Name, &p.AgentID, &p.Mandate,
		&classesJSON, &p.StartingCash, &p.Cash, &availableCash, &p.Status, &p.Mode, &p.BrokerSlug,
		&p.ExecutionEnvironment, &p.LiveArmed, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(classesJSON), &p.AllowedClasses); err != nil {
		p.AllowedClasses = []string{"equity", "etf"}
	}
	if availableCash.Valid {
		p.AvailableCash = ptr(availableCash.Float64)
	}
	return &p, nil
}

func dbListPositions(db *sql.DB, portfolioID int64) ([]*Position, error) {
	rows, err := db.Query(`
		SELECT p.symbol, COALESCE(p.security_id,''), p.asset_class, COALESCE(p.outcome, ''), p.qty, p.avg_cost,
		       COALESCE(a.gross_realized_pnl - a.fees_paid, p.realized_pnl)
		FROM positions p
		LEFT JOIN position_accounting a
		  ON a.portfolio_id = p.portfolio_id
		 AND a.symbol = p.symbol
		 AND a.outcome = COALESCE(p.outcome, '')
		WHERE p.portfolio_id = ? AND p.qty != 0
		ORDER BY ABS(p.qty * p.avg_cost) DESC`, portfolioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.Symbol, &p.SecurityID, &p.AssetClass, &p.Outcome,
			&p.Qty, &p.AvgCost, &p.RealizedPnL); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

func dbGetOrder(db *sql.DB, projectID, id string) (*Order, error) {
	row := db.QueryRow(`
		SELECT id, portfolio_id, symbol, COALESCE(security_id,''), asset_class, side, COALESCE(outcome, ''), type, qty, filled_qty, avg_fill_price,
		       limit_price, stop_price, tif, status, rationale, source,
		       COALESCE(rejection_code, ''), COALESCE(rejection_detail, ''),
		       placed_at, COALESCE(resolved_at, ''), liquidity_role
		FROM orders WHERE id = ? AND project_id = ?`, id, projectID)
	return scanOrder(row)
}

func dbListOrders(db *sql.DB, portfolioID int64, status string, limit int) ([]*Order, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return dbListOrdersInternal(db, portfolioID, status, limit)
}

func dbListOrdersInternal(db *sql.DB, portfolioID int64, status string, limit int) ([]*Order, error) {
	q := `SELECT id, portfolio_id, symbol, COALESCE(security_id,''), asset_class, side, COALESCE(outcome, ''), type, qty, filled_qty, avg_fill_price,
	             limit_price, stop_price, tif, status, rationale, source,
	             COALESCE(rejection_code, ''), COALESCE(rejection_detail, ''),
	             placed_at, COALESCE(resolved_at, ''), liquidity_role
	      FROM orders WHERE portfolio_id = ?`
	args := []any{portfolioID}
	if status != "" && status != "all" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY placed_at DESC LIMIT ?`
	if limit != -1 && (limit <= 0 || limit > 200) {
		limit = 50
	}
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		o, err := scanOrderRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func dbUpsertMark(db *sql.DB, m *Mark) error {
	return dbUpsertMarkExec(db, m)
}

func dbGetMark(db *sql.DB, symbol string) (*Mark, error) {
	row := db.QueryRow(`
		SELECT symbol, asset_class, price, no_price, prev_close, volume_24h, marked_at,
		       source, timestamp_kind, volume_unit, COALESCE(received_at, ''),
		       bid_price, ask_price, bid_size, ask_size, last_trade_price, last_trade_size,
		       feed, COALESCE(quote_at, '')
		FROM marks WHERE symbol = ?`, symbol)
	var m Mark
	var no, pc, vol, bid, ask, bidSize, askSize, lastTrade, lastSize sql.NullFloat64
	if err := row.Scan(&m.Symbol, &m.AssetClass, &m.Price, &no, &pc, &vol, &m.MarkedAt,
		&m.Source, &m.TimestampKind, &m.VolumeUnit, &m.ReceivedAt,
		&bid, &ask, &bidSize, &askSize, &lastTrade, &lastSize, &m.Feed, &m.QuoteAt); err != nil {
		return nil, err
	}
	if no.Valid {
		v := no.Float64
		m.NoPrice = &v
	}
	if pc.Valid {
		v := pc.Float64
		m.PrevClose = &v
	}
	if vol.Valid {
		v := vol.Float64
		m.Volume24h = &v
	}
	if bid.Valid {
		m.BidPrice = ptr(bid.Float64)
	}
	if ask.Valid {
		m.AskPrice = ptr(ask.Float64)
	}
	if bidSize.Valid {
		m.BidSize = ptr(bidSize.Float64)
	}
	if askSize.Valid {
		m.AskSize = ptr(askSize.Float64)
	}
	if lastTrade.Valid {
		m.LastTradePrice = ptr(lastTrade.Float64)
	}
	if lastSize.Valid {
		m.LastTradeSize = ptr(lastSize.Float64)
	}
	m.Instrument, _ = dbGetInstrument(db, symbol)
	return &m, nil
}

func dbGetDayBaseline(db *sql.DB, portfolioID int64, utcDay string) (float64, bool, error) {
	row := db.QueryRow(`SELECT equity FROM day_baselines WHERE portfolio_id = ? AND utc_day = ?`,
		portfolioID, utcDay)
	var v float64
	if err := row.Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return v, true, nil
}

func nullable(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func utcDay(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// markPriceForSide picks YES vs NO for polymarket positions; passes
// through for everything else.
func markPriceForSide(m *Mark, outcome string) float64 {
	if m.AssetClass == "polymarket" && outcome == "NO" && m.NoPrice != nil {
		return *m.NoPrice
	}
	return m.Price
}

// computeEquity is snapshotPortfolio's lightweight cousin — just the
// number, no per-position fluff. Used by the engine on every tick.
func computeEquity(db *sql.DB, p *Portfolio) (float64, error) {
	var equity float64
	err := db.QueryRow(`SELECT p.cash+COALESCE(SUM(q.qty*CASE WHEN q.outcome='NO' AND m.no_price IS NOT NULL THEN m.no_price ELSE COALESCE(NULLIF(m.price,0),q.avg_cost) END),0)
	 FROM portfolios p LEFT JOIN positions q ON q.portfolio_id=p.id LEFT JOIN marks m ON m.symbol=q.symbol WHERE p.id=? GROUP BY p.id`, p.ID).Scan(&equity)
	return equity, err
}

func resetTradingBackoff() {
	tradingBackoff.Lock()
	defer tradingBackoff.Unlock()
	tradingBackoff.next, tradingBackoff.delay = map[int64]time.Time{}, map[int64]time.Duration{}
}

func resolveVenueProfile(db *sql.DB, pf *Portfolio, symbol, class string) VenueExecutionProfile {
	venue := executionVenue(pf)
	p := defaultVenueProfile(venue, class)
	if stored, err := dbVenueProfile(db, venue, class, symbol); err == nil {
		p = *stored
	}
	if instrument, err := dbGetInstrument(db, symbol); err == nil && instrumentRulesMatchVenue(instrument, venue) {
		if instrument.Calendar != "" {
			p.Calendar = instrument.Calendar
		}
		if instrument.TickSize > 0 {
			p.PriceTick = instrument.TickSize
		}
		if instrument.LotSize > 0 {
			p.QtyStep = instrument.LotSize
		}
		if instrument.MinQty > 0 {
			p.MinQty = instrument.MinQty
		}
		if instrument.MinNotional > 0 {
			p.MinNotional = instrument.MinNotional
		}
		if instrument.QuoteCurrency != "" {
			p.FeeCurrency = instrument.QuoteCurrency
		}
	}

	settings := dbPortfolioExecutionSettings(db, pf.ID)
	if cfg, err := dbPortfolioConfig(db, pf.ID); err == nil {
		if _, ok := cfg["fee_bps"]; ok {
			p.MakerFeeBps, p.TakerFeeBps = settings.FeeBps, settings.FeeBps
		}
		if _, ok := cfg["slippage_bps"]; ok {
			p.SlippageBps = settings.SlippageBps
		}
	}
	p.Runtime = ptrVenueRuntime(venueRuntimeSnapshot(venue))
	return p
}

const defaultFeeBps = 0.0

func defaultUniversePolicy(portfolio *Portfolio) *PortfolioUniversePolicy {
	p := &PortfolioUniversePolicy{SelectionMode: "all_allowed_classes", IncludeSymbols: []string{}, ExcludeSymbols: []string{}, EnforcementEnabled: true}
	if portfolio != nil {
		p.PortfolioID, p.ProjectID = portfolio.ID, portfolio.ProjectID
	}
	return p
}

func activeListingExists(db *sql.DB, symbol string, asOf time.Time) (bool, error) {
	var one int
	day := asOf.Format("2006-01-02")
	err := db.QueryRow(`SELECT 1 FROM security_listings WHERE symbol=? AND active=1
		AND (valid_from='' OR valid_from<=?) AND (valid_to='' OR valid_to>?) LIMIT 1`, canonicalSymbol(symbol), day, day).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func referenceUniverseContainsSymbol(db *sql.DB, universeID, symbol string, asOf time.Time) (bool, error) {
	var one int
	day := asOf.Format("2006-01-02")
	err := db.QueryRow(`SELECT 1 FROM universe_memberships u JOIN security_listings l ON l.security_id=u.security_id
		WHERE u.universe_id=? AND l.symbol=? AND l.active=1
		AND u.valid_from<=? AND (u.valid_to='' OR u.valid_to>?)
		AND (l.valid_from='' OR l.valid_from<=?) AND (l.valid_to='' OR l.valid_to>?) LIMIT 1`,
		universeID, canonicalSymbol(symbol), day, day, day, day).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

const referenceProviderAlpaca = "alpaca"

func legacyRiskPolicy(portfolio *Portfolio) *PortfolioRiskPolicy {
	daily := configuredDailyLossHaltPct()
	return &PortfolioRiskPolicy{RiskLevel: "custom", MaxDailyLossPct: daily, MaxDrawdownPct: 100, MaxPositionPct: 100, MaxGrossExposurePct: 100, MaxOrderPct: 100}
}

func scanPortfolioRows(rows *sql.Rows) (*Portfolio, error) {
	var p Portfolio
	var classesJSON string
	var availableCash sql.NullFloat64
	if err := rows.Scan(&p.ID, &p.ProjectID, &p.Name, &p.AgentID, &p.Mandate,
		&classesJSON, &p.StartingCash, &p.Cash, &availableCash, &p.Status, &p.Mode, &p.BrokerSlug,
		&p.ExecutionEnvironment, &p.LiveArmed, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(classesJSON), &p.AllowedClasses); err != nil {
		p.AllowedClasses = []string{"equity", "etf"}
	}
	if availableCash.Valid {
		p.AvailableCash = ptr(availableCash.Float64)
	}
	return &p, nil
}

func scanOrder(row *sql.Row) (*Order, error) {
	var o Order
	var lp, sp sql.NullFloat64
	var resolvedAt string
	if err := row.Scan(&o.ID, &o.PortfolioID, &o.Symbol, &o.SecurityID, &o.AssetClass, &o.Side, &o.Outcome, &o.Type,
		&o.Qty, &o.FilledQty, &o.AvgFillPrice, &lp, &sp, &o.TIF, &o.Status,
		&o.Rationale, &o.Source, &o.RejectionCode, &o.RejectionDetail,
		&o.PlacedAt, &resolvedAt, &o.LiquidityRole); err != nil {
		return nil, err
	}
	if lp.Valid {
		v := lp.Float64
		o.LimitPrice = &v
	}
	if sp.Valid {
		v := sp.Float64
		o.StopPrice = &v
	}
	if resolvedAt != "" {
		o.ResolvedAt = resolvedAt
	}
	return &o, nil
}

func scanOrderRows(rows *sql.Rows) (*Order, error) {
	var o Order
	var lp, sp sql.NullFloat64
	var resolvedAt string
	if err := rows.Scan(&o.ID, &o.PortfolioID, &o.Symbol, &o.SecurityID, &o.AssetClass, &o.Side, &o.Outcome, &o.Type,
		&o.Qty, &o.FilledQty, &o.AvgFillPrice, &lp, &sp, &o.TIF, &o.Status,
		&o.Rationale, &o.Source, &o.RejectionCode, &o.RejectionDetail,
		&o.PlacedAt, &resolvedAt, &o.LiquidityRole); err != nil {
		return nil, err
	}
	if lp.Valid {
		v := lp.Float64
		o.LimitPrice = &v
	}
	if sp.Valid {
		v := sp.Float64
		o.StopPrice = &v
	}
	if resolvedAt != "" {
		o.ResolvedAt = resolvedAt
	}
	return &o, nil
}

func dbUpsertMarkExec(exec sqlExecer, m *Mark) error {
	source := "unknown"
	if m != nil && strings.TrimSpace(m.Source) != "" {
		source = m.Source
	}
	normalized, err := normalizeMark(source, m, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := dbInsertQuoteInstrument(exec, normalized.Instrument); err != nil {
		return err
	}
	_, err = exec.Exec(`
		INSERT INTO marks (symbol, security_id, asset_class, price, no_price, prev_close, volume_24h, marked_at, source, timestamp_kind, volume_unit, received_at,
			bid_price, ask_price, bid_size, ask_size, last_trade_price, last_trade_size, feed, quote_at)
		VALUES (?, (SELECT security_id FROM security_listings WHERE symbol=? ORDER BY active DESC,updated_at DESC LIMIT 1), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(symbol) DO UPDATE SET
			security_id = COALESCE(excluded.security_id, marks.security_id),
			asset_class = excluded.asset_class,
			price       = excluded.price,
			no_price    = excluded.no_price,
			prev_close  = excluded.prev_close,
			volume_24h  = excluded.volume_24h,
			marked_at   = excluded.marked_at,
			source      = excluded.source,
			timestamp_kind = excluded.timestamp_kind,
			volume_unit = excluded.volume_unit,
			received_at = excluded.received_at,
			bid_price = excluded.bid_price,
			ask_price = excluded.ask_price,
			bid_size = excluded.bid_size,
			ask_size = excluded.ask_size,
			last_trade_price = excluded.last_trade_price,
			last_trade_size = excluded.last_trade_size,
			feed = CASE WHEN excluded.feed != '' THEN excluded.feed ELSE marks.feed END,
			quote_at = excluded.quote_at
		WHERE julianday(excluded.marked_at) >= julianday(marks.marked_at)`,
		normalized.Symbol, normalized.Symbol, normalized.AssetClass, normalized.Price, nullable(normalized.NoPrice), nullable(normalized.PrevClose),
		nullable(normalized.Volume24h), normalized.MarkedAt, normalized.Source, normalized.TimestampKind,
		normalized.VolumeUnit, normalized.ReceivedAt, nullable(normalized.BidPrice), nullable(normalized.AskPrice),
		nullable(normalized.BidSize), nullable(normalized.AskSize), nullable(normalized.LastTradePrice),
		nullable(normalized.LastTradeSize), normalized.Feed, nullableString(normalized.QuoteAt))
	return err
}

func dbVenueProfile(db *sql.DB, venue, class, symbol string) (*VenueExecutionProfile, error) {
	venue, class, symbol = strings.ToLower(strings.TrimSpace(venue)), strings.ToLower(strings.TrimSpace(class)), canonicalSymbol(symbol)
	for _, candidate := range []string{symbol, "*"} {
		if candidate == "" {
			continue
		}
		p, err := scanVenueProfile(db.QueryRow(`SELECT `+venueProfileColumns+` FROM venue_execution_profiles
			WHERE venue_slug=? AND asset_class=? AND symbol=?`, venue, class, candidate))
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return nil, sql.ErrNoRows
}

func instrumentRulesMatchVenue(instrument *Instrument, venue string) bool {
	if instrument == nil {
		return false
	}
	venue = strings.ToLower(strings.TrimSpace(venue))
	if venue == "simulation" {
		return true
	}
	source, exchange := strings.ToLower(instrument.Source), strings.ToLower(instrument.Exchange)
	switch venue {
	case "binance-trading":
		return strings.Contains(source, "binance") || strings.Contains(exchange, "binance")
	case "alpaca-trading":
		return strings.Contains(source, "alpaca") || instrument.AssetClass == "equity" || instrument.AssetClass == "etf"
	case "polymarket-clob":
		return strings.Contains(source, "polymarket") || strings.Contains(exchange, "polymarket")
	default:
		return strings.Contains(source, venue) || strings.Contains(exchange, venue)
	}
}

func ptrVenueRuntime(v VenueRuntimeHealth) *VenueRuntimeHealth { return &v }

// configuredDailyLossHaltPct returns the install-wide compatibility default.
func configuredDailyLossHaltPct() float64 {
	if globalCtx == nil {
		return -defaultLossHalt
	}
	cfgRaw := globalCtx.Config().Get("daily_loss_halt_pct")
	if cfgRaw != "" {
		if v, err := strconv.ParseFloat(cfgRaw, 64); err == nil && v > 0 {
			return v
		}
	}
	return -defaultLossHalt
}

// normalizeMark is the single live-data quality gate. It gives timestamps
// explicit semantics: provider event time when supplied, otherwise receipt
// time. All persisted timestamps are RFC3339 UTC.
func normalizeMark(source string, mark *Mark, receivedAt time.Time) (*Mark, error) {
	if mark == nil {
		return nil, errors.New("nil market mark")
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	receivedAt = receivedAt.UTC()
	mark.Symbol = canonicalSymbol(mark.Symbol)
	if mark.Symbol == "" {
		return nil, errors.New("market mark symbol is required")
	}
	mark.AssetClass = strings.ToLower(strings.TrimSpace(mark.AssetClass))
	if mark.AssetClass == "" {
		mark.AssetClass = inferAssetClass(mark.Symbol)
	}
	if !finite(mark.Price) || mark.Price <= 0 {
		return nil, fmt.Errorf("invalid price for %s", mark.Symbol)
	}
	if mark.AssetClass == "polymarket" && mark.Price > 1 {
		return nil, fmt.Errorf("invalid YES probability for %s", mark.Symbol)
	}
	for label, value := range map[string]*float64{
		"no_price": mark.NoPrice, "prev_close": mark.PrevClose, "volume_24h": mark.Volume24h,
		"bid_price": mark.BidPrice, "ask_price": mark.AskPrice, "bid_size": mark.BidSize,
		"ask_size": mark.AskSize, "last_trade_price": mark.LastTradePrice, "last_trade_size": mark.LastTradeSize,
	} {
		if value == nil {
			continue
		}
		if !finite(*value) || *value < 0 {
			return nil, fmt.Errorf("invalid %s for %s", label, mark.Symbol)
		}
	}
	if mark.NoPrice != nil && *mark.NoPrice > 1 {
		return nil, fmt.Errorf("invalid NO probability for %s", mark.Symbol)
	}
	if mark.BidPrice != nil && mark.AskPrice != nil && *mark.BidPrice > *mark.AskPrice {
		return nil, fmt.Errorf("crossed quote for %s: bid %.8f > ask %.8f", mark.Symbol, *mark.BidPrice, *mark.AskPrice)
	}
	if strings.TrimSpace(mark.QuoteAt) != "" {
		quoteAt, err := time.Parse(time.RFC3339Nano, mark.QuoteAt)
		if err != nil {
			return nil, fmt.Errorf("invalid quote timestamp for %s: %w", mark.Symbol, err)
		}
		if quoteAt.After(receivedAt.Add(time.Minute)) {
			return nil, fmt.Errorf("quote timestamp for %s is more than one minute in the future", mark.Symbol)
		}
		mark.QuoteAt = quoteAt.UTC().Format(time.RFC3339Nano)
	}

	if mark.QuoteAt != "" {
		qt, _ := time.Parse(time.RFC3339Nano, mark.QuoteAt)
		if receivedAt.Sub(qt) > staleAfter {
			mark.BidPrice = nil
			mark.AskPrice = nil
			mark.BidSize = nil
			mark.AskSize = nil
		}
	}
	mark.Source = strings.TrimSpace(source)
	mark.ReceivedAt = receivedAt.Format(time.RFC3339Nano)
	if strings.TrimSpace(mark.MarkedAt) == "" {
		mark.MarkedAt = receivedAt.Format(time.RFC3339Nano)
		mark.TimestampKind = "received"
	} else {
		at, err := time.Parse(time.RFC3339Nano, mark.MarkedAt)
		if err != nil {
			return nil, fmt.Errorf("invalid market timestamp for %s: %w", mark.Symbol, err)
		}
		at = at.UTC()
		if at.After(receivedAt.Add(time.Minute)) {
			return nil, fmt.Errorf("market timestamp for %s is more than one minute in the future", mark.Symbol)
		}
		mark.MarkedAt = at.Format(time.RFC3339Nano)
		if strings.TrimSpace(mark.TimestampKind) == "" {
			mark.TimestampKind = "exchange"
		}
	}
	if mark.Volume24h != nil && strings.TrimSpace(mark.VolumeUnit) == "" {
		switch mark.AssetClass {
		case "equity", "etf":
			mark.VolumeUnit = "shares"
		default:
			mark.VolumeUnit = "quote_currency"
		}
	}
	instrument, err := normalizeInstrument(mark.Instrument, mark.Symbol, mark.AssetClass, source, receivedAt)
	if err != nil {
		return nil, err
	}
	if mark.VolumeUnit != "" {
		instrument.VolumeUnit = mark.VolumeUnit
	} else {
		mark.VolumeUnit = instrument.VolumeUnit
	}
	mark.Instrument = instrument
	return mark, nil
}

func dbInsertQuoteInstrument(exec sqlExecer, i *Instrument) error {
	return dbUpsertInstrumentExec(insertOnlyInstrument{exec}, i)
}

func scanVenueProfile(scanner interface{ Scan(...any) error }) (*VenueExecutionProfile, error) {
	var p VenueExecutionProfile
	var postOnly, reduceOnly int
	err := scanner.Scan(&p.VenueSlug, &p.AssetClass, &p.Symbol, &p.Status, &p.Calendar,
		&p.SessionPolicy, &p.MakerFeeBps, &p.TakerFeeBps, &p.FeeCurrency, &p.SpreadModel,
		&p.FallbackSpreadBps, &p.SlippageModel, &p.SlippageBps, &p.MinQty, &p.MinNotional,
		&p.QtyStep, &p.PriceTick, &p.FundingRateBps, &p.FundingIntervalHours,
		&postOnly, &reduceOnly, &p.Source, &p.UpdatedAt)
	p.SupportsPostOnly, p.SupportsReduceOnly = postOnly != 0, reduceOnly != 0
	return &p, err
}

const venueProfileColumns = `venue_slug,asset_class,symbol,status,calendar,session_policy,
	maker_fee_bps,taker_fee_bps,fee_currency,spread_model,fallback_spread_bps,
	slippage_model,slippage_bps,min_qty,min_notional,qty_step,price_tick,
	funding_rate_bps,funding_interval_hours,supports_post_only,supports_reduce_only,source,updated_at`

const defaultLossHalt = -5.0

func normalizeInstrument(i *Instrument, symbol, assetClass, source string, now time.Time) (*Instrument, error) {
	base := defaultInstrument(symbol, assetClass, source, now)
	if i == nil {
		return base, nil
	}
	if strings.TrimSpace(i.Symbol) != "" {
		base.Symbol = canonicalSymbol(i.Symbol)
	}
	if base.Symbol != canonicalSymbol(symbol) {
		return nil, fmt.Errorf("instrument symbol %q does not match mark symbol %q", base.Symbol, symbol)
	}
	if v := strings.TrimSpace(i.ProviderSymbol); v != "" {
		base.ProviderSymbol = v
	}
	if v := strings.TrimSpace(i.Name); v != "" {
		base.Name = v
	}
	if v := strings.ToLower(strings.TrimSpace(i.AssetClass)); v != "" {
		base.AssetClass = v
	}
	if base.AssetClass != strings.ToLower(strings.TrimSpace(assetClass)) {
		return nil, fmt.Errorf("instrument asset class %q does not match mark asset class %q", base.AssetClass, assetClass)
	}
	if v := strings.ToUpper(strings.TrimSpace(i.Exchange)); v != "" {
		base.Exchange = v
	}
	if v := strings.TrimSpace(i.ExchangeTimezone); v != "" {
		if _, err := time.LoadLocation(v); err != nil {
			return nil, fmt.Errorf("invalid exchange timezone %q: %w", v, err)
		}
		base.ExchangeTimezone = v
	}
	if v := strings.ToUpper(strings.TrimSpace(i.Calendar)); v != "" {
		base.Calendar = v
	}
	if v := strings.ToUpper(strings.TrimSpace(i.BaseCurrency)); v != "" {
		base.BaseCurrency = v
	}
	if v := strings.ToUpper(strings.TrimSpace(i.QuoteCurrency)); v != "" {
		base.QuoteCurrency = v
	}
	if v := strings.ToLower(strings.TrimSpace(i.VolumeUnit)); v != "" {
		base.VolumeUnit = v
	}
	if i.TickSize < 0 || i.LotSize < 0 || i.MinQty < 0 || i.MinNotional < 0 ||
		!finite(i.TickSize) || !finite(i.LotSize) || !finite(i.MinQty) || !finite(i.MinNotional) {
		return nil, errors.New("instrument tick_size, lot_size, min_qty, and min_notional must be finite and non-negative")
	}
	base.TickSize = i.TickSize
	base.LotSize = i.LotSize
	base.MinQty = i.MinQty
	base.MinNotional = i.MinNotional
	base.Active = i.Active
	if v := strings.TrimSpace(i.ExpiresAt); v != "" {
		at, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return nil, fmt.Errorf("invalid instrument expiry %q: %w", v, err)
		}
		base.ExpiresAt = at.UTC().Format(time.RFC3339)
	}
	if v := strings.TrimSpace(i.Source); v != "" {
		base.Source = v
	}
	base.UpdatedAt = now.UTC().Format(time.RFC3339)
	return base, nil
}

func dbUpsertInstrumentExec(exec sqlExecer, i *Instrument) error {
	if i == nil {
		return nil
	}
	_, err := exec.Exec(`
		INSERT INTO instruments (
			symbol, provider_symbol, name, asset_class, exchange, exchange_timezone,
			calendar, base_currency, quote_currency, volume_unit, tick_size,
			lot_size, min_qty, min_notional, active, expires_at, source, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)
		ON CONFLICT(symbol) DO UPDATE SET
			provider_symbol=excluded.provider_symbol, name=excluded.name,
			asset_class=excluded.asset_class, exchange=excluded.exchange,
			exchange_timezone=excluded.exchange_timezone, calendar=excluded.calendar,
			base_currency=excluded.base_currency, quote_currency=excluded.quote_currency,
			volume_unit=excluded.volume_unit,
			tick_size=CASE WHEN excluded.tick_size>0 THEN excluded.tick_size ELSE instruments.tick_size END,
			lot_size=CASE WHEN excluded.lot_size>0 THEN excluded.lot_size ELSE instruments.lot_size END,
			min_qty=CASE WHEN excluded.min_qty>0 THEN excluded.min_qty ELSE instruments.min_qty END,
			min_notional=CASE WHEN excluded.min_notional>0 THEN excluded.min_notional ELSE instruments.min_notional END,
			active=excluded.active,
			expires_at=excluded.expires_at, source=excluded.source,
			updated_at=excluded.updated_at`,
		i.Symbol, i.ProviderSymbol, i.Name, i.AssetClass, i.Exchange, i.ExchangeTimezone,
		i.Calendar, i.BaseCurrency, i.QuoteCurrency, i.VolumeUnit, i.TickSize,
		i.LotSize, i.MinQty, i.MinNotional, i.Active, i.ExpiresAt, i.Source, i.UpdatedAt)
	return err
}
func dbGetInstrument(db *sql.DB, symbol string) (*Instrument, error) {
	row := db.QueryRow(`
		SELECT symbol, provider_symbol, name, asset_class, exchange, exchange_timezone,
		       calendar, base_currency, quote_currency, volume_unit, tick_size,
		       lot_size, min_qty, min_notional, active, COALESCE(expires_at, ''), source, updated_at
		FROM instruments WHERE symbol = ?`, canonicalSymbol(symbol))
	var i Instrument
	if err := row.Scan(&i.Symbol, &i.ProviderSymbol, &i.Name, &i.AssetClass, &i.Exchange,
		&i.ExchangeTimezone, &i.Calendar, &i.BaseCurrency, &i.QuoteCurrency,
		&i.VolumeUnit, &i.TickSize, &i.LotSize, &i.MinQty, &i.MinNotional, &i.Active, &i.ExpiresAt,
		&i.Source, &i.UpdatedAt); err != nil {
		return nil, err
	}
	return &i, nil
}
