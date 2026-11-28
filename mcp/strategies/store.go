package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"strings"
	"time"
)

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
	FundingPaid float64  `json:"funding_paid,omitempty"`
	TotalPnL    float64  `json:"total_pnl,omitempty"`
	TotalPnLPct float64  `json:"total_pnl_pct,omitempty"`
	BuyingPower float64  `json:"buying_power,omitempty"`
	Watchlist   []string `json:"watchlist,omitempty"`
}

type Position struct {
	Symbol           string  `json:"symbol"`
	SecurityID       string  `json:"security_id,omitempty"`
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
	SecurityID      string   `json:"security_id,omitempty"`
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
	LiquidityRole   string   `json:"liquidity_role,omitempty"`
	VenueFeeBps     float64  `json:"-"`
	RejectionCode   string   `json:"rejection_code,omitempty"`
	RejectionDetail string   `json:"rejection_detail,omitempty"`
	PlacedAt        string   `json:"placed_at"`
	ResolvedAt      string   `json:"resolved_at,omitempty"`
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
	AdjustmentMode         string         `json:"adjustment_mode"`
	ReferenceManifest      map[string]any `json:"reference_manifest,omitempty"`
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

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

type insertOnlyInstrument struct{ sqlExecer }

func (e insertOnlyInstrument) Exec(query string, args ...any) (sql.Result, error) {
	if i := strings.Index(query, "ON CONFLICT(symbol)"); i >= 0 {
		query = query[:i] + " ON CONFLICT(symbol) DO NOTHING"
	}
	return e.sqlExecer.Exec(query, args...)
}

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
		if _, err := tx.Exec(`UPDATE strategy_scorecard_policies SET promotion_stage='research' WHERE strategy_id=?`, id); err != nil {
			return nil, err
		}
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
	if run.Summary == nil {
		run.Summary = map[string]any{}
	}
	// Freeze the portfolio's current execution policy from Trading.
	if run.Summary["execution_policy"] == nil {
		if globalCtx == nil {
			return 0, errors.New("Trading is unavailable to capture the execution policy")
		}
		_, policy, err := captureReplayPolicy(globalCtx, run.ProjectID, run.PortfolioID, run.Symbols)
		if err != nil {
			return 0, err
		}
		run.Summary["execution_policy"] = policy
	}
	run.Summary["execution_kernel_version"] = "2"
	symbolsJSON, err := json.Marshal(run.Symbols)
	if err != nil {
		return 0, err
	}
	summaryJSON, _ := json.Marshal(run.Summary)
	manifestJSON, _ := json.Marshal(run.ReferenceManifest)
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
			total_steps, summary_json, adjustment_mode, reference_manifest_json
		) VALUES (?, ?, ?, NULLIF(?, 0), ?, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ProjectID, run.PortfolioID, run.SourceAgentID, run.StrategyID,
		nonEmpty(run.RunKind, "agent"), run.StrategyVersion, run.Name, status, string(symbolsJSON),
		run.StartAt, run.EndAt, run.Interval, run.StartingCash, run.FeeBps, run.SlippageBps,
		run.TotalSteps, string(summaryJSON), nonEmpty(run.AdjustmentMode, "provider_adjusted"), string(manifestJSON))
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
		       current_step, total_steps, summary_json, adjustment_mode, reference_manifest_json, COALESCE(error, ''),
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
		       current_step, total_steps, summary_json, adjustment_mode, reference_manifest_json, COALESCE(error, ''),
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
	var symbolsJSON, summaryJSON, manifestJSON string
	if err := row.Scan(&run.ID, &run.ProjectID, &run.PortfolioID, &run.SourceAgentID,
		&run.StrategyID, &run.RunKind, &run.StrategyVersion,
		&run.EnvironmentID, &run.EnvironmentAgentID, &run.EnvironmentPortfolioID,
		&run.Name, &run.Status, &symbolsJSON, &run.StartAt, &run.EndAt, &run.Interval,
		&run.StartingCash, &run.FeeBps, &run.SlippageBps, &run.CurrentStep, &run.TotalSteps,
		&summaryJSON, &run.AdjustmentMode, &manifestJSON, &run.Error, &run.CreatedAt, &run.UpdatedAt, &run.CompletedAt); err != nil {
		return nil, err
	}
	decodeBacktestJSON(&run, symbolsJSON, summaryJSON, manifestJSON)
	return &run, nil
}

func scanBacktestRunRows(rows *sql.Rows) (*BacktestRun, error) {
	var run BacktestRun
	var symbolsJSON, summaryJSON, manifestJSON string
	if err := rows.Scan(&run.ID, &run.ProjectID, &run.PortfolioID, &run.SourceAgentID,
		&run.StrategyID, &run.RunKind, &run.StrategyVersion,
		&run.EnvironmentID, &run.EnvironmentAgentID, &run.EnvironmentPortfolioID,
		&run.Name, &run.Status, &symbolsJSON, &run.StartAt, &run.EndAt, &run.Interval,
		&run.StartingCash, &run.FeeBps, &run.SlippageBps, &run.CurrentStep, &run.TotalSteps,
		&summaryJSON, &run.AdjustmentMode, &manifestJSON, &run.Error, &run.CreatedAt, &run.UpdatedAt, &run.CompletedAt); err != nil {
		return nil, err
	}
	decodeBacktestJSON(&run, symbolsJSON, summaryJSON, manifestJSON)
	return &run, nil
}

func decodeBacktestJSON(run *BacktestRun, symbolsJSON, summaryJSON, manifestJSON string) {
	_ = json.Unmarshal([]byte(symbolsJSON), &run.Symbols)
	if run.Symbols == nil {
		run.Symbols = []string{}
	}
	_ = json.Unmarshal([]byte(summaryJSON), &run.Summary)
	if run.Summary == nil {
		run.Summary = map[string]any{}
	}
	_ = json.Unmarshal([]byte(manifestJSON), &run.ReferenceManifest)
	if run.ReferenceManifest == nil {
		run.ReferenceManifest = map[string]any{}
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
		 WHERE id = ? AND (status!='cancelled' OR ?='cancelled') AND (status!='paused' OR ?!='failed')`, completedExpr), status, errText, runID, status, status)
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
		   SET current_step = ?, summary_json = json_patch(COALESCE(summary_json,'{}'), ?), status = ?, updated_at = CURRENT_TIMESTAMP, completed_at = %s
		 WHERE id = ? AND status NOT IN ('paused','cancelled') AND current_step <= ?`, completedExpr), step, string(summaryJSON), status, runID, step)
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

func dbUpsertBacktestSnapshot(db sqlExecer, s *BacktestSnapshot) error {
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
	var status string
	if err := tx.QueryRow(`SELECT status FROM backtest_runs WHERE id=?`, runID).Scan(&status); err != nil {
		return err
	}
	if status != "queued" && status != "failed" {
		return errors.New("cannot replace dataset after replay starts")
	}
	if _, err := tx.Exec(`DELETE FROM backtest_market_bars WHERE run_id = ?`, runID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO backtest_market_bars (
			run_id, step, symbol, security_id, asset_class, t, o, h, l, c, v, source, volume_unit, timestamp_kind
		) VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
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
	metadata, _ := json.Marshal(map[string]any{"dataset_sha256": backtestMarketBarsChecksum(bars)})
	if _, err := tx.Exec(`UPDATE backtest_runs SET summary_json=json_patch(COALESCE(summary_json,'{}'),?) WHERE id=?`, string(metadata), runID); err != nil {
		return err
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
