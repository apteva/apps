package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
	rules "github.com/apteva/apps/mcp/strategies/internal/ruleengine"
)

type StrategyAllocation struct {
	Symbol string  `json:"symbol"`
	Weight float64 `json:"weight"`
}

type StrategyEvaluation struct {
	StrategyID        int64                `json:"strategy_id,omitempty"`
	StrategyVersion   int                  `json:"strategy_version,omitempty"`
	AsOf              string               `json:"as_of"`
	TargetAllocations []StrategyAllocation `json:"target_allocations"`
	Decisions         []string             `json:"decisions"`
	Warnings          []string             `json:"warnings,omitempty"`
}

type StrategyValidationResult struct {
	StrategyID      int64                        `json:"strategy_id"`
	StrategyVersion int                          `json:"strategy_version"`
	SplitPct        float64                      `json:"split_pct"`
	MarketSource    string                       `json:"market_source"`
	Train           *StrategyValidationPeriod    `json:"train"`
	Test            *StrategyValidationPeriod    `json:"test"`
	Verdict         string                       `json:"verdict"`
	Scorecard       *StrategyScorecardEvaluation `json:"scorecard,omitempty"`
}

type StrategyValidationPeriod struct {
	Label       string               `json:"label"`
	Run         *BacktestRun         `json:"run"`
	Performance *BacktestPerformance `json:"performance,omitempty"`
	Metrics     map[string]float64   `json:"metrics,omitempty"`
}

func avgFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func stddevFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	mean := avgFloat(values)
	var sum float64
	for _, v := range values {
		d := v - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(values)))
}

func strategyCadenceDuration(cadence string) (time.Duration, bool) {
	switch cadence {
	case "1m":
		return time.Minute, true
	case "5m":
		return 5 * time.Minute, true
	case "15m":
		return 15 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "4h":
		return 4 * time.Hour, true
	case "1d":
		return 24 * time.Hour, true
	case "1w":
		return 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

type strategyBarsProvider interface {
	StrategyBars(symbol, interval string, limit int) ([]Bar, error)
}

func loadLiveStrategyBars(provider Provider, symbol, interval string, limit int) ([]Bar, error) {
	if provider == nil {
		return nil, errors.New("provider not ready")
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	if sp, ok := provider.(strategyBarsProvider); ok {
		return sp.StrategyBars(symbol, interval, limit)
	}
	return provider.Bars(symbol, "3M")
}

// Some agent transports preserve object arguments as JSON text. Both wire
// forms must reach the same validator and durable definition, never a silent
// no-op during updates. This does not relax the trading-program schema.
func strategyDefinitionArgument(value any) (map[string]any, error) {
	if object, ok := value.(map[string]any); ok && object != nil {
		return object, nil
	}
	if text, ok := value.(string); ok {
		var object map[string]any
		if err := json.Unmarshal([]byte(text), &object); err == nil && object != nil {
			return object, nil
		}
	}
	return nil, errors.New("definition must be an object or JSON object text")
}

func (a *App) toolStrategyCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	defRaw, err := strategyDefinitionArgument(args["definition"])
	if err != nil {
		return nil, err
	}
	if _, _, err := validateStrategyDefinition(defRaw); err != nil {
		return nil, err
	}
	id, err := dbCreateStrategy(ctx.AppDB(), &Strategy{
		ProjectID:        pid,
		Name:             name,
		Description:      strArg(args, "description"),
		Status:           nonEmpty(strArg(args, "status"), "draft"),
		Definition:       defRaw,
		Version:          1,
		CreatedByAgentID: int64Arg(args, "created_by_agent_id", 0),
	})
	if err != nil {
		return nil, err
	}
	strategy, _ := dbGetStrategy(ctx.AppDB(), pid, id)
	emit("strategy.created", map[string]any{"id": id, "name": name})
	return map[string]any{"strategy": strategy}, nil
}

func (a *App) toolStrategyUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "strategy_id", 0)
	if id <= 0 {
		return nil, errors.New("strategy_id required")
	}
	patch := &Strategy{Name: strArg(args, "name"), Description: strArg(args, "description"), Status: strArg(args, "status")}
	if raw, supplied := args["definition"]; supplied {
		def, err := strategyDefinitionArgument(raw)
		if err != nil {
			return nil, err
		}
		if _, _, err := validateStrategyDefinition(def); err != nil {
			return nil, err
		}
		patch.Definition = def
	}
	strategy, err := dbUpdateStrategy(ctx.AppDB(), pid, id, patch)
	if err != nil {
		return nil, err
	}
	emit("strategy.updated", map[string]any{"id": id, "name": strategy.Name, "version": strategy.Version})
	return map[string]any{"strategy": strategy}, nil
}

func (a *App) toolStrategyGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "strategy_id", 0)
	strategy, err := dbGetStrategy(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if err := enrichStrategyRuntime(ctx.AppDB(), pid, []*Strategy{strategy}, time.Now()); err != nil {
		return nil, err
	}
	return map[string]any{"strategy": strategy}, nil
}

func (a *App) toolStrategyList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := dbListStrategies(ctx.AppDB(), pid, strArg(args, "status"))
	if err != nil {
		return nil, err
	}
	if err := enrichStrategyRuntime(ctx.AppDB(), pid, rows, time.Now()); err != nil {
		return nil, err
	}
	return map[string]any{"strategies": rows, "count": len(rows)}, nil
}

func enrichStrategyRuntime(db *sql.DB, projectID string, strategies []*Strategy, now time.Time) error {
	assignments, err := dbActiveStrategyAssignmentsForProject(db, projectID)
	if err != nil {
		return err
	}
	portfolioNames := tradingPortfolioNames(projectID, len(assignments) > 0)
	byStrategy := map[int64][]*StrategyAssignment{}
	for _, assignment := range assignments {
		assignment.PortfolioName = portfolioNames[assignment.PortfolioID]
		pinned, err := dbGetStrategyVersion(db, projectID, assignment.StrategyID, assignment.StrategyVersion)
		if err == nil {
			if def, _, validationErr := validateStrategyDefinition(pinned.Definition); validationErr == nil {
				decorateStrategyAssignmentSchedule(assignment, def, now)
			}
		}
		byStrategy[assignment.StrategyID] = append(byStrategy[assignment.StrategyID], assignment)
	}
	for _, strategy := range strategies {
		strategy.Assignments = byStrategy[strategy.ID]
		if len(strategy.Assignments) == 0 {
			strategy.AssignmentStatus = "unassigned"
		} else {
			strategy.AssignmentStatus = "assigned"
		}
	}
	return nil
}

func decorateStrategyAssignmentSchedule(assignment *StrategyAssignment, def *StrategyDefinition, now time.Time) {
	if !strategyHasStocks(def) {
		slot, _ := strategyAssignmentCheckSlot(def, now)
		if strategyAssignmentCheckDue(assignment, slot) {
			assignment.Eligibility = "eligible"
			assignment.EligibilityReason = "cadence_ready"
			assignment.NextEligibleAt = now.UTC().Format(time.RFC3339)
			return
		}
		duration, ok := strategyCadenceDuration(strategyHistoryInterval(def))
		if !ok || duration <= 0 {
			duration = time.Hour
		}
		assignment.Eligibility = "waiting"
		assignment.EligibilityReason = "waiting_for_next_cadence"
		assignment.NextEligibleAt = slot.Add(duration).UTC().Format(time.RFC3339)
		return
	}

	session := usEquitySessionAt(now)
	if session.OpenDay {
		assignment.SessionOpenAt = session.Open.UTC().Format(time.RFC3339)
		assignment.SessionCloseAt = session.Close.UTC().Format(time.RFC3339)
	}
	if !session.OpenDay {
		assignment.Eligibility = "waiting"
		assignment.EligibilityReason = session.Reason
		assignment.NextEligibleAt = nextUSEquityOpen(now).Format(time.RFC3339)
		return
	}
	if now.Before(session.Open) {
		assignment.Eligibility = "waiting"
		assignment.EligibilityReason = "before_market_open"
		assignment.NextEligibleAt = session.Open.UTC().Format(time.RFC3339)
		return
	}
	if !now.Before(session.Close) {
		assignment.Eligibility = "waiting"
		assignment.EligibilityReason = "market_closed"
		assignment.NextEligibleAt = nextUSEquityOpen(session.Close.Add(time.Second)).Format(time.RFC3339)
		return
	}
	slot, ok := strategyAssignmentCheckSlot(def, now)
	if !ok || !strategyAssignmentCheckDue(assignment, slot) {
		assignment.Eligibility = "waiting"
		assignment.EligibilityReason = "waiting_for_next_cadence"
		assignment.NextEligibleAt = nextStockStrategyCheck(def, slot, session).Format(time.RFC3339)
		return
	}
	if globalEngine != nil {
		if _, live := globalEngine.provider.(*tradingProvider); !live {
			assignment.Eligibility = "eligible"
			assignment.EligibilityReason = "regular_session_ready"
			assignment.NextEligibleAt = now.UTC().Format(time.RFC3339)
			return
		}
		for _, symbol := range def.Universe {
			class := inferAssetClass(symbol)
			if class != "equity" && class != "etf" {
				continue
			}
			mark, err := globalEngine.provider.Quote(symbol)
			if err != nil || !markFresh(mark, now.UTC()) {
				assignment.Eligibility = "waiting"
				assignment.EligibilityReason = "waiting_for_fresh_market_data"
				assignment.NextEligibleAt = now.UTC().Format(time.RFC3339)
				return
			}
		}
	}
	assignment.Eligibility = "eligible"
	assignment.EligibilityReason = "regular_session_ready"
	assignment.NextEligibleAt = now.UTC().Format(time.RFC3339)
}

func strategyHasStocks(def *StrategyDefinition) bool {
	for _, symbol := range def.Universe {
		class := inferAssetClass(symbol)
		if class == "equity" || class == "etf" {
			return true
		}
	}
	return false
}

func nextStockStrategyCheck(def *StrategyDefinition, slot time.Time, session usEquitySession) time.Time {
	cadence := strategyHistoryInterval(def)
	if cadence == "1d" {
		return nextUSEquityOpen(session.Close.Add(time.Second))
	}
	if cadence == "1w" {
		return nextUSEquityOpen(slot.AddDate(0, 0, 7))
	}
	duration, ok := strategyCadenceDuration(cadence)
	if !ok || duration <= 0 {
		duration = time.Hour
	}
	next := slot.Add(duration)
	if next.Before(session.Close) {
		return next.UTC()
	}
	return nextUSEquityOpen(session.Close.Add(time.Second))
}

func (a *App) toolStrategyValidate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	defRaw, err := strategyDefinitionArgument(args["definition"])
	if err != nil {
		return nil, err
	}
	def, warnings, err := validateStrategyDefinition(defRaw)
	if err != nil {
		return map[string]any{"valid": false, "error": err.Error()}, nil
	}
	return map[string]any{"valid": true, "definition": def, "warnings": warnings}, nil
}

func (a *App) toolStrategyEvaluate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	strategy, err := dbGetStrategy(ctx.AppDB(), pid, int64Arg(args, "strategy_id", 0))
	if err != nil {
		return nil, err
	}
	def, _, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		return nil, err
	}
	// Preview against a portfolio's current state, or an empty cash account.
	state := &TradingState{Portfolio: &Portfolio{Cash: 100000}, Equity: 100000, Marks: map[string]*Mark{}}
	if portfolioID := int64Arg(args, "portfolio_id", 0); portfolioID > 0 {
		if state, err = newTradingReader(ctx).State(pid, portfolioID, def.Universe); err != nil {
			return nil, err
		}
	}
	result, err := evaluateRulesLive(def, state, nil, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return map[string]any{"bar_at": result.BarAt, "evaluation": result.Targets, "orders": result.Orders, "reports": result.Reports}, nil
}

func (a *App) toolStrategyAssign(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID := int64Arg(args, "portfolio_id", 0)
	strategyID := int64Arg(args, "strategy_id", 0)
	if portfolioID <= 0 || strategyID <= 0 {
		return nil, errors.New("portfolio_id and strategy_id required")
	}
	strategy, err := dbGetStrategy(ctx.AppDB(), pid, strategyID)
	if err != nil {
		return nil, err
	}
	def, _, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		return nil, err
	}
	if err := liveSupport(def); err != nil {
		return nil, fmt.Errorf("strategy cannot run live: %w", err)
	}
	state, err := newTradingReader(ctx).State(pid, portfolioID, def.Universe)
	if err != nil {
		return nil, err
	}
	portfolio := state.Portfolio
	if err := validateStrategyUniverse(portfolio, def, state.Violations); err != nil {
		return nil, err
	}
	if allowed, reason := scorecardAllowsExecution(ctx.AppDB(), state, strategy.ID); !allowed {
		return nil, fmt.Errorf("strategy scorecard gate: %s", reason)
	}
	cadence := strings.TrimSpace(strArg(args, "cadence"))
	if cadence == "" {
		cadence = strings.TrimSpace(def.Cadence)
	}
	id, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{
		ProjectID: pid, PortfolioID: portfolioID, StrategyID: strategyID,
		StrategyVersion: strategy.Version,
		ControlMode:     nonEmpty(strArg(args, "control_mode"), "strategy"),
		Cadence:         nonEmpty(cadence, "1d"),
	})
	if err != nil {
		return nil, err
	}
	assignment, _ := dbActiveStrategyAssignment(ctx.AppDB(), pid, portfolioID)
	emit("strategy.assigned", map[string]any{"id": id, "portfolio_id": portfolioID, "strategy_id": strategyID, "strategy_version": strategy.Version})
	return map[string]any{"assignment": assignment}, nil
}

func (a *App) toolStrategyBacktestCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID := int64Arg(args, "portfolio_id", 0)
	strategyID := int64Arg(args, "strategy_id", 0)
	if portfolioID <= 0 || strategyID <= 0 {
		return nil, errors.New("portfolio_id and strategy_id required")
	}
	pf, err := tradingPortfolio(ctx, pid, portfolioID)
	if err != nil {
		return nil, err
	}
	strategy, err := dbGetStrategy(ctx.AppDB(), pid, strategyID)
	if err != nil {
		return nil, err
	}
	def, _, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		return nil, err
	}
	if raw, ok := args["inputs"]; ok {
		data, _ := json.Marshal(raw)
		var inputs []sim.Input
		if err := json.Unmarshal(data, &inputs); err != nil {
			return nil, err
		}
		if hasMarketInputs(inputs) {
			var config *sim.Config
			if raw, ok := args["simulation"]; ok {
				data, _ := json.Marshal(raw)
				if err := json.Unmarshal(data, &config); err != nil {
					return nil, err
				}
			}
			run, err := createRuleEventBacktest(ctx, pf, strategy, strArg(args, "name"), floatArg(args, "starting_cash", 0), config, inputs)
			if err != nil {
				return nil, err
			}
			return map[string]any{"backtest": run}, nil
		}
	}
	interval, err := strategyInterval(def, strArg(args, "interval"))
	if err != nil {
		return nil, err
	}
	startAt, endAt, err := resolveBacktestRange(strArg(args, "start_at"), strArg(args, "end_at"))
	if err != nil {
		return nil, err
	}
	adjustmentMode, err := normalizeBacktestAdjustmentMode(strArg(args, "adjustment_mode"))
	if err != nil {
		return nil, err
	}
	marketBars, steps, marketSource, err := captureBacktestMarketBarsAdjusted(context.Background(), def.Universe, interval, startAt, endAt, adjustmentMode)
	if err != nil {
		return nil, err
	}
	if steps <= 0 {
		return nil, errors.New("market data range must contain at least one step")
	}
	startingCash := floatArg(args, "starting_cash", 0)
	if startingCash <= 0 {
		startingCash = pf.StartingCash
	}
	name := strArg(args, "name")
	if name == "" {
		name = fmt.Sprintf("%s strategy backtest", strategy.Name)
	}
	manifest, err := referenceManifest(ctx, pid, def.Universe, startAt.Format("2006-01-02"), endAt.Format("2006-01-02"), adjustmentMode)
	if err != nil {
		return nil, err
	}
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
		ProjectID:         pid,
		PortfolioID:       portfolioID,
		StrategyID:        strategyID,
		RunKind:           "strategy",
		StrategyVersion:   strategy.Version,
		Name:              name,
		Status:            "queued",
		Symbols:           def.Universe,
		StartAt:           startAt.Format("2006-01-02"),
		EndAt:             endAt.Format("2006-01-02"),
		Interval:          interval,
		StartingCash:      startingCash,
		FeeBps:            floatArg(args, "fee_bps", 0),
		SlippageBps:       floatArg(args, "slippage_bps", defaultSlippageBps),
		AdjustmentMode:    adjustmentMode,
		ReferenceManifest: manifest,
		TotalSteps:        steps,
		Summary: map[string]any{
			"portfolio_name": pf.Name,
			"strategy_name":  strategy.Name,
			"market_source":  marketSource,
			"market_data":    summarizeBacktestMarketCapture(marketBars, def.Universe, startAt, endAt),
		},
	})
	if err != nil {
		return nil, err
	}
	if err := dbReplaceBacktestMarketBars(ctx.AppDB(), id, marketBars); err != nil {
		_ = dbSetBacktestStatus(ctx.AppDB(), id, "failed", err.Error())
		return nil, err
	}
	var options *sim.Config
	var extra []sim.Input
	if raw, ok := args["simulation"]; ok {
		data, _ := json.Marshal(raw)
		if err := json.Unmarshal(data, &options); err != nil {
			return nil, err
		}
	}
	if raw, ok := args["inputs"]; ok {
		data, _ := json.Marshal(raw)
		if err := json.Unmarshal(data, &extra); err != nil {
			return nil, err
		}
	}
	if err := enableEventBacktest(ctx.AppDB(), pid, id, options, extra); err != nil {
		_ = dbSetBacktestStatus(ctx.AppDB(), id, "failed", err.Error())
		return nil, err
	}
	run, _ := dbGetBacktestRun(ctx.AppDB(), pid, id)
	_, _ = dbInsertBacktestEvent(ctx.AppDB(), id, "created", "Strategy backtest created", map[string]any{"strategy_id": strategyID, "symbols": def.Universe, "market_source": marketSource, "bars": len(marketBars)})
	emitBacktest("trading.backtest.created", id, map[string]any{"portfolio_id": pf.ID, "strategy_id": strategyID, "run_kind": "strategy"})
	return map[string]any{"backtest": run}, nil
}

func (a *App) toolStrategyValidateBacktest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	result, err := a.createStrategyValidation(ctx, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"validation": result}, nil
}

func (a *App) createStrategyValidation(ctx *sdk.AppCtx, args map[string]any) (*StrategyValidationResult, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolioID := int64Arg(args, "portfolio_id", 0)
	strategyID := int64Arg(args, "strategy_id", 0)
	if portfolioID <= 0 || strategyID <= 0 {
		return nil, errors.New("portfolio_id and strategy_id required")
	}
	pf, err := tradingPortfolio(ctx, pid, portfolioID)
	if err != nil {
		return nil, err
	}
	strategy, err := dbGetStrategy(ctx.AppDB(), pid, strategyID)
	if err != nil {
		return nil, err
	}
	def, _, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		return nil, err
	}
	interval, err := strategyInterval(def, strArg(args, "interval"))
	if err != nil {
		return nil, err
	}
	startAt, endAt, err := resolveBacktestRange(strArg(args, "start_at"), strArg(args, "end_at"))
	if err != nil {
		return nil, err
	}
	adjustmentMode, err := normalizeBacktestAdjustmentMode(strArg(args, "adjustment_mode"))
	if err != nil {
		return nil, err
	}
	marketBars, steps, marketSource, err := captureBacktestMarketBarsAdjusted(context.Background(), def.Universe, interval, startAt, endAt, adjustmentMode)
	if err != nil {
		return nil, err
	}
	splitPct := floatArg(args, "split_pct", 0.7)
	if splitPct <= 0 {
		splitPct = 0.7
	}
	if splitPct >= 1 {
		return nil, errors.New("split_pct must be between 0 and 1")
	}
	trainSteps := int(math.Floor(float64(steps) * splitPct))
	testSteps := steps - trainSteps
	if trainSteps < 2 || testSteps < 2 {
		return nil, fmt.Errorf("validation needs at least 2 train and 2 test steps; got %d/%d", trainSteps, testSteps)
	}
	startingCash := floatArg(args, "starting_cash", 0)
	if startingCash <= 0 {
		startingCash = pf.StartingCash
	}
	name := strArg(args, "name")
	if name == "" {
		name = fmt.Sprintf("%s validation", strategy.Name)
	}
	feeBps := floatArg(args, "fee_bps", 0)
	slippageBps := floatArg(args, "slippage_bps", defaultSlippageBps)
	train, err := a.createCompletedStrategyValidationRun(ctx, pf, strategy, def, strategyValidationRunSpec{
		Label:          "in_sample",
		Name:           name + " · in sample",
		Interval:       interval,
		StartingCash:   startingCash,
		FeeBps:         feeBps,
		SlippageBps:    slippageBps,
		MarketSource:   marketSource,
		AdjustmentMode: adjustmentMode,
		Bars:           reindexBacktestMarketBars(marketBars, 1, trainSteps),
	})
	if err != nil {
		return nil, err
	}
	test, err := a.createCompletedStrategyValidationRun(ctx, pf, strategy, def, strategyValidationRunSpec{
		Label:          "out_of_sample",
		Name:           name + " · out of sample",
		Interval:       interval,
		StartingCash:   startingCash,
		FeeBps:         feeBps,
		SlippageBps:    slippageBps,
		MarketSource:   marketSource,
		AdjustmentMode: adjustmentMode,
		Bars:           reindexValidationMarketBars(marketBars, trainSteps+1, steps, rules.RequiredBaseBars(def.Program)),
	})
	if err != nil {
		return nil, err
	}
	result := &StrategyValidationResult{
		StrategyID:      strategy.ID,
		StrategyVersion: strategy.Version,
		SplitPct:        splitPct,
		MarketSource:    marketSource,
		Train:           train,
		Test:            test,
	}
	result.Verdict = strategyValidationVerdict(train.Metrics, test.Metrics)
	scorecard, scoreErr := evaluateAndStoreStrategyScorecard(ctx.AppDB(), pid, portfolioID, strategyID, test.Run.ID)
	if scoreErr != nil {
		return nil, fmt.Errorf("persist validation scorecard: %w", scoreErr)
	}
	result.Scorecard = scorecard
	emit("strategy.scorecard.evaluated", map[string]any{"portfolio_id": portfolioID, "strategy_id": strategyID, "evaluation": scorecard})
	return result, nil
}

type strategyValidationRunSpec struct {
	Label          string
	Name           string
	Interval       string
	StartingCash   float64
	FeeBps         float64
	SlippageBps    float64
	MarketSource   string
	AdjustmentMode string
	Bars           []*BacktestMarketBar
}

func (a *App) createCompletedStrategyValidationRun(ctx *sdk.AppCtx, pf *Portfolio, strategy *Strategy, def *StrategyDefinition, spec strategyValidationRunSpec) (*StrategyValidationPeriod, error) {
	if len(spec.Bars) == 0 {
		return nil, fmt.Errorf("%s validation period has no market bars", spec.Label)
	}
	startAt, endAt := marketBarDateRange(spec.Bars)
	steps := maxBacktestMarketStep(spec.Bars)
	manifest, err := referenceManifest(ctx, pf.ProjectID, def.Universe, startAt, endAt, spec.AdjustmentMode)
	if err != nil {
		return nil, err
	}
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
		ProjectID:         pf.ProjectID,
		PortfolioID:       pf.ID,
		StrategyID:        strategy.ID,
		RunKind:           "strategy",
		StrategyVersion:   strategy.Version,
		Name:              spec.Name,
		Status:            "queued",
		Symbols:           def.Universe,
		StartAt:           startAt,
		EndAt:             endAt,
		Interval:          spec.Interval,
		StartingCash:      spec.StartingCash,
		FeeBps:            spec.FeeBps,
		SlippageBps:       spec.SlippageBps,
		AdjustmentMode:    spec.AdjustmentMode,
		ReferenceManifest: manifest,
		TotalSteps:        steps,
		Summary: map[string]any{
			"portfolio_name":    pf.Name,
			"strategy_name":     strategy.Name,
			"market_source":     spec.MarketSource,
			"dataset_sha256":    backtestMarketBarsChecksum(spec.Bars),
			"validation_period": spec.Label,
		},
	})
	if err != nil {
		return nil, err
	}
	if err := dbReplaceBacktestMarketBars(ctx.AppDB(), id, spec.Bars); err != nil {
		_ = dbSetBacktestStatus(ctx.AppDB(), id, "failed", err.Error())
		return nil, err
	}
	if err := enableEventBacktest(ctx.AppDB(), pf.ProjectID, id, nil, nil); err != nil {
		return nil, err
	}
	run, err := dbGetBacktestRun(ctx.AppDB(), pf.ProjectID, id)
	if err != nil {
		return nil, err
	}
	if _, err := runEventSimulationToEnd(run); err != nil {
		_ = dbSetBacktestStatus(ctx.AppDB(), id, "failed", err.Error())
		return nil, err
	}
	run, err = dbGetBacktestRun(ctx.AppDB(), pf.ProjectID, id)
	if err != nil {
		return nil, err
	}
	perf, err := backtestPerformance(run)
	if err != nil {
		return nil, err
	}
	_, _ = dbInsertBacktestEvent(ctx.AppDB(), id, "validation", fmt.Sprintf("%s validation completed", spec.Label), map[string]any{"period": spec.Label})
	return &StrategyValidationPeriod{Label: spec.Label, Run: run, Performance: perf, Metrics: perf.Metrics}, nil
}

func reindexBacktestMarketBars(rows []*BacktestMarketBar, fromStep, toStep int) []*BacktestMarketBar {
	out := []*BacktestMarketBar{}
	for _, row := range rows {
		if row == nil || row.Step < fromStep || row.Step > toStep {
			continue
		}
		cp := *row
		cp.Step = row.Step - fromStep + 1
		out = append(out, &cp)
	}
	return out
}

func reindexValidationMarketBars(rows []*BacktestMarketBar, fromStep, toStep, warmupSteps int) []*BacktestMarketBar {
	if warmupSteps < 0 {
		warmupSteps = 0
	}
	warmupFrom := maxInt(1, fromStep-warmupSteps)
	out := []*BacktestMarketBar{}
	for _, row := range rows {
		if row == nil || row.Step < warmupFrom || row.Step > toStep {
			continue
		}
		cp := *row
		cp.Step = row.Step - fromStep + 1
		out = append(out, &cp)
	}
	return out
}

func marketBarDateRange(rows []*BacktestMarketBar) (string, string) {
	var minT, maxT int64
	for _, row := range rows {
		if row == nil || row.T <= 0 || row.Step < 1 {
			continue
		}
		if minT == 0 || row.T < minT {
			minT = row.T
		}
		if row.T > maxT {
			maxT = row.T
		}
	}
	if minT == 0 {
		today := time.Now().UTC().Format("2006-01-02")
		return today, today
	}
	return time.Unix(minT, 0).UTC().Format("2006-01-02"), time.Unix(maxT, 0).UTC().Format("2006-01-02")
}

func maxBacktestMarketStep(rows []*BacktestMarketBar) int {
	maxStep := 0
	for _, row := range rows {
		if row != nil && row.Step > maxStep {
			maxStep = row.Step
		}
	}
	return maxStep
}

func strategyValidationVerdict(train, test map[string]float64) string {
	testReturn := test["return_pct"]
	testSharpe, hasTestSharpe := test["sharpe_ratio"]
	if testReturn > 0 && (!hasTestSharpe || testSharpe > 0) {
		return "pass"
	}
	trainReturn := train["return_pct"]
	if trainReturn > 0 && testReturn <= 0 {
		return "overfit_risk"
	}
	return "fail"
}

func (a *App) handleHTTPStrategies(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/strategies")
	rest = strings.Trim(rest, "/")
	if rest == "catalog" {
		if r.Method != http.MethodGet {
			httpErr(w, 405, "GET")
			return
		}
		httpJSON(w, 200, strategyCatalog(strings.Split(r.URL.Query().Get("symbols"), ",")))
		return
	}
	if rest == "live" {
		a.handleHTTPStrategiesLive(w, r)
		return
	}
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			rows, err := dbListStrategies(globalCtx.AppDB(), pid, r.URL.Query().Get("status"))
			if err != nil {
				httpErr(w, 500, err.Error())
				return
			}
			if err := enrichStrategyRuntime(globalCtx.AppDB(), pid, rows, time.Now()); err != nil {
				httpErr(w, 500, err.Error())
				return
			}
			httpJSON(w, 200, map[string]any{"strategies": rows})
		case http.MethodPost:
			var body struct {
				Name             string         `json:"name"`
				Description      string         `json:"description"`
				Status           string         `json:"status"`
				Definition       map[string]any `json:"definition"`
				CreatedByAgentID int64          `json:"created_by_agent_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				httpErr(w, 400, err.Error())
				return
			}
			out, err := a.toolStrategyCreate(globalCtx, map[string]any{
				"_project_id":         pid,
				"name":                body.Name,
				"description":         body.Description,
				"status":              body.Status,
				"definition":          body.Definition,
				"created_by_agent_id": body.CreatedByAgentID,
			})
			if err != nil {
				httpErr(w, 400, err.Error())
				return
			}
			httpJSON(w, 201, out)
		default:
			httpErr(w, 405, "GET or POST")
		}
		return
	}
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, 400, "strategy id must be integer")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	subaction := ""
	if len(parts) > 2 {
		subaction = parts[2]
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		strategy, err := dbGetStrategy(globalCtx.AppDB(), pid, id)
		if err != nil {
			httpErr(w, 404, "strategy not found")
			return
		}
		if err := enrichStrategyRuntime(globalCtx.AppDB(), pid, []*Strategy{strategy}, time.Now()); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, 200, map[string]any{"strategy": strategy})
	case action == "" && r.Method == http.MethodPatch:
		var body struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Status      string         `json:"status"`
			Definition  map[string]any `json:"definition"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		args := map[string]any{
			"_project_id": pid,
			"strategy_id": id,
			"name":        body.Name,
			"description": body.Description,
			"status":      body.Status,
		}
		if body.Definition != nil {
			args["definition"] = body.Definition
		}
		out, err := a.toolStrategyUpdate(globalCtx, args)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "evaluate" && r.Method == http.MethodPost:
		out, err := a.toolStrategyEvaluate(globalCtx, map[string]any{"_project_id": pid, "strategy_id": id})
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "validate" && r.Method == http.MethodPost:
		var body struct {
			PortfolioID  int64   `json:"portfolio_id"`
			Name         string  `json:"name"`
			StartAt      string  `json:"start_at"`
			EndAt        string  `json:"end_at"`
			Interval     string  `json:"interval"`
			SplitPct     float64 `json:"split_pct"`
			StartingCash float64 `json:"starting_cash"`
			FeeBps       float64 `json:"fee_bps"`
			SlippageBps  float64 `json:"slippage_bps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		out, err := a.toolStrategyValidateBacktest(globalCtx, map[string]any{
			"_project_id":   pid,
			"strategy_id":   id,
			"portfolio_id":  body.PortfolioID,
			"name":          body.Name,
			"start_at":      body.StartAt,
			"end_at":        body.EndAt,
			"interval":      body.Interval,
			"split_pct":     body.SplitPct,
			"starting_cash": body.StartingCash,
			"fee_bps":       body.FeeBps,
			"slippage_bps":  body.SlippageBps,
		})
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "assign" && r.Method == http.MethodPost:
		var body struct {
			PortfolioID int64  `json:"portfolio_id"`
			ControlMode string `json:"control_mode"`
			Cadence     string `json:"cadence"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		out, err := a.toolStrategyAssign(globalCtx, map[string]any{
			"_project_id": pid, "strategy_id": id, "portfolio_id": body.PortfolioID,
			"control_mode": body.ControlMode, "cadence": body.Cadence,
		})
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "scorecard" && subaction == "" && r.Method == http.MethodGet:
		out, err := a.toolStrategyScorecardGet(globalCtx, map[string]any{
			"_project_id": pid, "strategy_id": id, "portfolio_id": r.URL.Query().Get("portfolio_id"), "limit": r.URL.Query().Get("limit"),
		})
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "scorecard" && subaction == "" && r.Method == http.MethodPut:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid json")
			return
		}
		body["_project_id"], body["strategy_id"] = pid, id
		out, err := a.toolStrategyScorecardUpdate(globalCtx, body)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "scorecard" && subaction == "evaluate" && r.Method == http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid json")
			return
		}
		body["_project_id"], body["strategy_id"] = pid, id
		out, err := a.toolStrategyScorecardEvaluate(globalCtx, body)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, 200, out)
	case action == "promotion" && r.Method == http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, 400, "invalid json")
			return
		}
		body["_project_id"], body["strategy_id"] = pid, id
		out, err := a.toolStrategyPromotionUpdate(globalCtx, body)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, 200, out)
	default:
		httpErr(w, 404, "no such strategy route")
	}
}

type strategyBacktestState struct {
	RealizedPnL float64
	Cash        float64
	Positions   map[string]*Position
}

func backtestExecutionPrices(prices []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(prices))
	for _, row := range prices {
		copyRow := make(map[string]any, len(row))
		for key, value := range row {
			copyRow[key] = value
		}
		if open := anyFloat(row["open"]); open > 0 {
			copyRow["price"] = open
		}
		out = append(out, copyRow)
	}
	return out
}

func backtestPriceMap(prices []map[string]any) map[string]float64 {
	out := map[string]float64{}
	for _, row := range prices {
		symbol := strings.ToUpper(strings.TrimSpace(fmt.Sprint(row["symbol"])))
		price := anyFloat(row["price"])
		if symbol != "" && price > 0 {
			out[symbol] = price
		}
	}
	return out
}

// tradingPortfolioNames labels assignments with Trading's portfolio names.
// Names are decoration: when Trading is unreachable the list still renders.
func tradingPortfolioNames(projectID string, needed bool) map[int64]string {
	names := map[int64]string{}
	if !needed || globalCtx == nil {
		return names
	}
	var out struct {
		Portfolios []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"portfolios"`
	}
	if err := callTrading(globalCtx, "portfolio_list", map[string]any{"_project_id": projectID}, &out); err != nil {
		return names
	}
	for _, portfolio := range out.Portfolios {
		names[portfolio.ID] = portfolio.Name
	}
	return names
}
