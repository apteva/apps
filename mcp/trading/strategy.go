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
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

type StrategyDefinition struct {
	Universe       []string       `json:"universe"`
	Cadence        string         `json:"cadence,omitempty"`
	RebalanceEvery int            `json:"rebalance_every,omitempty"`
	Rules          []StrategyRule `json:"rules"`
	Risk           StrategyRisk   `json:"risk,omitempty"`
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
	Min     float64  `json:"min,omitempty"`
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

type StrategyValidationResult struct {
	StrategyID      int64                     `json:"strategy_id"`
	StrategyVersion int                       `json:"strategy_version"`
	SplitPct        float64                   `json:"split_pct"`
	MarketSource    string                    `json:"market_source"`
	Train           *StrategyValidationPeriod `json:"train"`
	Test            *StrategyValidationPeriod `json:"test"`
	Verdict         string                    `json:"verdict"`
}

type StrategyValidationPeriod struct {
	Label       string               `json:"label"`
	Run         *BacktestRun         `json:"run"`
	Performance *BacktestPerformance `json:"performance,omitempty"`
	Metrics     map[string]float64   `json:"metrics,omitempty"`
}

type strategyMarket struct {
	prices   map[string]float64
	history  map[string][]float64
	asOf     time.Time
	barTimes []time.Time
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
			if r.When != nil && strings.TrimSpace(r.When.Symbol) != "" {
				def.Universe = append(def.Universe, r.When.Symbol)
			}
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
	universe := make(map[string]bool, len(def.Universe))
	for _, symbol := range def.Universe {
		universe[symbol] = true
	}
	for _, rule := range def.Rules {
		if len(rule.Allocate) == 0 && rule.Rank == nil {
			warnings = append(warnings, fmt.Sprintf("rule %q has no allocation output", nonEmpty(rule.Name, "(unnamed)")))
		}
		if rule.When != nil && strings.TrimSpace(rule.When.Indicator) == "" {
			return nil, nil, fmt.Errorf("rule %q condition indicator required", nonEmpty(rule.Name, "(unnamed)"))
		}
		if rule.When != nil {
			symbol := strings.ToUpper(strings.TrimSpace(rule.When.Symbol))
			if symbol != "" && !universe[symbol] {
				return nil, nil, fmt.Errorf("rule %q condition symbol %s is outside the strategy universe", nonEmpty(rule.Name, "(unnamed)"), symbol)
			}
		}
		for _, allocation := range rule.Allocate {
			if !universe[allocation.Symbol] {
				return nil, nil, fmt.Errorf("rule %q allocation symbol %s is outside the strategy universe", nonEmpty(rule.Name, "(unnamed)"), allocation.Symbol)
			}
		}
		if rule.Rank != nil {
			for _, symbol := range rule.Rank.Symbols {
				if !universe[symbol] {
					return nil, nil, fmt.Errorf("rule %q rank symbol %s is outside the strategy universe", nonEmpty(rule.Name, "(unnamed)"), symbol)
				}
			}
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
			return nil, "", fmt.Errorf("rank %s metric unavailable for %s: %w", rank.By, symbol, err)
		}
		rows = append(rows, row{symbol: symbol, value: v})
	}
	if len(rows) == 0 {
		return nil, "", fmt.Errorf("rank %s has no computable symbols", rank.By)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].value > rows[j].value })
	rankedValues := make([]string, 0, len(rows))
	for _, r := range rows {
		rankedValues = append(rankedValues, fmt.Sprintf("%s %.4f", r.symbol, r.value))
	}
	if rank.Min != 0 {
		filtered := rows[:0]
		for _, r := range rows {
			if r.value >= rank.Min {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
		if len(rows) == 0 {
			return []StrategyAllocation{}, fmt.Sprintf("ranked by %s: no symbol met min %.4f; values: %s", rank.By, rank.Min, strings.Join(rankedValues, ", ")), nil
		}
	}
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

func shouldRebalanceStrategy(def *StrategyDefinition, interval string, step int) bool {
	if step <= 1 {
		return true
	}
	every := strategyRebalanceEvery(def, interval)
	return every <= 1 || (step-1)%every == 0
}

func strategyRebalanceEvery(def *StrategyDefinition, interval string) int {
	if def == nil {
		return 1
	}
	if def.RebalanceEvery > 1 {
		return def.RebalanceEvery
	}
	cadence := strings.ToLower(strings.TrimSpace(def.Cadence))
	if cadence == "" {
		return 1
	}
	if n, err := strconv.Atoi(cadence); err == nil && n > 0 {
		return n
	}
	cadenceDuration, ok := strategyCadenceDuration(cadence)
	if !ok {
		return 1
	}
	intervalDuration := backtestIntervalDuration(interval)
	if intervalDuration <= 0 {
		return 1
	}
	every := int(math.Round(float64(cadenceDuration) / float64(intervalDuration)))
	if every < 1 {
		return 1
	}
	return every
}

func strategyCadenceDuration(cadence string) (time.Duration, bool) {
	switch cadence {
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

func liveStrategyMarket(ctx *sdk.AppCtx, def *StrategyDefinition) (strategyMarket, error) {
	market := strategyMarket{prices: map[string]float64{}, history: map[string][]float64{}, asOf: time.Now().UTC()}
	interval := strategyHistoryInterval(def)
	limit := strategyRequiredBars(def)
	if scheduleLimit := strategyRebalanceEvery(def, interval) + 1; scheduleLimit > limit {
		limit = scheduleLimit
	}
	if limit > 1000 {
		limit = 1000
	}
	if globalEngine == nil || globalEngine.provider == nil {
		return market, errors.New("live strategy market provider not ready")
	}
	type historyResult struct {
		symbol string
		bars   []Bar
		err    error
	}
	results := make(chan historyResult, len(def.Universe))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, symbol := range def.Universe {
		symbol := symbol
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			bars, err := loadLiveStrategyBars(globalEngine.provider, symbol, interval, limit)
			<-sem
			results <- historyResult{symbol: symbol, bars: bars, err: err}
		}()
	}
	wg.Wait()
	close(results)
	loaded := make(map[string]historyResult, len(def.Universe))
	for result := range results {
		loaded[result.symbol] = result
	}
	scheduleSymbol := def.Universe[0]
	for _, symbol := range def.Universe {
		class := inferAssetClass(symbol)
		if class == "equity" || class == "etf" {
			scheduleSymbol = symbol
			break
		}
	}
	for _, symbol := range def.Universe {
		result, ok := loaded[symbol]
		if !ok || result.err != nil {
			if ok {
				return market, fmt.Errorf("strategy history unavailable for %s: %w", symbol, result.err)
			}
			return market, fmt.Errorf("strategy history unavailable for %s", symbol)
		}
		appendStrategyHistory(market.history, symbol, result.bars)
		h := market.history[symbol]
		if len(h) < limit {
			return market, fmt.Errorf("strategy history incomplete for %s: need %d valid closed bars, got %d", symbol, limit, len(h))
		}
		market.prices[symbol] = h[len(h)-1]
		if symbol == scheduleSymbol {
			for _, bar := range result.bars {
				if bar.T > 0 && strategyBarPrice(bar) > 0 {
					market.barTimes = append(market.barTimes, time.Unix(bar.T, 0).UTC())
				}
			}
		}
	}
	if len(market.barTimes) == 0 {
		return market, errors.New("strategy schedule has no completed market bars")
	}
	sort.Slice(market.barTimes, func(i, j int) bool { return market.barTimes[i].Before(market.barTimes[j]) })
	deduped := market.barTimes[:0]
	for _, barAt := range market.barTimes {
		if len(deduped) == 0 || !barAt.Equal(deduped[len(deduped)-1]) {
			deduped = append(deduped, barAt)
		}
	}
	market.barTimes = deduped
	market.asOf = market.barTimes[len(market.barTimes)-1]
	return market, nil
}

func strategyBarPrice(bar Bar) float64 {
	if bar.C > 0 {
		return bar.C
	}
	if bar.Yes > 0 {
		return bar.Yes
	}
	return bar.O
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

func appendStrategyHistory(history map[string][]float64, symbol string, bars []Bar) {
	for _, b := range bars {
		price := strategyBarPrice(b)
		if price > 0 {
			history[symbol] = append(history[symbol], price)
		}
	}
}

func strategyHistoryInterval(def *StrategyDefinition) string {
	if def == nil {
		return "1d"
	}
	cadence := strings.ToLower(strings.TrimSpace(def.Cadence))
	if _, ok := strategyCadenceDuration(cadence); ok {
		return cadence
	}
	return "1d"
}

func strategyRequiredBars(def *StrategyDefinition) int {
	maxBars := 1
	if def != nil {
		for _, rule := range def.Rules {
			if rule.When != nil {
				maxBars = max(maxBars, indicatorRequiredBars(rule.When.Indicator))
				maxBars = max(maxBars, indicatorRequiredBars(rule.When.Compare))
			}
			if rule.Rank != nil {
				maxBars = max(maxBars, indicatorRequiredBars(rule.Rank.By))
			}
		}
	}
	if maxBars < 1 {
		return 1
	}
	if maxBars > 1000 {
		return 1000
	}
	return maxBars
}

func indicatorRequiredBars(indicator string) int {
	indicator = strings.ToLower(strings.TrimSpace(indicator))
	switch {
	case indicator == "", indicator == "price":
		return 1
	case strings.HasPrefix(indicator, "sma_"):
		return parseMetricWindow(indicator, "sma", 20)
	case strings.HasPrefix(indicator, "ema_"):
		return parseMetricWindow(indicator, "ema", 20)
	case strings.HasPrefix(indicator, "rsi_") || indicator == "rsi":
		return parseMetricWindow(indicator, "rsi", 14) + 1
	case strings.HasPrefix(indicator, "return_") || indicator == "return":
		return parseMetricWindow(indicator, "return", 20) + 1
	case strings.HasPrefix(indicator, "volatility_") || indicator == "volatility":
		return parseMetricWindow(indicator, "volatility", 20) + 1
	default:
		return 1
	}
}

func backtestStrategyMarket(run *BacktestRun, step int) (strategyMarket, error) {
	market := strategyMarket{prices: map[string]float64{}, history: map[string][]float64{}, asOf: backtestReplayTime(run, step)}
	bars, err := dbBacktestMarketHistory(globalCtx.AppDB(), run.ID, step)
	if err != nil {
		return market, err
	}
	for _, bar := range bars {
		if bar == nil || bar.C <= 0 {
			continue
		}
		symbol := strings.ToUpper(strings.TrimSpace(bar.Symbol))
		market.history[symbol] = append(market.history[symbol], bar.C)
		if bar.Step == step {
			market.prices[symbol] = bar.C
		}
	}
	return market, nil
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
	market, err := liveStrategyMarket(ctx, def)
	if err != nil {
		return nil, err
	}
	eval, err := evaluateStrategy(strategy, market)
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
	strategy, err := dbGetStrategy(ctx.AppDB(), pid, strategyID)
	if err != nil {
		return nil, err
	}
	cadence := strings.TrimSpace(strArg(args, "cadence"))
	if cadence == "" {
		if def, _, err := validateStrategyDefinition(strategy.Definition); err == nil {
			cadence = strings.TrimSpace(def.Cadence)
		}
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
	marketBars, steps, marketSource, err := captureBacktestMarketBars(def.Universe, interval, startAt, endAt)
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
			"market_source":  marketSource,
		},
	})
	if err != nil {
		return nil, err
	}
	if err := dbReplaceBacktestMarketBars(ctx.AppDB(), id, marketBars); err != nil {
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
	marketBars, steps, marketSource, err := captureBacktestMarketBars(def.Universe, interval, startAt, endAt)
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
		Label:        "in_sample",
		Name:         name + " · in sample",
		Interval:     interval,
		StartingCash: startingCash,
		FeeBps:       feeBps,
		SlippageBps:  slippageBps,
		MarketSource: marketSource,
		Bars:         reindexBacktestMarketBars(marketBars, 1, trainSteps),
	})
	if err != nil {
		return nil, err
	}
	test, err := a.createCompletedStrategyValidationRun(ctx, pf, strategy, def, strategyValidationRunSpec{
		Label:        "out_of_sample",
		Name:         name + " · out of sample",
		Interval:     interval,
		StartingCash: startingCash,
		FeeBps:       feeBps,
		SlippageBps:  slippageBps,
		MarketSource: marketSource,
		Bars:         reindexValidationMarketBars(marketBars, trainSteps+1, steps, strategyRequiredBars(def)-1),
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
	return result, nil
}

type strategyValidationRunSpec struct {
	Label        string
	Name         string
	Interval     string
	StartingCash float64
	FeeBps       float64
	SlippageBps  float64
	MarketSource string
	Bars         []*BacktestMarketBar
}

func (a *App) createCompletedStrategyValidationRun(ctx *sdk.AppCtx, pf *Portfolio, strategy *Strategy, def *StrategyDefinition, spec strategyValidationRunSpec) (*StrategyValidationPeriod, error) {
	if len(spec.Bars) == 0 {
		return nil, fmt.Errorf("%s validation period has no market bars", spec.Label)
	}
	startAt, endAt := marketBarDateRange(spec.Bars)
	steps := maxBacktestMarketStep(spec.Bars)
	id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{
		ProjectID:       pf.ProjectID,
		PortfolioID:     pf.ID,
		StrategyID:      strategy.ID,
		RunKind:         "strategy",
		StrategyVersion: strategy.Version,
		Name:            spec.Name,
		Status:          "queued",
		Symbols:         def.Universe,
		StartAt:         startAt,
		EndAt:           endAt,
		Interval:        spec.Interval,
		StartingCash:    spec.StartingCash,
		FeeBps:          spec.FeeBps,
		SlippageBps:     spec.SlippageBps,
		TotalSteps:      steps,
		Summary: map[string]any{
			"portfolio_name":    pf.Name,
			"strategy_name":     strategy.Name,
			"market_source":     spec.MarketSource,
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
	run, err := dbGetBacktestRun(ctx.AppDB(), pf.ProjectID, id)
	if err != nil {
		return nil, err
	}
	if _, err := runStrategyBacktestToEnd(run); err != nil {
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
	default:
		httpErr(w, 404, "no such strategy route")
	}
}

// ─── Strategy backtest simulator ───────────────────────────────────

type strategyBacktestState struct {
	Cash      float64
	Positions map[string]*Position
}

func startStrategyBacktestRun(run *BacktestRun) (map[string]any, error) {
	if run.Status != "queued" && run.Status != "failed" {
		return map[string]any{"backtest": run}, nil
	}
	if err := initializeStrategyBacktestRun(run); err != nil {
		return nil, err
	}
	next, _ := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	return stepStrategyBacktestRun(next)
}

func initializeStrategyBacktestRun(run *BacktestRun) error {
	if run == nil {
		return errors.New("backtest run required")
	}
	if run.StrategyID <= 0 {
		return errors.New("strategy_id required")
	}
	strategy, err := dbGetStrategyVersion(globalCtx.AppDB(), run.ProjectID, run.StrategyID, run.StrategyVersion)
	if err != nil {
		return err
	}
	if _, _, err := validateStrategyDefinition(strategy.Definition); err != nil {
		return err
	}
	if err := dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "running", ""); err != nil {
		return err
	}
	_ = dbUpsertBacktestSnapshot(globalCtx.AppDB(), &BacktestSnapshot{
		RunID: run.ID, Step: 0, Equity: run.StartingCash, Cash: run.StartingCash,
		BuyingPower: run.StartingCash, Positions: []*Position{}, Orders: []*Order{}, Prices: []map[string]any{},
	})
	_, _ = dbInsertBacktestEvent(globalCtx.AppDB(), run.ID, "started", "Strategy replay started", map[string]any{"strategy_id": run.StrategyID})
	emitBacktest("trading.backtest.started", run.ID, map[string]any{"portfolio_id": run.PortfolioID, "strategy_id": run.StrategyID, "run_kind": "strategy"})
	return nil
}

func runStrategyBacktestToEnd(run *BacktestRun) (map[string]any, error) {
	if run.Status == "queued" || run.Status == "failed" {
		if err := initializeStrategyBacktestRun(run); err != nil {
			return nil, err
		}
	} else if run.Status == "paused" {
		if err := dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "running", ""); err != nil {
			return nil, err
		}
	} else if run.Status != "running" {
		return nil, fmt.Errorf("backtest is %s; cannot run", run.Status)
	}
	next, err := dbGetBacktestRun(globalCtx.AppDB(), run.ProjectID, run.ID)
	if err != nil {
		return nil, err
	}
	strategy, err := dbGetStrategyVersion(globalCtx.AppDB(), next.ProjectID, next.StrategyID, next.StrategyVersion)
	if err != nil {
		return nil, err
	}
	if _, _, err := validateStrategyDefinition(strategy.Definition); err != nil {
		return nil, err
	}
	state, err := loadStrategyBacktestState(next)
	if err != nil {
		return nil, err
	}
	market, err := backtestStrategyMarket(next, next.CurrentStep)
	if err != nil {
		return nil, err
	}
	for {
		if next.Status != "running" || next.CurrentStep >= next.TotalSteps {
			if fresh, err := dbGetBacktestRun(globalCtx.AppDB(), next.ProjectID, next.ID); err == nil && fresh != nil {
				next = fresh
			}
			return map[string]any{"backtest": next, "status": next.Status}, nil
		}
		next, err = runStrategyBacktestStep(next, strategy, state, &market)
		if err != nil {
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
	strategy, err := dbGetStrategyVersion(globalCtx.AppDB(), run.ProjectID, run.StrategyID, run.StrategyVersion)
	if err != nil {
		return nil, err
	}
	def, _, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		return nil, err
	}
	state, err := loadStrategyBacktestState(run)
	if err != nil {
		return nil, err
	}
	market, err := backtestStrategyMarket(run, step)
	if err != nil {
		return nil, err
	}
	eval, rebalance, err := evaluateStrategyBacktestStep(strategy, def, run, step, market)
	if err != nil {
		return nil, err
	}
	prices, err := backtestMarks(run, step)
	if err != nil {
		return nil, err
	}
	orders := []*Order{}
	executedSignalStep := 0
	if step > 1 && shouldRebalanceStrategy(def, run.Interval, step-1) {
		priorMarket, marketErr := backtestStrategyMarket(run, step-1)
		if marketErr != nil {
			return nil, marketErr
		}
		priorEval, evalErr := evaluateStrategy(strategy, priorMarket)
		if evalErr != nil {
			return nil, evalErr
		}
		orders = applyStrategyTargets(run, state, priorEval.TargetAllocations, backtestExecutionPrices(prices))
		executedSignalStep = step - 1
	}
	snap := strategySnapshot(run, step, state, prices, orders)
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
		"last_step":            step,
		"prices":               prices,
		"rebalance":            rebalance,
		"strategy_id":          strategy.ID,
		"strategy_name":        strategy.Name,
		"target_allocations":   eval.TargetAllocations,
		"decisions":            eval.Decisions,
		"signal_as_of":         eval.AsOf,
		"executed_signal_step": executedSignalStep,
		"execution_model":      "next_bar_open",
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

func runStrategyBacktestStep(run *BacktestRun, strategy *Strategy, state *strategyBacktestState, market *strategyMarket) (*BacktestRun, error) {
	if run == nil {
		return nil, errors.New("backtest run required")
	}
	if strategy == nil {
		return nil, errors.New("strategy required")
	}
	if state == nil {
		return nil, errors.New("strategy backtest state required")
	}
	step := run.CurrentStep + 1
	if step > run.TotalSteps {
		_ = dbSetBacktestStatus(globalCtx.AppDB(), run.ID, "completed", "")
		next := *run
		next.Status = "completed"
		return &next, nil
	}
	def, _, err := validateStrategyDefinition(strategy.Definition)
	if err != nil {
		return nil, err
	}
	var executionEval *StrategyEvaluation
	executedSignalStep := 0
	if step > 1 && shouldRebalanceStrategy(def, run.Interval, step-1) {
		executionEval, err = evaluateStrategy(strategy, *market)
		if err != nil {
			return nil, err
		}
		executedSignalStep = step - 1
	}
	prices, err := advanceBacktestStrategyMarket(run, step, market)
	if err != nil {
		return nil, err
	}
	eval, rebalance, err := evaluateStrategyBacktestStep(strategy, def, run, step, *market)
	if err != nil {
		return nil, err
	}
	stepRun := *run
	stepRun.CurrentStep = step - 1
	orders := []*Order{}
	if executionEval != nil {
		orders = applyStrategyTargets(&stepRun, state, executionEval.TargetAllocations, backtestExecutionPrices(prices))
	}
	snap := strategySnapshot(&stepRun, step, state, prices, orders)
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
		"last_step":            step,
		"prices":               prices,
		"rebalance":            rebalance,
		"strategy_id":          strategy.ID,
		"strategy_name":        strategy.Name,
		"target_allocations":   eval.TargetAllocations,
		"decisions":            eval.Decisions,
		"signal_as_of":         eval.AsOf,
		"executed_signal_step": executedSignalStep,
		"execution_model":      "next_bar_open",
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
	next := *run
	next.CurrentStep = step
	next.Status = status
	next.Summary = summary
	return &next, nil
}

func evaluateStrategyBacktestStep(strategy *Strategy, def *StrategyDefinition, run *BacktestRun, step int, market strategyMarket) (*StrategyEvaluation, bool, error) {
	if shouldRebalanceStrategy(def, run.Interval, step) {
		eval, err := evaluateStrategy(strategy, market)
		return eval, true, err
	}
	every := strategyRebalanceEvery(def, run.Interval)
	return &StrategyEvaluation{
		StrategyID:        strategy.ID,
		StrategyVersion:   strategy.Version,
		AsOf:              market.asOf.Format(time.RFC3339),
		TargetAllocations: nil,
		Decisions:         []string{fmt.Sprintf("holding existing allocation; next rebalance every %d step(s)", every)},
	}, false, nil
}

func loadStrategyBacktestState(run *BacktestRun) (*strategyBacktestState, error) {
	state := &strategyBacktestState{Cash: run.StartingCash, Positions: map[string]*Position{}}
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
	return state, nil
}

func advanceBacktestStrategyMarket(run *BacktestRun, step int, market *strategyMarket) ([]map[string]any, error) {
	prices, err := backtestMarks(run, step)
	if err != nil {
		return nil, err
	}
	if market == nil {
		return prices, nil
	}
	if market.history == nil {
		market.history = map[string][]float64{}
	}
	market.prices = map[string]float64{}
	market.asOf = backtestReplayTime(run, step)
	for _, row := range prices {
		symbol := strings.ToUpper(strings.TrimSpace(fmt.Sprint(row["symbol"])))
		price := anyFloat(row["price"])
		if symbol == "" || price <= 0 {
			continue
		}
		market.history[symbol] = append(market.history[symbol], price)
		market.prices[symbol] = price
	}
	return prices, nil
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
	type plannedTrade struct {
		symbol     string
		price      float64
		diff       float64
		currentQty float64
	}
	plans := []plannedTrade{}
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
		plans = append(plans, plannedTrade{symbol: symbol, price: price, diff: diff, currentQty: currentQty})
	}
	// Execute reductions before additions. The target plan is based on the
	// same pre-trade equity, while sell proceeds are available to fund buys.
	sort.SliceStable(plans, func(i, j int) bool {
		if (plans[i].diff < 0) != (plans[j].diff < 0) {
			return plans[i].diff < 0
		}
		return plans[i].symbol < plans[j].symbol
	})
	orders := []*Order{}
	for _, plan := range plans {
		symbol, price, diff, currentQty := plan.symbol, plan.price, plan.diff, plan.currentQty
		pos := state.Positions[symbol]
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
		if side == "buy" {
			fee := fillFee(qty, fillPrice, run.FeeBps)
			cost := qty*fillPrice + fee
			if cost > state.Cash && fillPrice > 0 {
				feeRate := math.Max(0, run.FeeBps) / 10_000
				qty = math.Max(0, state.Cash/(fillPrice*(1+feeRate)))
				fee = fillFee(qty, fillPrice, run.FeeBps)
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
			fee := fillFee(qty, fillPrice, run.FeeBps)
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

func strategySnapshot(run *BacktestRun, step int, state *strategyBacktestState, prices []map[string]any, orders []*Order) *BacktestSnapshot {
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
		Orders:      orders,
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
