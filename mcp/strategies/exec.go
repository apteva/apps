package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
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

var globalEngine *engine

const (
	defaultSlippageBps = 1.0 // 1 bp default; sells fill below mark, buys above
	// paper default unless a portfolio overrides it
	// %
)

func evaluateLiveStrategyAssignments(e *engine, app *sdk.AppCtx, now time.Time) (int, error) {
	assignments, err := dbActiveStrategyAssignments(e.db)
	if err != nil {
		return 0, err
	}
	totalOrders := 0
	for _, a := range assignments {
		control := strings.ToLower(strings.TrimSpace(a.ControlMode))
		if control != "" && control != "strategy" && control != "hybrid" {
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
		err = e.db.QueryRow(`SELECT targets_json FROM strategy_rebalances WHERE assignment_id=? AND strategy_version=? AND status='pending'`, a.ID, strategy.Version).Scan(&pendingTargets)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			e.logger.Warn("pending rebalance lookup failed", "assignment_id", a.ID, "err", err)
			continue
		}
		hasPendingRebalance := err == nil
		runtimeDef := strategyAssignmentDefinition(a, def)
		checkSlot, ok := strategyAssignmentCheckSlot(runtimeDef, now)
		if !hasPendingRebalance && (!ok || !strategyAssignmentCheckDue(a, checkSlot)) {
			continue
		}
		// One Trading read per due assignment; everything below sizes from it.
		if tradingReadDeferred(a.PortfolioID, now) {
			continue
		}
		state, err := newTradingReader(app).State(a.ProjectID, a.PortfolioID, runtimeDef.Universe)
		recordTradingRead(a.PortfolioID, now, err)
		if err != nil {
			e.logger.Warn("Trading unavailable; strategy run deferred", "portfolio_id", a.PortfolioID, "assignment_id", a.ID, "err", err)
			app.Emit("strategy.run.deferred", map[string]any{"schema_version": 1, "portfolio_id": a.PortfolioID, "assignment_id": a.ID,
				"strategy_id": a.StrategyID, "reason": "trading_unavailable", "error": err.Error()})
			continue
		}
		// An assignment can change while waiting for Trading.
		current, err := dbActiveStrategyAssignment(e.db, a.ProjectID, a.PortfolioID)
		if err != nil || *current != *a {
			continue
		}
		if latest, err := dbGetStrategy(e.db, a.ProjectID, strategy.ID); err != nil || latest.Status != "active" || latest.Version != strategy.Version {
			continue
		}
		pf := state.Portfolio
		if pf.Status != "active" || !portfolioAllowsAutomatedExecution(pf) || state.UsesBacktestPricing() {
			continue
		}
		if hasPendingRebalance {
			var targets []StrategyAllocation
			if json.Unmarshal([]byte(pendingTargets), &targets) == nil {
				_, _, err := placeStrategyPaperOrders(e, app, state, strategy, a, &StrategyEvaluation{TargetAllocations: targets, Decisions: []string{"Resume pending rebalance"}})
				if err != nil {
					e.logger.Warn("rebalance pending", "assignment_id", a.ID, "err", err)
				}
			}
			continue
		}
		if err := validateStrategyUniverse(pf, def, state.Violations); err != nil {
			continue
		}
		if allowed, _ := scorecardAllowsExecution(e.db, state, strategy.ID); !allowed {
			continue
		}
		if !stockStrategyExecutionReady(e, state, runtimeDef, now) {
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
		created, pending, err := placeStrategyPaperOrders(e, app, state, strategy, a, eval)
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
	return totalOrders, nil
}

func stockStrategyExecutionReady(e *engine, state *TradingState, def *StrategyDefinition, now time.Time) bool {
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
		if !markFresh(state.Mark(symbol), now.UTC()) {
			return false
		}
	}
	return true
}

func usEquityRegularSession(now time.Time) bool {
	session := usEquitySessionAt(now)
	return session.OpenDay && !now.Before(session.Open) && now.Before(session.Close)
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

type strategyOrderPlan struct {
	symbol  string
	side    string
	qty     float64
	price   float64
	qtyStep float64
}

// planStrategyOrders sizes one rebalance step from Trading's state: sells
// first, then buys scaled to the cash they leave. pending means Trading still
// has working orders, so nothing new is sized on top of them.
func planStrategyOrders(state *TradingState, eval *StrategyEvaluation) (plans []strategyOrderPlan, sells bool, targets map[string]float64, pending bool, err error) {
	pf, equity := state.Portfolio, state.Equity
	targets = map[string]float64{}
	if equity <= 0 {
		return nil, false, targets, false, nil
	}
	if len(state.WorkingOrders) > 0 {
		return nil, false, targets, true, nil
	}
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
	for _, p := range state.Positions {
		symbol := strings.ToUpper(p.Symbol)
		symbols[symbol] = true
		currentQty[symbol] += p.Qty
	}
	sellPlans, buyPlans := []strategyOrderPlan{}, []strategyOrderPlan{}
	threshold := math.Max(1, equity*0.001)
	orderedSymbols := make([]string, 0, len(symbols))
	for symbol := range symbols {
		orderedSymbols = append(orderedSymbols, symbol)
	}
	sort.Strings(orderedSymbols)
	for _, symbol := range orderedSymbols {
		mark := state.Mark(symbol)
		if mark == nil || mark.Price <= 0 {
			return nil, false, targets, false, fmt.Errorf("executable mark unavailable for %s", symbol)
		}
		profile := state.Profile(symbol)
		curValue := currentQty[symbol] * mark.Price
		diff := equity*targets[symbol] - curValue
		if math.Abs(diff) < threshold {
			continue
		}
		if diff > 0 {
			if qty := floorStrategyOrderQuantity(diff/mark.Price, profile.QtyStep); qty > 0 {
				buyPlans = append(buyPlans, strategyOrderPlan{symbol: symbol, side: "buy", qty: qty, price: mark.Price, qtyStep: profile.QtyStep})
			}
			continue
		}
		if qty := floorStrategyOrderQuantity(math.Min(currentQty[symbol], -diff/mark.Price), profile.QtyStep); qty > 0 {
			sellPlans = append(sellPlans, strategyOrderPlan{symbol: symbol, side: "sell", qty: qty, price: mark.Price, qtyStep: profile.QtyStep})
		}
	}
	settings := state.ExecutionSettings
	budget := pf.Cash
	if pf.AvailableCash != nil && *pf.AvailableCash < budget {
		budget = *pf.AvailableCash
	}
	for _, p := range sellPlans {
		fillPrice := applySlippage(p.price, p.side, settings.SlippageBps)
		budget += p.qty*fillPrice - fillFee(p.qty, fillPrice, settings.FeeBps)
	}
	var desiredBuyCost float64
	for _, p := range buyPlans {
		fillPrice := applySlippage(p.price, p.side, settings.SlippageBps)
		desiredBuyCost += p.qty*fillPrice + fillFee(p.qty, fillPrice, settings.FeeBps)
	}
	if desiredBuyCost > budget && desiredBuyCost > 0 {
		scale := math.Max(0, budget/desiredBuyCost)
		for i := range buyPlans {
			buyPlans[i].qty = floorStrategyOrderQuantity(buyPlans[i].qty*scale, buyPlans[i].qtyStep)
		}
	}
	if len(sellPlans) > 0 {
		return sellPlans, true, targets, false, nil
	}
	return buyPlans, false, targets, false, nil
}

// strategyReplans bounds how often one tick re-reads Trading after an order
// was rejected because the account moved; past that the next tick resumes.
const strategyReplans = 2

func placeStrategyPaperOrders(e *engine, app *sdk.AppCtx, state *TradingState, strategy *Strategy, assignment *StrategyAssignment, eval *StrategyEvaluation) ([]*Order, bool, error) {
	rebalanceMu.Lock()
	defer rebalanceMu.Unlock()
	pf := state.Portfolio
	created := []*Order{}
	persisted := false
	for replan := 0; ; replan++ {
		plans, sells, targets, pending, err := planStrategyOrders(state, eval)
		if err != nil || pending {
			return created, pending, err
		}
		if !persisted {
			if err := persistRebalance(e.db, pf, strategy, assignment, eval); err != nil {
				return created, false, err
			}
			persisted = true
		}
		// Chain the batch on the revision each accepted order returns, so
		// every order is checked against the state it was sized from.
		revision, chained, stale := state.Revision, true, false
		for _, p := range plans {
			if p.qty <= 0 {
				continue
			}
			if !chained {
				stale = true
				break
			}
			rationale := fmt.Sprintf("Strategy %s assignment #%d rebalance: %s", strategy.Name, assignment.ID, strings.Join(eval.Decisions, "; "))
			out, err := (&App{}).submitStrategyOrder(app, state, map[string]any{
				"_project_id": pf.ProjectID, "portfolio_id": pf.ID, "symbol": p.symbol,
				"side": p.side, "type": "market", "qty": p.qty, "tif": "day",
				"rationale": rationale, "strategy_id": strategy.ID,
				"assignment_id": assignment.ID, "target_weight": targets[p.symbol], "if_revision": revision,
			})
			if err != nil {
				return created, false, err
			}
			if fmt.Sprint(out["status"]) == "rejected" {
				if fmt.Sprint(out["code"]) == "stale_state" {
					stale = true
					break
				}
				return created, false, fmt.Errorf("strategy order rejected: %s", fmt.Sprint(out["detail"]))
			}
			orderID := strings.TrimSpace(fmt.Sprint(out["order_id"]))
			if orderID == "" || orderID == "<nil>" {
				return created, false, errors.New("strategy order returned no order_id")
			}
			created = append(created, &Order{ID: orderID, PortfolioID: pf.ID, Symbol: p.symbol, Side: p.side, Qty: p.qty, Status: fmt.Sprint(out["status"])})
			next, ok := out["revision"].(float64)
			revision, chained = int64(next), ok
		}
		if !stale {
			if sells {
				return created, true, nil
			}
			if _, err := e.db.Exec(`UPDATE strategy_rebalances SET status='completed' WHERE assignment_id=?`, assignment.ID); err != nil {
				return created, false, err
			}
			return created, false, nil
		}
		if replan >= strategyReplans {
			return created, true, nil
		}
		// Re-read the same scope: the scorecard gate compares execution policy
		// over the strategy's universe, which the first read covered.
		symbols := make([]string, 0, len(state.Profiles)+len(targets))
		for symbol := range state.Profiles {
			symbols = append(symbols, symbol)
		}
		for symbol := range targets {
			symbols = append(symbols, symbol)
		}
		if state, err = newTradingReader(app).State(pf.ProjectID, pf.ID, symbols); err != nil {
			return created, true, nil
		}
		pf = state.Portfolio
	}
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
