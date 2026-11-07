package main

import (
	"database/sql"

	"errors"
	"fmt"
	"math"

	"strings"

	"time"
)

// tryFill — given a working order and the fresh marks, decide whether
// to fill. Single-pass, single-tick.
func tryFill(e *engine, o *Order) error {
	orderPlacementMu.Lock()
	defer orderPlacementMu.Unlock()
	if o.Status != "working" {
		return nil
	}
	if o.TIF == "day" {
		placed, err := parseOrderTime(o.PlacedAt)
		if err == nil && utcDay(placed) != utcDay(executionTime(e.db, o.PortfolioID)) {
			_, err = e.db.Exec(`UPDATE orders SET status='cancelled',resolved_at=CURRENT_TIMESTAMP,rejection_detail='day_expired' WHERE id=? AND status='working'`, o.ID)
			return err
		}
	}
	if o.TIF == "ioc" || o.TIF == "fok" {
		defer func() {
			_, _ = e.db.Exec(`UPDATE orders SET status='cancelled',resolved_at=CURRENT_TIMESTAMP,rejection_detail='immediate_order_unfilled' WHERE id=? AND status='working'`, o.ID)
		}()
	}
	mark, err := dbGetMark(e.db, o.Symbol)
	if err != nil {
		return nil
	}
	if _, live := e.provider.(*tradingProvider); live && !markFresh(mark, time.Now().UTC()) {
		return nil
	}
	pf, err := dbGetPortfolioAnyProject(e.db, o.PortfolioID)
	if err != nil {
		return err
	}
	if pf.Status != "active" {

		return nil
	}

	if err := validateWorkingPolicy(e.db, pf, o); err != nil {
		_, cancelErr := dbCancelOrder(e.db, pf.ProjectID, o.ID, err.Error())
		return cancelErr
	}
	if isBuySide(o.Side) {
		violation, err := portfolioUniverseViolation(e.db, pf, o.Symbol)
		if err != nil {
			return err
		}
		if violation != nil {
			_, err = dbCancelOrder(e.db, pf.ProjectID, o.ID, violation.Detail)
			return err
		}
		policy, err := dbGetPortfolioRiskPolicy(e.db, pf)
		if err != nil {
			return err
		}
		equity, err := computeEquity(e.db, pf)
		if err != nil {
			return err
		}
		breach, err := currentLossBreach(e.db, pf, policy, equity)
		if err != nil {
			return err
		}
		if breach != nil {
			_, err = dbCancelOrder(e.db, pf.ProjectID, o.ID, breach.Detail)
			return err
		}
	}

	outcome := polyOutcome(o)
	mp := mark.Price
	if o.AssetClass == "polymarket" {
		if outcome == "NO" && mark.NoPrice != nil {
			mp = *mark.NoPrice
		}
	}

	if mark.QuoteAt != "" {
		q := *mark
		q.MarkedAt = mark.QuoteAt
		if !markFresh(&q, time.Now()) {
			mark.BidPrice = nil
			mark.AskPrice = nil
		}
	}
	profile := resolveVenueProfile(e.db, pf, o.Symbol, o.AssetClass)
	if violation := validateExecutionOrder(profile, mark.Instrument, mark, o.Qty, mp); violation != nil {

		return nil
	}

	// Decide fill price by order type.
	var fillPrice float64
	estimate := estimateSimulationExecution(mark, o.Side, o.Type, o.Qty, mp, profile, o.LiquidityRole)
	switch o.Type {
	case "market":
		fillPrice = estimate.Price
	case "limit":
		if o.LimitPrice == nil {
			return nil
		}

		triggerPrice := mp
		if isBuySide(o.Side) && mark.AskPrice != nil && *mark.AskPrice > 0 {
			triggerPrice = *mark.AskPrice
		} else if o.Side == "sell" && mark.BidPrice != nil && *mark.BidPrice > 0 {
			triggerPrice = *mark.BidPrice
		}
		ok := false
		switch o.Side {
		case "buy":
			ok = triggerPrice <= *o.LimitPrice+priceTolerance
		case "sell":
			ok = triggerPrice >= *o.LimitPrice-priceTolerance
		case "yes", "no":

			ok = triggerPrice <= *o.LimitPrice+priceTolerance
		}
		if !ok {
			return nil
		}
		fillPrice = estimate.Price
		if estimate.LiquidityRole == "maker" {
			fillPrice = *o.LimitPrice
			estimate.SpreadCost, estimate.SlippageCost = 0, 0
		} else if isBuySide(o.Side) && fillPrice > *o.LimitPrice {
			fillPrice = *o.LimitPrice
		}
		if o.Side == "sell" && fillPrice < *o.LimitPrice {
			fillPrice = *o.LimitPrice
		}

		if estimate.LiquidityRole == "taker" {
			estimate.SlippageCost = math.Abs(fillPrice-estimate.TouchPrice) * o.Qty
		}
	case "stop":
		if o.StopPrice == nil {
			return nil
		}

		ok := false
		switch o.Side {
		case "buy":
			ok = mp >= *o.StopPrice
		case "sell":
			ok = mp <= *o.StopPrice
		default:
			return nil
		}
		if !ok {
			return nil
		}
		fillPrice = estimate.Price
	default:
		return nil
	}
	if isBuySide(o.Side) {
		breach, err := preTradeRiskCheckExcluding(e.db, pf, o.Symbol, o.Side, o.Qty-o.FilledQty, fillPrice, o.ID)
		if err != nil {
			return err
		}
		if breach != nil {
			_, err := dbCancelOrder(e.db, pf.ProjectID, o.ID, breach.Detail)
			return err
		}
	}
	estimate.Price = fillPrice
	estimate.Fee = fillFee(o.Qty, fillPrice, estimate.FeeBps)
	fee := estimate.Fee

	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentStatus string
	if err := tx.QueryRow(`SELECT status FROM portfolios WHERE id=?`, pf.ID).Scan(&currentStatus); err != nil {
		return err
	}
	if currentStatus != "active" {
		return nil
	}
	claimed, err := dbMarkOrderFilled(tx, o.ID, o.Qty, fillPrice)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	rejectCode, rejectDetail := "", ""
	if isBuySide(o.Side) {
		var cash float64
		if err := tx.QueryRow(`SELECT cash FROM portfolios WHERE id = ?`, pf.ID).Scan(&cash); err != nil {
			return err
		}
		needed := o.Qty*fillPrice + fee
		if cash < needed-1e-6 {
			rejectCode = "insufficient_cash"
			rejectDetail = fmt.Sprintf("need %.2f, have %.2f", needed, cash)
		}
	} else {
		var have float64
		err := tx.QueryRow(`SELECT qty FROM positions WHERE portfolio_id = ? AND symbol = ? AND COALESCE(outcome, '') = ?`,
			pf.ID, o.Symbol, polyOutcome(o)).Scan(&have)
		if errors.Is(err, sql.ErrNoRows) {
			have = 0
		} else if err != nil {
			return err
		}
		if have < o.Qty-1e-9 {
			rejectCode = "insufficient_position"
			rejectDetail = fmt.Sprintf("need %v, have %v", o.Qty, have)
		}
	}
	if rejectCode != "" {
		if _, err := tx.Exec(`UPDATE orders SET status='rejected', filled_qty=0, avg_fill_price=0,
			rejection_code=?, rejection_detail=?, resolved_at=CURRENT_TIMESTAMP WHERE id=?`,
			rejectCode, rejectDetail, o.ID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		emit("order.rejected", map[string]any{"order_id": o.ID, "portfolio_id": pf.ID, "code": rejectCode, "detail": rejectDetail})
		notifyInstances(e, pf, fmt.Sprintf("REJECTED %s — %s", o.ID, rejectDetail))
		return nil
	}
	if err := dbInsertFillDetailed(tx, pf.ProjectID, o.ID, pf.ID, o.Qty, fillPrice, fee, FillCostDetails{
		VenueSlug: profile.VenueSlug, FeeCurrency: estimate.FeeCurrency, FeeSource: "model",
		LiquidityRole: estimate.LiquidityRole, FeeBps: estimate.FeeBps,
		SpreadCost: estimate.SpreadCost, SlippageCost: estimate.SlippageCost,
		Metadata: map[string]any{"reference_price": estimate.ReferencePrice, "touch_price": estimate.TouchPrice},
	}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := dbApplyFill(tx, pf.ID, pf.ProjectID, o, o.Qty, fillPrice); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := dbAccruePositionAccountingTx(tx, pf.ID, o.Symbol, polyOutcome(o), 0, fee); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := accrueStrategyExecutionCost(tx, pf.ID, o, fee, estimate.SpreadCost, estimate.SlippageCost); err != nil {
		_ = tx.Rollback()
		return err
	}
	if fee != 0 {
		if _, err := tx.Exec(`UPDATE portfolios SET cash = cash - ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, fee, pf.ID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	body := fmt.Sprintf("%s %s %v @ %s — %s",
		strings.ToUpper(o.Symbol), strings.ToUpper(o.Side), o.Qty, formatPrice(fillPrice, o.AssetClass), o.ID)
	if err := dbInsertJournalTx(tx, pf.ProjectID, pf.ID, "fill", body, map[string]any{
		"order_id": o.ID, "qty": o.Qty, "price": fillPrice, "fee": fee,
		"fee_bps": estimate.FeeBps, "slippage_bps": profile.SlippageBps,
		"liquidity_role": estimate.LiquidityRole, "fee_currency": estimate.FeeCurrency,
		"spread_cost": estimate.SpreadCost, "slippage_cost": estimate.SlippageCost,
		"venue_slug": profile.VenueSlug,
		"side":       o.Side, "symbol": o.Symbol,
	}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	e.bumpFillCounter()

	emit("order.filled", map[string]any{
		"order_id": o.ID, "portfolio_id": pf.ID, "symbol": o.Symbol,
		"side": o.Side, "qty": o.Qty, "price": fillPrice, "fee": fee,
		"fee_currency": estimate.FeeCurrency, "liquidity_role": estimate.LiquidityRole,
		"spread_cost": estimate.SpreadCost, "slippage_cost": estimate.SlippageCost,
		"venue_slug": profile.VenueSlug,
	})
	if newPos, _ := dbGetPosition(e.db, pf.ID, o.Symbol, polyOutcome(o)); newPos != nil {
		emit("position.changed", map[string]any{
			"portfolio_id": pf.ID, "symbol": newPos.Symbol,
			"asset_class": newPos.AssetClass, "outcome": newPos.Outcome,
			"qty": newPos.Qty, "avg_cost": newPos.AvgCost,
			"realized_pnl": newPos.RealizedPnL,
		})
	} else {

		emit("position.changed", map[string]any{
			"portfolio_id": pf.ID, "symbol": o.Symbol, "qty": 0.0, "closed": true,
		})
	}
	emit("journal.appended", map[string]any{
		"portfolio_id": pf.ID, "kind": "fill", "body": body,
	})
	notifyInstances(e, pf, "FILL "+body)
	return nil
}

func formatPrice(p float64, class string) string {
	if class == "polymarket" {
		return fmt.Sprintf("%.2f¢", p*100)
	}
	return fmt.Sprintf("$%.2f", p)
}

// notifyInstances fans a short text message to every Apteva instance
// bound to this portfolio. Best-effort — failures are logged but don't
// break the engine.
func notifyInstances(e *engine, p *Portfolio, msg string) {
	rows, err := e.db.Query(`SELECT instance_id FROM portfolio_bindings WHERE portfolio_id = ?`, p.ID)
	if err != nil {
		return
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if err := e.platform.SendEvent(id, msg); err != nil {
			e.logger.Warn("send_event failed", "instance_id", id, "err", err)
		}
	}
}

func parseOrderTime(value string) (time.Time, error) {
	for _, format := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(format, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid order time %q", value)
}
