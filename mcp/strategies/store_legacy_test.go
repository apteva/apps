package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"strings"
)

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
		INSERT OR IGNORE INTO positions (project_id, portfolio_id, symbol, security_id, asset_class, outcome, qty, avg_cost)
		VALUES (?, ?, ?, (SELECT security_id FROM security_listings WHERE symbol=? ORDER BY active DESC,updated_at DESC LIMIT 1), ?, ?, ?, ?)`,
		projectID, portfolioID, symbol, symbol, assetClass, outcomeArg, qty, avgCost)
	return err
}

func dbGetPosition(db *sql.DB, portfolioID int64, symbol, outcome string) (*Position, error) {
	row := db.QueryRow(`
		SELECT p.symbol, COALESCE(p.security_id,''), p.asset_class, COALESCE(p.outcome, ''), p.qty, p.avg_cost,
		       COALESCE(a.gross_realized_pnl - a.fees_paid, p.realized_pnl)
		FROM positions p
		LEFT JOIN position_accounting a
		  ON a.portfolio_id = p.portfolio_id
		 AND a.symbol = p.symbol
		 AND a.outcome = COALESCE(p.outcome, '')
		WHERE p.portfolio_id = ? AND p.symbol = ? AND COALESCE(p.outcome, '') = ?`,
		portfolioID, symbol, outcome)
	var p Position
	err := row.Scan(&p.Symbol, &p.SecurityID, &p.AssetClass, &p.Outcome, &p.Qty, &p.AvgCost, &p.RealizedPnL)
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

	strategyID, err := dbOrderStrategyID(tx, o.ID)
	if err != nil {
		return err
	}
	if err := dbAccrueStrategyFillTx(tx, portfolioID, strategyID, o.Symbol, outcome, o.Side, qty, price, 0, 0, 0); err != nil {
		return err
	}

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

		if !exists || curQty < qty-1e-9 {
			return fmt.Errorf("cannot sell %v %s — only %v available", qty, o.Symbol, curQty)
		}
		realized := (price - curAvgCost) * qty
		if err := dbAccruePositionAccountingTx(tx, portfolioID, o.Symbol, outcome, realized, 0); err != nil {
			return err
		}
		newQty := curQty - qty
		if newQty < 1e-9 {

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

		if _, err := tx.Exec(`UPDATE portfolios SET cash = cash + ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			qty*price, portfolioID); err != nil {
			return err
		}
		return nil
	}

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
			INSERT INTO positions (project_id, portfolio_id, symbol, security_id, asset_class, outcome, qty, avg_cost)
			VALUES (?, ?, ?, (SELECT security_id FROM security_listings WHERE symbol=? ORDER BY active DESC,updated_at DESC LIMIT 1), ?, ?, ?, ?)`,
			projectID, portfolioID, o.Symbol, o.Symbol, o.AssetClass, outcomeArg, qty, price); err != nil {
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

func dbInsertOrder(db sqlExecer, o *Order, projectID string) error {
	_, err := db.Exec(`
		INSERT INTO orders (id, project_id, portfolio_id, symbol, security_id, asset_class, side, outcome, type,
		                    qty, limit_price, stop_price, tif, status, rationale, source, liquidity_role)
		VALUES (?, ?, ?, ?, (SELECT security_id FROM security_listings WHERE symbol=? ORDER BY active DESC,updated_at DESC LIMIT 1), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, projectID, o.PortfolioID, o.Symbol, o.Symbol, o.AssetClass, o.Side, nullableString(o.Outcome), o.Type,
		o.Qty, nullable(o.LimitPrice), nullable(o.StopPrice), o.TIF, o.Status, o.Rationale, o.Source, firstString(o.LiquidityRole, "unknown"))
	return err
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

func dbInsertFillDetailed(tx *sql.Tx, projectID, orderID string, portfolioID int64, qty, price, fee float64, cost FillCostDetails) error {
	if cost.VenueSlug == "" {
		cost.VenueSlug = "simulation"
	}
	if cost.FeeCurrency == "" {
		cost.FeeCurrency = "USD"
	}
	if cost.FeeSource == "" {
		cost.FeeSource = "model"
	}
	if cost.LiquidityRole == "" {
		cost.LiquidityRole = "unknown"
	}
	res, err := tx.Exec(`
		INSERT INTO fills (project_id, order_id, portfolio_id, qty, price, fee, fee_currency,
			liquidity_role, spread_cost, slippage_cost, venue_slug, fee_source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, projectID, orderID, portfolioID, qty, price, fee,
		strings.ToUpper(cost.FeeCurrency), cost.LiquidityRole, cost.SpreadCost, cost.SlippageCost,
		cost.VenueSlug, cost.FeeSource)
	if err != nil {
		return err
	}
	fillID, _ := res.LastInsertId()
	if fee != 0 {
		rate := cost.FeeBps
		kind := "fee"
		if fee < 0 {
			kind = "rebate"
		}
		if _, _, err := dbInsertExecutionCostTx(tx, projectID, portfolioID, orderID, &fillID,
			cost.VenueSlug, "", kind, fee, cost.FeeCurrency, &rate, cost.LiquidityRole, "", cost.Metadata, ""); err != nil {
			return err
		}
	}
	if cost.SpreadCost > 0 {
		if _, _, err := dbInsertExecutionCostTx(tx, projectID, portfolioID, orderID, &fillID,
			cost.VenueSlug, "", "spread", cost.SpreadCost, cost.FeeCurrency, nil, cost.LiquidityRole, "", cost.Metadata, ""); err != nil {
			return err
		}
	}
	if cost.SlippageCost > 0 {
		if _, _, err := dbInsertExecutionCostTx(tx, projectID, portfolioID, orderID, &fillID,
			cost.VenueSlug, "", "slippage", cost.SlippageCost, cost.FeeCurrency, nil, cost.LiquidityRole, "", cost.Metadata, ""); err != nil {
			return err
		}
	}
	return nil
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
