package main

// Database layer. Every read + write the sidecar performs lands here;
// tools.go and exec.go call into this file. Thin wrappers over SQL —
// no business logic lives below this line.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ─── Domain types (mirror the JSON shapes returned by tools / REST) ──

type Portfolio struct {
	ID                   int64    `json:"id"`
	ProjectID            string   `json:"project_id,omitempty"`
	Name                 string   `json:"name"`
	AgentID              string   `json:"agent_id,omitempty"`
	Mandate              string   `json:"mandate"`
	AllowedClasses       []string `json:"allowed_classes"`
	StartingCash         float64  `json:"starting_cash"`
	Cash                 float64  `json:"cash"`
	AvailableCash        *float64 `json:"available_cash,omitempty"`
	Status               string   `json:"status"`
	Mode                 string   `json:"mode"`
	ExecutionEnvironment string   `json:"execution_environment"`
	LiveArmed            bool     `json:"live_armed"`
	BrokerSlug           string   `json:"broker_slug,omitempty"` // "binance-trading", "alpaca-trading", … — NULL for paper
	CreatedAt            string   `json:"created_at,omitempty"`
	UpdatedAt            string   `json:"updated_at,omitempty"`

	// Computed by snapshot — not stored.
	Equity      float64  `json:"equity,omitempty"`
	DayPnL      float64  `json:"day_pnl,omitempty"`
	DayPnLPct   float64  `json:"day_pnl_pct,omitempty"`
	OpenPnL     float64  `json:"open_pnl,omitempty"`
	OpenPnLPct  float64  `json:"open_pnl_pct,omitempty"`
	RealizedPnL float64  `json:"realized_pnl,omitempty"`
	FeesPaid    float64  `json:"fees_paid,omitempty"`
	TotalPnL    float64  `json:"total_pnl,omitempty"`
	TotalPnLPct float64  `json:"total_pnl_pct,omitempty"`
	BuyingPower float64  `json:"buying_power,omitempty"`
	Watchlist   []string `json:"watchlist,omitempty"`
}

type Position struct {
	Symbol           string  `json:"symbol"`
	AssetClass       string  `json:"asset_class"`
	Outcome          string  `json:"outcome,omitempty"`
	Qty              float64 `json:"qty"`
	AvgCost          float64 `json:"avg_cost"`
	MarketPrice      float64 `json:"market_price"`
	MarketValue      float64 `json:"market_value"`
	UnrealizedPnL    float64 `json:"unrealized_pnl"`
	UnrealizedPnLPct float64 `json:"unrealized_pnl_pct"`
	RealizedPnL      float64 `json:"realized_pnl"`
	DayPnL           float64 `json:"day_pnl"`
	WeightPct        float64 `json:"weight_pct"`
}

type Order struct {
	ID              string   `json:"id"`
	PortfolioID     int64    `json:"portfolio_id"`
	Symbol          string   `json:"symbol"`
	AssetClass      string   `json:"asset_class"`
	Side            string   `json:"side"`
	Outcome         string   `json:"outcome,omitempty"`
	Type            string   `json:"type"`
	Qty             float64  `json:"qty"`
	FilledQty       float64  `json:"filled_qty"`
	AvgFillPrice    float64  `json:"avg_fill_price,omitempty"`
	LimitPrice      *float64 `json:"limit_price,omitempty"`
	StopPrice       *float64 `json:"stop_price,omitempty"`
	TIF             string   `json:"tif"`
	Status          string   `json:"status"`
	Rationale       string   `json:"rationale"`
	Source          string   `json:"source"`
	RejectionCode   string   `json:"rejection_code,omitempty"`
	RejectionDetail string   `json:"rejection_detail,omitempty"`
	PlacedAt        string   `json:"placed_at"`
	ResolvedAt      string   `json:"resolved_at,omitempty"`
}

type Fill struct {
	ID       int64   `json:"id"`
	OrderID  string  `json:"order_id"`
	Qty      float64 `json:"qty"`
	Price    float64 `json:"price"`
	Fee      float64 `json:"fee"`
	FilledAt string  `json:"filled_at"`
}

type JournalEntry struct {
	ID          int64          `json:"id"`
	PortfolioID int64          `json:"portfolio_id"`
	Kind        string         `json:"kind"`
	Body        string         `json:"body"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   string         `json:"created_at"`
}

type Mark struct {
	Symbol         string      `json:"symbol"`
	AssetClass     string      `json:"asset_class"`
	Price          float64     `json:"price"`
	NoPrice        *float64    `json:"no_price,omitempty"`
	PrevClose      *float64    `json:"prev_close,omitempty"`
	Volume24h      *float64    `json:"volume_24h,omitempty"`
	VolumeUnit     string      `json:"volume_unit,omitempty"`
	BidPrice       *float64    `json:"bid_price,omitempty"`
	AskPrice       *float64    `json:"ask_price,omitempty"`
	BidSize        *float64    `json:"bid_size,omitempty"`
	AskSize        *float64    `json:"ask_size,omitempty"`
	LastTradePrice *float64    `json:"last_trade_price,omitempty"`
	LastTradeSize  *float64    `json:"last_trade_size,omitempty"`
	Feed           string      `json:"feed,omitempty"`
	QuoteAt        string      `json:"quote_at,omitempty"`
	MarkedAt       string      `json:"marked_at"`
	ReceivedAt     string      `json:"received_at,omitempty"`
	TimestampKind  string      `json:"timestamp_kind,omitempty"`
	Source         string      `json:"source,omitempty"`
	Instrument     *Instrument `json:"instrument,omitempty"`
}

type MarketBar struct {
	Symbol     string  `json:"symbol"`
	Timeframe  string  `json:"timeframe"`
	BarAt      string  `json:"bar_at"`
	Open       float64 `json:"open"`
	High       float64 `json:"high"`
	Low        float64 `json:"low"`
	Close      float64 `json:"close"`
	Volume     float64 `json:"volume"`
	TradeCount int64   `json:"trade_count,omitempty"`
	VWAP       float64 `json:"vwap,omitempty"`
	Source     string  `json:"source"`
	Feed       string  `json:"feed,omitempty"`
	ReceivedAt string  `json:"received_at"`
	Complete   bool    `json:"complete"`
}

type Alert struct {
	ID          int64   `json:"id"`
	PortfolioID int64   `json:"portfolio_id"`
	Symbol      string  `json:"symbol"`
	Rule        string  `json:"rule"`
	Threshold   float64 `json:"threshold"`
	Status      string  `json:"status"`
	ExpiresAt   string  `json:"expires_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
	FiredAt     string  `json:"fired_at,omitempty"`
}

type PortfolioExecutionSettings struct {
	FeeBps      float64
	SlippageBps float64
}

type Strategy struct {
	ID               int64                 `json:"id"`
	ProjectID        string                `json:"project_id,omitempty"`
	Name             string                `json:"name"`
	Description      string                `json:"description"`
	Status           string                `json:"status"`
	Definition       map[string]any        `json:"definition"`
	Version          int                   `json:"version"`
	CreatedByAgentID int64                 `json:"created_by_agent_id,omitempty"`
	CreatedAt        string                `json:"created_at"`
	UpdatedAt        string                `json:"updated_at"`
	AssignmentStatus string                `json:"assignment_status,omitempty"`
	Assignments      []*StrategyAssignment `json:"assignments,omitempty"`
}

type StrategyAssignment struct {
	ID                int64  `json:"id"`
	ProjectID         string `json:"project_id,omitempty"`
	PortfolioID       int64  `json:"portfolio_id"`
	StrategyID        int64  `json:"strategy_id"`
	StrategyVersion   int    `json:"strategy_version"`
	ControlMode       string `json:"control_mode"`
	Status            string `json:"status"`
	AssignedAgentID   int64  `json:"assigned_agent_id,omitempty"`
	Cadence           string `json:"cadence"`
	LastEvaluatedAt   string `json:"last_evaluated_at,omitempty"`
	LastMarketBarAt   string `json:"last_market_bar_at,omitempty"`
	LastSeenBarAt     string `json:"last_seen_bar_at,omitempty"`
	LastCheckedAt     string `json:"last_checked_at,omitempty"`
	PortfolioName     string `json:"portfolio_name,omitempty"`
	Eligibility       string `json:"eligibility,omitempty"`
	EligibilityReason string `json:"eligibility_reason,omitempty"`
	NextEligibleAt    string `json:"next_eligible_at,omitempty"`
	SessionOpenAt     string `json:"session_open_at,omitempty"`
	SessionCloseAt    string `json:"session_close_at,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type StrategyRunEvent struct {
	ID              int64
	ProjectID       string
	PortfolioID     int64
	AssignmentID    int64
	StrategyID      int64
	StrategyVersion int
	SignalBarAt     time.Time
	Status          string
}

type BacktestRun struct {
	ID                     int64          `json:"id"`
	ProjectID              string         `json:"project_id,omitempty"`
	PortfolioID            int64          `json:"portfolio_id"`
	SourceAgentID          int64          `json:"source_agent_id"`
	StrategyID             int64          `json:"strategy_id,omitempty"`
	RunKind                string         `json:"run_kind,omitempty"`
	StrategyVersion        int            `json:"strategy_version,omitempty"`
	EnvironmentID          string         `json:"environment_id,omitempty"`
	EnvironmentAgentID     int64          `json:"environment_agent_id,omitempty"`
	EnvironmentPortfolioID int64          `json:"environment_portfolio_id,omitempty"`
	Name                   string         `json:"name"`
	Status                 string         `json:"status"`
	Symbols                []string       `json:"symbols"`
	StartAt                string         `json:"start_at"`
	EndAt                  string         `json:"end_at"`
	Interval               string         `json:"interval"`
	StartingCash           float64        `json:"starting_cash"`
	FeeBps                 float64        `json:"fee_bps"`
	SlippageBps            float64        `json:"slippage_bps"`
	CurrentStep            int            `json:"current_step"`
	TotalSteps             int            `json:"total_steps"`
	Summary                map[string]any `json:"summary,omitempty"`
	Error                  string         `json:"error,omitempty"`
	CreatedAt              string         `json:"created_at"`
	UpdatedAt              string         `json:"updated_at"`
	CompletedAt            string         `json:"completed_at,omitempty"`
}

type BacktestEvent struct {
	ID        int64          `json:"id"`
	RunID     int64          `json:"run_id"`
	Kind      string         `json:"kind"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt string         `json:"created_at"`
}

type BacktestSnapshot struct {
	RunID       int64            `json:"run_id"`
	Step        int              `json:"step"`
	Equity      float64          `json:"equity"`
	Cash        float64          `json:"cash"`
	BuyingPower float64          `json:"buying_power"`
	OpenPnL     float64          `json:"open_pnl"`
	OpenPnLPct  float64          `json:"open_pnl_pct"`
	RealizedPnL float64          `json:"realized_pnl"`
	Exposure    float64          `json:"exposure"`
	Positions   []*Position      `json:"positions,omitempty"`
	Orders      []*Order         `json:"orders,omitempty"`
	Prices      []map[string]any `json:"prices,omitempty"`
	CreatedAt   string           `json:"created_at,omitempty"`
	UpdatedAt   string           `json:"updated_at,omitempty"`
}

// ─── Portfolio ─────────────────────────────────────────────────────

func dbCreatePortfolio(db *sql.DB, p *Portfolio) (int64, error) {
	classesJSON, err := json.Marshal(p.AllowedClasses)
	if err != nil {
		return 0, err
	}
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	if mode == "" {
		mode = "paper"
	}
	executionEnvironment := normalizeExecutionEnvironment(p.ExecutionEnvironment, mode, p.BrokerSlug)
	var brokerArg any
	if strings.TrimSpace(p.BrokerSlug) != "" {
		brokerArg = p.BrokerSlug
	}
	res, err := db.Exec(`
		INSERT INTO portfolios (project_id, name, agent_id, mandate, allowed_classes, starting_cash, cash, mode, broker_slug, execution_environment, live_armed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ProjectID, p.Name, p.AgentID, p.Mandate, string(classesJSON), p.StartingCash, p.StartingCash, mode, brokerArg,
		executionEnvironment, boolInt(p.LiveArmed))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func normalizeExecutionEnvironment(value, legacyMode, brokerSlug string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "simulation", "broker_paper", "broker_live", "backtest":
		return strings.ToLower(strings.TrimSpace(value))
	}
	if strings.EqualFold(strings.TrimSpace(legacyMode), "live") {
		return "broker_live"
	}
	return "simulation"
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func portfolioBrokerBacked(p *Portfolio) bool {
	if p == nil {
		return false
	}
	env := normalizeExecutionEnvironment(p.ExecutionEnvironment, p.Mode, p.BrokerSlug)
	return env == "broker_paper" || env == "broker_live"
}

func portfolioAllowsAutomatedExecution(p *Portfolio) bool {
	if p == nil {
		return false
	}
	switch normalizeExecutionEnvironment(p.ExecutionEnvironment, p.Mode, p.BrokerSlug) {
	case "simulation":
		return true
	case "broker_paper":
		return true
	case "broker_live":
		return p.LiveArmed
	default:
		return false
	}
}

func dbSetPortfolioLiveArmed(db *sql.DB, projectID string, id int64, armed bool) error {
	res, err := db.Exec(`UPDATE portfolios SET live_armed = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND project_id = ? AND execution_environment = 'broker_live'`, boolInt(armed), id, projectID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("only broker_live portfolios can be armed")
	}
	return nil
}

func dbUpdatePortfolioConfig(db *sql.DB, id int64, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	var raw string
	if err := db.QueryRow(`SELECT config_json FROM portfolios WHERE id = ?`, id).Scan(&raw); err != nil {
		return err
	}
	cfg := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	for k, v := range updates {
		cfg[k] = v
	}
	next, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE portfolios SET config_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(next), id)
	return err
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

func dbHasBacktestPortfolio(db *sql.DB, projectID string) bool {
	rows, err := db.Query(`
		SELECT config_json FROM portfolios
		WHERE (? = '' OR project_id = ?) AND status != 'halted'`,
		projectID, projectID)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		cfg := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			continue
		}
		if fmt.Sprint(cfg["source_override"]) == "backtest" ||
			fmt.Sprint(cfg["pricing_mode"]) == "backtest" ||
			fmt.Sprint(cfg["source"]) == "backtest" {
			return true
		}
	}
	return false
}

func dbGetPortfolio(db *sql.DB, projectID string, id int64) (*Portfolio, error) {
	row := db.QueryRow(`
		SELECT id, project_id, name, COALESCE(agent_id, ''), mandate, allowed_classes,
		       starting_cash, cash, available_cash, status, mode, COALESCE(broker_slug, ''),
		       execution_environment, live_armed, created_at, updated_at
		FROM portfolios WHERE id = ? AND project_id = ?`, id, projectID)
	return scanPortfolio(row)
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

// dbAllPortfolios — engine sweep across every portfolio (e.g. tick).
func dbAllPortfolios(db *sql.DB) ([]*Portfolio, error) {
	rows, err := db.Query(`
		SELECT id, project_id, name, COALESCE(agent_id, ''), mandate, allowed_classes,
		       starting_cash, cash, available_cash, status, mode, COALESCE(broker_slug, ''),
		       execution_environment, live_armed, created_at, updated_at
		FROM portfolios ORDER BY id`)
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

func dbSetPortfolioStatus(db *sql.DB, id int64, status string) error {
	_, err := db.Exec(`UPDATE portfolios SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	return err
}

func dbBindPortfolioAgent(db *sql.DB, portfolioID, agentID int64) (string, error) {
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM portfolio_bindings WHERE portfolio_id = ? AND role = 'manager'`, portfolioID); err != nil {
		return "", err
	}

	agentRef := ""
	var agentArg any
	if agentID > 0 {
		agentRef = fmt.Sprintf("apteva-instance:%d", agentID)
		agentArg = agentRef
		if _, err := tx.Exec(`
			INSERT INTO portfolio_bindings (portfolio_id, instance_id, role)
			VALUES (?, ?, 'manager')
			ON CONFLICT(portfolio_id, instance_id) DO UPDATE SET role = 'manager'`,
			portfolioID, agentID); err != nil {
			return "", err
		}
	}

	if _, err := tx.Exec(`
		UPDATE portfolios SET agent_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		agentArg, portfolioID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return agentRef, nil
}

func dbAddCash(db *sql.DB, id int64, delta float64) error {
	_, err := db.Exec(`UPDATE portfolios SET cash = cash + ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, delta, id)
	return err
}

// ─── Positions ─────────────────────────────────────────────────────

func dbListPositions(db *sql.DB, portfolioID int64) ([]*Position, error) {
	rows, err := db.Query(`
		SELECT p.symbol, p.asset_class, COALESCE(p.outcome, ''), p.qty, p.avg_cost,
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
		if err := rows.Scan(&p.Symbol, &p.AssetClass, &p.Outcome,
			&p.Qty, &p.AvgCost, &p.RealizedPnL); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// dbInsertPositionRaw — direct insert without going through dbApplyFill.
// Used by the live-portfolio bootstrap seed: when we create a live
// portfolio, the broker's existing holdings come in as positions with
// no fill history. avg_cost is "best-known" (current mark or 0); the
// reconciler updates it on subsequent fills.
func dbInsertPositionRaw(db *sql.DB, projectID string, portfolioID int64, symbol, assetClass, outcome string, qty, avgCost float64) error {
	var outcomeArg any
	if outcome != "" {
		outcomeArg = outcome
	}
	_, err := db.Exec(`
		INSERT OR IGNORE INTO positions (project_id, portfolio_id, symbol, asset_class, outcome, qty, avg_cost)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, portfolioID, symbol, assetClass, outcomeArg, qty, avgCost)
	return err
}

func dbGetPosition(db *sql.DB, portfolioID int64, symbol, outcome string) (*Position, error) {
	row := db.QueryRow(`
		SELECT p.symbol, p.asset_class, COALESCE(p.outcome, ''), p.qty, p.avg_cost,
		       COALESCE(a.gross_realized_pnl - a.fees_paid, p.realized_pnl)
		FROM positions p
		LEFT JOIN position_accounting a
		  ON a.portfolio_id = p.portfolio_id
		 AND a.symbol = p.symbol
		 AND a.outcome = COALESCE(p.outcome, '')
		WHERE p.portfolio_id = ? AND p.symbol = ? AND COALESCE(p.outcome, '') = ?`,
		portfolioID, symbol, outcome)
	var p Position
	err := row.Scan(&p.Symbol, &p.AssetClass, &p.Outcome, &p.Qty, &p.AvgCost, &p.RealizedPnL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// dbApplyFill mutates a position to absorb a fill. Buys add qty
// (weighted-avg cost); sells reduce qty (realized P&L per share).
// Polymarket: 'yes' is a buy on the YES outcome; 'no' is a buy on NO.
// Selling polymarket exits an existing leg.
func dbApplyFill(tx *sql.Tx, portfolioID int64, projectID string, o *Order, qty, price float64) error {
	outcome := ""
	if o.AssetClass == "polymarket" {
		outcome = polyOutcome(o)
	}

	// Read current position (if any).
	row := tx.QueryRow(`
		SELECT id, qty, avg_cost, realized_pnl
		FROM positions WHERE portfolio_id = ? AND symbol = ? AND COALESCE(outcome, '') = ?`,
		portfolioID, o.Symbol, outcome)
	var (
		posID                      int64
		curQty, curAvgCost, curRPL float64
		exists                     = true
	)
	if err := row.Scan(&posID, &curQty, &curAvgCost, &curRPL); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		exists = false
	}

	isBuy := o.Side == "buy" || o.Side == "yes" || o.Side == "no"
	if !isBuy {
		// Sell on equity/crypto: reduces an existing long position.
		if !exists || curQty < qty-1e-9 {
			return fmt.Errorf("cannot sell %v %s — only %v available", qty, o.Symbol, curQty)
		}
		realized := (price - curAvgCost) * qty
		if err := dbAccruePositionAccountingTx(tx, portfolioID, o.Symbol, outcome, realized, 0); err != nil {
			return err
		}
		newQty := curQty - qty
		if newQty < 1e-9 {
			// Close it.
			if _, err := tx.Exec(`DELETE FROM positions WHERE id = ?`, posID); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(`
				UPDATE positions SET qty = ?, realized_pnl = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?`, newQty, curRPL+realized, posID); err != nil {
				return err
			}
		}
		// Sell credits cash (less fees, applied at the engine layer).
		if _, err := tx.Exec(`UPDATE portfolios SET cash = cash + ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			qty*price, portfolioID); err != nil {
			return err
		}
		return nil
	}

	// Buy: weighted avg cost; debit cash.
	if exists {
		newQty := curQty + qty
		newAvg := (curQty*curAvgCost + qty*price) / newQty
		if _, err := tx.Exec(`
			UPDATE positions SET qty = ?, avg_cost = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`, newQty, newAvg, posID); err != nil {
			return err
		}
	} else {
		var outcomeArg any
		if outcome != "" {
			outcomeArg = outcome
		}
		if _, err := tx.Exec(`
			INSERT INTO positions (project_id, portfolio_id, symbol, asset_class, outcome, qty, avg_cost)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			projectID, portfolioID, o.Symbol, o.AssetClass, outcomeArg, qty, price); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE portfolios SET cash = cash - ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		qty*price, portfolioID); err != nil {
		return err
	}
	return nil
}

// dbAccruePositionAccountingTx records gross closed-lot P&L and execution
// fees independently from the open position row. The row therefore survives
// a full close and a later re-entry into the same symbol.
func dbAccruePositionAccountingTx(tx *sql.Tx, portfolioID int64, symbol, outcome string, grossRealized, fee float64) error {
	_, err := tx.Exec(`
		INSERT INTO position_accounting (portfolio_id, symbol, outcome, gross_realized_pnl, fees_paid)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(portfolio_id, symbol, outcome) DO UPDATE SET
			gross_realized_pnl = gross_realized_pnl + excluded.gross_realized_pnl,
			fees_paid = fees_paid + excluded.fees_paid,
			updated_at = CURRENT_TIMESTAMP`,
		portfolioID, symbol, outcome, grossRealized, fee)
	return err
}

func dbPortfolioAccounting(db *sql.DB, portfolioID int64) (realized, fees float64, err error) {
	err = db.QueryRow(`
		SELECT COALESCE(SUM(gross_realized_pnl - fees_paid), 0), COALESCE(SUM(fees_paid), 0)
		FROM position_accounting WHERE portfolio_id = ?`, portfolioID).Scan(&realized, &fees)
	return
}

// dbRebuildPositionAccounting reconstructs average-cost lots from the
// append-only fills ledger. It is idempotent and runs before workers start,
// repairing existing installs whose closed position rows lost realized P&L.
func dbRebuildPositionAccounting(db *sql.DB) error {
	type fillRow struct {
		portfolioID int64
		symbol      string
		side        string
		outcome     string
		qty         float64
		price       float64
		fee         float64
	}
	rows, err := db.Query(`
		SELECT f.portfolio_id, o.symbol, o.side,
		       CASE
		         WHEN o.asset_class = 'polymarket' THEN COALESCE(NULLIF(o.outcome, ''), UPPER(o.side))
		         ELSE ''
		       END,
		       f.qty, f.price, f.fee
		FROM fills f
		JOIN orders o ON o.id = f.order_id
		ORDER BY f.portfolio_id, f.filled_at, f.id`)
	if err != nil {
		return err
	}
	var fills []fillRow
	for rows.Next() {
		var f fillRow
		if err := rows.Scan(&f.portfolioID, &f.symbol, &f.side, &f.outcome, &f.qty, &f.price, &f.fee); err != nil {
			rows.Close()
			return err
		}
		fills = append(fills, f)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	type lot struct{ qty, avg, gross, fees float64 }
	lots := map[string]*lot{}
	keyFor := func(f fillRow) string {
		return fmt.Sprintf("%d\x00%s\x00%s", f.portfolioID, f.symbol, f.outcome)
	}
	for _, f := range fills {
		key := keyFor(f)
		l := lots[key]
		if l == nil {
			l = &lot{}
			lots[key] = l
		}
		l.fees += f.fee
		if f.side == "buy" || f.side == "yes" || f.side == "no" {
			newQty := l.qty + f.qty
			if newQty > 0 {
				l.avg = (l.qty*l.avg + f.qty*f.price) / newQty
				l.qty = newQty
			}
			continue
		}
		if l.qty <= 0 {
			continue // imported broker history may begin after the opening buy
		}
		closedQty := f.qty
		if closedQty > l.qty {
			closedQty = l.qty
		}
		l.gross += (f.price - l.avg) * closedQty
		l.qty -= closedQty
		if l.qty < 1e-9 {
			l.qty = 0
			l.avg = 0
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM position_accounting`); err != nil {
		return err
	}
	for key, l := range lots {
		parts := strings.Split(key, "\x00")
		portfolioID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO position_accounting
				(portfolio_id, symbol, outcome, gross_realized_pnl, fees_paid)
			VALUES (?, ?, ?, ?, ?)`, portfolioID, parts[1], parts[2], l.gross, l.fees); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ─── Orders ────────────────────────────────────────────────────────

func dbInsertOrder(db *sql.DB, o *Order, projectID string) error {
	_, err := db.Exec(`
		INSERT INTO orders (id, project_id, portfolio_id, symbol, asset_class, side, outcome, type,
		                    qty, limit_price, stop_price, tif, status, rationale, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, projectID, o.PortfolioID, o.Symbol, o.AssetClass, o.Side, nullableString(o.Outcome), o.Type,
		o.Qty, nullable(o.LimitPrice), nullable(o.StopPrice), o.TIF, o.Status, o.Rationale, o.Source)
	return err
}

func dbGetOrder(db *sql.DB, projectID, id string) (*Order, error) {
	row := db.QueryRow(`
		SELECT id, portfolio_id, symbol, asset_class, side, COALESCE(outcome, ''), type, qty, filled_qty, avg_fill_price,
		       limit_price, stop_price, tif, status, rationale, source,
		       COALESCE(rejection_code, ''), COALESCE(rejection_detail, ''),
		       placed_at, COALESCE(resolved_at, '')
		FROM orders WHERE id = ? AND project_id = ?`, id, projectID)
	return scanOrder(row)
}

func dbGetOrderAnyProject(db *sql.DB, id string) (*Order, error) {
	row := db.QueryRow(`
		SELECT id, portfolio_id, symbol, asset_class, side, COALESCE(outcome, ''), type, qty, filled_qty, avg_fill_price,
		       limit_price, stop_price, tif, status, rationale, source,
		       COALESCE(rejection_code, ''), COALESCE(rejection_detail, ''),
		       placed_at, COALESCE(resolved_at, '')
		FROM orders WHERE id = ?`, id)
	return scanOrder(row)
}

func dbListOrders(db *sql.DB, portfolioID int64, status string, limit int) ([]*Order, error) {
	q := `SELECT id, portfolio_id, symbol, asset_class, side, COALESCE(outcome, ''), type, qty, filled_qty, avg_fill_price,
	             limit_price, stop_price, tif, status, rationale, source,
	             COALESCE(rejection_code, ''), COALESCE(rejection_detail, ''),
	             placed_at, COALESCE(resolved_at, '')
	      FROM orders WHERE portfolio_id = ?`
	args := []any{portfolioID}
	if status != "" && status != "all" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY placed_at DESC LIMIT ?`
	if limit <= 0 || limit > 200 {
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

// dbWorkingOrders — engine-side, no project filter (the engine sees all).
func dbWorkingOrders(db *sql.DB) ([]*Order, error) {
	rows, err := db.Query(`
		SELECT id, portfolio_id, symbol, asset_class, side, COALESCE(outcome, ''), type, qty, filled_qty, avg_fill_price,
		       limit_price, stop_price, tif, status, rationale, source,
		       COALESCE(rejection_code, ''), COALESCE(rejection_detail, ''),
		       placed_at, COALESCE(resolved_at, '')
		FROM orders WHERE status = 'working' ORDER BY placed_at`)
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

func scanOrder(row *sql.Row) (*Order, error) {
	var o Order
	var lp, sp sql.NullFloat64
	var resolvedAt string
	if err := row.Scan(&o.ID, &o.PortfolioID, &o.Symbol, &o.AssetClass, &o.Side, &o.Outcome, &o.Type,
		&o.Qty, &o.FilledQty, &o.AvgFillPrice, &lp, &sp, &o.TIF, &o.Status,
		&o.Rationale, &o.Source, &o.RejectionCode, &o.RejectionDetail,
		&o.PlacedAt, &resolvedAt); err != nil {
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
	if err := rows.Scan(&o.ID, &o.PortfolioID, &o.Symbol, &o.AssetClass, &o.Side, &o.Outcome, &o.Type,
		&o.Qty, &o.FilledQty, &o.AvgFillPrice, &lp, &sp, &o.TIF, &o.Status,
		&o.Rationale, &o.Source, &o.RejectionCode, &o.RejectionDetail,
		&o.PlacedAt, &resolvedAt); err != nil {
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

func dbCancelOrder(db *sql.DB, projectID, id, reason string) (string, error) {
	o, err := dbGetOrder(db, projectID, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("order %s not found", id)
		}
		return "", err
	}
	if o.Status != "working" {
		return o.Status, nil
	}
	_, err = db.Exec(`UPDATE orders SET status='cancelled', resolved_at=CURRENT_TIMESTAMP,
		rejection_detail = ? WHERE id = ?`, reason, id)
	if err != nil {
		return "", err
	}
	return "cancelled", nil
}

func dbRejectOrder(db *sql.DB, id, code, detail string) error {
	_, err := db.Exec(`
		UPDATE orders SET status='rejected', rejection_code=?, rejection_detail=?,
		                  resolved_at=CURRENT_TIMESTAMP WHERE id = ?`, code, detail, id)
	return err
}

// dbOrderIDByBrokerID — reverse of dbBrokerOrderIDFor. Given an
// exchange-side order id, finds the local Order.ID it maps to (if
// any) by looking up the rationale journal entry whose metadata
// carries that broker_order_id. Used by the backfill path to skip
// re-importing orders we already have.
func dbOrderIDByBrokerID(db *sql.DB, brokerOrderID string) (string, error) {
	if brokerOrderID == "" {
		return "", nil
	}
	row := db.QueryRow(`
		SELECT json_extract(metadata, '$.order_id')
		FROM journal
		WHERE kind = 'rationale' AND json_extract(metadata, '$.broker_order_id') = ?
		ORDER BY created_at DESC LIMIT 1`, brokerOrderID)
	var s sql.NullString
	if err := row.Scan(&s); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return s.String, nil
}

// dbInsertBackfilledOrder — order writer that takes EXPLICIT placed_at
// and resolved_at timestamps, so historical orders pulled from the
// broker land with their real chronology instead of the
// CURRENT_TIMESTAMP default dbInsertOrder uses. Empty timestamps fall
// through to the column defaults (e.g. unfilled open orders that
// haven't resolved).
func dbInsertBackfilledOrder(
	db *sql.DB, projectID string, portfolioID int64, id,
	symbol, assetClass, side, otype string,
	qty, filledQty, avgFillPrice, limitPrice, stopPrice float64,
	tif, status, rationale, source, placedAt, resolvedAt string,
) error {
	var lp, sp any
	if limitPrice > 0 {
		lp = limitPrice
	}
	if stopPrice > 0 {
		sp = stopPrice
	}
	var placed, resolved any
	if placedAt != "" {
		placed = placedAt
	}
	if resolvedAt != "" {
		resolved = resolvedAt
	}
	_, err := db.Exec(`
		INSERT INTO orders (
			id, project_id, portfolio_id, symbol, asset_class, side, outcome, type,
			qty, filled_qty, avg_fill_price, limit_price, stop_price,
			tif, status, rationale, source, placed_at, resolved_at
		) VALUES (
			?, ?, ?, ?, ?, ?, CASE WHEN ? IN ('yes','no') THEN UPPER(?) ELSE NULL END, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?,
			COALESCE(?, CURRENT_TIMESTAMP), ?
		)`,
		id, projectID, portfolioID, symbol, assetClass, side, side, side, otype,
		qty, filledQty, avgFillPrice, lp, sp,
		tif, status, rationale, source, placed, resolved)
	return err
}

// dbInsertBackfilledFill — fill writer that takes an explicit
// filled_at timestamp. Companion to dbInsertBackfilledOrder; the
// regular dbInsertFill takes a *sql.Tx and uses CURRENT_TIMESTAMP.
func dbInsertBackfilledFill(
	db *sql.DB, projectID, orderID string, portfolioID int64,
	qty, price, fee float64, filledAt string,
) error {
	var at any
	if filledAt != "" {
		at = filledAt
	}
	_, err := db.Exec(`
		INSERT INTO fills (project_id, order_id, portfolio_id, qty, price, fee, filled_at)
		VALUES (?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP))`,
		projectID, orderID, portfolioID, qty, price, fee, at)
	if err != nil {
		return err
	}
	return dbRebuildPositionAccounting(db)
}

// dbBrokerOrderIDFor — pulls the broker's order id out of the rationale
// journal row that order_place writes when in live mode. The journal
// metadata is the authoritative store; no broker_order_id column on
// orders. Returns "" when not found (paper order, or rationale row
// missing for an old order).
func dbBrokerOrderIDFor(db *sql.DB, orderID string) (string, error) {
	row := db.QueryRow(`
		SELECT json_extract(metadata, '$.broker_order_id')
		FROM journal
		WHERE kind = 'rationale' AND json_extract(metadata, '$.order_id') = ?
		ORDER BY created_at DESC LIMIT 1`, orderID)
	var s sql.NullString
	if err := row.Scan(&s); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return s.String, nil
}

// ─── Fills + journal ───────────────────────────────────────────────

func dbInsertFill(tx *sql.Tx, projectID, orderID string, portfolioID int64, qty, price, fee float64) error {
	_, err := tx.Exec(`
		INSERT INTO fills (project_id, order_id, portfolio_id, qty, price, fee)
		VALUES (?, ?, ?, ?, ?, ?)`, projectID, orderID, portfolioID, qty, price, fee)
	return err
}

func dbMarkOrderFilled(tx *sql.Tx, orderID string, qty, avgFill float64) (bool, error) {
	res, err := tx.Exec(`
		UPDATE orders SET status='filled', filled_qty=?, avg_fill_price=?,
		                  resolved_at=CURRENT_TIMESTAMP WHERE id = ? AND status = 'working'`, qty, avgFill, orderID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func dbInsertJournal(db *sql.DB, projectID string, portfolioID int64, kind, body string, metadata map[string]any) (int64, error) {
	metaBytes, _ := json.Marshal(metadata)
	if metaBytes == nil {
		metaBytes = []byte("{}")
	}
	res, err := db.Exec(`
		INSERT INTO journal (project_id, portfolio_id, kind, body, metadata)
		VALUES (?, ?, ?, ?, ?)`, projectID, portfolioID, kind, body, string(metaBytes))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbInsertJournalTx(tx *sql.Tx, projectID string, portfolioID int64, kind, body string, metadata map[string]any) error {
	metaBytes, _ := json.Marshal(metadata)
	if metaBytes == nil {
		metaBytes = []byte("{}")
	}
	_, err := tx.Exec(`
		INSERT INTO journal (project_id, portfolio_id, kind, body, metadata)
		VALUES (?, ?, ?, ?, ?)`, projectID, portfolioID, kind, body, string(metaBytes))
	return err
}

func dbReadJournal(db *sql.DB, portfolioID int64, kind, since string, limit int) ([]*JournalEntry, error) {
	q := `SELECT id, portfolio_id, kind, body, metadata, created_at
	      FROM journal WHERE portfolio_id = ?`
	args := []any{portfolioID}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	if since != "" {
		q += ` AND created_at >= ?`
		args = append(args, since)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JournalEntry
	for rows.Next() {
		var e JournalEntry
		var metaJSON string
		if err := rows.Scan(&e.ID, &e.PortfolioID, &e.Kind, &e.Body, &metaJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(metaJSON), &e.Metadata)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ─── Marks ─────────────────────────────────────────────────────────

func dbUpsertMark(db *sql.DB, m *Mark) error {
	return dbUpsertMarkExec(db, m)
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
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
	if err := dbUpsertInstrumentExec(exec, normalized.Instrument); err != nil {
		return err
	}
	_, err = exec.Exec(`
		INSERT INTO marks (symbol, asset_class, price, no_price, prev_close, volume_24h, marked_at, source, timestamp_kind, volume_unit, received_at,
			bid_price, ask_price, bid_size, ask_size, last_trade_price, last_trade_size, feed, quote_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(symbol) DO UPDATE SET
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
			bid_price = COALESCE(excluded.bid_price, marks.bid_price),
			ask_price = COALESCE(excluded.ask_price, marks.ask_price),
			bid_size = COALESCE(excluded.bid_size, marks.bid_size),
			ask_size = COALESCE(excluded.ask_size, marks.ask_size),
			last_trade_price = COALESCE(excluded.last_trade_price, marks.last_trade_price),
			last_trade_size = COALESCE(excluded.last_trade_size, marks.last_trade_size),
			feed = CASE WHEN excluded.feed != '' THEN excluded.feed ELSE marks.feed END,
			quote_at = COALESCE(excluded.quote_at, marks.quote_at)`,
		normalized.Symbol, normalized.AssetClass, normalized.Price, nullable(normalized.NoPrice), nullable(normalized.PrevClose),
		nullable(normalized.Volume24h), normalized.MarkedAt, normalized.Source, normalized.TimestampKind,
		normalized.VolumeUnit, normalized.ReceivedAt, nullable(normalized.BidPrice), nullable(normalized.AskPrice),
		nullable(normalized.BidSize), nullable(normalized.AskSize), nullable(normalized.LastTradePrice),
		nullable(normalized.LastTradeSize), normalized.Feed, nullableString(normalized.QuoteAt))
	return err
}

func dbUpsertInstrumentExec(exec sqlExecer, i *Instrument) error {
	if i == nil {
		return nil
	}
	_, err := exec.Exec(`
		INSERT INTO instruments (
			symbol, provider_symbol, name, asset_class, exchange, exchange_timezone,
			calendar, base_currency, quote_currency, volume_unit, tick_size,
			lot_size, active, expires_at, source, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)
		ON CONFLICT(symbol) DO UPDATE SET
			provider_symbol=excluded.provider_symbol, name=excluded.name,
			asset_class=excluded.asset_class, exchange=excluded.exchange,
			exchange_timezone=excluded.exchange_timezone, calendar=excluded.calendar,
			base_currency=excluded.base_currency, quote_currency=excluded.quote_currency,
			volume_unit=excluded.volume_unit, tick_size=excluded.tick_size,
			lot_size=excluded.lot_size, active=excluded.active,
			expires_at=excluded.expires_at, source=excluded.source,
			updated_at=excluded.updated_at`,
		i.Symbol, i.ProviderSymbol, i.Name, i.AssetClass, i.Exchange, i.ExchangeTimezone,
		i.Calendar, i.BaseCurrency, i.QuoteCurrency, i.VolumeUnit, i.TickSize,
		i.LotSize, i.Active, i.ExpiresAt, i.Source, i.UpdatedAt)
	return err
}

func dbGetInstrument(db *sql.DB, symbol string) (*Instrument, error) {
	row := db.QueryRow(`
		SELECT symbol, provider_symbol, name, asset_class, exchange, exchange_timezone,
		       calendar, base_currency, quote_currency, volume_unit, tick_size,
		       lot_size, active, COALESCE(expires_at, ''), source, updated_at
		FROM instruments WHERE symbol = ?`, canonicalSymbol(symbol))
	var i Instrument
	if err := row.Scan(&i.Symbol, &i.ProviderSymbol, &i.Name, &i.AssetClass, &i.Exchange,
		&i.ExchangeTimezone, &i.Calendar, &i.BaseCurrency, &i.QuoteCurrency,
		&i.VolumeUnit, &i.TickSize, &i.LotSize, &i.Active, &i.ExpiresAt,
		&i.Source, &i.UpdatedAt); err != nil {
		return nil, err
	}
	return &i, nil
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

func dbListMarks(db *sql.DB) ([]*Mark, error) {
	rows, err := db.Query(`
		SELECT symbol, asset_class, price, no_price, prev_close, volume_24h, marked_at,
		       source, timestamp_kind, volume_unit, COALESCE(received_at, ''),
		       bid_price, ask_price, bid_size, ask_size, last_trade_price, last_trade_size,
		       feed, COALESCE(quote_at, '')
		FROM marks ORDER BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Mark{}
	for rows.Next() {
		var m Mark
		var no, pc, vol, bid, ask, bidSize, askSize, lastTrade, lastSize sql.NullFloat64
		if err := rows.Scan(&m.Symbol, &m.AssetClass, &m.Price, &no, &pc, &vol, &m.MarkedAt,
			&m.Source, &m.TimestampKind, &m.VolumeUnit, &m.ReceivedAt,
			&bid, &ask, &bidSize, &askSize, &lastTrade, &lastSize, &m.Feed, &m.QuoteAt); err != nil {
			return nil, err
		}
		if no.Valid {
			m.NoPrice = ptr(no.Float64)
		}
		if pc.Valid {
			m.PrevClose = ptr(pc.Float64)
		}
		if vol.Valid {
			m.Volume24h = ptr(vol.Float64)
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
		out = append(out, &m)
	}
	return out, rows.Err()
}

func dbUpsertMarketBar(db *sql.DB, bar *MarketBar) error {
	if bar == nil || canonicalSymbol(bar.Symbol) == "" {
		return errors.New("market bar symbol required")
	}
	if bar.Timeframe == "" || bar.BarAt == "" || bar.Source == "" {
		return errors.New("market bar timeframe, timestamp, and source required")
	}
	if _, err := time.Parse(time.RFC3339Nano, bar.BarAt); err != nil {
		return fmt.Errorf("market bar timestamp: %w", err)
	}
	if !finite(bar.Open) || !finite(bar.High) || !finite(bar.Low) || !finite(bar.Close) || !finite(bar.Volume) ||
		bar.Open <= 0 || bar.High < maxFloat(bar.Open, bar.Close) || bar.Low > minFloat(bar.Open, bar.Close) || bar.Low <= 0 || bar.Volume < 0 {
		return errors.New("invalid market bar OHLCV")
	}
	if bar.ReceivedAt == "" {
		bar.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := db.Exec(`INSERT INTO market_bars
		(symbol, timeframe, bar_at, open, high, low, close, volume, trade_count, vwap, source, feed, received_at, complete)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(symbol, timeframe, bar_at, source, feed) DO UPDATE SET
			open=excluded.open, high=excluded.high, low=excluded.low, close=excluded.close,
			volume=excluded.volume, trade_count=excluded.trade_count, vwap=excluded.vwap,
			received_at=excluded.received_at, complete=excluded.complete`,
		canonicalSymbol(bar.Symbol), bar.Timeframe, bar.BarAt, bar.Open, bar.High, bar.Low, bar.Close,
		bar.Volume, bar.TradeCount, bar.VWAP, bar.Source, bar.Feed, bar.ReceivedAt, boolInt(bar.Complete))
	return err
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// dbListRefreshSymbols returns only symbols that can affect executable state.
// Historical/orphaned marks are intentionally excluded so a removed watchlist
// item cannot cause a permanent provider retry loop.
func dbListRefreshSymbols(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`
		SELECT symbol FROM watchlist
		UNION
		SELECT symbol FROM positions WHERE qty != 0
		UNION
		SELECT symbol FROM orders WHERE status = 'working'
		UNION
		SELECT symbol FROM alerts WHERE status = 'active'
		ORDER BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var symbol string
		if err := rows.Scan(&symbol); err != nil {
			return nil, err
		}
		out = append(out, symbol)
	}
	return out, rows.Err()
}

// ─── Strategies ────────────────────────────────────────────────────

func dbCreateStrategy(db *sql.DB, s *Strategy) (int64, error) {
	raw, err := json.Marshal(s.Definition)
	if err != nil {
		return 0, err
	}
	status := strings.TrimSpace(s.Status)
	if status == "" {
		status = "draft"
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	version := maxInt(s.Version, 1)
	res, err := tx.Exec(`
		INSERT INTO strategies (project_id, name, description, status, definition_json, version, created_by_agent_id)
		VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, 0))`,
		s.ProjectID, s.Name, s.Description, status, string(raw), version, s.CreatedByAgentID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		INSERT INTO strategy_versions (strategy_id, version, definition_json)
		VALUES (?, ?, ?)`, id, version, string(raw)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func dbUpdateStrategy(db *sql.DB, projectID string, id int64, patch *Strategy) (*Strategy, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	cur, err := scanStrategy(tx.QueryRow(`
		SELECT id, project_id, name, description, status, definition_json, version,
		       COALESCE(created_by_agent_id, 0), created_at, updated_at
		FROM strategies WHERE id = ? AND project_id = ?`, id, projectID))
	if err != nil {
		return nil, err
	}
	previousVersion := cur.Version
	if strings.TrimSpace(patch.Name) != "" {
		cur.Name = strings.TrimSpace(patch.Name)
	}
	if patch.Description != "" {
		cur.Description = patch.Description
	}
	if strings.TrimSpace(patch.Status) != "" {
		cur.Status = strings.TrimSpace(patch.Status)
	}
	if patch.Definition != nil {
		cur.Definition = patch.Definition
		cur.Version++
	}
	raw, err := json.Marshal(cur.Definition)
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(`
		UPDATE strategies
		   SET name = ?, description = ?, status = ?, definition_json = ?, version = ?,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ? AND version = ?`,
		cur.Name, cur.Description, cur.Status, string(raw), cur.Version, id, projectID, previousVersion)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("strategy changed concurrently")
	}
	if patch.Definition != nil {
		if _, err := tx.Exec(`
			INSERT INTO strategy_versions (strategy_id, version, definition_json)
			VALUES (?, ?, ?)`, id, cur.Version, string(raw)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetStrategy(db, projectID, id)
}

func dbGetStrategy(db *sql.DB, projectID string, id int64) (*Strategy, error) {
	row := db.QueryRow(`
		SELECT id, project_id, name, description, status, definition_json, version,
		       COALESCE(created_by_agent_id, 0), created_at, updated_at
		FROM strategies WHERE id = ? AND project_id = ?`, id, projectID)
	return scanStrategy(row)
}

func dbGetStrategyVersion(db *sql.DB, projectID string, id int64, version int) (*Strategy, error) {
	if version <= 0 {
		return dbGetStrategy(db, projectID, id)
	}
	row := db.QueryRow(`
		SELECT s.id, s.project_id, s.name, s.description, s.status, v.definition_json, v.version,
		       COALESCE(s.created_by_agent_id, 0), s.created_at, s.updated_at
		FROM strategies s
		JOIN strategy_versions v ON v.strategy_id = s.id
		WHERE s.id = ? AND s.project_id = ? AND v.version = ?`, id, projectID, version)
	return scanStrategy(row)
}

func dbListStrategies(db *sql.DB, projectID, status string) ([]*Strategy, error) {
	q := `
		SELECT id, project_id, name, description, status, definition_json, version,
		       COALESCE(created_by_agent_id, 0), created_at, updated_at
		FROM strategies
		WHERE project_id = ? AND (? = '' OR status = ?)
		ORDER BY updated_at DESC, id DESC`
	rows, err := db.Query(q, projectID, status, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Strategy{}
	for rows.Next() {
		s, err := scanStrategyRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanStrategy(row *sql.Row) (*Strategy, error) {
	var s Strategy
	var raw string
	if err := row.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Description, &s.Status,
		&raw, &s.Version, &s.CreatedByAgentID, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	decodeStrategyDefinition(&s, raw)
	return &s, nil
}

func scanStrategyRows(rows *sql.Rows) (*Strategy, error) {
	var s Strategy
	var raw string
	if err := rows.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Description, &s.Status,
		&raw, &s.Version, &s.CreatedByAgentID, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	decodeStrategyDefinition(&s, raw)
	return &s, nil
}

func decodeStrategyDefinition(s *Strategy, raw string) {
	_ = json.Unmarshal([]byte(raw), &s.Definition)
	if s.Definition == nil {
		s.Definition = map[string]any{}
	}
}

func dbAssignStrategy(db *sql.DB, a *StrategyAssignment) (int64, error) {
	control := strings.TrimSpace(a.ControlMode)
	if control == "" {
		control = "strategy"
	}
	cadence := strings.TrimSpace(a.Cadence)
	if cadence == "" {
		cadence = "1d"
	}
	version := a.StrategyVersion
	if version <= 0 {
		if err := db.QueryRow(`SELECT version FROM strategies WHERE id = ? AND project_id = ?`,
			a.StrategyID, a.ProjectID).Scan(&version); err != nil {
			return 0, err
		}
	}
	var exists int
	if err := db.QueryRow(`
		SELECT 1
		  FROM strategy_versions v
		  JOIN strategies s ON s.id = v.strategy_id
		 WHERE s.id = ? AND s.project_id = ? AND v.version = ?`,
		a.StrategyID, a.ProjectID, version).Scan(&exists); err != nil {
		return 0, err
	}
	if err := dbUnassignStrategy(db, a.ProjectID, a.PortfolioID); err != nil {
		return 0, err
	}
	res, err := db.Exec(`
		INSERT INTO portfolio_strategy_assignments (
			project_id, portfolio_id, strategy_id, strategy_version, control_mode, status, assigned_agent_id, cadence
		) VALUES (?, ?, ?, ?, ?, 'active', NULLIF(?, 0), ?)`,
		a.ProjectID, a.PortfolioID, a.StrategyID, version, control, a.AssignedAgentID, cadence)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbActiveStrategyAssignment(db *sql.DB, projectID string, portfolioID int64) (*StrategyAssignment, error) {
	row := db.QueryRow(`
		SELECT id, project_id, portfolio_id, strategy_id, strategy_version, control_mode, status,
		       COALESCE(assigned_agent_id, 0), cadence, COALESCE(last_evaluated_at, ''),
		       COALESCE(last_market_bar_at, ''), COALESCE(last_seen_bar_at, ''), COALESCE(last_checked_at, ''),
		       created_at, updated_at
		FROM portfolio_strategy_assignments
		WHERE project_id = ? AND portfolio_id = ? AND status = 'active'
		ORDER BY id DESC LIMIT 1`, projectID, portfolioID)
	var a StrategyAssignment
	if err := row.Scan(&a.ID, &a.ProjectID, &a.PortfolioID, &a.StrategyID, &a.StrategyVersion, &a.ControlMode,
		&a.Status, &a.AssignedAgentID, &a.Cadence, &a.LastEvaluatedAt,
		&a.LastMarketBarAt, &a.LastSeenBarAt, &a.LastCheckedAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

func dbActiveStrategyAssignments(db *sql.DB) ([]*StrategyAssignment, error) {
	rows, err := db.Query(`
		SELECT id, project_id, portfolio_id, strategy_id, strategy_version, control_mode, status,
		       COALESCE(assigned_agent_id, 0), cadence, COALESCE(last_evaluated_at, ''),
		       COALESCE(last_market_bar_at, ''), COALESCE(last_seen_bar_at, ''), COALESCE(last_checked_at, ''),
		       created_at, updated_at
		FROM portfolio_strategy_assignments
		WHERE status = 'active'
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StrategyAssignment
	for rows.Next() {
		var a StrategyAssignment
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.PortfolioID, &a.StrategyID, &a.StrategyVersion, &a.ControlMode,
			&a.Status, &a.AssignedAgentID, &a.Cadence, &a.LastEvaluatedAt,
			&a.LastMarketBarAt, &a.LastSeenBarAt, &a.LastCheckedAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func dbActiveStrategyAssignmentsForProject(db *sql.DB, projectID string) ([]*StrategyAssignment, error) {
	rows, err := db.Query(`
		SELECT id, project_id, portfolio_id, strategy_id, strategy_version, control_mode, status,
		       COALESCE(assigned_agent_id, 0), cadence, COALESCE(last_evaluated_at, ''),
		       COALESCE(last_market_bar_at, ''), COALESCE(last_seen_bar_at, ''), COALESCE(last_checked_at, ''),
		       created_at, updated_at
		FROM portfolio_strategy_assignments
		WHERE project_id = ? AND status = 'active'
		ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StrategyAssignment
	for rows.Next() {
		var a StrategyAssignment
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.PortfolioID, &a.StrategyID, &a.StrategyVersion, &a.ControlMode,
			&a.Status, &a.AssignedAgentID, &a.Cadence, &a.LastEvaluatedAt,
			&a.LastMarketBarAt, &a.LastSeenBarAt, &a.LastCheckedAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func dbSetStrategyAssignmentObserved(db *sql.DB, id int64, marketBarAt, checkedAt time.Time) error {
	_, err := db.Exec(`
		UPDATE portfolio_strategy_assignments
		   SET last_seen_bar_at = ?, last_checked_at = ?
		 WHERE id = ?`, marketBarAt.UTC().Format(time.RFC3339), checkedAt.UTC().Format(time.RFC3339), id)
	return err
}

func dbInitializeStrategyAssignmentMarketBar(db *sql.DB, id int64, marketBarAt, checkedAt time.Time) error {
	_, err := db.Exec(`
		UPDATE portfolio_strategy_assignments
		   SET last_market_bar_at = ?, last_seen_bar_at = ?, last_checked_at = ?
		 WHERE id = ?`, marketBarAt.UTC().Format(time.RFC3339), marketBarAt.UTC().Format(time.RFC3339),
		checkedAt.UTC().Format(time.RFC3339), id)
	return err
}

func dbSetStrategyAssignmentEvaluated(db *sql.DB, id int64, evaluatedAt, marketBarAt, checkedAt time.Time) error {
	_, err := db.Exec(`
		UPDATE portfolio_strategy_assignments
		   SET last_evaluated_at = ?, last_market_bar_at = ?, last_seen_bar_at = ?, last_checked_at = ?,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, evaluatedAt.UTC().Format(time.RFC3339), marketBarAt.UTC().Format(time.RFC3339),
		marketBarAt.UTC().Format(time.RFC3339), checkedAt.UTC().Format(time.RFC3339), id)
	return err
}

func dbUnassignStrategy(db *sql.DB, projectID string, portfolioID int64) error {
	_, err := db.Exec(`
		UPDATE portfolio_strategy_assignments
		   SET status = 'inactive', updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND portfolio_id = ? AND status = 'active'`, projectID, portfolioID)
	return err
}

// ─── Backtests ─────────────────────────────────────────────────────

// dbClaimStrategyRun is the durable, exactly-once boundary for automated
// execution. The unique assignment/bar key prevents a restarted worker or a
// second process from submitting the same signal twice.
func dbClaimStrategyRun(db *sql.DB, event *StrategyRunEvent, decisions, targets any) (bool, error) {
	decisionsJSON, _ := json.Marshal(decisions)
	targetsJSON, _ := json.Marshal(targets)
	res, err := db.Exec(`
		INSERT INTO strategy_run_events (
			project_id, portfolio_id, assignment_id, strategy_id, strategy_version,
			signal_bar_at, status, decisions_json, targets_json
		) VALUES (?, ?, ?, ?, ?, ?, 'started', ?, ?)
		ON CONFLICT(assignment_id, signal_bar_at) DO NOTHING`,
		event.ProjectID, event.PortfolioID, event.AssignmentID, event.StrategyID,
		event.StrategyVersion, event.SignalBarAt.UTC().Format(time.RFC3339Nano),
		string(decisionsJSON), string(targetsJSON))
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	return rows == 1, err
}

func dbFinishStrategyRun(db *sql.DB, assignmentID int64, signalBarAt time.Time, status string, orderIDs []string, runErr error) error {
	orderIDsJSON, _ := json.Marshal(orderIDs)
	errorText := ""
	if runErr != nil {
		errorText = runErr.Error()
	}
	_, err := db.Exec(`
		UPDATE strategy_run_events
		   SET status = ?, order_ids_json = ?, error = ?, completed_at = CURRENT_TIMESTAMP
		 WHERE assignment_id = ? AND signal_bar_at = ?`,
		status, string(orderIDsJSON), errorText, assignmentID, signalBarAt.UTC().Format(time.RFC3339Nano))
	return err
}

func dbCreateBacktestRun(db *sql.DB, run *BacktestRun) (int64, error) {
	symbolsJSON, err := json.Marshal(run.Symbols)
	if err != nil {
		return 0, err
	}
	summaryJSON, _ := json.Marshal(run.Summary)
	if len(summaryJSON) == 0 {
		summaryJSON = []byte("{}")
	}
	status := strings.TrimSpace(run.Status)
	if status == "" {
		status = "queued"
	}
	res, err := db.Exec(`
		INSERT INTO backtest_runs (
			project_id, portfolio_id, source_agent_id, strategy_id, run_kind, strategy_version,
			name, status, symbols,
			start_at, end_at, interval, starting_cash, fee_bps, slippage_bps,
			total_steps, summary_json
		) VALUES (?, ?, ?, NULLIF(?, 0), ?, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ProjectID, run.PortfolioID, run.SourceAgentID, run.StrategyID,
		nonEmpty(run.RunKind, "agent"), run.StrategyVersion, run.Name, status, string(symbolsJSON),
		run.StartAt, run.EndAt, run.Interval, run.StartingCash, run.FeeBps, run.SlippageBps,
		run.TotalSteps, string(summaryJSON))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbGetBacktestRun(db *sql.DB, projectID string, id int64) (*BacktestRun, error) {
	row := db.QueryRow(`
		SELECT id, project_id, portfolio_id, source_agent_id,
		       COALESCE(strategy_id, 0), COALESCE(run_kind, 'agent'), COALESCE(strategy_version, 0),
		       COALESCE(environment_id, ''), COALESCE(environment_agent_id, 0),
		       COALESCE(environment_portfolio_id, 0), name, status, symbols,
		       start_at, end_at, interval, starting_cash, fee_bps, slippage_bps,
		       current_step, total_steps, summary_json, COALESCE(error, ''),
		       created_at, updated_at, COALESCE(completed_at, '')
		FROM backtest_runs WHERE id = ? AND project_id = ?`, id, projectID)
	return scanBacktestRun(row)
}

func dbListBacktestRuns(db *sql.DB, projectID string, portfolioID int64) ([]*BacktestRun, error) {
	rows, err := db.Query(`
		SELECT id, project_id, portfolio_id, source_agent_id,
		       COALESCE(strategy_id, 0), COALESCE(run_kind, 'agent'), COALESCE(strategy_version, 0),
		       COALESCE(environment_id, ''), COALESCE(environment_agent_id, 0),
		       COALESCE(environment_portfolio_id, 0), name, status, symbols,
		       start_at, end_at, interval, starting_cash, fee_bps, slippage_bps,
		       current_step, total_steps, summary_json, COALESCE(error, ''),
		       created_at, updated_at, COALESCE(completed_at, '')
		FROM backtest_runs
		WHERE project_id = ? AND (? = 0 OR portfolio_id = ?)
		ORDER BY id DESC`, projectID, portfolioID, portfolioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*BacktestRun{}
	for rows.Next() {
		run, err := scanBacktestRunRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func scanBacktestRun(row *sql.Row) (*BacktestRun, error) {
	var run BacktestRun
	var symbolsJSON, summaryJSON string
	if err := row.Scan(&run.ID, &run.ProjectID, &run.PortfolioID, &run.SourceAgentID,
		&run.StrategyID, &run.RunKind, &run.StrategyVersion,
		&run.EnvironmentID, &run.EnvironmentAgentID, &run.EnvironmentPortfolioID,
		&run.Name, &run.Status, &symbolsJSON, &run.StartAt, &run.EndAt, &run.Interval,
		&run.StartingCash, &run.FeeBps, &run.SlippageBps, &run.CurrentStep, &run.TotalSteps,
		&summaryJSON, &run.Error, &run.CreatedAt, &run.UpdatedAt, &run.CompletedAt); err != nil {
		return nil, err
	}
	decodeBacktestJSON(&run, symbolsJSON, summaryJSON)
	return &run, nil
}

func scanBacktestRunRows(rows *sql.Rows) (*BacktestRun, error) {
	var run BacktestRun
	var symbolsJSON, summaryJSON string
	if err := rows.Scan(&run.ID, &run.ProjectID, &run.PortfolioID, &run.SourceAgentID,
		&run.StrategyID, &run.RunKind, &run.StrategyVersion,
		&run.EnvironmentID, &run.EnvironmentAgentID, &run.EnvironmentPortfolioID,
		&run.Name, &run.Status, &symbolsJSON, &run.StartAt, &run.EndAt, &run.Interval,
		&run.StartingCash, &run.FeeBps, &run.SlippageBps, &run.CurrentStep, &run.TotalSteps,
		&summaryJSON, &run.Error, &run.CreatedAt, &run.UpdatedAt, &run.CompletedAt); err != nil {
		return nil, err
	}
	decodeBacktestJSON(&run, symbolsJSON, summaryJSON)
	return &run, nil
}

func decodeBacktestJSON(run *BacktestRun, symbolsJSON, summaryJSON string) {
	_ = json.Unmarshal([]byte(symbolsJSON), &run.Symbols)
	if run.Symbols == nil {
		run.Symbols = []string{}
	}
	_ = json.Unmarshal([]byte(summaryJSON), &run.Summary)
	if run.Summary == nil {
		run.Summary = map[string]any{}
	}
	if run.RunKind == "" {
		run.RunKind = "agent"
	}
}

func dbUpdateBacktestEnvironment(db *sql.DB, runID int64, environmentID string, environmentAgentID, environmentPortfolioID int64) error {
	_, err := db.Exec(`
		UPDATE backtest_runs
		   SET environment_id = ?, environment_agent_id = ?, environment_portfolio_id = ?,
		       status = 'running', updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, environmentID, environmentAgentID, environmentPortfolioID, runID)
	return err
}

func dbSetBacktestStatus(db *sql.DB, runID int64, status, errText string) error {
	completedExpr := "NULL"
	if status == "completed" || status == "failed" || status == "cancelled" {
		completedExpr = "CURRENT_TIMESTAMP"
	}
	_, err := db.Exec(fmt.Sprintf(`
		UPDATE backtest_runs
		   SET status = ?, error = NULLIF(?, ''), updated_at = CURRENT_TIMESTAMP, completed_at = %s
		 WHERE id = ?`, completedExpr), status, errText, runID)
	return err
}

func dbAdvanceBacktestStep(db *sql.DB, runID int64, step int, summary map[string]any, status string) error {
	summaryJSON, _ := json.Marshal(summary)
	completedExpr := "completed_at"
	if status == "completed" {
		completedExpr = "CURRENT_TIMESTAMP"
	}
	_, err := db.Exec(fmt.Sprintf(`
		UPDATE backtest_runs
		   SET current_step = ?, summary_json = ?, status = ?, updated_at = CURRENT_TIMESTAMP, completed_at = %s
		 WHERE id = ?`, completedExpr), step, string(summaryJSON), status, runID)
	return err
}

func dbInsertBacktestEvent(db *sql.DB, runID int64, kind, message string, data map[string]any) (int64, error) {
	raw, _ := json.Marshal(data)
	res, err := db.Exec(`
		INSERT INTO backtest_events (run_id, kind, message, data)
		VALUES (?, ?, ?, ?)`, runID, kind, message, string(raw))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbListBacktestEvents(db *sql.DB, runID int64, limit int) ([]*BacktestEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 80
	}
	rows, err := db.Query(`
		SELECT id, run_id, kind, message, data, created_at
		FROM backtest_events WHERE run_id = ?
		ORDER BY id DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*BacktestEvent{}
	for rows.Next() {
		var ev BacktestEvent
		var dataJSON string
		if err := rows.Scan(&ev.ID, &ev.RunID, &ev.Kind, &ev.Message, &dataJSON, &ev.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(dataJSON), &ev.Data)
		if ev.Data == nil {
			ev.Data = map[string]any{}
		}
		out = append(out, &ev)
	}
	return out, rows.Err()
}

func dbUpsertBacktestSnapshot(db *sql.DB, s *BacktestSnapshot) error {
	if s == nil {
		return errors.New("snapshot required")
	}
	positionsJSON, _ := json.Marshal(s.Positions)
	ordersJSON, _ := json.Marshal(s.Orders)
	pricesJSON, _ := json.Marshal(s.Prices)
	_, err := db.Exec(`
		INSERT INTO backtest_snapshots (
			run_id, step, equity, cash, buying_power, open_pnl, open_pnl_pct,
			realized_pnl, exposure, positions_json, orders_json, prices_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, step) DO UPDATE SET
			equity = excluded.equity,
			cash = excluded.cash,
			buying_power = excluded.buying_power,
			open_pnl = excluded.open_pnl,
			open_pnl_pct = excluded.open_pnl_pct,
			realized_pnl = excluded.realized_pnl,
			exposure = excluded.exposure,
			positions_json = excluded.positions_json,
			orders_json = excluded.orders_json,
			prices_json = excluded.prices_json,
			updated_at = CURRENT_TIMESTAMP`,
		s.RunID, s.Step, s.Equity, s.Cash, s.BuyingPower, s.OpenPnL, s.OpenPnLPct,
		s.RealizedPnL, s.Exposure, string(positionsJSON), string(ordersJSON), string(pricesJSON))
	return err
}

func dbListBacktestSnapshots(db *sql.DB, runID int64) ([]*BacktestSnapshot, error) {
	rows, err := db.Query(`
		SELECT run_id, step, equity, cash, buying_power, open_pnl, open_pnl_pct,
		       realized_pnl, exposure, positions_json, orders_json, prices_json,
		       created_at, updated_at
		FROM backtest_snapshots
		WHERE run_id = ?
		ORDER BY step ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*BacktestSnapshot{}
	for rows.Next() {
		var s BacktestSnapshot
		var positionsJSON, ordersJSON, pricesJSON string
		if err := rows.Scan(&s.RunID, &s.Step, &s.Equity, &s.Cash, &s.BuyingPower,
			&s.OpenPnL, &s.OpenPnLPct, &s.RealizedPnL, &s.Exposure,
			&positionsJSON, &ordersJSON, &pricesJSON, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(positionsJSON), &s.Positions)
		_ = json.Unmarshal([]byte(ordersJSON), &s.Orders)
		_ = json.Unmarshal([]byte(pricesJSON), &s.Prices)
		if s.Positions == nil {
			s.Positions = []*Position{}
		}
		if s.Orders == nil {
			s.Orders = []*Order{}
		}
		if s.Prices == nil {
			s.Prices = []map[string]any{}
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

type BacktestMarketBar struct {
	RunID         int64
	Step          int
	Symbol        string
	AssetClass    string
	T             int64
	O             float64
	H             float64
	L             float64
	C             float64
	V             float64
	Source        string
	VolumeUnit    string
	TimestampKind string
}

func dbReplaceBacktestMarketBars(db *sql.DB, runID int64, bars []*BacktestMarketBar) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM backtest_market_bars WHERE run_id = ?`, runID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO backtest_market_bars (
			run_id, step, symbol, asset_class, t, o, h, l, c, v, source, volume_unit, timestamp_kind
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, b := range bars {
		if b == nil {
			continue
		}
		if b.VolumeUnit == "" {
			b.VolumeUnit = historicalVolumeUnit(b.Source, b.AssetClass)
		}
		if b.TimestampKind == "" {
			b.TimestampKind = "exchange"
		}
		if _, err := stmt.Exec(runID, b.Step, b.Symbol, b.AssetClass, b.T, b.O, b.H, b.L, b.C, b.V, b.Source, b.VolumeUnit, b.TimestampKind); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func dbBacktestMarketMarks(db *sql.DB, runID int64, step int, symbols []string) ([]map[string]any, error) {
	rows, err := db.Query(`
		SELECT symbol, asset_class, t, o, h, l, c, v, source, volume_unit, timestamp_kind
		FROM backtest_market_bars
		WHERE run_id = ? AND step = ?
		ORDER BY symbol ASC`, runID, step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bySymbol := map[string]map[string]any{}
	for rows.Next() {
		var symbol, cls, source, volumeUnit, timestampKind string
		var t int64
		var o, h, l, c, v float64
		if err := rows.Scan(&symbol, &cls, &t, &o, &h, &l, &c, &v, &source, &volumeUnit, &timestampKind); err != nil {
			return nil, err
		}
		bySymbol[strings.ToUpper(symbol)] = map[string]any{
			"symbol": symbol, "asset_class": cls, "time": time.Unix(t, 0).UTC().Format(time.RFC3339),
			"open": o, "high": h, "low": l, "price": c, "close": c, "volume": v, "source": source,
			"volume_unit": volumeUnit, "timestamp_kind": timestampKind,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(symbols))
	for _, symbol := range cleanSymbols(symbols) {
		row := bySymbol[strings.ToUpper(symbol)]
		if row == nil {
			return nil, fmt.Errorf("missing real market bar for %s at step %d", symbol, step)
		}
		out = append(out, row)
	}
	return out, nil
}

func dbBacktestMarketStepTime(db *sql.DB, runID int64, step int) (time.Time, bool) {
	var unixSeconds int64
	if err := db.QueryRow(`SELECT MIN(t) FROM backtest_market_bars WHERE run_id = ? AND step = ?`, runID, step).Scan(&unixSeconds); err != nil || unixSeconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(unixSeconds, 0).UTC(), true
}

func dbBacktestMarketHistory(db *sql.DB, runID int64, throughStep int) ([]*BacktestMarketBar, error) {
	rows, err := db.Query(`
		SELECT run_id, step, symbol, asset_class, t, o, h, l, c, v, source, volume_unit, timestamp_kind
		FROM backtest_market_bars
		WHERE run_id = ? AND step <= ?
		ORDER BY step ASC, symbol ASC`, runID, throughStep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*BacktestMarketBar{}
	for rows.Next() {
		var bar BacktestMarketBar
		if err := rows.Scan(&bar.RunID, &bar.Step, &bar.Symbol, &bar.AssetClass, &bar.T,
			&bar.O, &bar.H, &bar.L, &bar.C, &bar.V, &bar.Source, &bar.VolumeUnit, &bar.TimestampKind); err != nil {
			return nil, err
		}
		out = append(out, &bar)
	}
	return out, rows.Err()
}

// ─── Watchlist ─────────────────────────────────────────────────────

func dbWatchlistAdd(db *sql.DB, projectID string, portfolioID int64, symbol string) (bool, error) {
	res, err := db.Exec(`
		INSERT OR IGNORE INTO watchlist (project_id, portfolio_id, symbol)
		VALUES (?, ?, ?)`, projectID, portfolioID, symbol)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func dbWatchlistRemove(db *sql.DB, portfolioID int64, symbol string) (bool, error) {
	res, err := db.Exec(`DELETE FROM watchlist WHERE portfolio_id = ? AND symbol = ?`, portfolioID, symbol)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func dbWatchlist(db *sql.DB, portfolioID int64) ([]string, error) {
	rows, err := db.Query(`SELECT symbol FROM watchlist WHERE portfolio_id = ? ORDER BY added_at`, portfolioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ─── Alerts ────────────────────────────────────────────────────────

func dbInsertAlert(db *sql.DB, projectID string, a *Alert) (int64, error) {
	var expiresArg any
	if a.ExpiresAt != "" {
		expiresArg = a.ExpiresAt
	}
	res, err := db.Exec(`
		INSERT INTO alerts (project_id, portfolio_id, symbol, rule, threshold, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, a.PortfolioID, a.Symbol, a.Rule, a.Threshold, expiresArg)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbActiveAlerts(db *sql.DB) ([]*Alert, error) {
	rows, err := db.Query(`
		SELECT id, portfolio_id, symbol, rule, threshold, status,
		       COALESCE(expires_at, ''), created_at, COALESCE(fired_at, '')
		FROM alerts WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.PortfolioID, &a.Symbol, &a.Rule, &a.Threshold,
			&a.Status, &a.ExpiresAt, &a.CreatedAt, &a.FiredAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func dbFireAlert(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE alerts SET status='fired', fired_at=CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// ─── Day baselines ─────────────────────────────────────────────────

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

func dbSetDayBaseline(db *sql.DB, portfolioID int64, utcDay string, equity float64) error {
	_, err := db.Exec(`
		INSERT INTO day_baselines (portfolio_id, utc_day, equity) VALUES (?, ?, ?)
		ON CONFLICT(portfolio_id, utc_day) DO UPDATE SET equity = excluded.equity`,
		portfolioID, utcDay, equity)
	return err
}

// ─── Helpers ───────────────────────────────────────────────────────

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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func utcDay(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// snapshotPortfolio computes equity, day P&L, open P&L, weights for a
// portfolio by joining current marks against open positions. Pure read,
// no DB writes.
func snapshotPortfolio(db *sql.DB, p *Portfolio) (*Portfolio, error) {
	marks, err := dbMarksBySymbol(db)
	if err != nil {
		return nil, err
	}
	return snapshotPortfolioWithMarks(db, p, marks)
}

func snapshotPortfolioWithMarks(db *sql.DB, p *Portfolio, marks map[string]*Mark) (*Portfolio, error) {
	pos, err := dbListPositions(db, p.ID)
	if err != nil {
		return nil, err
	}
	var openValue, openCost, openDay float64
	for _, q := range pos {
		mark := marks[strings.ToUpper(q.Symbol)]
		if mark == nil {
			// No mark yet — use avg_cost (assume flat).
			q.MarketPrice = q.AvgCost
		} else {
			q.MarketPrice = markPriceForSide(mark, q.Outcome)
		}
		q.MarketValue = q.MarketPrice * q.Qty
		q.UnrealizedPnL = (q.MarketPrice - q.AvgCost) * q.Qty
		if q.AvgCost > 0 && q.Qty > 0 {
			q.UnrealizedPnLPct = (q.MarketPrice/q.AvgCost - 1) * 100
		}
		openValue += q.MarketValue
		openCost += q.AvgCost * q.Qty
	}
	equity := p.Cash + openValue
	for _, q := range pos {
		if equity > 0 {
			q.WeightPct = q.MarketValue / equity * 100
		}
	}

	// Day P&L from baseline. If there's no row yet, treat now as the baseline.
	day := utcDay(time.Now())
	baseline, ok, _ := dbGetDayBaseline(db, p.ID, day)
	if !ok {
		baseline = equity
		_ = dbSetDayBaseline(db, p.ID, day, equity)
	}
	openDay = equity - baseline

	p.Equity = equity
	p.DayPnL = openDay
	if baseline > 0 {
		p.DayPnLPct = openDay / baseline * 100
	}
	p.OpenPnL = openValue - openCost
	if openCost > 0 {
		p.OpenPnLPct = p.OpenPnL / openCost * 100
	}
	p.RealizedPnL, p.FeesPaid, _ = dbPortfolioAccounting(db, p.ID)
	p.TotalPnL = p.RealizedPnL + p.OpenPnL
	returnBasis := p.StartingCash
	if p.Mode == "live" {
		// Live portfolios are seeded with broker cash plus pre-existing
		// holdings. Their starting_cash is therefore not starting equity;
		// current cost basis is the defensible denominator.
		returnBasis = equity - p.TotalPnL
	}
	if returnBasis > 0 {
		p.TotalPnLPct = p.TotalPnL / returnBasis * 100
	}
	p.BuyingPower = p.Cash
	if p.Mode == "live" && p.AvailableCash != nil {
		p.BuyingPower = *p.AvailableCash
	}
	wl, _ := dbWatchlist(db, p.ID)
	if wl == nil {
		wl = []string{}
	}
	p.Watchlist = wl
	return p, nil
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
	pos, err := dbListPositions(db, p.ID)
	if err != nil {
		return 0, err
	}
	value := p.Cash
	marks, err := dbMarksBySymbol(db)
	if err != nil {
		return 0, err
	}
	for _, q := range pos {
		mark := marks[strings.ToUpper(q.Symbol)]
		if mark == nil {
			continue
		}
		value += markPriceForSide(mark, q.Outcome) * q.Qty
	}
	return value, nil
}

func dbMarksBySymbol(db *sql.DB) (map[string]*Mark, error) {
	marks, err := dbListMarks(db)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Mark, len(marks))
	for _, mark := range marks {
		if mark != nil {
			out[strings.ToUpper(mark.Symbol)] = mark
		}
	}
	return out, nil
}
