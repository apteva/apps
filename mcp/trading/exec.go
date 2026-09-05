package main

// Paper-execution engine. Two periodic loops:
//
//   markTick   — refresh marks from the Provider, recompute equity,
//                check daily-loss halt, try to fill working orders.
//   alertTick  — re-evaluate every active alert; on match, fire a
//                SendEvent to the bound instances + journal entry.
//
// Both are registered as Workers via main.go's Workers() so the SDK
// supervises them; we don't manage goroutines directly.

import (
	"context"
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

// markTick — runs every tick_seconds. Refreshes marks, then attempts
// fills against the new marks, then evaluates daily-loss halts. One
// pass; deterministic.
func markTick(ctx context.Context, app *sdk.AppCtx) error {
	e := globalEngine
	if e == nil {
		return errors.New("engine not initialised")
	}
	tickStart := time.Now().UTC()
	if portfolios, err := dbAllPortfolios(e.db); err == nil {
		for _, pf := range portfolios {
			if pf.Status != "active" {
				continue
			}
			equity, err := computeEquity(e.db, pf)
			if err != nil {
				continue
			}
			policy, err := dbGetPortfolioRiskPolicy(e.db, pf)
			if err != nil {
				continue
			}
			breach, err := currentLossBreach(e.db, pf, policy, equity)
			if err != nil || breach == nil {
				continue
			}
			if err := dbSetPortfolioStatus(e.db, pf.ID, "halted"); err != nil {
				continue
			}
			if pf.Mode == "live" {
				_, _ = e.db.Exec(`UPDATE orders SET cancel_requested=1 WHERE portfolio_id=? AND status='working'`, pf.ID)
			}
			emit("risk.limit.breached", map[string]any{"portfolio_id": pf.ID, "project_id": pf.ProjectID, "code": breach.Code, "detail": breach.Detail})
			emit("portfolio.status.changed", map[string]any{"id": pf.ID, "status": "halted", "reason": breach.Code})
		}
	}
	// 1. Refresh marks. One transaction per tick so we hold the
	// writer lock once instead of N times. A single bad row gets
	// logged but does NOT poison the whole batch — we keep going
	// and commit the rows that did succeed. This is the difference
	// between "one weird symbol stalls the engine" and "engine
	// ticks reliably regardless of provider hiccups".
	marksOK := 0
	marks := []*Mark{}
	if !dbHasBacktestPortfolio(e.db, projectIDFromEnvOnly()) {
		marks = e.provider.Universe()
		marks = append(marks, refreshPersistedMarks(e, marks)...)
		if tx, err := e.db.Begin(); err == nil {
			for _, m := range marks {
				if err := dbUpsertMarkExec(tx, m); err != nil {
					symbol := ""
					if m != nil {
						symbol = m.Symbol
					}
					e.logger.Warn("upsert mark failed", "symbol", symbol, "err", err)
					continue
				}
				marksOK++
			}
			if err := tx.Commit(); err != nil {
				e.logger.Warn("mark batch commit failed", "err", err)
				marksOK = 0
			}
		} else {
			e.logger.Warn("mark batch begin failed", "err", err)
		}
	}

	// 1.5 Strategy assignments. App ticks stay fast for fresh marks and
	// fills, but deterministic strategies only rebalance when their
	// assignment cadence says they are due.
	strategyOrders := evaluateLiveStrategyAssignments(e, app, tickStart)

	// 2. Working orders — dispatch per portfolio mode.
	//    paper → in-process tryFill against the marks we just refreshed.
	//    live  → tryReconcile polls the broker for state and mirrors locally.
	working, err := dbWorkingOrders(e.db)
	if err != nil {
		e.logger.Warn("query working orders failed", "err", err)
		return nil
	}
	fillsThisTick := 0
	for _, o := range working {
		pf, perr := dbGetPortfolioAnyProject(e.db, o.PortfolioID)
		if perr != nil {
			e.logger.Warn("portfolio lookup failed for order", "order_id", o.ID, "err", perr)
			continue
		}
		switch pf.Mode {
		case "live":
			if err := tryReconcile(e, pf, o); err != nil {
				e.logger.Warn("reconcile failed", "order_id", o.ID, "err", err)
			}
		default: // "paper" | ""
			if err := tryFill(e, o); err != nil {
				e.logger.Warn("fill attempt failed", "order_id", o.ID, "err", err)
				continue
			}
		}
	}
	fillsThisTick = e.takeFillCounter()

	// 2.5 Periodic account reconcile for live portfolios — every 12 ticks
	//     (60s @ default 5s tick). Catches cash drift + positions placed
	//     outside our app (broker UI, mobile) so the agent doesn't reason
	//     on stale numbers.
	e.mu.Lock()
	ticksBefore := e.ticks
	e.mu.Unlock()
	if ticksBefore > 0 && ticksBefore%12 == 0 {
		reconcileLiveAccounts(e)
	}

	// 3. Portfolio risk + objective sweep, shared by simulation, broker
	// paper, and broker live portfolios.
	pfs, err := dbAllPortfolios(e.db)
	if err != nil {
		return nil
	}
	for _, p := range pfs {
		eq, err := computeEquity(e.db, p)
		if err != nil {
			continue
		}
		now := executionTime(e.db, p.ID)
		state, _ := dbUpdatePortfolioRiskState(e.db, p, eq)
		_ = initializeDueObjectiveBaselines(e.db, p, eq, now)
		policy, policyErr := dbGetPortfolioRiskPolicy(e.db, p)
		if policyErr != nil {
			continue
		}
		day := utcDay(now)
		baseline, ok, _ := dbGetDayBaseline(e.db, p.ID, day)
		if !ok {
			_ = dbSetDayBaseline(e.db, p.ID, day, eq)
			baseline = eq
		}
		dayPctMove := 0.0
		if baseline > 0 {
			dayPctMove = (eq - baseline) / baseline * 100
		}

		snap, _ := snapshotPortfolio(e.db, p)
		objectives, _ := dbListPortfolioObjectives(e.db, p, false)
		for _, objective := range objectives {
			objectiveProgress(e.db, p, objective, snap)
		}
		emit("portfolio.performance.updated", map[string]any{
			"portfolio_id": p.ID, "project_id": p.ProjectID, "equity": eq,
			"day_return_pct": dayPctMove, "risk_policy": policy, "risk_state": state,
			"objectives": objectives,
		})

		if p.Status == "halted" {
			if p.Mode == "live" {
				cancelLiveWorkingOrders(e, p, "risk_halted")
			}
			continue
		}
		reason, actual, limit := "", 0.0, 0.0
		if state != nil && state.CurrentDrawdownPct < -policy.MaxDrawdownPct {
			reason, actual, limit = "max_drawdown_halt", state.CurrentDrawdownPct, -policy.MaxDrawdownPct
		} else if dayPctMove < -policy.MaxDailyLossPct {
			reason, actual, limit = "daily_loss_halt", dayPctMove, -policy.MaxDailyLossPct
		}
		if reason == "" {
			continue
		}
		// Cancel broker working orders before flipping the local status. The
		// cancellation is best-effort; reconciliation keeps checking it.
		if p.Mode == "live" {
			cancelLiveWorkingOrders(e, p, reason)
		}
		_ = dbSetPortfolioStatus(e.db, p.ID, "halted")
		emit("portfolio.status.changed", map[string]any{
			"id": p.ID, "status": "halted", "reason": reason,
			"actual_pct": actual, "threshold_pct": limit, "mode": p.Mode,
		})
		emit("risk.limit.breached", map[string]any{
			"portfolio_id": p.ID, "project_id": p.ProjectID, "code": reason,
			"actual_pct": actual, "limit_pct": limit, "mode": p.Mode,
		})
		body := fmt.Sprintf("Risk halt fired (%s) — equity %.2f, actual %.2f%%, boundary %.2f%%.",
			reason, eq, actual, limit)
		if entryID, jerr := dbInsertJournal(e.db, p.ProjectID, p.ID, "alert", body,
			map[string]any{"rule": reason, "actual_pct": actual, "threshold_pct": limit}); jerr == nil {
			emit("journal.appended", map[string]any{
				"id": entryID, "portfolio_id": p.ID, "kind": "alert", "body": body,
			})
		}
		notifyInstances(e, p, fmt.Sprintf("HALT %s — %s fired (%.2f%%).", p.Name, reason, actual))
	}

	// 4. Record tick metrics + emit one-line summary so a sidecar log
	// tail tells you whether the engine is actually working.
	e.mu.Lock()
	e.lastTickAt = tickStart
	e.ticks++
	e.lastWorkingSeen = int64(len(working))
	e.lastFillsThisTick = int64(fillsThisTick)
	e.lastMarksRefreshed = int64(marksOK)
	tickN := e.ticks
	e.mu.Unlock()
	e.logger.Info("tick",
		"n", tickN, "marks", marksOK, "working", len(working),
		"strategy_orders", strategyOrders,
		"fills_this_tick", fillsThisTick, "fills_total", e.fillsThisRun)

	// 5. App-event: one slim payload per tick. Carries the providers
	// snapshot (UI's data-source pill reads this) + a marks delta the
	// desk can apply directly to its in-memory universe without
	// re-fetching /universe. Empty marks list still emits — it's a
	// heartbeat the UI uses to confirm liveness.
	delta := significantMarkDeltas(e, marks)
	emit("tick", map[string]any{
		"n":               tickN,
		"providers":       providerHealthSnapshot(),
		"marks":           delta,
		"working":         len(working),
		"strategy_orders": strategyOrders,
		"fills_this_tick": fillsThisTick,
	})
	app.Emit("market.heartbeat", map[string]any{
		"schema_version": 1, "tick": tickN, "providers": providerHealthSnapshot(),
		"streams": alpacaStreamHealthSnapshot(app.CurrentProject()), "marks_refreshed": marksOK,
		"working_orders": len(working), "strategy_orders": strategyOrders,
	})
	return nil
}

// refreshPersistedMarks keeps dynamically admitted symbols (watchlists,
// positions, and orders outside the bootstrap universe) current. Network
// fetches are bounded and concurrent so one slow symbol does not serialize the
// entire execution tick.
func refreshPersistedMarks(e *engine, refreshed []*Mark) []*Mark {
	persisted, err := dbListRefreshSymbols(e.db)
	if err != nil {
		return nil
	}
	base := map[string]bool{}
	for _, mark := range refreshed {
		if mark != nil {
			base[strings.ToUpper(mark.Symbol)] = true
		}
	}
	symbols := []string{}
	for _, symbol := range persisted {
		if !base[strings.ToUpper(symbol)] {
			symbols = append(symbols, symbol)
		}
	}
	if len(symbols) == 0 {
		return nil
	}
	type result struct {
		mark *Mark
		err  error
	}
	results := make(chan result, len(symbols))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, symbol := range symbols {
		symbol := symbol
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			mark, err := e.provider.Quote(symbol)
			<-sem
			results <- result{mark: mark, err: err}
		}()
	}
	wg.Wait()
	close(results)
	out := make([]*Mark, 0, len(symbols))
	for res := range results {
		if res.err != nil || res.mark == nil {
			continue
		}
		out = append(out, res.mark)
	}
	return out
}

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
			if portfolioBrokerBacked(pf) {
				stored, err := dbGetOrder(e.db, pf.ProjectID, order.ID)
				if err != nil || stored.Status == "working" {
					resolved = false
				}
				continue
			}
			if err := tryFill(e, order); err != nil {
				e.logger.Warn("strategy order execution failed", "assignment_id", a.ID, "order_id", order.ID, "err", err)
				resolved = false
				break
			}
			stored, err := dbGetOrder(e.db, pf.ProjectID, order.ID)
			if err != nil || stored.Status != "filled" {
				resolved = false
				break
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
	if _, live := e.provider.(*liveProvider); !live {
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
		symbol string
		side   string
		qty    float64
		price  float64
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
		curValue := currentQty[symbol] * mark.Price
		targetValue := equity * targets[symbol]
		diff := targetValue - curValue
		if math.Abs(diff) < threshold {
			continue
		}
		if diff > 0 {
			qty := floor4(diff / mark.Price)
			if qty > 0 {
				buys = append(buys, plan{symbol: symbol, side: "buy", qty: qty, price: mark.Price})
			}
			continue
		}
		qty := floor4(math.Min(currentQty[symbol], -diff/mark.Price))
		if qty > 0 {
			sells = append(sells, plan{symbol: symbol, side: "sell", qty: qty, price: mark.Price})
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
			buys[i].qty = floor4(buys[i].qty * scale)
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

func floor4(v float64) float64 {
	return math.Floor(v*10_000+1e-9) / 10_000
}

// significantMarkDeltas filters the universe to symbols whose mark
// moved enough to bother sending. Threshold per asset class:
//
//	crypto/equity/etf — 0.1% relative move
//	polymarket        — 0.5 cent (0.005) absolute move on YES
//
// On the very first tick (no last-emitted yet) we send everything so
// fresh subscribers don't have to wait for movement.
func significantMarkDeltas(e *engine, marks []*Mark) []*Mark {
	e.deltaMu.Lock()
	defer e.deltaMu.Unlock()
	if e.lastEmitted == nil {
		e.lastEmitted = map[string]float64{}
	}
	out := make([]*Mark, 0, len(marks))
	first := len(e.lastEmitted) == 0
	for _, m := range marks {
		prev, ok := e.lastEmitted[m.Symbol]
		send := first || !ok
		if !send {
			if m.AssetClass == "polymarket" {
				if abs(m.Price-prev) >= 0.005 {
					send = true
				}
			} else if prev > 0 && abs((m.Price-prev)/prev) >= 0.001 {
				send = true
			}
		}
		if send {
			out = append(out, m)
			e.lastEmitted[m.Symbol] = m.Price
		}
	}
	return out
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
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
		return nil // no mark yet — skip
	}
	if _, live := e.provider.(*liveProvider); live && !markFresh(mark, time.Now().UTC()) {
		return nil // never execute against stale live data
	}
	pf, err := dbGetPortfolioAnyProject(e.db, o.PortfolioID)
	if err != nil {
		return err
	}
	if pf.Status != "active" {
		// Working orders on a paused/halted portfolio just sit. They'll
		// resume when the portfolio is resumed; or be cancelled by the operator.
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
	// Mark used for the rule decision: YES vs NO for polymarket.
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
		// A working order waits through a closed session or venue outage. New
		// orders are rejected earlier by toolOrderPlace; existing intent is
		// preserved and resumes when the venue becomes executable again.
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
		// Limit orders become executable at the opposite quote, not merely at
		// the midpoint/last trade. Falling back to mp keeps older marks and
		// prediction-market feeds without a book compatible.
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
			// polymarket — buyer of YES/NO is willing to pay at most limit
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
		// A marketable limit may cap adverse slippage. Attribute the actual
		// executed distance rather than retaining the pre-cap estimate.
		if estimate.LiquidityRole == "taker" {
			estimate.SlippageCost = math.Abs(fillPrice-estimate.TouchPrice) * o.Qty
		}
	case "stop":
		if o.StopPrice == nil {
			return nil
		}
		// Stop fires when mark crosses; turns into market.
		ok := false
		switch o.Side {
		case "buy":
			ok = mp >= *o.StopPrice
		case "sell":
			ok = mp <= *o.StopPrice
		default:
			return nil // no stops on polymarket in v0.1
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

	// Claim, validate, and apply inside one transaction. The status CAS stops
	// overlapping ticks from filling one order twice, while the in-transaction
	// balance checks prevent distinct concurrent buys from spending stale cash.
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

	// App-events: one fill = three logical things changed (the order
	// resolved, a position mutated, the journal got a row). Emit each
	// so UI subscribers can do narrow cache-invalidation rather than
	// re-fetching the whole portfolio.
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
		// Position closed entirely (sell flat). Surface it explicitly.
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

// ─── Alert engine ──────────────────────────────────────────────────

func alertTick(ctx context.Context, app *sdk.AppCtx) error {
	e := globalEngine
	if e == nil {
		return nil
	}
	alerts, err := dbActiveAlerts(e.db)
	if err != nil {
		return nil
	}
	for _, a := range alerts {
		if a.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, a.ExpiresAt); err == nil && time.Now().After(t) {
				_, _ = e.db.Exec(`UPDATE alerts SET status='expired' WHERE id = ?`, a.ID)
				continue
			}
		}
		match, value := evaluateAlert(e, a)
		if !match {
			continue
		}
		_ = dbFireAlert(e.db, a.ID)
		pf, _ := dbGetPortfolioAnyProject(e.db, a.PortfolioID)
		if pf == nil {
			continue
		}
		body := fmt.Sprintf("ALERT %s — %s %s threshold (%v ↔ %v)",
			a.Symbol, a.Rule, "matched", value, a.Threshold)
		emit("alert.fired", map[string]any{
			"alert_id": a.ID, "portfolio_id": pf.ID,
			"symbol": a.Symbol, "rule": a.Rule,
			"threshold": a.Threshold, "value": value,
		})
		if entryID, jerr := dbInsertJournal(e.db, pf.ProjectID, pf.ID, "alert", body, map[string]any{
			"alert_id": a.ID, "rule": a.Rule, "threshold": a.Threshold, "value": value, "symbol": a.Symbol,
		}); jerr == nil {
			emit("journal.appended", map[string]any{
				"id": entryID, "portfolio_id": pf.ID, "kind": "alert", "body": body,
			})
		}
		notifyInstances(e, pf, body)
	}
	return nil
}

func evaluateAlert(e *engine, a *Alert) (bool, float64) {
	switch a.Rule {
	case "mark_above", "mark_below", "yes_above", "yes_below":
		mark, err := dbGetMark(e.db, a.Symbol)
		if err != nil {
			return false, 0
		}
		mp := mark.Price
		if a.Rule == "yes_above" || a.Rule == "yes_below" {
			// 'yes' rules already use mark.Price (which is YES probability for polymarkets)
		}
		switch a.Rule {
		case "mark_above", "yes_above":
			return mp > a.Threshold, mp
		case "mark_below", "yes_below":
			return mp < a.Threshold, mp
		}
	case "day_pnl_below":
		pf, err := dbGetPortfolioAnyProject(e.db, a.PortfolioID)
		if err != nil {
			return false, 0
		}
		eq, _ := computeEquity(e.db, pf)
		baseline, ok, _ := dbGetDayBaseline(e.db, pf.ID, utcDay(time.Now()))
		if !ok || baseline <= 0 {
			return false, 0
		}
		pct := (eq - baseline) / baseline * 100
		return pct < a.Threshold, pct
	}
	return false, 0
}

// ─── Live broker integration ──────────────────────────────────────
//
// tryReconcile: poll the broker for a working live order's current
// state and mirror progress (fills, status flips) into local tables.
// Soft-fails on transient broker errors — the order stays working and
// the next tick retries. The agent doesn't see flapping.
//
// All three callers (this, the inline-fill path in toolOrderPlace, and
// halt-cancels) go through applyBrokerProgress for the "what changed,
// what to write, what to emit" rules. One source of truth.

func tryReconcile(e *engine, pf *Portfolio, o *Order) error {
	if globalCtx == nil {
		return errors.New("no app ctx — engine not fully mounted")
	}
	bb, ferr := brokerFor(globalCtx, pf)
	if ferr != nil {
		// Operator unbound the broker (or the slug isn't registered).
		// Don't reject — the order may resume when rebound. Log once per
		// tick. The agent sees the order stay 'working' which is the
		// truthful state given we can't poll.
		e.logger.Warn("live order has no broker bound; staying working",
			"order_id", o.ID, "broker_slug", pf.BrokerSlug, "err", ferr)
		return nil
	}
	brokerOrderID, _ := dbBrokerOrderIDFor(e.db, o.ID)
	var cancel int
	if err := e.db.QueryRow(`SELECT cancel_requested FROM orders WHERE id=?`, o.ID).Scan(&cancel); err != nil {
		return err
	}
	if err := validateWorkingPolicy(e.db, pf, o); err != nil {
		cancel = 1
	}
	if cancel != 0 {
		_, err := (&App{}).toolOrderCancel(globalCtx, map[string]any{"_project_id": pf.ProjectID, "order_id": o.ID, "reason": "pending_cancel"})
		if err != nil {
			return err
		}
	}
	if cancel != 0 {
		fresh, err := dbGetOrder(e.db, pf.ProjectID, o.ID)
		if err != nil {
			return err
		}
		*o = *fresh
		if o.Status != "working" {
			return nil
		}
		brokerOrderID, _ = dbBrokerOrderIDFor(e.db, o.ID)
	}
	if brokerOrderID == "" && !recoverableByClientID(bb.Adapter.Slug()) {
		_, err := e.db.Exec(`UPDATE orders SET reconciliation_required=1,rejection_detail='broker response missing: manual account reconciliation required' WHERE id=?`, o.ID)
		return err
	}
	args := bb.Adapter.StatusArgs(o, brokerOrderID)
	res, err := globalCtx.PlatformAPI().ExecuteIntegrationTool(
		bb.ConnectionID, bb.toolFor("order.status"), args,
	)
	if err != nil {
		noteVenueCall(bb.Adapter.Slug(), err)
		// Transient — retry next tick.
		return err
	}
	if res == nil || !res.Success {
		code, detail := bb.Adapter.ErrText(res, nil)
		noteVenueCall(bb.Adapter.Slug(), fmt.Errorf("%s: %s", code, detail))
		// Broker confirms the order doesn't exist on its side — likely
		// the placement itself failed silently. Reject locally so the
		// agent stops waiting on a phantom.
		if bb.Adapter.IsUnknownOrderError(code, detail) && brokerOrderID != "" {
			_ = dbRejectOrder(e.db, o.ID, "broker_unknown_order",
				"broker reports order does not exist; treating as failed-to-place")
			emit("order.rejected", map[string]any{
				"order_id": o.ID, "code": "broker_unknown_order", "detail": detail,
			})
			return nil
		}
		return fmt.Errorf("broker get_order: %s: %s", code, detail)
	}
	br, perr := bb.Adapter.ParseOrder(res.Data)
	if perr != nil {
		noteVenueCall(bb.Adapter.Slug(), perr)
		return perr
	}
	noteVenueCall(bb.Adapter.Slug(), nil)
	previousFilled := o.FilledQty
	changed, aerr := applyBrokerProgress(e.db, pf.ProjectID, pf, o, br)
	if aerr != nil {
		return aerr
	}
	if changed && br.ExecutedQty > previousFilled+1e-9 {
		e.bumpFillCounter()
	}
	return nil
}

// applyBrokerProgress mirrors a parsed broker response into local
// tables. Used by:
//   - toolOrderPlace (inline-fill path) when create_order returns FILLED
//     synchronously with a fills array.
//   - tryReconcile (every tick) when a polled get_order shows progress.
//   - cancelLiveWorkingOrders (halt path) when the broker confirms
//     a cancel.
//
// Mutates `o` to reflect new filled_qty / avg_fill_price / status so
// the caller can return current state without re-reading. Emits all
// the same SSE events as the paper engine — UI stays mode-agnostic.
//
// Returns (changed, error). changed=true means at least one of fills
// or status flipped; callers can use this to decide whether to bump
// metrics.
func applyBrokerProgress(db *sql.DB, projectID string, pf *Portfolio, o *Order, br *brokerOrderResult) (bool, error) {
	if br == nil || !finite(br.ExecutedQty) || !finite(br.CummulativeQuoteQty) || br.ExecutedQty < 0 || br.CummulativeQuoteQty < 0 {
		return false, errors.New("invalid broker cumulative execution")
	}
	deltaQty := br.ExecutedQty - o.FilledQty
	changed := false

	if deltaQty > 1e-9 {
		// VWAP for the new fill chunk. Prefer the synchronous fills
		// array (create_order with newOrderRespType=FULL); fall back to
		// whole-order VWAP via cumulative quote qty (polled get_order
		// doesn't carry per-fill detail).
		profile := resolveVenueProfile(db, pf, o.Symbol, o.AssetClass)
		var deltaPrice, fee float64
		rawCommissions := make([]map[string]any, 0)
		feeComplete := true
		if len(br.Fills) > 0 {
			var qSum, pvSum float64
			for _, f := range br.Fills {
				qSum += f.Qty
				pvSum += f.Qty * f.Price
				converted, ok := convertCommissionToQuote(db, o, f.Commission, f.CommissionAsset, profile.FeeCurrency, f.Price)
				if ok {
					fee += converted
				} else {
					feeComplete = false
				}
				rawCommissions = append(rawCommissions, map[string]any{"amount": f.Commission, "currency": f.CommissionAsset, "converted": converted, "conversion_ok": ok})
			}
			if qSum > 0 {
				deltaPrice = pvSum / qSum
			}
		}
		// Broker quantities/notional are cumulative. Subtract the amounts
		// already committed, never apply the cumulative average to a delta.
		cumulativeNotional := br.CummulativeQuoteQty
		if cumulativeNotional <= 0 && deltaPrice > 0 {
			cumulativeNotional = deltaPrice * br.ExecutedQty
		}
		if cumulativeNotional > 0 {
			deltaPrice = (cumulativeNotional - o.FilledQty*o.AvgFillPrice) / deltaQty
		}
		deltas, err := commissionDeltas(db, o, br)
		if err != nil {
			return false, err
		}
		fee = 0
		for _, c := range deltas {
			converted, ok := convertCommissionToQuote(db, o, c.Delta, c.Currency, profile.FeeCurrency, deltaPrice)
			if ok {
				fee += converted
			} else {
				feeComplete = false
			}
		}

		if deltaPrice <= 0 {
			return false, fmt.Errorf("cannot resolve fill price for order %s (executed_qty=%v, fills=%d)",
				o.ID, br.ExecutedQty, len(br.Fills))
		}

		tx, err := db.Begin()
		if err != nil {
			return false, err
		}
		// CAS guard: claim the delta by updating filled_qty conditioned on
		// the value we read into `o`. Two overlapping ticks (or an
		// order_place inline-apply racing with a tryReconcile) can both
		// observe the same stale FilledQty and otherwise both apply the
		// same delta. The conditional UPDATE serializes them: the second
		// arrival sees rows_affected = 0 and bails before touching fills,
		// positions, or cash.
		cumAvg := (o.FilledQty*o.AvgFillPrice + deltaQty*deltaPrice) / br.ExecutedQty
		casRes, err := tx.Exec(`UPDATE orders SET filled_qty = ?, avg_fill_price = ?
			WHERE id = ? AND ABS(filled_qty - ?) < 1e-9`,
			br.ExecutedQty, cumAvg, o.ID, o.FilledQty)
		if err != nil {
			_ = tx.Rollback()
			return false, err
		}
		if n, _ := casRes.RowsAffected(); n == 0 {
			_ = tx.Rollback()
			return false, nil // another tick already applied this fill
		}
		if err := dbInsertFillDetailed(tx, projectID, o.ID, pf.ID, deltaQty, deltaPrice, fee, FillCostDetails{
			VenueSlug: profile.VenueSlug, FeeCurrency: profile.FeeCurrency, FeeSource: "broker",
			LiquidityRole: "unknown", Metadata: map[string]any{"raw_commissions": rawCommissions, "fee_conversion_complete": feeComplete},
		}); err != nil {
			_ = tx.Rollback()
			return false, err
		}
		if err := dbApplyFill(tx, pf.ID, projectID, o, deltaQty, deltaPrice); err != nil {
			_ = tx.Rollback()
			return false, err
		}
		if err := applyCommissionBalances(tx, pf, o, deltas); err != nil {
			tx.Rollback()
			return false, err
		}

		if err := dbAccruePositionAccountingTx(tx, pf.ID, o.Symbol, polyOutcome(o), 0, fee); err != nil {
			_ = tx.Rollback()
			return false, err
		}
		body := fmt.Sprintf("%s %s %v @ %s — %s (broker %s)",
			strings.ToUpper(o.Symbol), strings.ToUpper(o.Side), deltaQty,
			formatPrice(deltaPrice, o.AssetClass), o.ID, br.BrokerOrderID)
		if err := dbInsertJournalTx(tx, projectID, pf.ID, "fill", body, map[string]any{
			"order_id": o.ID, "qty": deltaQty, "price": deltaPrice, "fee": fee,
			"fee_currency": profile.FeeCurrency, "fee_source": "broker", "liquidity_role": "unknown",
			"raw_commissions": rawCommissions, "fee_conversion_complete": feeComplete,
			"side": o.Side, "symbol": o.Symbol,
			"source": "broker", "broker_order_id": br.BrokerOrderID,
		}); err != nil {
			_ = tx.Rollback()
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}

		o.FilledQty = br.ExecutedQty
		o.AvgFillPrice = cumAvg
		changed = true

		emit("order.filled", map[string]any{
			"order_id": o.ID, "portfolio_id": pf.ID, "symbol": o.Symbol,
			"side": o.Side, "qty": deltaQty, "price": deltaPrice,
			"fee": fee, "fee_currency": profile.FeeCurrency, "fee_source": "broker",
			"liquidity_role": "unknown", "broker_order_id": br.BrokerOrderID,
		})
		if newPos, _ := dbGetPosition(db, pf.ID, o.Symbol, polyOutcome(o)); newPos != nil {
			emit("position.changed", map[string]any{
				"portfolio_id": pf.ID, "symbol": newPos.Symbol,
				"asset_class": newPos.AssetClass, "qty": newPos.Qty,
				"avg_cost": newPos.AvgCost, "realized_pnl": newPos.RealizedPnL,
			})
		} else {
			emit("position.changed", map[string]any{
				"portfolio_id": pf.ID, "symbol": o.Symbol, "qty": 0.0, "closed": true,
			})
		}
		emit("journal.appended", map[string]any{
			"portfolio_id": pf.ID, "kind": "fill", "body": body,
		})
	}

	// Some venues attach commission details after the execution update.
	// Apply these even when cumulative quantity has not advanced.
	if deltaQty <= 1e-9 && math.Abs(br.ExecutedQty-o.FilledQty) < 1e-9 && o.FilledQty > 0 && len(br.Fills) > 0 {
		feesChanged, err := applyLateBrokerCommissions(db, pf, o, br)
		if err != nil {
			return changed, err
		}
		changed = changed || feesChanged
	}

	// Terminal status flips. Status moves are independent of fills —
	// PARTIALLY_FILLED stays "working", FILLED resolves, CANCELED /
	// REJECTED close out. UPDATEs are conditional on the row still being
	// 'working' so racing reconciles don't re-emit the terminal event.
	switch br.Status {
	case "filled":
		if o.Status != "filled" {
			res, err := db.Exec(`UPDATE orders SET status='filled', resolved_at=CURRENT_TIMESTAMP
				WHERE id = ? AND status = 'working'`, o.ID)
			if err != nil {
				return changed, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				o.Status = "filled"
				changed = true
			}
		}
	case "cancelled":
		if o.Status != "cancelled" {
			res, err := db.Exec(`UPDATE orders SET status='cancelled', resolved_at=CURRENT_TIMESTAMP, rejection_detail=?
				WHERE id = ? AND status = 'working'`,
				"broker_"+br.BrokerStatus, o.ID)
			if err != nil {
				return changed, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				o.Status = "cancelled"
				emit("order.cancelled", map[string]any{
					"order_id": o.ID, "broker_status": br.BrokerStatus,
				})
				changed = true
			}
		}
	case "rejected":
		if o.Status != "rejected" {
			res, err := db.Exec(`UPDATE orders SET status='rejected', rejection_code=?, rejection_detail=?,
				resolved_at=CURRENT_TIMESTAMP WHERE id = ? AND status = 'working'`,
				"broker_rejected", br.BrokerStatus, o.ID)
			if err != nil {
				return changed, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				o.Status = "rejected"
				emit("order.rejected", map[string]any{
					"order_id": o.ID, "code": "broker_rejected", "detail": br.BrokerStatus,
				})
				changed = true
			}
		}
	}

	return changed, nil
}

// reconcileLiveAccounts — periodic sweep that pulls broker account state
// for every live portfolio and reconciles cash + holdings against local.
// Best-effort: errors get logged, the next sweep retries.
//
// Catches:
//   - cash drift (commissions in non-USDT, dust the order path missed)
//   - positions placed outside our app (broker UI / mobile / another bot)
//
// Does NOT modify avg_cost — that's the local cost-basis source of
// truth. New positions discovered here are seeded with avg_cost = current
// mark (best-effort) and journaled with source=broker_reconcile so the
// audit trail is honest about provenance.
func reconcileLiveAccounts(e *engine) {
	if globalCtx == nil {
		return
	}
	pfs, err := dbAllPortfolios(e.db)
	if err != nil {
		return
	}
	for _, p := range pfs {
		if p.Mode != "live" {
			continue
		}
		bb, ferr := brokerFor(globalCtx, p)
		if ferr != nil {
			// Broker not bound for this portfolio's slug — skip silently;
			// next reconcile after rebind will catch up.
			continue
		}
		// Capture an ID watermark before the broker call. Timestamps in older
		// databases only have second precision, so comparing fill timestamps
		// can miss a fill that lands in the same second as this snapshot.
		revision, err := accountRevision(e.db, p.ID)
		if err != nil {
			continue
		}
		res, err := globalCtx.PlatformAPI().ExecuteIntegrationTool(
			bb.ConnectionID, bb.toolFor("account.summary"), map[string]any{},
		)
		if err != nil || res == nil || !res.Success {
			code, detail := bb.Adapter.ErrText(res, err)
			noteVenueCall(bb.Adapter.Slug(), fmt.Errorf("%s: %s", code, detail))
			e.logger.Warn("account reconcile failed", "portfolio_id", p.ID, "broker", bb.Adapter.Slug())
			continue
		}
		acct, perr := bb.Adapter.ParseAccount(res.Data)
		if perr != nil {
			noteVenueCall(bb.Adapter.Slug(), perr)
			e.logger.Warn("account parse failed", "portfolio_id", p.ID, "err", perr)
			continue
		}
		// Adapters with a separate holdings call (Alpaca) — fetch +
		// merge here so the downstream discovery logic sees a single
		// unified acct view.
		holdingsComplete := acct.HoldingsComplete
		if tool := bb.Adapter.HoldingsTool(); tool != "" {
			holdingsComplete = false
			posRaw, herr := globalCtx.PlatformAPI().ExecuteIntegrationTool(
				bb.ConnectionID, tool, map[string]any{},
			)
			if herr == nil && posRaw != nil && posRaw.Success {
				if holdings, perr2 := bb.Adapter.ParseHoldings(posRaw.Data); perr2 == nil {
					if acct.Holdings == nil {
						acct.Holdings = map[string]brokerBalance{}
					}
					for k, v := range holdings {
						acct.Holdings[k] = v
					}
					holdingsComplete = true
				} else {
					noteVenueCall(bb.Adapter.Slug(), perr2)
				}
			} else {
				noteVenueCall(bb.Adapter.Slug(), firstError(herr, errors.New("holdings response unsuccessful")))
			}
			if !holdingsComplete {
				continue
			}
		}
		if holdingsComplete {
			noteVenueCall(bb.Adapter.Slug(), nil)
		}
		if err := applyAccountSnapshot(e.db, p, acct, holdingsComplete, revision); err != nil {
			e.logger.Warn("account reconciliation deferred", "portfolio_id", p.ID, "err", err)
		}

	}
}

func brokerBalanceTotal(b brokerBalance) float64 {
	if b.Total != 0 {
		return b.Total
	}
	return b.Free
}

// cancelLiveWorkingOrders — invoked from the daily-loss halt sweep.
// Best-effort cancels every working broker order on the halted portfolio
// before status flips. Failures are logged; the next reconcile tick
// will catch any that didn't cancel cleanly.
func cancelLiveWorkingOrders(e *engine, p *Portfolio, reason string) {
	if globalCtx == nil {
		return
	}
	working, err := dbWorkingOrders(e.db)
	if err != nil {
		return
	}
	for _, o := range working {
		if o.PortfolioID != p.ID {
			continue
		}
		_, err := (&App{}).toolOrderCancel(globalCtx, map[string]any{"_project_id": p.ProjectID, "order_id": o.ID, "reason": reason})
		if err != nil {
			e.logger.Warn("halt cancellation pending", "order_id", o.ID, "err", err)
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
