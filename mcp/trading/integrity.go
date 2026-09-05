package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"sync"
	"time"
)

var brokerBindingMu sync.Mutex
var orderRequestMu sync.Mutex
var rebalanceMu sync.Mutex
var strategyReplayMu sync.Mutex

func pinBroker(ctx *sdk.AppCtx, pf *Portfolio, connectionID int64) error {
	if pf.ID == 0 {
		return nil
	}
	var required int
	if err := ctx.AppDB().QueryRow(`SELECT broker_binding_required FROM portfolios WHERE id=?`, pf.ID).Scan(&required); err != nil {
		return err
	}
	if required != 0 {
		return errors.New("legacy portfolio requires explicit broker account binding")
	}
	environment := normalizeExecutionEnvironment(pf.ExecutionEnvironment, pf.Mode, pf.BrokerSlug)
	if pf.BrokerSlug == "alpaca-trading" {
		actual, verified := alpacaConnectionEnvironment(ctx, connectionID)
		if !verified || actual != environment {
			return errors.New("broker environment is unverified or does not match portfolio")
		}
	}
	_, err := ctx.AppDB().Exec(`INSERT INTO broker_bindings(portfolio_id,connection_id,execution_environment) VALUES(?,?,?) ON CONFLICT(portfolio_id) DO NOTHING`, pf.ID, connectionID, environment)
	if err != nil {
		return fmt.Errorf("broker account already belongs to another portfolio: %w", err)
	}
	var stored int64
	var env string
	if err := ctx.AppDB().QueryRow(`SELECT connection_id,execution_environment FROM broker_bindings WHERE portfolio_id=?`, pf.ID).Scan(&stored, &env); err != nil {
		return err
	}
	if stored != connectionID || env != environment {
		return errors.New("broker binding changed; execution remains disarmed until the original binding is restored")
	}
	return nil
}

func orderIntentHash(args map[string]any) string {
	intent := map[string]any{}
	for _, key := range []string{"symbol", "side", "type", "outcome", "qty", "tif", "limit_price", "stop_price", "strategy_id", "assignment_id"} {
		if v, ok := args[key]; ok {
			intent[key] = v
		}
	}
	raw, _ := json.Marshal(intent)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func previousOrderRequest(db *sql.DB, pid string, portfolioID int64, key, hash string) (map[string]any, error) {
	var oid, stored string
	err := db.QueryRow(`SELECT order_id,intent_hash FROM order_requests WHERE project_id=? AND portfolio_id=? AND request_key=?`, pid, portfolioID, key).Scan(&oid, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if stored != hash {
		return nil, errors.New("idempotency key reused with different order intent")
	}
	o, err := dbGetOrder(db, pid, oid)
	if err != nil {
		return nil, err
	}
	var uncertain, cancel int
	if err := db.QueryRow(`SELECT reconciliation_required,cancel_requested FROM orders WHERE id=?`, oid).Scan(&uncertain, &cancel); err != nil {
		return nil, err
	}
	return map[string]any{"order_id": oid, "status": o.Status, "replayed": true, "uncertain": uncertain != 0, "cancel_requested": cancel != 0}, nil
}

func recoverableByClientID(slug string) bool {
	return oneOfString(slug, "alpaca-trading", "binance-trading", "okx-trading", "bybit-trading", "bitstamp-trading")
}

func workingPortfolioOrders(db *sql.DB, id int64) ([]*Order, error) {
	// Same projection as the public list, with an explicit unbounded internal path.
	return dbListOrdersInternal(db, id, "working", -1)
}

func persistRebalance(db *sql.DB, pf *Portfolio, s *Strategy, a *StrategyAssignment, eval *StrategyEvaluation) error {
	raw, err := json.Marshal(eval.TargetAllocations)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO strategy_rebalances(assignment_id,portfolio_id,strategy_id,strategy_version,targets_json) VALUES(?,?,?,?,?) ON CONFLICT(assignment_id) DO UPDATE SET targets_json=excluded.targets_json,strategy_version=excluded.strategy_version,status='pending',updated_at=CURRENT_TIMESTAMP WHERE strategy_rebalances.status='completed' OR strategy_rebalances.strategy_version!=excluded.strategy_version`, a.ID, pf.ID, s.ID, s.Version, string(raw))
	return err
}

func validateWorkingPolicy(db *sql.DB, pf *Portfolio, o *Order) error {
	if isBuySide(o.Side) {
		v, err := portfolioUniverseViolation(db, pf, o.Symbol)
		if err != nil {
			return err
		}
		if v != nil {
			return errors.New(v.Detail)
		}
	}
	var sid int64
	var version int
	if err := db.QueryRow(`SELECT strategy_id,strategy_version FROM orders WHERE id=?`, o.ID).Scan(&sid, &version); err != nil {
		return err
	}
	if o.Source == "strategy" && sid == 0 {
		return errors.New("strategy order provenance unavailable")
	}
	if sid > 0 {
		s, err := dbGetStrategy(db, pf.ProjectID, sid)
		if err != nil {
			return err
		}
		if s.Version != version {
			return errors.New("strategy version changed")
		}
		if allowed, reason := scorecardAllowsExecution(db, pf, sid); !allowed {
			return errors.New(reason)
		}
	}
	return nil
}

// Economic revisions cover cash, fills, external positions and corporate actions.
func accountRevision(db *sql.DB, id int64) (int64, error) {
	var revision int64
	err := db.QueryRow(`SELECT COALESCE((SELECT revision FROM portfolio_revisions WHERE portfolio_id=?),0)`, id).Scan(&revision)
	return revision, err
}

func applyAccountSnapshot(db *sql.DB, pf *Portfolio, acct *brokerAccount, complete bool, revision int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRow(`SELECT COALESCE((SELECT revision FROM portfolio_revisions WHERE portfolio_id=?),0)`, pf.ID).Scan(&current); err != nil {
		return err
	}
	if current != revision {
		return errors.New("account changed while broker snapshot was in flight")
	}
	var working int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM orders WHERE portfolio_id=? AND status='working'`, pf.ID).Scan(&working); err != nil {
		return err
	}
	if working > 0 {
		return nil
	} // Wait until fills have been reconciled before importing balances.
	if _, err := tx.Exec(`UPDATE portfolios SET cash=?,available_cash=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, acct.QuoteCash, acct.QuoteAvailable, pf.ID); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT symbol FROM positions WHERE portfolio_id=? AND COALESCE(outcome,'')=''`, pf.ID)
	if err != nil {
		return err
	}
	var known []string
	for rows.Next() {
		var symbol string
		if err := rows.Scan(&symbol); err != nil {
			rows.Close()
			return err
		}
		known = append(known, symbol)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for symbol, balance := range acct.Holdings {
		qty := brokerBalanceTotal(balance)
		if !finite(qty) || qty < 0 {
			return errors.New("broker short position requires reconciliation")
		}
		if qty <= 1e-9 {
			if complete {
				if _, err := tx.Exec(`DELETE FROM positions WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, pf.ID, symbol); err != nil {
					return err
				}
			}
			continue
		}
		_, err := tx.Exec(`INSERT INTO positions(project_id,portfolio_id,symbol,asset_class,outcome,qty,avg_cost) VALUES(?,?,?,?,'',?,?) ON CONFLICT(portfolio_id,symbol,COALESCE(outcome,'')) DO UPDATE SET qty=excluded.qty,avg_cost=CASE WHEN excluded.avg_cost>0 THEN excluded.avg_cost ELSE positions.avg_cost END,updated_at=CURRENT_TIMESTAMP`, pf.ProjectID, pf.ID, symbol, inferAssetClass(symbol), qty, balance.AvgCost)
		if err != nil {
			return err
		}
	}
	if complete {
		for _, symbol := range known {
			if _, ok := acct.Holdings[symbol]; !ok {
				if _, err := tx.Exec(`DELETE FROM positions WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=''`, pf.ID, symbol); err != nil {
					return err
				}
			}
		}
	}
	var after int64
	if err := tx.QueryRow(`SELECT COALESCE((SELECT revision FROM portfolio_revisions WHERE portfolio_id=?),0)`, pf.ID).Scan(&after); err != nil {
		return err
	}
	if after != revision {
		if err := dbInsertJournalTx(tx, pf.ProjectID, pf.ID, "note", "Applied broker account snapshot atomically", map[string]any{"source": "broker_reconcile", "revision": revision}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func commitStrategyStep(db *sql.DB, runID int64, step int, summary map[string]any, status string, snap *BacktestSnapshot) error {
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE backtest_runs SET current_step=?,summary_json=json_patch(COALESCE(summary_json,'{}'),?),status=?,updated_at=CURRENT_TIMESTAMP,completed_at=CASE WHEN ?='completed' THEN CURRENT_TIMESTAMP ELSE completed_at END WHERE id=? AND status='running' AND current_step=?`, step, string(raw), status, status, runID, step-1)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("backtest step cancelled, paused, or already committed")
	}
	if err := dbUpsertBacktestSnapshot(tx, snap); err != nil {
		return err
	}
	return tx.Commit()
}

func historicalQuantity(db *sql.DB, portfolioID int64, symbol, date string) (float64, error) {
	var qty float64
	err := db.QueryRow(`SELECT qty FROM position_history WHERE portfolio_id=? AND symbol=? AND outcome='' AND julianday(observed_at)<julianday(?) ORDER BY julianday(observed_at) DESC,id DESC LIMIT 1`, portfolioID, symbol, date+"T00:00:00Z").Scan(&qty)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return qty, err
}

type replayExecutionPolicy struct {
	Risk        *PortfolioRiskPolicy             `json:"risk"`
	Allowed     map[string]bool                  `json:"allowed"`
	Profiles    map[string]VenueExecutionProfile `json:"profiles"`
	Instruments map[string]*Instrument           `json:"instruments"`
}

func captureReplayPolicy(db *sql.DB, pf *Portfolio, symbols []string) (*replayExecutionPolicy, error) {
	risk, err := dbGetPortfolioRiskPolicy(db, pf)
	if err != nil {
		return nil, err
	}
	p := &replayExecutionPolicy{Risk: risk, Allowed: map[string]bool{}, Profiles: map[string]VenueExecutionProfile{}, Instruments: map[string]*Instrument{}}
	for _, symbol := range symbols {
		v, err := portfolioUniverseViolation(db, pf, symbol)
		if err != nil {
			return nil, err
		}
		p.Allowed[symbol] = v == nil && contains(pf.AllowedClasses, inferAssetClass(symbol))
		p.Profiles[symbol] = resolveVenueProfile(db, pf, symbol, inferAssetClass(symbol))
		p.Instruments[symbol], _ = dbGetInstrument(db, symbol)
	}
	return p, nil
}
func decodeReplayPolicy(run *BacktestRun) *replayExecutionPolicy {
	raw, err := json.Marshal(run.Summary["execution_policy"])
	if err != nil {
		return nil
	}
	var p replayExecutionPolicy
	if json.Unmarshal(raw, &p) != nil || p.Risk == nil {
		return nil
	}
	return &p
}
func exposureBreach(policy *PortfolioRiskPolicy, equity, orderNotional, symbolValue, gross float64) *RiskBreach {
	if equity <= 0 {
		return &RiskBreach{Code: "risk_no_equity", Detail: "positive equity required"}
	}
	checks := []struct {
		actual, limit float64
		code          string
	}{{orderNotional / equity * 100, policy.MaxOrderPct, "risk_max_order"}, {(symbolValue + orderNotional) / equity * 100, policy.MaxPositionPct, "risk_max_position"}, {(gross + orderNotional) / equity * 100, policy.MaxGrossExposurePct, "risk_max_gross_exposure"}}
	for _, c := range checks {
		if c.actual > c.limit+1e-9 {
			return &RiskBreach{Code: c.code, Detail: fmt.Sprintf("projected %.2f%% exceeds %.2f%% maximum", c.actual, c.limit), ActualPct: c.actual, LimitPct: c.limit}
		}
	}
	return nil
}

type commissionDelta struct {
	Currency     string
	Total, Delta float64
}

func commissionDeltas(db interface{ QueryRow(string, ...any) *sql.Row }, o *Order, br *brokerOrderResult) ([]commissionDelta, error) {
	totals := map[string]float64{}
	for _, f := range br.Fills {
		if !finite(f.Commission) || f.Commission < 0 {
			return nil, errors.New("invalid broker commission")
		}
		if f.Commission == 0 {
			continue
		}
		currency := strings.ToUpper(f.CommissionAsset)
		if currency == "" {
			return nil, errors.New("broker commission currency missing")
		}
		totals[currency] += f.Commission
	}
	var out []commissionDelta
	for currency, total := range totals {
		var old float64
		err := db.QueryRow(`SELECT amount FROM order_commissions WHERE order_id=? AND currency=?`, o.ID, currency).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if total > old {
			out = append(out, commissionDelta{currency, total, total - old})
		}
	}
	return out, nil
}
func applyCommissionBalances(tx *sql.Tx, pf *Portfolio, o *Order, deltas []commissionDelta) error {
	for _, c := range deltas {
		if _, err := tx.Exec(`INSERT INTO order_commissions(order_id,currency,amount) VALUES(?,?,?) ON CONFLICT(order_id,currency) DO UPDATE SET amount=excluded.amount`, o.ID, c.Currency, c.Total); err != nil {
			return err
		}
		if oneOfString(c.Currency, "USD", "USDT", "USDC") {
			if _, err := tx.Exec(`UPDATE portfolios SET cash=cash-? WHERE id=?`, c.Delta, pf.ID); err != nil {
				return err
			}
			continue
		}
		symbol := c.Currency + "-USD"
		result, err := tx.Exec(`UPDATE positions SET qty=qty-?,updated_at=CURRENT_TIMESTAMP WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')='' AND qty>=?`, c.Delta, pf.ID, symbol, c.Delta)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			if _, err := tx.Exec(`UPDATE orders SET reconciliation_required=1,rejection_detail='fee asset balance requires reconciliation' WHERE id=?`, o.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) toolPortfolioBrokerBind(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, int64Arg(args, "portfolio_id", 0))
	if err != nil {
		return nil, err
	}
	connectionID := int64Arg(args, "connection_id", 0)
	if strArg(args, "confirmation") != "BIND BROKER ACCOUNT" || connectionID <= 0 || pf.Mode != "live" {
		return nil, errors.New("broker portfolio, connection_id and BIND BROKER ACCOUNT confirmation required")
	}
	conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{AppSlug: pf.BrokerSlug})
	if err != nil {
		return nil, err
	}
	found := false
	for _, c := range conns {
		if c.ID == connectionID && (c.Status == "active" || c.Status == "connected" || c.Status == "") {
			found = true
		}
	}
	if !found {
		return nil, errors.New("selected broker connection unavailable")
	}
	environment := normalizeExecutionEnvironment(pf.ExecutionEnvironment, pf.Mode, pf.BrokerSlug)
	if pf.BrokerSlug == "alpaca-trading" {
		actual, verified := alpacaConnectionEnvironment(ctx, connectionID)
		if !verified || actual != environment {
			return nil, errors.New("broker environment mismatch")
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing int64
	err = tx.QueryRow(`SELECT connection_id FROM broker_bindings WHERE portfolio_id=?`, pf.ID).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && existing != connectionID {
		return nil, errors.New("existing broker binding cannot be redirected")
	}
	if _, err := tx.Exec(`INSERT INTO broker_bindings(portfolio_id,connection_id,execution_environment) VALUES(?,?,?) ON CONFLICT(portfolio_id) DO NOTHING`, pf.ID, connectionID, environment); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE portfolios SET broker_binding_required=0,live_armed=0 WHERE id=?`, pf.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"portfolio_id": pf.ID, "connection_id": connectionID, "live_armed": false}, nil
}

func replayPolicyHash(policy *replayExecutionPolicy) string {
	raw, _ := json.Marshal(policy)
	var value any
	_ = json.Unmarshal(raw, &value)
	var strip func(any)
	strip = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, k := range []string{"updated_at", "created_at", "received_at", "runtime"} {
				delete(x, k)
			}
			for _, child := range x {
				strip(child)
			}
		case []any:
			for _, child := range x {
				strip(child)
			}
		}
	}
	strip(value)
	raw, _ = json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func executionTime(db *sql.DB, portfolioID int64) time.Time {
	var at string
	if err := db.QueryRow(`SELECT replay_at FROM replay_steps WHERE portfolio_id=?`, portfolioID).Scan(&at); err == nil {
		if parsed, err := time.Parse(time.RFC3339Nano, at); err == nil {
			return parsed
		}
	}
	return time.Now().UTC()
}

// Fee-only updates read their cumulative watermark inside the transaction,
// so duplicate or concurrent notifications cannot debit balances twice.
func applyLateBrokerCommissions(db *sql.DB, pf *Portfolio, o *Order, br *brokerOrderResult) (bool, error) {
	profile := resolveVenueProfile(db, pf, o.Symbol, o.AssetClass)
	// Resolve market conversions before opening the single-connection transaction.
	rates := map[string]float64{}
	for _, fill := range br.Fills {
		currency := strings.ToUpper(fill.CommissionAsset)
		rate, ok := convertCommissionToQuote(db, o, 1, currency, profile.FeeCurrency, o.AvgFillPrice)
		if ok {
			rates[currency] = rate
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	deltas, err := commissionDeltas(tx, o, br)
	if err != nil {
		return false, err
	}
	if len(deltas) == 0 {
		return false, nil
	}
	fee := 0.0
	complete := true
	for _, c := range deltas {
		rate, ok := rates[c.Currency]
		complete = complete && ok
		fee += c.Delta * rate
	}
	if err := applyCommissionBalances(tx, pf, o, deltas); err != nil {
		return false, err
	}
	if err := dbAccruePositionAccountingTx(tx, pf.ID, o.Symbol, polyOutcome(o), 0, fee); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE fills SET fee=fee+? WHERE id=(SELECT MAX(id) FROM fills WHERE order_id=?)`, fee, o.ID); err != nil {
		return false, err
	}
	if fee != 0 {
		if _, _, err := dbInsertExecutionCostTx(tx, pf.ProjectID, pf.ID, o.ID, nil, profile.VenueSlug, o.Symbol, "fee", fee, profile.FeeCurrency, nil, "unknown", "", map[string]any{"source": "delayed_broker_commission"}, ""); err != nil {
			return false, err
		}
	}
	if !complete {
		if _, err := tx.Exec(`UPDATE orders SET reconciliation_required=1,rejection_detail='commission valuation requires reconciliation' WHERE id=?`, o.ID); err != nil {
			return false, err
		}
	}
	if err := dbInsertJournalTx(tx, pf.ProjectID, pf.ID, "note", "Applied delayed broker commission", map[string]any{"order_id": o.ID, "fee": fee, "commissions": deltas, "fee_conversion_complete": complete}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
