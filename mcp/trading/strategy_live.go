package main

import (
	"database/sql"
	"net/http"
	"sort"
	"strings"
)

// Live strategy rollup — what the dashboard widget reads.
//
// One row per (strategy, portfolio) pair, because the same strategy assigned
// to a paper and a live portfolio is two different things to watch. Realized
// P&L comes from the strategy's own lot book; unrealized marks that book's
// open lots at the current price rather than the portfolio's blend.

type StrategyLivePosition struct {
	Symbol        string  `json:"symbol"`
	Outcome       string  `json:"outcome,omitempty"`
	Qty           float64 `json:"qty"`
	AvgCost       float64 `json:"avg_cost"`
	Mark          float64 `json:"mark,omitempty"`
	MarketValue   float64 `json:"market_value"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
	Shared        bool    `json:"shared,omitempty"` // another book also trades this symbol
}

type StrategyLiveRow struct {
	StrategyID      int64  `json:"strategy_id"`
	StrategyName    string `json:"strategy_name"`
	StrategyStatus  string `json:"strategy_status"`
	StrategyVersion int    `json:"strategy_version"`

	PortfolioID          int64  `json:"portfolio_id"`
	PortfolioName        string `json:"portfolio_name"`
	Mode                 string `json:"mode"`
	ExecutionEnvironment string `json:"execution_environment"`
	LiveArmed            bool   `json:"live_armed"`
	BrokerSlug           string `json:"broker_slug,omitempty"`

	AssignmentID     int64  `json:"assignment_id,omitempty"`
	AssignmentStatus string `json:"assignment_status"`
	ControlMode      string `json:"control_mode,omitempty"`
	Cadence          string `json:"cadence,omitempty"`
	Eligibility      string `json:"eligibility,omitempty"`
	LastEvaluatedAt  string `json:"last_evaluated_at,omitempty"`
	NextEligibleAt   string `json:"next_eligible_at,omitempty"`

	GrossRealizedPnL float64 `json:"gross_realized_pnl"`
	FeesPaid         float64 `json:"fees_paid"`
	SpreadCost       float64 `json:"spread_cost"`
	SlippageCost     float64 `json:"slippage_cost"`
	ExecutionCost    float64 `json:"execution_cost"`
	RealizedPnL      float64 `json:"realized_pnl"`
	UnrealizedPnL    float64 `json:"unrealized_pnl"`
	TotalPnL         float64 `json:"total_pnl"`
	MarketValue      float64 `json:"market_value"`
	NotionalTraded   float64 `json:"notional_traded"`
	FillCount        int     `json:"fill_count"`
	LastFillAt       string  `json:"last_fill_at,omitempty"`

	RunsCompleted       int    `json:"runs_completed"`
	RunsOrdersSubmitted int    `json:"runs_orders_submitted"`
	RunsFailed          int    `json:"runs_failed"`
	LastRunAt           string `json:"last_run_at,omitempty"`
	LastRunStatus       string `json:"last_run_status,omitempty"`
	LastRunError        string `json:"last_run_error,omitempty"`

	// SharedSymbols counts symbols this strategy trades that another book
	// also touches. Above zero, realized P&L depends on the strategy-own
	// close convention and the UI should say so rather than imply one number.
	SharedSymbols int                    `json:"shared_symbols"`
	Positions     []StrategyLivePosition `json:"positions,omitempty"`
}

type strategyAccountingRow struct {
	portfolioID int64
	strategyID  int64
	symbol      string
	outcome     string
	openQty     float64
	avgCost     float64
	gross       float64
	fees        float64
	spread      float64
	slippage    float64
	notional    float64
	fillCount   int
	lastFillAt  string
}

func dbListStrategyAccounting(db *sql.DB, projectID string) ([]strategyAccountingRow, error) {
	rows, err := db.Query(`
		SELECT a.portfolio_id, a.strategy_id, a.symbol, a.outcome, a.open_qty, a.avg_cost,
		       a.gross_realized_pnl, a.fees_paid, a.spread_cost, a.slippage_cost,
		       a.notional_traded, a.fill_count, COALESCE(a.last_fill_at, '')
		FROM strategy_position_accounting a
		JOIN portfolios p ON p.id = a.portfolio_id
		WHERE p.project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []strategyAccountingRow
	for rows.Next() {
		var r strategyAccountingRow
		if err := rows.Scan(&r.portfolioID, &r.strategyID, &r.symbol, &r.outcome, &r.openQty, &r.avgCost,
			&r.gross, &r.fees, &r.spread, &r.slippage, &r.notional, &r.fillCount, &r.lastFillAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type strategyRunHealth struct {
	completed, ordersSubmitted, failed int
	lastAt, lastStatus, lastError      string
}

func dbStrategyRunHealth(db *sql.DB, projectID string) (map[[2]int64]*strategyRunHealth, error) {
	rows, err := db.Query(`
		SELECT e.portfolio_id, e.strategy_id, e.status, COALESCE(e.started_at, ''), COALESCE(e.error, '')
		FROM strategy_run_events e
		WHERE e.project_id = ?
		ORDER BY e.started_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]int64]*strategyRunHealth{}
	for rows.Next() {
		var portfolioID, strategyID int64
		var status, startedAt, runErr string
		if err := rows.Scan(&portfolioID, &strategyID, &status, &startedAt, &runErr); err != nil {
			return nil, err
		}
		key := [2]int64{portfolioID, strategyID}
		h := out[key]
		if h == nil {
			h = &strategyRunHealth{}
			out[key] = h
		}
		switch status {
		case "completed":
			h.completed++
		case "orders_submitted":
			h.ordersSubmitted++
		case "failed":
			h.failed++
		}
		// Rows arrive oldest-first, so the last write wins and holds the latest run.
		h.lastAt, h.lastStatus, h.lastError = startedAt, status, runErr
	}
	return out, rows.Err()
}

// dbStrategyLiveRows assembles the rollup. Strategies that have traded appear
// even once their assignment is gone — a retired strategy still owns its P&L.
func dbStrategyLiveRows(db *sql.DB, projectID string, includeUnattributed bool) ([]*StrategyLiveRow, error) {
	portfolios, err := dbListPortfolios(db, projectID)
	if err != nil {
		return nil, err
	}
	byPortfolio := map[int64]*Portfolio{}
	for _, p := range portfolios {
		byPortfolio[p.ID] = p
	}
	strategies, err := dbListStrategies(db, projectID, "")
	if err != nil {
		return nil, err
	}
	byStrategy := map[int64]*Strategy{}
	for _, s := range strategies {
		byStrategy[s.ID] = s
	}
	assignments, err := dbActiveStrategyAssignmentsForProject(db, projectID)
	if err != nil {
		return nil, err
	}
	byAssignment := map[[2]int64]*StrategyAssignment{}
	for _, a := range assignments {
		byAssignment[[2]int64{a.PortfolioID, a.StrategyID}] = a
	}
	accounting, err := dbListStrategyAccounting(db, projectID)
	if err != nil {
		return nil, err
	}
	health, err := dbStrategyRunHealth(db, projectID)
	if err != nil {
		return nil, err
	}
	marks, err := dbMarksBySymbol(db)
	if err != nil {
		return nil, err
	}

	// A symbol is shared when more than one book (strategy or the
	// unattributed bucket) has traded it inside the same portfolio.
	owners := map[[2]interface{}]map[int64]bool{}
	ownerKey := func(portfolioID int64, symbol string) [2]interface{} {
		return [2]interface{}{portfolioID, symbol}
	}
	for _, r := range accounting {
		key := ownerKey(r.portfolioID, r.symbol)
		if owners[key] == nil {
			owners[key] = map[int64]bool{}
		}
		owners[key][r.strategyID] = true
	}

	rowsByKey := map[[2]int64]*StrategyLiveRow{}
	for _, r := range accounting {
		if r.strategyID == 0 && !includeUnattributed {
			continue
		}
		portfolio := byPortfolio[r.portfolioID]
		if portfolio == nil {
			continue
		}
		key := [2]int64{r.portfolioID, r.strategyID}
		row := rowsByKey[key]
		if row == nil {
			row = &StrategyLiveRow{
				StrategyID:           r.strategyID,
				StrategyName:         "Unattributed",
				StrategyStatus:       "unattributed",
				PortfolioID:          portfolio.ID,
				PortfolioName:        portfolio.Name,
				Mode:                 portfolio.Mode,
				ExecutionEnvironment: normalizeExecutionEnvironment(portfolio.ExecutionEnvironment, portfolio.Mode, portfolio.BrokerSlug),
				LiveArmed:            portfolio.LiveArmed,
				BrokerSlug:           portfolio.BrokerSlug,
				AssignmentStatus:     "unassigned",
			}
			if strategy := byStrategy[r.strategyID]; strategy != nil {
				row.StrategyName = strategy.Name
				row.StrategyStatus = strategy.Status
				row.StrategyVersion = strategy.Version
			}
			if a := byAssignment[key]; a != nil {
				row.AssignmentID = a.ID
				row.AssignmentStatus = a.Status
				row.ControlMode = a.ControlMode
				row.Cadence = a.Cadence
				row.Eligibility = a.Eligibility
				row.LastEvaluatedAt = a.LastEvaluatedAt
				row.NextEligibleAt = a.NextEligibleAt
			}
			if h := health[key]; h != nil {
				row.RunsCompleted = h.completed
				row.RunsOrdersSubmitted = h.ordersSubmitted
				row.RunsFailed = h.failed
				row.LastRunAt = h.lastAt
				row.LastRunStatus = h.lastStatus
				row.LastRunError = h.lastError
			}
			rowsByKey[key] = row
		}

		row.GrossRealizedPnL += r.gross
		row.FeesPaid += r.fees
		row.SpreadCost += r.spread
		row.SlippageCost += r.slippage
		row.NotionalTraded += r.notional
		row.FillCount += r.fillCount
		if r.lastFillAt > row.LastFillAt {
			row.LastFillAt = r.lastFillAt
		}

		shared := len(owners[ownerKey(r.portfolioID, r.symbol)]) > 1
		if shared {
			row.SharedSymbols++
		}
		if r.openQty > strategyLotEpsilon {
			position := StrategyLivePosition{
				Symbol:  r.symbol,
				Outcome: r.outcome,
				Qty:     r.openQty,
				AvgCost: r.avgCost,
				Shared:  shared,
			}
			if mark := marks[r.symbol]; mark != nil && mark.Price > 0 {
				position.Mark = mark.Price
				position.MarketValue = r.openQty * mark.Price
				position.UnrealizedPnL = (mark.Price - r.avgCost) * r.openQty
			}
			row.MarketValue += position.MarketValue
			row.UnrealizedPnL += position.UnrealizedPnL
			row.Positions = append(row.Positions, position)
		}
	}

	out := make([]*StrategyLiveRow, 0, len(rowsByKey))
	for _, row := range rowsByKey {
		row.ExecutionCost = row.FeesPaid + row.SpreadCost + row.SlippageCost
		row.RealizedPnL = row.GrossRealizedPnL - row.FeesPaid
		row.TotalPnL = row.RealizedPnL + row.UnrealizedPnL
		sort.Slice(row.Positions, func(i, j int) bool {
			return row.Positions[i].Symbol < row.Positions[j].Symbol
		})
		out = append(out, row)
	}
	// Live money first, then biggest P&L swing, then a stable name tiebreak.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ExecutionEnvironment != b.ExecutionEnvironment {
			return a.ExecutionEnvironment == "broker_live"
		}
		if absFloat(a.TotalPnL) != absFloat(b.TotalPnL) {
			return absFloat(a.TotalPnL) > absFloat(b.TotalPnL)
		}
		if a.StrategyName != b.StrategyName {
			return a.StrategyName < b.StrategyName
		}
		return a.PortfolioID < b.PortfolioID
	})
	return out, nil
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// handleHTTPStrategiesLive serves GET /strategies/live for the dashboard widget.
func (a *App) handleHTTPStrategiesLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	query := r.URL.Query()
	includeUnattributed := strings.EqualFold(query.Get("include_unattributed"), "true")
	rows, err := dbStrategyLiveRows(globalCtx.AppDB(), pid, includeUnattributed)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if scope := strings.ToLower(strings.TrimSpace(query.Get("scope"))); scope == "live" {
		filtered := rows[:0]
		for _, row := range rows {
			if row.ExecutionEnvironment == "broker_live" {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	totals := map[string]any{"realized_pnl": 0.0, "unrealized_pnl": 0.0, "total_pnl": 0.0, "execution_cost": 0.0}
	for _, row := range rows {
		totals["realized_pnl"] = totals["realized_pnl"].(float64) + row.RealizedPnL
		totals["unrealized_pnl"] = totals["unrealized_pnl"].(float64) + row.UnrealizedPnL
		totals["total_pnl"] = totals["total_pnl"].(float64) + row.TotalPnL
		totals["execution_cost"] = totals["execution_cost"].(float64) + row.ExecutionCost
	}
	httpJSON(w, 200, map[string]any{"strategies": rows, "totals": totals, "count": len(rows)})
}
