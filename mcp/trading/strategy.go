package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

type StrategyDefinition struct {
	Universe []string       `json:"universe"`
	Cadence  string         `json:"cadence,omitempty"`
	Rules    []StrategyRule `json:"rules"`
	Risk     StrategyRisk   `json:"risk,omitempty"`
}

type StrategyRule struct {
	Name     string               `json:"name,omitempty"`
	When     *StrategyCondition   `json:"when,omitempty"`
	Allocate []StrategyAllocation `json:"allocate,omitempty"`
	Rank     *StrategyRank        `json:"rank,omitempty"`
}

type StrategyCondition struct {
	Symbol    string  `json:"symbol,omitempty"`
	Indicator string  `json:"indicator"`
	Operator  string  `json:"operator"`
	Value     float64 `json:"value,omitempty"`
	Compare   string  `json:"compare,omitempty"`
}

type StrategyAllocation struct {
	Symbol string  `json:"symbol"`
	Weight float64 `json:"weight"`
}

type StrategyRank struct {
	Symbols []string `json:"symbols"`
	By      string   `json:"by"`
	Top     int      `json:"top"`
	Weight  string   `json:"weight,omitempty"`
}

type StrategyRisk struct {
	MaxPositionWeight float64 `json:"max_position_weight,omitempty"`
}

type StrategyEvaluation struct {
	StrategyID        int64                `json:"strategy_id,omitempty"`
	StrategyVersion   int                  `json:"strategy_version,omitempty"`
	AsOf              string               `json:"as_of"`
	TargetAllocations []StrategyAllocation `json:"target_allocations"`
	Decisions         []string             `json:"decisions"`
	Warnings          []string             `json:"warnings,omitempty"`
}

type strategyMarket struct {
	prices  map[string]float64
	history map[string][]float64
	asOf    time.Time
}

func parseStrategyDefinition(raw map[string]any) (*StrategyDefinition, error) {
	if raw == nil {
		return nil, errors.New("definition required")
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var def StrategyDefinition
	if err := json.Unmarshal(buf, &def); err != nil {
		return nil, err
	}
	def.Universe = cleanSymbols(def.Universe)
	for i := range def.Rules {
		def.Rules[i].Allocate = cleanAllocations(def.Rules[i].Allocate)
		if def.Rules[i].Rank != nil {
			def.Rules[i].Rank.Symbols = cleanSymbols(def.Rules[i].Rank.Symbols)
			if def.Rules[i].Rank.Top <= 0 {
				def.Rules[i].Rank.Top = 1
			}
			if def.Rules[i].Rank.By == "" {
				def.Rules[i].Rank.By = "return_20"
			}
		}
	}
	if len(def.Universe) == 0 {
		for _, r := range def.Rules {
			for _, a := range r.Allocate {
				def.Universe = append(def.Universe, a.Symbol)
			}
			if r.Rank != nil {
				def.Universe = append(def.Universe, r.Rank.Symbols...)
			}
		}
		def.Universe = cleanSymbols(def.Universe)
	}
	if len(def.Universe) == 0 {
		return nil, errors.New("strategy universe required")
	}
	if len(def.Rules) == 0 {
		return nil, errors.New("at least one strategy rule required")
	}
	if def.Risk.MaxPositionWeight <= 0 || def.Risk.MaxPositionWeight > 1 {
		def.Risk.MaxPositionWeight = 1
	}
	return &def, nil
}

func cleanAllocations(in []StrategyAllocation) []StrategyAllocation {
	out := []StrategyAllocation{}
	seen := map[string]bool{}
	for _, a := range in {
		symbol := strings.ToUpper(strings.TrimSpace(a.Symbol))
		if symbol == "" || symbol == "CASH" || a.Weight <= 0 || seen[symbol] {
			continue
		}
		seen[symbol] = true
		out = append(out, StrategyAllocation{Symbol: symbol, Weight: a.Weight})
	}
	return out
}

func validateStrategyDefinition(raw map[string]any) (*StrategyDefinition, []string, error) {
	def, err := parseStrategyDefinition(raw)
	if err != nil {
		return nil, nil, err
	}
	warnings := []string{}
	for _, rule := range def.Rules {
		if len(rule.Allocate) == 0 && rule.Rank == nil {
			warnings = append(warnings, fmt.Sprintf("rule %q has no allocation output", nonEmpty(rule.Name, "(unnamed)")))
		}
		if rule.When != nil && strings.TrimSpace(rule.When.Indicator) == "" {
			return nil, nil, fmt.Errorf("rule %q condition indicator required", nonEmpty(rule.Name, "(unnamed)"))
		}
	}
	return def, warnings, nil
}

func evaluateStrategy(strategy *Strategy, market strategyMarket) (*StrategyEvaluation, error) {
	if strategy == nil {
		return nil, errors.New("strategy required")
	}
	def, warnings, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		return nil, err
	}
	out := &StrategyEvaluation{
		StrategyID:      strategy.ID,
		StrategyVersion: strategy.Version,
		AsOf:            market.asOf.UTC().Format(time.RFC3339),
		Decisions:       []string{},
		Warnings:        warnings,
	}
	for _, rule := range def.Rules {
		ok := true
		reason := "default rule"
		if rule.When != nil {
			ok, reason, err = evalStrategyCondition(*rule.When, def, market)
			if err != nil {
				out.Warnings = append(out.Warnings, err.Error())
				ok = false
			}
		}
		if !ok {
			continue
		}
		allocs := rule.Allocate
		if rule.Rank != nil {
			allocs, reason, err = evalStrategyRank(*rule.Rank, market)
			if err != nil {
				out.Warnings = append(out.Warnings, err.Error())
				continue
			}
		}
		out.TargetAllocations = capAllocations(normalizeAllocations(allocs), def.Risk.MaxPositionWeight)
		out.Decisions = append(out.Decisions, fmt.Sprintf("%s: %s", nonEmpty(rule.Name, "rule"), reason))
		return out, nil
	}
	out.Decisions = append(out.Decisions, "no rule matched; holding cash")
	out.TargetAllocations = []StrategyAllocation{}
	return out, nil
}

func evalStrategyCondition(c StrategyCondition, def *StrategyDefinition, market strategyMarket) (bool, string, error) {
	symbol := strings.ToUpper(strings.TrimSpace(c.Symbol))
	if symbol == "" && len(def.Universe) > 0 {
		symbol = def.Universe[0]
	}
	lhs, err := strategyMetric(symbol, c.Indicator, market)
	if err != nil {
		return false, "", err
	}
	rhs := c.Value
	label := fmt.Sprintf("%s %s %.4f", c.Indicator, c.Operator, rhs)
	if strings.TrimSpace(c.Compare) != "" {
		rhs, err = strategyMetric(symbol, c.Compare, market)
		if err != nil {
			return false, "", err
		}
		label = fmt.Sprintf("%s %s %s", c.Indicator, c.Operator, c.Compare)
	}
	switch strings.ToLower(strings.TrimSpace(c.Operator)) {
	case ">", "above":
		return lhs > rhs, fmt.Sprintf("%s: %.4f > %.4f", label, lhs, rhs), nil
	case ">=", "at_or_above":
		return lhs >= rhs, fmt.Sprintf("%s: %.4f >= %.4f", label, lhs, rhs), nil
	case "<", "below":
		return lhs < rhs, fmt.Sprintf("%s: %.4f < %.4f", label, lhs, rhs), nil
	case "<=", "at_or_below":
		return lhs <= rhs, fmt.Sprintf("%s: %.4f <= %.4f", label, lhs, rhs), nil
	default:
		return false, "", fmt.Errorf("unsupported operator %q", c.Operator)
	}
}

func evalStrategyRank(rank StrategyRank, market strategyMarket) ([]StrategyAllocation, string, error) {
	symbols := cleanSymbols(rank.Symbols)
	if len(symbols) == 0 {
		return nil, "", errors.New("rank symbols required")
	}
	type row struct {
		symbol string
		value  float64
	}
	rows := []row{}
	for _, symbol := range symbols {
		v, err := strategyMetric(symbol, rank.By, market)
		if err != nil {
			continue
		}
		rows = append(rows, row{symbol: symbol, value: v})
	}
	if len(rows) == 0 {
		return nil, "", fmt.Errorf("rank %s has no computable symbols", rank.By)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].value > rows[j].value })
	top := rank.Top
	if top > len(rows) {
		top = len(rows)
	}
	weight := 1.0 / float64(top)
	out := make([]StrategyAllocation, 0, top)
	picked := []string{}
	for _, r := range rows[:top] {
		out = append(out, StrategyAllocation{Symbol: r.symbol, Weight: weight})
		picked = append(picked, fmt.Sprintf("%s %.4f", r.symbol, r.value))
	}
	return out, "ranked by " + rank.By + ": " + strings.Join(picked, ", "), nil
}

func strategyMetric(symbol, indicator string, market strategyMarket) (float64, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	indicator = strings.ToLower(strings.TrimSpace(indicator))
	if symbol == "" {
		return 0, errors.New("symbol required")
	}
	if indicator == "" || indicator == "price" {
		if v := market.prices[symbol]; v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("price unavailable for %s", symbol)
	}
	history := market.history[symbol]
	if len(history) == 0 {
		if v := market.prices[symbol]; v > 0 {
			history = []float64{v}
		}
	}
	switch {
	case strings.HasPrefix(indicator, "sma_"):
		return latestFloatSMA(history, parseMetricWindow(indicator, "sma", 20))
	case strings.HasPrefix(indicator, "ema_"):
		return latestFloatEMA(history, parseMetricWindow(indicator, "ema", 20))
	case strings.HasPrefix(indicator, "rsi_") || indicator == "rsi":
		return latestFloatRSI(history, parseMetricWindow(indicator, "rsi", 14))
	case strings.HasPrefix(indicator, "return_") || indicator == "return":
		return latestFloatReturn(history, parseMetricWindow(indicator, "return", 20))
	case strings.HasPrefix(indicator, "volatility_") || indicator == "volatility":
		return latestFloatVolatility(history, parseMetricWindow(indicator, "volatility", 20))
	default:
		return 0, fmt.Errorf("unsupported indicator %q", indicator)
	}
}

func parseMetricWindow(name, prefix string, fallback int) int {
	name = strings.TrimPrefix(name, prefix+"_")
	if name == prefix || name == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(name, "%d", &n); err == nil && n > 0 {
		return n
	}
	return fallback
}

func latestFloatSMA(values []float64, window int) (float64, error) {
	if len(values) < window {
		return 0, fmt.Errorf("need %d bars", window)
	}
	return avgFloat(values[len(values)-window:]), nil
}

func latestFloatEMA(values []float64, window int) (float64, error) {
	if len(values) < window {
		return 0, fmt.Errorf("need %d bars", window)
	}
	alpha := 2.0 / float64(window+1)
	ema := values[0]
	for _, v := range values[1:] {
		ema = v*alpha + ema*(1-alpha)
	}
	return ema, nil
}

func latestFloatRSI(values []float64, window int) (float64, error) {
	if len(values) <= window {
		return 0, fmt.Errorf("need %d bars", window+1)
	}
	var gains, losses float64
	start := len(values) - window
	for i := start; i < len(values); i++ {
		delta := values[i] - values[i-1]
		if delta >= 0 {
			gains += delta
		} else {
			losses -= delta
		}
	}
	avgLoss := losses / float64(window)
	if avgLoss == 0 {
		return 100, nil
	}
	rs := (gains / float64(window)) / avgLoss
	return 100 - 100/(1+rs), nil
}

func latestFloatReturn(values []float64, window int) (float64, error) {
	if len(values) <= window {
		return 0, fmt.Errorf("need %d bars", window+1)
	}
	first := values[len(values)-1-window]
	if first <= 0 {
		return 0, errors.New("zero starting price")
	}
	return (values[len(values)-1]/first - 1) * 100, nil
}

func latestFloatVolatility(values []float64, window int) (float64, error) {
	if len(values) <= window {
		return 0, fmt.Errorf("need %d bars", window+1)
	}
	returns := []float64{}
	for i := len(values) - window; i < len(values); i++ {
		if values[i-1] > 0 {
			returns = append(returns, math.Log(values[i]/values[i-1]))
		}
	}
	return stddevFloat(returns), nil
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

func normalizeAllocations(in []StrategyAllocation) []StrategyAllocation {
	in = cleanAllocations(in)
	var total float64
	for _, a := range in {
		total += a.Weight
	}
	if total <= 0 {
		return []StrategyAllocation{}
	}
	out := make([]StrategyAllocation, 0, len(in))
	scale := 1.0
	if total > 1 {
		scale = 1 / total
	}
	for _, a := range in {
		out = append(out, StrategyAllocation{Symbol: a.Symbol, Weight: round4(a.Weight * scale)})
	}
	return out
}

func capAllocations(in []StrategyAllocation, maxWeight float64) []StrategyAllocation {
	if maxWeight <= 0 || maxWeight >= 1 {
		return in
	}
	for i := range in {
		if in[i].Weight > maxWeight {
			in[i].Weight = maxWeight
		}
	}
	return in
}

func liveStrategyMarket(ctx *sdk.AppCtx, def *StrategyDefinition) strategyMarket {
	market := strategyMarket{prices: map[string]float64{}, history: map[string][]float64{}, asOf: time.Now().UTC()}
	for _, symbol := range def.Universe {
		if mark, err := dbGetMark(ctx.AppDB(), symbol); err == nil && mark != nil {
			market.prices[symbol] = mark.Price
		}
		if globalEngine != nil {
			if bars, err := globalEngine.provider.Bars(symbol, "3M"); err == nil {
				for _, b := range bars {
					price := b.C
					if price <= 0 {
						price = b.Yes
					}
					if price <= 0 {
						price = b.O
					}
					if price > 0 {
						market.history[symbol] = append(market.history[symbol], price)
					}
				}
			}
		}
		if len(market.history[symbol]) > 0 && market.prices[symbol] == 0 {
			h := market.history[symbol]
			market.prices[symbol] = h[len(h)-1]
		}
	}
	return market
}

func backtestStrategyMarket(run *BacktestRun, step int) strategyMarket {
	market := strategyMarket{prices: map[string]float64{}, history: map[string][]float64{}, asOf: backtestReplayTime(run, step)}
	for i := 1; i <= step; i++ {
		for _, row := range backtestMarks(run, i) {
			symbol := strings.ToUpper(strings.TrimSpace(fmt.Sprint(row["symbol"])))
			price := anyFloat(row["price"])
			if symbol == "" || price <= 0 {
				continue
			}
			market.history[symbol] = append(market.history[symbol], price)
			if i == step {
				market.prices[symbol] = price
			}
		}
	}
	return market
}

// ─── Strategy MCP tools ────────────────────────────────────────────

func (a *App) toolStrategyCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	defRaw, ok := args["definition"].(map[string]any)
	if !ok {
		return nil, errors.New("definition object required")
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
	if def, ok := args["definition"].(map[string]any); ok {
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
	return map[string]any{"strategies": rows, "count": len(rows)}, nil
}

func (a *App) toolStrategyValidate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	defRaw, ok := args["definition"].(map[string]any)
	if !ok {
		return nil, errors.New("definition object required")
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
	eval, err := evaluateStrategy(strategy, liveStrategyMarket(ctx, def))
	if err != nil {
		return nil, err
	}
	return map[string]any{"evaluation": eval}, nil
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
	if _, err := dbGetPortfolio(ctx.AppDB(), pid, portfolioID); err != nil {
		return nil, err
	}
	if _, err := dbGetStrategy(ctx.AppDB(), pid, strategyID); err != nil {
		return nil, err
	}
	id, err := dbAssignStrategy(ctx.AppDB(), &StrategyAssignment{
		ProjectID: pid, PortfolioID: portfolioID, StrategyID: strategyID,
		ControlMode: nonEmpty(strArg(args, "control_mode"), "strategy"),
		Cadence:     nonEmpty(strArg(args, "cadence"), "1d"),
	})
	if err != nil {
		return nil, err
	}
	assignment, _ := dbActiveStrategyAssignment(ctx.AppDB(), pid, portfolioID)
	emit("strategy.assigned", map[string]any{"id": id, "portfolio_id": portfolioID, "strategy_id": strategyID})
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
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, portfolioID)
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
	interval, err := normalizeBacktestInterval(strArg(args, "interval"))
	if err != nil {
		return nil, err
	}
	startAt, endAt := defaultBacktestRange(strArg(args, "start_at"), strArg(args, "end_at"))
	steps := estimateBacktestSteps(startAt, endAt, interval)
	if steps <= 0 {
		return nil, errors.New("date range must contain at least one step")
	}
	startingCash := floatArg(args, "starting_cash", 0)
	if startingCash <= 0 {
		startingCash = pf.StartingCash
	}
	name := strArg(args, "name")
	if name == "" {
		name = fmt.Sprintf("%s strategy backtest", strategy.Name)
	}
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
		ProjectID:       pid,
		PortfolioID:     portfolioID,
		StrategyID:      strategyID,
		RunKind:         "strategy",
		StrategyVersion: strategy.Version,
		Name:            name,
		Status:          "queued",
		Symbols:         def.Universe,
		StartAt:         startAt.Format("2006-01-02"),
		EndAt:           endAt.Format("2006-01-02"),
		Interval:        interval,
		StartingCash:    startingCash,
		FeeBps:          floatArg(args, "fee_bps", 0),
		SlippageBps:     floatArg(args, "slippage_bps", defaultSlippageBps),
		TotalSteps:      steps,
		Summary: map[string]any{
			"portfolio_name": pf.Name,
			"strategy_name":  strategy.Name,
		},
	})
	if err != nil {
		return nil, err
	}
	run, _ := dbGetBacktestRun(ctx.AppDB(), pid, id)
	_, _ = dbInsertBacktestEvent(ctx.AppDB(), id, "created", "Strategy backtest created", map[string]any{"strategy_id": strategyID, "symbols": def.Universe})
	emitBacktest("trading.backtest.created", id, map[string]any{"portfolio_id": pf.ID, "strategy_id": strategyID, "run_kind": "strategy"})
	return map[string]any{"backtest": run}, nil
}

// ─── Strategy REST handlers ────────────────────────────────────────

func (a *App) handleHTTPStrategies(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/strategies")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			rows, err := dbListStrategies(globalCtx.AppDB(), pid, r.URL.Query().Get("status"))
			if err != nil {
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
	switch {
	case action == "" && r.Method == http.MethodGet:
		strategy, err := dbGetStrategy(globalCtx.AppDB(), pid, id)
		if err != nil {
			httpErr(w, 404, "strategy not found")
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
	default:
		httpErr(w, 404, "no such strategy route")
	}
}

// ─── Strategy backtest simulator ───────────────────────────────────

type strategyBacktestState struct {
	Cash      float64
	Positions map[string]*Position
	Orders    []*Order
}

func startStrategyBacktestRun(run *BacktestRun) (map[string]any, error) {
	if run.Status != "queued" && run.Status != "failed" {
		return map[string]any{"backtest": run}, nil
	}
	if run.StrategyID <= 0 {
		return nil, errors.New("strategy_id required")
	}
	strategy, err := dbGetStrategy(globalCtx.AppDB(), run.ProjectID, run.StrategyID)
	if err != nil {
		return nil, err
	}
	if _, _, err := validateStrategyDefinition(strategy.Definition); err != nil {
		return nil, err
	}
	if err := dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "running", ""); err != nil {
		return nil, err
	}
	_ = dbUpsertBacktestSnapshot(globalCtx.AppDB(), &BacktestSnapshot{
		RunID: run.ID, Step: 0, Equity: run.StartingCash, Cash: run.StartingCash,
		BuyingPower: run.StartingCash, Positions: []*Position{}, Orders: []*Order{}, Prices: []map[string]any{},
	})
	_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "started", "Strategy replay started", map[string]any{"strategy_id": run.StrategyID})
	emitBacktest("trading.backtest.started", run.ID, map[string]any{"portfolio_id": run.PortfolioID, "strategy_id": run.StrategyID, "run_kind": "strategy"})
	next, _ := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	return stepStrategyBacktestRun(next)
}

func runStrategyBacktestToEnd(run *BacktestRun) (map[string]any, error) {
	if run.Status == "queued" || run.Status == "failed" {
		if _, err := startStrategyBacktestRun(run); err != nil {
			return nil, err
		}
	} else if run.Status == "paused" {
		if err := dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "running", ""); err != nil {
			return nil, err
		}
	} else if run.Status != "running" {
		return nil, fmt.Errorf("backtest is %s; cannot run", run.Status)
	}
	for {
		next, err := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
		if err != nil {
			return nil, err
		}
		if next.Status != "running" || next.CurrentStep >= next.TotalSteps {
			return map[string]any{"backtest": next, "status": next.Status}, nil
		}
		if _, err := stepStrategyBacktestRun(next); err != nil {
			return nil, err
		}
	}
}

func stepStrategyBacktestRun(run *BacktestRun) (map[string]any, error) {
	if run == nil {
		return nil, errors.New("backtest run required")
	}
	if run.Status != "running" && run.Status != "paused" {
		return nil, fmt.Errorf("backtest is %s, not running", run.Status)
	}
	step := run.CurrentStep + 1
	if step > run.TotalSteps {
		_ = dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "completed", "")
		return map[string]any{"status": "completed", "backtest": run}, nil
	}
	strategy, err := dbGetStrategy(globalCtx.AppDB(), run.ProjectID, run.StrategyID)
	if err != nil {
		return nil, err
	}
	state, err := loadStrategyBacktestState(run)
	if err != nil {
		return nil, err
	}
	market := backtestStrategyMarket(run, step)
	eval, err := evaluateStrategy(strategy, market)
	if err != nil {
		return nil, err
	}
	prices := backtestMarks(run, step)
	orders := applyStrategyTargets(run, state, eval.TargetAllocations, prices)
	state.Orders = append(state.Orders, orders...)
	snap := strategySnapshot(run, step, state, prices)
	if err := dbUpsertBacktestSnapshot(globalCtx.AppDB(), snap); err != nil {
		return nil, err
	}
	status := run.Status
	if status == "" {
		status = "running"
	}
	if step >= run.TotalSteps {
		status = "completed"
	}
	summary := map[string]any{
		"last_step":          step,
		"prices":             prices,
		"strategy_id":        strategy.ID,
		"strategy_name":      strategy.Name,
		"target_allocations": eval.TargetAllocations,
		"decisions":          eval.Decisions,
	}
	if err := dbAdvanceBacktestStep(globalCtx.AppDB(), run.ID, step, summary, status); err != nil {
		return nil, err
	}
	_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "step", fmt.Sprintf("Strategy step %d/%d evaluated", step, run.TotalSteps), summary)
	if len(orders) > 0 {
		_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "orders", fmt.Sprintf("Strategy generated %d order(s)", len(orders)), map[string]any{"orders": orders})
	}
	emitBacktest("trading.backtest.tick", run.ID, map[string]any{
		"portfolio_id": run.PortfolioID, "step": step, "total_steps": run.TotalSteps, "prices": prices, "run_kind": "strategy",
	})
	if status == "completed" {
		_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "completed", "Strategy backtest completed", summary)
		emitBacktest("trading.backtest.completed", run.ID, map[string]any{"portfolio_id": run.PortfolioID, "run_kind": "strategy"})
	}
	next, _ := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	return map[string]any{"backtest": next}, nil
}

func loadStrategyBacktestState(run *BacktestRun) (*strategyBacktestState, error) {
	state := &strategyBacktestState{Cash: run.StartingCash, Positions: map[string]*Position{}, Orders: []*Order{}}
	snaps, err := dbListBacktestSnapshots(globalCtx.AppDB(), run.ID)
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return state, nil
	}
	last := snaps[len(snaps)-1]
	state.Cash = last.Cash
	for _, p := range last.Positions {
		if p != nil && p.Qty > 0 {
			cp := *p
			state.Positions[strings.ToUpper(p.Symbol)] = &cp
		}
	}
	state.Orders = append(state.Orders, last.Orders...)
	return state, nil
}

func applyStrategyTargets(run *BacktestRun, state *strategyBacktestState, targets []StrategyAllocation, prices []map[string]any) []*Order {
	priceBySymbol := backtestPriceMap(prices)
	targetBySymbol := map[string]float64{}
	for _, target := range targets {
		targetBySymbol[strings.ToUpper(target.Symbol)] = target.Weight
	}
	equity := state.Cash
	for symbol, pos := range state.Positions {
		price := priceBySymbol[symbol]
		if price <= 0 {
			price = pos.AvgCost
		}
		equity += pos.Qty * price
	}
	orders := []*Order{}
	symbols := cleanSymbols(run.Symbols)
	seen := map[string]bool{}
	for _, target := range targets {
		symbols = append(symbols, target.Symbol)
	}
	for _, symbol := range symbols {
		if seen[symbol] {
			continue
		}
		seen[symbol] = true
		price := priceBySymbol[symbol]
		if price <= 0 {
			continue
		}
		pos := state.Positions[symbol]
		currentQty := 0.0
		if pos != nil {
			currentQty = pos.Qty
		}
		currentValue := currentQty * price
		targetValue := equity * targetBySymbol[symbol]
		diff := targetValue - currentValue
		if math.Abs(diff) < math.Max(1, equity*0.001) {
			continue
		}
		side := "buy"
		qty := math.Abs(diff) / price
		fillPrice := applySlippage(price, side, run.SlippageBps)
		if diff < 0 {
			side = "sell"
			fillPrice = applySlippage(price, side, run.SlippageBps)
			if qty > currentQty {
				qty = currentQty
			}
		}
		if qty <= 0 {
			continue
		}
		fee := fillFee(qty, fillPrice, run.FeeBps)
		if side == "buy" {
			cost := qty*fillPrice + fee
			if cost > state.Cash && fillPrice > 0 {
				qty = math.Max(0, (state.Cash-fee)/fillPrice)
				cost = qty*fillPrice + fee
			}
			if qty <= 0 {
				continue
			}
			state.Cash -= cost
			if pos == nil {
				pos = &Position{Symbol: symbol, AssetClass: inferAssetClass(symbol)}
				state.Positions[symbol] = pos
			}
			newQty := pos.Qty + qty
			if newQty > 0 {
				pos.AvgCost = ((pos.AvgCost * pos.Qty) + (fillPrice * qty)) / newQty
			}
			pos.Qty = newQty
		} else {
			proceeds := qty*fillPrice - fee
			state.Cash += proceeds
			if pos != nil {
				pos.RealizedPnL += (fillPrice - pos.AvgCost) * qty
				pos.Qty -= qty
				if pos.Qty <= 1e-9 {
					delete(state.Positions, symbol)
				}
			}
		}
		order := &Order{
			ID:           "bt-" + uuid.NewString(),
			PortfolioID:  run.PortfolioID,
			Symbol:       symbol,
			AssetClass:   inferAssetClass(symbol),
			Side:         side,
			Type:         "market",
			Qty:          round4(qty),
			FilledQty:    round4(qty),
			AvgFillPrice: round4(fillPrice),
			TIF:          "day",
			Status:       "filled",
			Rationale:    "strategy target rebalance",
			Source:       "strategy",
			PlacedAt:     backtestReplayTime(run, run.CurrentStep+1).Format(time.RFC3339),
			ResolvedAt:   backtestReplayTime(run, run.CurrentStep+1).Format(time.RFC3339),
		}
		orders = append(orders, order)
	}
	return orders
}

func strategySnapshot(run *BacktestRun, step int, state *strategyBacktestState, prices []map[string]any) *BacktestSnapshot {
	priceBySymbol := backtestPriceMap(prices)
	positions := []*Position{}
	for _, pos := range state.Positions {
		cp := *pos
		price := priceBySymbol[strings.ToUpper(cp.Symbol)]
		if price <= 0 {
			price = cp.AvgCost
		}
		cp.MarketPrice = price
		cp.MarketValue = cp.Qty * price
		cp.UnrealizedPnL = (price - cp.AvgCost) * cp.Qty
		if cp.AvgCost > 0 {
			cp.UnrealizedPnLPct = (price/cp.AvgCost - 1) * 100
		}
		positions = append(positions, &cp)
	}
	sort.Slice(positions, func(i, j int) bool { return positions[i].Symbol < positions[j].Symbol })
	equity, openPnL, openPnLPct, realizedPnL, exposure := valueBacktestPositions(state.Cash, positions, prices)
	return &BacktestSnapshot{
		RunID:       run.ID,
		Step:        step,
		Equity:      equity,
		Cash:        state.Cash,
		BuyingPower: state.Cash,
		OpenPnL:     openPnL,
		OpenPnLPct:  openPnLPct,
		RealizedPnL: realizedPnL,
		Exposure:    exposure,
		Positions:   positions,
		Orders:      state.Orders,
		Prices:      prices,
	}
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
