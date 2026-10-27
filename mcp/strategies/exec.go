package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// engine bundles everything the tick loops need. Instantiated once
// in OnMount and stashed in a package var so workers can reach it.
type engine struct {
	db       *sql.DB
	provider Provider
	logger   sdk.Logger
	platform sdk.PlatformClient

	// Metrics surfaced via /healthz/details. Used by tests too.
	mu                 sync.Mutex
	lastTickAt         time.Time
	ticks              int64
	fillsThisRun       int64
	fillsPending       int64
	lastWorkingSeen    int64
	lastFillsThisTick  int64
	lastMarksRefreshed int64

	// significantMarkDeltas state — the last price emitted per symbol,
	// so we send only meaningful changes on the `tick` event. Separate
	// mutex from `mu` to avoid stalling tick metrics while the
	// emit-payload computation runs.
	deltaMu     sync.Mutex
	lastEmitted map[string]float64
}

func (e *engine) snapshotMetrics() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return map[string]any{
		"last_tick_at":         e.lastTickAt,
		"ticks":                e.ticks,
		"fills_this_run":       e.fillsThisRun,
		"last_working_seen":    e.lastWorkingSeen,
		"last_fills_this_tick": e.lastFillsThisTick,
		"last_marks_refreshed": e.lastMarksRefreshed,
	}
}

var globalEngine *engine

const (
	defaultSlippageBps = 1.0  // 1 bp default; sells fill below mark, buys above
	defaultFeeBps      = 0.0  // paper default unless a portfolio overrides it
	defaultLossHalt    = -5.0 // %
	priceTolerance     = 1e-9
)

func evaluateLiveStrategyAssignments(e *engine, app *sdk.AppCtx, now time.Time) int {
	assignments, err := dbActiveStrategyAssignments(e.db)
	if err != nil {
		e.logger.Warn("query strategy assignments failed", "err", err)
		return 0
	}
	totalOrders := 0
	for _, a := range assignments {
		control := strings.ToLower(strings.TrimSpace(a.ControlMode))
		if control != "" && control != "strategy" && control != "hybrid" {
			continue
		}
		pf, err := dbGetPortfolioAnyProject(e.db, a.PortfolioID)
		if err != nil {
			e.logger.Warn("strategy portfolio lookup failed", "assignment_id", a.ID, "err", err)
			continue
		}
		if pf.Status != "active" || !portfolioAllowsAutomatedExecution(pf) {
			continue
		}
		if portfolioUsesBacktestPricing(e.db, pf.ID) {
			continue
		}
		strategy, err := dbGetStrategyVersion(e.db, a.ProjectID, a.StrategyID, a.StrategyVersion)
		if err != nil {
			e.logger.Warn("strategy lookup failed", "assignment_id", a.ID, "strategy_id", a.StrategyID, "err", err)
			continue
		}
		if strategy.Status != "active" {
			continue
		}
		if latest, err := dbGetStrategy(e.db, pf.ProjectID, strategy.ID); err != nil || latest.Version != strategy.Version {
			continue
		}
		def, _, err := validateStrategyDefinition(strategy.Definition)
		if err != nil {
			e.logger.Warn("strategy definition invalid", "assignment_id", a.ID, "strategy_id", a.StrategyID, "err", err)
			continue
		}
		if def.Engine == "rules" {
			e.logger.Warn("rule execution adapter unavailable", "assignment_id", a.ID)
			continue
		}
		var pendingTargets string
		if err := e.db.QueryRow(`SELECT targets_json FROM strategy_rebalances WHERE assignment_id=? AND strategy_version=? AND status='pending'`, a.ID, strategy.Version).Scan(&pendingTargets); err == nil {
			var targets []StrategyAllocation
			if json.Unmarshal([]byte(pendingTargets), &targets) == nil {
				_, _, err := placeStrategyPaperOrders(e, app, pf, strategy, a, &StrategyEvaluation{TargetAllocations: targets, Decisions: []string{"Resume pending rebalance"}})
				if err != nil {
					e.logger.Warn("rebalance pending", "assignment_id", a.ID, "err", err)
				}
			}
			continue
		}
		runtimeDef := strategyAssignmentDefinition(a, def)
		checkSlot, ok := strategyAssignmentCheckSlot(runtimeDef, now)
		if !ok || !strategyAssignmentCheckDue(a, checkSlot) {
			continue
		}
		if err := validateStrategyUniverseForPortfolio(e.db, pf, def); err != nil {
			continue
		}
		if allowed, _ := scorecardAllowsExecution(e.db, pf, strategy.ID); !allowed {
			continue
		}
		if !stockStrategyExecutionReady(e, runtimeDef, now) {
			continue
		}
		market, err := liveStrategyMarket(app, runtimeDef)
		if err != nil {
			e.logger.Warn("strategy market load failed", "assignment_id", a.ID, "strategy_id", a.StrategyID, "err", err)
			continue
		}
		marketBarAt := market.barTimes[len(market.barTimes)-1]
		if a.LastMarketBarAt == "" && a.LastEvaluatedAt != "" {
			if err := dbInitializeStrategyAssignmentMarketBar(e.db, a.ID, marketBarAt, checkSlot); err != nil {
				e.logger.Warn("strategy assignment schedule initialization failed", "assignment_id", a.ID, "err", err)
			}
			continue
		}
		if seen, err := time.Parse(time.RFC3339, strings.TrimSpace(a.LastSeenBarAt)); err == nil && !marketBarAt.After(seen) {
			continue
		}
		if !strategyAssignmentDueForMarket(a, runtimeDef, market) {
			if err := dbSetStrategyAssignmentObserved(e.db, a.ID, marketBarAt, checkSlot); err != nil {
				e.logger.Warn("strategy assignment observation update failed", "assignment_id", a.ID, "err", err)
			}
			continue
		}
		eval, err := evaluateStrategy(strategy, market)
		if err != nil {
			e.logger.Warn("strategy evaluation failed", "assignment_id", a.ID, "strategy_id", a.StrategyID, "err", err)
			continue
		}
		claimed, err := dbClaimStrategyRun(e.db, &StrategyRunEvent{
			ProjectID: a.ProjectID, PortfolioID: pf.ID, AssignmentID: a.ID,
			StrategyID: strategy.ID, StrategyVersion: strategy.Version, SignalBarAt: marketBarAt,
		}, eval.Decisions, eval.TargetAllocations)
		if err != nil {
			e.logger.Warn("strategy run claim failed", "assignment_id", a.ID, "err", err)
			continue
		}
		if !claimed {
			_ = dbSetStrategyAssignmentEvaluated(e.db, a.ID, checkSlot, marketBarAt, checkSlot)
			continue
		}
		app.Emit("strategy.run.started", map[string]any{
			"schema_version": 1, "portfolio_id": pf.ID, "assignment_id": a.ID,
			"strategy_id": strategy.ID, "strategy_version": strategy.Version,
			"signal_bar_at":         marketBarAt.Format(time.RFC3339Nano),
			"execution_environment": normalizeExecutionEnvironment(pf.ExecutionEnvironment, pf.Mode, pf.BrokerSlug),
		})
		app.Emit("strategy.decision.created", map[string]any{
			"schema_version": 1, "portfolio_id": pf.ID, "assignment_id": a.ID,
			"strategy_id": strategy.ID, "strategy_version": strategy.Version,
			"signal_bar_at": marketBarAt.Format(time.RFC3339Nano),
			"decisions":     eval.Decisions, "target_allocations": eval.TargetAllocations,
		})
		created, pending, err := placeStrategyPaperOrders(e, app, pf, strategy, a, eval)
		if err != nil {
			e.logger.Warn("strategy order placement failed", "assignment_id", a.ID, "strategy_id", a.StrategyID, "err", err)
			app.Emit("strategy.run.failed", map[string]any{"schema_version": 1, "portfolio_id": pf.ID,
				"assignment_id": a.ID, "strategy_id": strategy.ID, "error": err.Error()})
			_ = dbFinishStrategyRun(e.db, a.ID, marketBarAt, "failed", nil, err)
			continue
		}
		resolved := !pending
		for _, order := range created {
			if order.Status != "filled" {
				resolved = false
			}
		}

		if resolved {
			if err := dbSetStrategyAssignmentEvaluated(e.db, a.ID, checkSlot, marketBarAt, checkSlot); err != nil {
				e.logger.Warn("strategy assignment timestamp update failed", "assignment_id", a.ID, "err", err)
			}
		}
		orderIDs := make([]string, 0, len(created))
		for _, order := range created {
			orderIDs = append(orderIDs, order.ID)
		}
		eventTopic := "strategy.run.completed"
		runStatus := "completed"
		if !resolved {
			eventTopic = "strategy.run.orders_submitted"
			runStatus = "orders_submitted"
		}
		_ = dbFinishStrategyRun(e.db, a.ID, marketBarAt, runStatus, orderIDs, nil)
		app.Emit(eventTopic, map[string]any{
			"schema_version": 1, "portfolio_id": pf.ID, "assignment_id": a.ID,
			"strategy_id": strategy.ID, "strategy_version": strategy.Version,
			"signal_bar_at": marketBarAt.Format(time.RFC3339Nano), "order_ids": orderIDs,
			"orders_pending": !resolved,
		})
		totalOrders += len(created)
	}
	return totalOrders
}

func stockStrategyExecutionReady(e *engine, def *StrategyDefinition, now time.Time) bool {
	stockSymbols := []string{}
	for _, symbol := range def.Universe {
		class := inferAssetClass(symbol)
		if class == "equity" || class == "etf" {
			stockSymbols = append(stockSymbols, symbol)
		}
	}
	if len(stockSymbols) == 0 {
		return true
	}
	if !usEquityRegularSession(now) {
		return false
	}
	if _, live := e.provider.(*tradingProvider); !live {
		return true
	}
	for _, symbol := range stockSymbols {
		mark, err := dbGetMark(e.db, symbol)
		if err != nil || !markFresh(mark, now.UTC()) {
			return false
		}
	}
	return true
}

func usEquityRegularSession(now time.Time) bool {
	session := usEquitySessionAt(now)
	return session.OpenDay && !now.Before(session.Open) && now.Before(session.Close)
}

func portfolioUsesBacktestPricing(db *sql.DB, portfolioID int64) bool {
	cfg, err := dbPortfolioConfig(db, portfolioID)
	if err != nil {
		return false
	}
	return fmt.Sprint(cfg["source_override"]) == "backtest" ||
		fmt.Sprint(cfg["pricing_mode"]) == "backtest" ||
		fmt.Sprint(cfg["source"]) == "backtest"
}

func strategyAssignmentDefinition(a *StrategyAssignment, def *StrategyDefinition) *StrategyDefinition {
	copyDef := *def
	if cadence := strings.ToLower(strings.TrimSpace(a.Cadence)); cadence != "" {
		if _, ok := strategyCadenceDuration(cadence); ok {
			copyDef.Cadence = cadence
		}
	}
	return &copyDef
}

func strategyAssignmentCheckDue(a *StrategyAssignment, slot time.Time) bool {
	if strings.TrimSpace(a.LastCheckedAt) == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, strings.TrimSpace(a.LastCheckedAt))
	return err != nil || last.Before(slot)
}

func strategyAssignmentDueForMarket(a *StrategyAssignment, def *StrategyDefinition, market strategyMarket) bool {
	if strings.TrimSpace(a.LastMarketBarAt) == "" {
		return strings.TrimSpace(a.LastEvaluatedAt) == ""
	}
	last, err := time.Parse(time.RFC3339, strings.TrimSpace(a.LastMarketBarAt))
	if err != nil {
		return true
	}
	every := strategyRebalanceEvery(def, strategyHistoryInterval(def))
	completed := 0
	for _, barAt := range market.barTimes {
		if barAt.After(last) {
			completed++
		}
	}
	return completed >= every
}

func strategyAssignmentCheckSlot(def *StrategyDefinition, now time.Time) (time.Time, bool) {
	cadence := strategyHistoryInterval(def)
	hasStocks := false
	for _, symbol := range def.Universe {
		class := inferAssetClass(symbol)
		if class == "equity" || class == "etf" {
			hasStocks = true
			break
		}
	}
	if !hasStocks {
		return strategyClosedCandleBoundary(now, cadence), true
	}
	session := usEquitySessionAt(now)
	if !session.OpenDay || now.Before(session.Open) || !now.Before(session.Close) {
		return time.Time{}, false
	}
	local := now.In(session.Open.Location())
	open := session.Open
	if cadence == "1d" {
		return open.UTC(), true
	}
	if cadence == "1w" {
		daysSinceMonday := (int(local.Weekday()) + 6) % 7
		return open.AddDate(0, 0, -daysSinceMonday).UTC(), true
	}
	duration, ok := strategyCadenceDuration(cadence)
	if !ok || duration <= 0 {
		return open.UTC(), true
	}
	elapsed := local.Sub(open)
	return open.Add(time.Duration(int64(elapsed) / int64(duration) * int64(duration))).UTC(), true
}

func placeStrategyPaperOrders(e *engine, app *sdk.AppCtx, pf *Portfolio, strategy *Strategy, assignment *StrategyAssignment, eval *StrategyEvaluation) ([]*Order, bool, error) {
	rebalanceMu.Lock()
	defer rebalanceMu.Unlock()
	equity, err := computeEquity(e.db, pf)
	if err != nil {
		return nil, false, err
	}
	if equity <= 0 {
		return nil, false, nil
	}
	positions, err := dbListPositions(e.db, pf.ID)
	if err != nil {
		return nil, false, err
	}
	working, err := workingPortfolioOrders(e.db, pf.ID)
	if err != nil {
		return nil, false, err
	}
	if len(working) > 0 {
		return nil, true, nil
	}

	targets := map[string]float64{}
	for _, a := range eval.TargetAllocations {
		symbol := strings.ToUpper(strings.TrimSpace(a.Symbol))
		if symbol == "" || a.Weight <= 0 {
			continue
		}
		targets[symbol] += a.Weight
	}
	symbols := map[string]bool{}
	for symbol := range targets {
		symbols[symbol] = true
	}
	currentQty := map[string]float64{}
	for _, p := range positions {
		symbol := strings.ToUpper(p.Symbol)
		symbols[symbol] = true
		currentQty[symbol] += p.Qty
	}

	type plan struct {
		symbol  string
		side    string
		qty     float64
		price   float64
		qtyStep float64
	}
	sells := []plan{}
	buys := []plan{}
	threshold := math.Max(1, equity*0.001)
	orderedSymbols := make([]string, 0, len(symbols))
	for symbol := range symbols {
		orderedSymbols = append(orderedSymbols, symbol)
	}
	sort.Strings(orderedSymbols)
	for _, symbol := range orderedSymbols {
		mark, err := dbGetMark(e.db, symbol)
		if err != nil || mark == nil || mark.Price <= 0 {
			return nil, false, fmt.Errorf("executable mark unavailable for %s", symbol)
		}
		profile := resolveVenueProfile(e.db, pf, symbol, inferAssetClass(symbol))
		curValue := currentQty[symbol] * mark.Price
		targetValue := equity * targets[symbol]
		diff := targetValue - curValue
		if math.Abs(diff) < threshold {
			continue
		}
		if diff > 0 {
			qty := floorStrategyOrderQuantity(diff/mark.Price, profile.QtyStep)
			if qty > 0 {
				buys = append(buys, plan{symbol: symbol, side: "buy", qty: qty, price: mark.Price, qtyStep: profile.QtyStep})
			}
			continue
		}
		qty := floorStrategyOrderQuantity(math.Min(currentQty[symbol], -diff/mark.Price), profile.QtyStep)
		if qty > 0 {
			sells = append(sells, plan{symbol: symbol, side: "sell", qty: qty, price: mark.Price, qtyStep: profile.QtyStep})
		}
	}
	settings := dbPortfolioExecutionSettings(e.db, pf.ID)
	budget := pf.Cash
	if pf.AvailableCash != nil && *pf.AvailableCash < budget {
		budget = *pf.AvailableCash
	}
	for _, p := range sells {
		fillPrice := applySlippage(p.price, p.side, settings.SlippageBps)
		budget += p.qty*fillPrice - fillFee(p.qty, fillPrice, settings.FeeBps)
	}
	var desiredBuyCost float64
	for _, p := range buys {
		fillPrice := applySlippage(p.price, p.side, settings.SlippageBps)
		desiredBuyCost += p.qty*fillPrice + fillFee(p.qty, fillPrice, settings.FeeBps)
	}
	if desiredBuyCost > budget && desiredBuyCost > 0 {
		scale := math.Max(0, budget/desiredBuyCost)
		for i := range buys {
			buys[i].qty = floorStrategyOrderQuantity(buys[i].qty*scale, buys[i].qtyStep)
		}
	}
	if err := persistRebalance(e.db, pf, strategy, assignment, eval); err != nil {
		return nil, false, err
	}
	plans := buys
	if len(sells) > 0 {
		plans = sells
	}
	created := make([]*Order, 0, len(plans))
	for _, p := range plans {
		if p.qty <= 0 {
			continue
		}
		rationale := fmt.Sprintf("Strategy %s assignment #%d rebalance: %s", strategy.Name, assignment.ID, strings.Join(eval.Decisions, "; "))
		out, err := (&App{}).toolOrderPlace(app, map[string]any{
			"_project_id": pf.ProjectID, "portfolio_id": pf.ID, "symbol": p.symbol,
			"side": p.side, "type": "market", "qty": p.qty, "tif": "day",
			"rationale": rationale, "source_override": "strategy", "strategy_id": strategy.ID,
			"assignment_id": assignment.ID, "target_weight": targets[p.symbol],
		})
		if err != nil {
			return created, false, err
		}
		result, _ := out.(map[string]any)
		if fmt.Sprint(result["status"]) == "rejected" {
			return created, false, fmt.Errorf("strategy order rejected: %s", fmt.Sprint(result["detail"]))
		}
		orderID := strings.TrimSpace(fmt.Sprint(result["order_id"]))
		if orderID == "" {
			return created, false, errors.New("strategy order returned no order_id")
		}
		order, err := dbGetOrder(e.db, pf.ProjectID, orderID)
		if err != nil {
			return created, false, err
		}
		created = append(created, order)
	}
	if len(sells) > 0 {
		return created, true, nil
	}
	if _, err := e.db.Exec(`UPDATE strategy_rebalances SET status='completed' WHERE assignment_id=?`, assignment.ID); err != nil {
		return created, false, err
	}
	return created, false, nil
}

// Strategy orders use the same quantity grid as the venue pre-trade check.
// Reapply the grid after cash scaling, which can otherwise create off-lot buys.
func floorStrategyOrderQuantity(qty, step float64) float64 {
	if step <= 0 {
		return floor4(qty)
	}
	return math.Floor(qty/step+1e-9) * step
}

func floor4(v float64) float64 {
	return math.Floor(v*10_000+1e-9) / 10_000
}

func (e *engine) bumpFillCounter() {
	e.mu.Lock()
	e.fillsThisRun++
	e.fillsPending++
	e.mu.Unlock()
}

func (e *engine) takeFillCounter() int {
	e.mu.Lock()
	n := e.fillsPending
	e.fillsPending = 0
	e.mu.Unlock()
	return int(n)
}

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

func markFresh(mark *Mark, now time.Time) bool {
	if mark == nil {
		return false
	}
	markedAt, err := time.Parse(time.RFC3339Nano, mark.MarkedAt)
	if err != nil {
		return false
	}
	age := now.Sub(markedAt)
	return age >= -time.Minute && age <= staleAfter
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

// applySlippage — sells fill below mark, buys above. The trader always
// pays the spread.
func applySlippage(mark float64, side string, bps float64) float64 {
	bp := bps / 10_000.0
	switch side {
	case "buy", "yes", "no":
		return mark + mark*bp
	case "sell":
		return mark - mark*bp
	}
	return mark
}

func fillFee(qty, price, bps float64) float64 {
	if qty <= 0 || price <= 0 || bps == 0 || !finite(bps) {
		return 0
	}
	return qty * price * bps / 10_000.0
}

func isBuySide(side string) bool {
	return side == "buy" || side == "yes" || side == "no"
}
