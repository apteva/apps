package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Strategy-level P&L attribution.
//
// The portfolio book (positions + position_accounting) answers "what does this
// portfolio own and what has it made". It cannot answer the same question per
// strategy, because a portfolio may carry several active assignments and its
// average cost blends every buyer of a symbol. strategy_position_accounting
// keeps a parallel average-cost book scoped to the strategy that originated
// each fill, read from orders.strategy_id (migration 018).
//
// Convention: a strategy closes against its own lots, never the portfolio's
// blend. Where a symbol has exactly one owner the two books agree exactly.
// Where several owners overlap they diverge by construction — that is the
// point — and StrategyLiveRow.SharedSymbols reports the overlap so a caller
// can say so rather than imply a single number.
//
// strategy_id 0 collects manual, agent and imported broker fills, so the
// strategy books plus bucket 0 reconcile against the portfolio book.

// strategyLotEpsilon matches the position engine's close tolerance so a lot
// that is fully sold rounds to flat instead of leaving dust behind.
const strategyLotEpsilon = 1e-9

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
		// A strategy may sell more than its own book holds when an operator
		// closed part of the position by hand, or when broker history was
		// imported after the opening buy. Realize only what this strategy can
		// account for; the remainder belongs to whoever opened it.
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

// dbRebuildStrategyAttribution replays the append-only fills ledger into the
// strategy book. It is a full rebuild rather than a backfill: every row is
// derived, so replaying is both idempotent and self-healing, and it recovers
// history for installs that traded before this table existed.
//
// Unlike position_accounting this cannot recover imported opening balances or
// corporate actions, which never pass through fills. Those stay in the
// portfolio book and show up as the gap between it and the strategy books.
func dbRebuildStrategyAttribution(db *sql.DB) error {
	type fillRow struct {
		portfolioID  int64
		strategyID   int64
		symbol       string
		outcome      string
		side         string
		qty          float64
		price        float64
		fee          float64
		spreadCost   float64
		slippageCost float64
		filledAt     string
	}
	rows, err := db.Query(`
		SELECT f.portfolio_id, COALESCE(o.strategy_id, 0), o.symbol,
		       CASE
		         WHEN o.asset_class = 'polymarket' THEN COALESCE(NULLIF(o.outcome, ''), UPPER(o.side))
		         ELSE ''
		       END,
		       o.side, f.qty, f.price, f.fee,
		       COALESCE(f.spread_cost, 0), COALESCE(f.slippage_cost, 0),
		       COALESCE(f.filled_at, '')
		FROM fills f
		JOIN orders o ON o.id = f.order_id
		ORDER BY f.portfolio_id, f.filled_at, f.id`)
	if err != nil {
		return err
	}
	var fills []fillRow
	for rows.Next() {
		var f fillRow
		if err := rows.Scan(&f.portfolioID, &f.strategyID, &f.symbol, &f.outcome, &f.side,
			&f.qty, &f.price, &f.fee, &f.spreadCost, &f.slippageCost, &f.filledAt); err != nil {
			rows.Close()
			return err
		}
		fills = append(fills, f)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	type lot struct {
		openQty, avgCost           float64
		gross, fees                float64
		spread, slippage, notional float64
		fillCount                  int
		firstAt, lastAt            string
	}
	lots := map[string]*lot{}
	keyFor := func(f fillRow) string {
		return fmt.Sprintf("%d\x00%d\x00%s\x00%s", f.portfolioID, f.strategyID, f.symbol, f.outcome)
	}
	for _, f := range fills {
		key := keyFor(f)
		l := lots[key]
		if l == nil {
			l = &lot{firstAt: f.filledAt}
			lots[key] = l
		}
		l.fees += f.fee
		l.spread += f.spreadCost
		l.slippage += f.slippageCost
		l.notional += f.qty * f.price
		l.fillCount++
		l.lastAt = f.filledAt
		if isBuySide(f.side) {
			newQty := l.openQty + f.qty
			if newQty > 0 {
				l.avgCost = (l.openQty*l.avgCost + f.qty*f.price) / newQty
				l.openQty = newQty
			}
			continue
		}
		if l.openQty <= 0 {
			continue // the opening buy predates this ledger
		}
		closedQty := f.qty
		if closedQty > l.openQty {
			closedQty = l.openQty
		}
		l.gross += (f.price - l.avgCost) * closedQty
		l.openQty -= closedQty
		if l.openQty < strategyLotEpsilon {
			l.openQty, l.avgCost = 0, 0
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM strategy_position_accounting`); err != nil {
		return err
	}
	keys := make([]string, 0, len(lots))
	for key := range lots {
		keys = append(keys, key)
	}
	sort.Strings(keys) // deterministic write order keeps rebuilds diffable
	for _, key := range keys {
		l := lots[key]
		parts := strings.Split(key, "\x00")
		portfolioID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return err
		}
		strategyID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return err
		}
		var firstAt, lastAt any
		if l.firstAt != "" {
			firstAt = l.firstAt
		}
		if l.lastAt != "" {
			lastAt = l.lastAt
		}
		if _, err := tx.Exec(`
			INSERT INTO strategy_position_accounting
				(portfolio_id, strategy_id, symbol, outcome, open_qty, avg_cost,
				 gross_realized_pnl, fees_paid, spread_cost, slippage_cost,
				 notional_traded, fill_count, first_fill_at, last_fill_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			portfolioID, strategyID, parts[2], parts[3], l.openQty, l.avgCost,
			l.gross, l.fees, l.spread, l.slippage, l.notional, l.fillCount,
			firstAt, lastAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
