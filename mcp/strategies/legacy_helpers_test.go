package main

// Test-only copies of Trading execution helpers. Strategies no longer executes
// or attributes fills itself, but its legacy fixtures replay old Trading
// behaviour (exec_legacy_test.go, store_legacy_test.go, trading_fixture_test.go).

import (
	"database/sql"
	"errors"
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
