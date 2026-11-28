package main

import (
	"database/sql"
	"errors"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// strategyLotEpsilon matches the position engine's close tolerance so a lot
// that is fully sold rounds to flat instead of leaving dust behind.
const strategyLotEpsilon = 1e-9

const (
	strategyFillPage     = 500
	strategyFillMaxPages = 200
)

var strategyFillMu sync.Mutex

// syncStrategyFills folds new fills of Strategies' own orders into each
// strategy's lot book. Trading's fills_feed is append-only and keyset-paged,
// and the cursor advances in the same transaction as the lots, so a restart
// or a repeated call never folds a fill twice.
//
// Only fills of orders Strategies placed appear (plus, after a legacy import,
// fills of the imported Trading-era strategies). Manual and agent fills stay
// in Trading's portfolio book.
func syncStrategyFills(ctx *sdk.AppCtx, pid string) error {
	strategyFillMu.Lock()
	defer strategyFillMu.Unlock()
	db := ctx.AppDB()
	var cursor int64
	if err := db.QueryRow(`SELECT COALESCE((SELECT last_fill_id FROM trading_fill_cursor WHERE project_id=?),0)`, pid).Scan(&cursor); err != nil {
		return err
	}
	var imported int
	if err := db.QueryRow(`SELECT COUNT(*) FROM research_imports WHERE project_id=?`, pid).Scan(&imported); err != nil {
		return err
	}
	reader := newTradingReader(ctx)
	for page := 0; page < strategyFillMaxPages; page++ {
		fills, next, more, err := reader.FillsSince(pid, cursor, strategyFillPage, imported > 0)
		if err != nil {
			return err
		}
		if len(fills) == 0 && next == cursor {
			return nil
		}
		if err := applyStrategyFills(db, pid, fills, next); err != nil {
			return err
		}
		cursor = next
		if !more {
			return nil
		}
	}
	return errors.New("Trading fills feed exceeded the page limit; sync continues on the next read")
}

// resetStrategyFills clears a project's lot book so the next sync replays
// every fill from the start.
func resetStrategyFills(db *sql.DB, pid string) error {
	strategyFillMu.Lock()
	defer strategyFillMu.Unlock()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM strategy_position_accounting WHERE project_id=?`, pid); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM trading_fill_cursor WHERE project_id=?`, pid); err != nil {
		return err
	}
	return tx.Commit()
}

func strategyFillOutcome(f TradingFill) string {
	if f.AssetClass != "polymarket" {
		return ""
	}
	if f.Outcome != "" {
		return f.Outcome
	}
	return strings.ToUpper(f.Side)
}

// applyStrategyFills folds one page and advances the cursor atomically.
// Each strategy closes against its own average-cost lots, never the
// portfolio's blended average.
func applyStrategyFills(db *sql.DB, pid string, fills []TradingFill, next int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, f := range fills {
		if f.ExternalStrategyID <= 0 {
			continue
		}
		outcome := strategyFillOutcome(f)
		var openQty, avgCost, gross float64
		var firstAt sql.NullString
		err := tx.QueryRow(`SELECT open_qty,avg_cost,gross_realized_pnl,first_fill_at FROM strategy_position_accounting
			WHERE portfolio_id=? AND strategy_id=? AND symbol=? AND outcome=?`, f.PortfolioID, f.ExternalStrategyID, f.Symbol, outcome).Scan(&openQty, &avgCost, &gross, &firstAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if isBuySide(f.Side) {
			if newQty := openQty + f.Qty; newQty > 0 {
				avgCost = (openQty*avgCost + f.Qty*f.Price) / newQty
				openQty = newQty
			}
		} else if openQty > 0 {
			closed := f.Qty
			if closed > openQty {
				closed = openQty
			}
			gross += (f.Price - avgCost) * closed
			openQty -= closed
			if openQty < strategyLotEpsilon {
				openQty, avgCost = 0, 0
			}
		}
		var filledAt any
		if f.FilledAt != "" {
			filledAt = f.FilledAt
		}
		first := filledAt
		if firstAt.Valid && firstAt.String != "" {
			first = firstAt.String
		}
		if _, err := tx.Exec(`
			INSERT INTO strategy_position_accounting
				(project_id, portfolio_id, strategy_id, symbol, outcome, open_qty, avg_cost,
				 gross_realized_pnl, fees_paid, spread_cost, slippage_cost,
				 notional_traded, fill_count, first_fill_at, last_fill_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(portfolio_id, strategy_id, symbol, outcome) DO UPDATE SET
				open_qty=excluded.open_qty, avg_cost=excluded.avg_cost, gross_realized_pnl=excluded.gross_realized_pnl,
				fees_paid=fees_paid+excluded.fees_paid, spread_cost=spread_cost+excluded.spread_cost,
				slippage_cost=slippage_cost+excluded.slippage_cost, notional_traded=notional_traded+excluded.notional_traded,
				fill_count=fill_count+1, last_fill_at=excluded.last_fill_at, updated_at=CURRENT_TIMESTAMP`,
			pid, f.PortfolioID, f.ExternalStrategyID, f.Symbol, outcome, openQty, avgCost,
			gross, f.Fee, f.SpreadCost, f.SlippageCost, f.Qty*f.Price, first, filledAt); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO trading_fill_cursor(project_id,last_fill_id) VALUES(?,?)
		ON CONFLICT(project_id) DO UPDATE SET last_fill_id=excluded.last_fill_id,updated_at=CURRENT_TIMESTAMP`, pid, next); err != nil {
		return err
	}
	return tx.Commit()
}
