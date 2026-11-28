package main

import (
	"math"
	"strings"

	sdk "github.com/apteva/app-sdk"
	sim "github.com/apteva/apps/mcp/strategies/internal/backtest"
	rules "github.com/apteva/apps/mcp/strategies/internal/ruleengine"
)

type strategyPreset struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Shorts      bool               `json:"shorts,omitempty"` // backtest with simulation.shorts enabled
	Source      string             `json:"source,omitempty"`
	Definition  StrategyDefinition `json:"definition"`
	Simulation  map[string]any     `json:"simulation,omitempty"` // simulation fields the template assumes; the rest keep their defaults
}

func num(v float64) rules.Expr                   { return rules.Number(v) }
func op(o string, args ...rules.Expr) rules.Expr { return rules.Operation(o, args...) }
func metric(name string, period int) rules.Expr  { return rules.Expr{Metric: name, Period: period} }
func ref(name string) rules.Expr                 { return rules.Expr{Name: name} }
func exprPtr(e rules.Expr) *rules.Expr           { return &e }

// monthly wraps a rebalance so it runs on the first closed bar of each month.
func monthly(id string, action rules.Action) []rules.Rule {
	newMonth := op("!=", rules.Expr{Metric: "month"}, ref("last_month"))
	return []rules.Rule{{ID: id, On: "bar.close", When: &newMonth, Repeat: "always", Actions: []rules.Action{action, {Kind: "set", Name: "last_month", Value: exprPtr(rules.Expr{Metric: "month"})}}}}
}

func presetDefinition(p *rules.Program) StrategyDefinition {
	return StrategyDefinition{Program: p, Universe: append([]string(nil), p.Symbols...), Cadence: p.Timeframe}
}

func strategyPresets(symbols []string) []strategyPreset {
	symbols = cleanSymbols(symbols)
	if len(symbols) == 0 {
		symbols = []string{"AAPL", "MSFT", "GOOGL", "AMZN", "NVDA", "META", "JPM", "XOM", "JNJ", "WMT", "COST", "UNH"}
	}
	daily := func(syms []string, rs []rules.Rule, calcs map[string]rules.Expr) *rules.Program {
		return &rules.Program{Version: rules.Version, Symbols: syms, Timeframe: "1d", Timezone: "America/New_York", Initial: map[string]float64{"last_month": 0}, Calculations: calcs, Rules: rs}
	}
	decile := max(1, len(symbols)/10)
	trend := op("and", op(">", metric("close", 0), metric("sma", 200)), op("top", metric("return", 126), num(math.Min(4, float64(len(symbols))))))
	rotation := op("if", trend, num(1), num(0))
	forecast := rules.Expr{Metric: "feature", Feature: "forecast", Field: "value"}
	spread := op("if", op("top", ref("score"), num(float64(decile))), num(1), op("if", op("bottom", ref("score"), num(float64(decile))), num(-1), num(0)))
	annualVol := op("*", metric("volatility", 60), num(math.Sqrt(252)))
	tsmom := op("*", op("sign", metric("return", 252)), op("clamp", op("/", op("/", num(0.4), annualVol), num(float64(len(symbols)))), num(0), num(0.2)))
	factors := []string{"MTUM", "QUAL", "VLUE", "USMV", "SIZE", "SPHD"}
	factorWeight := op("if", op("top", metric("return", 252), num(2)), num(1), op("if", op("bottom", metric("return", 252), num(2)), num(-1), num(0)))
	sentiment := op("clamp", rules.Expr{Metric: "feature", Feature: "sentiment", Field: "score"}, num(-1), num(1))
	target := func(w rules.Expr, c rules.Construct) rules.Action {
		return rules.Action{Kind: "target", Weight: &w, Construct: &c}
	}
	ratio := op("/", rules.Expr{Metric: "close", Symbol: "KO"}, rules.Expr{Metric: "close", Symbol: "PEP"})
	z := op("/", op("-", ref("ratio"), rules.Expr{Metric: "sma", Period: 60, Input: exprPtr(ref("ratio"))}), rules.Expr{Metric: "stddev", Period: 60, Input: exprPtr(ref("ratio"))})
	flatKO := op("==", rules.Expr{Metric: "position_qty", Symbol: "KO"}, num(0))
	half := rules.Sizing{Mode: "equity_pct", Amount: 0.45}
	pairs := &rules.Program{Version: rules.Version, Symbols: []string{"KO", "PEP"}, Timeframe: "1d", Timezone: "America/New_York",
		Calculations: map[string]rules.Expr{"ratio": ratio, "z": z},
		Rules: []rules.Rule{
			{ID: "short_ko", On: "bar.close", When: exprPtr(op("and", op(">", ref("z"), num(2)), flatKO)), Actions: []rules.Action{{Kind: "enter", Symbol: "KO", Side: "sell", Sizing: half}}},
			{ID: "long_pep", On: "bar.close", When: exprPtr(op("and", op(">", ref("z"), num(2)), flatKO)), Actions: []rules.Action{{Kind: "enter", Symbol: "PEP", Side: "buy", Sizing: half}}},
			{ID: "long_ko", On: "bar.close", When: exprPtr(op("and", op("<", ref("z"), num(-2)), flatKO)), Actions: []rules.Action{{Kind: "enter", Symbol: "KO", Side: "buy", Sizing: half}}},
			{ID: "short_pep", On: "bar.close", When: exprPtr(op("and", op("<", ref("z"), num(-2)), flatKO)), Actions: []rules.Action{{Kind: "enter", Symbol: "PEP", Side: "sell", Sizing: half}}},
			{ID: "exit", On: "bar.close", When: exprPtr(op("and", op("<", op("abs", ref("z")), num(0.5)), op("not", flatKO))), Actions: []rules.Action{{Kind: "flatten"}}},
		}}
	return []strategyPreset{
		{ID: "trend_rotation", Name: "Trend rotation", Description: "Monthly: hold up to four symbols that are above their 200-day average, chosen by 6-month return, equal weight, 25% cap. Long only; runs live.",
			Definition: presetDefinition(daily(symbols, monthly("rotate", target(rotation, rules.Construct{Normalize: "gross", Gross: 1, MaxPosition: 0.25})), nil))},
		{ID: "decile_spread", Name: "Forecast decile spread", Shorts: true, Source: "Gu, Kelly & Xiu (2020); Jiang, Kelly & Xiu (2023)",
			Description: "Monthly long/short book on an imported model forecast (feature forecast.value): long the top decile, short the bottom decile, dollar neutral. Import forecasts as features with available_at to avoid look-ahead.",
			Definition:  presetDefinition(daily(symbols, monthly("spread", target(spread, rules.Construct{Normalize: "legs", Gross: 1})), map[string]rules.Expr{"score": forecast}))},
		{ID: "tsmom_vol_scaled", Name: "Time-series momentum, volatility scaled", Shorts: true, Source: "Moskowitz, Ooi & Pedersen (2012); Lim, Zohren & Roberts (2019)",
			Description: "Monthly: long each market with a positive 12-month return and short each with a negative one, sized to 40% annual volatility across the book, 20% cap per market. Works on equities, ETFs, FX and crypto.",
			Definition:  presetDefinition(daily(symbols, monthly("tsmom", target(tsmom, rules.Construct{Normalize: "none", MaxGross: 2})), nil))},
		{ID: "factor_momentum", Name: "Factor momentum", Shorts: true, Source: "Ehsani & Linnainmaa (2022)",
			Description: "Monthly: long the two factor ETFs with the best 12-month return, short the two worst, dollar neutral.",
			Definition:  presetDefinition(daily(factors, monthly("factors", target(factorWeight, rules.Construct{Normalize: "legs", Gross: 1})), nil))},
		{ID: "news_sentiment", Name: "News sentiment", Shorts: true, Source: "Lopez-Lira & Tang (2023)",
			Description: "Daily long/short on an imported sentiment score (feature sentiment.score in [-1, 1]), dollar neutral. Each score's available_at must be before the close it trades on.",
			Definition:  presetDefinition(&rules.Program{Version: rules.Version, Symbols: symbols, Timeframe: "1d", Timezone: "America/New_York", Rules: []rules.Rule{{ID: "sentiment", On: "bar.close", Actions: []rules.Action{target(sentiment, rules.Construct{Normalize: "legs", Gross: 1, MinTradePct: 0.5})}}}})},
		{ID: "pairs_ko_pep", Name: "Pairs: KO / PEP", Shorts: true, Source: "Gatev, Goetzmann & Rouwenhorst (2006)",
			Description: "Trade the KO/PEP price ratio: when its 60-day z-score passes ±2, short the rich leg and buy the cheap one; flatten when it returns inside ±0.5.",
			Definition:  presetDefinition(pairs)},
	}
}

func strategyCatalog(symbols []string) map[string]any {
	presets := strategyPresets(symbols)
	for i := range presets {
		if presets[i].Shorts {
			presets[i].Simulation = map[string]any{"shorts": sim.DefaultShorts()}
		}
	}
	return map[string]any{
		"presets":      presets,
		"rule_presets": ruleExamplePresets(),
		"rule_engine":  ruleEngineCatalog(),
		"limitations":  "Research templates, not proven profitable strategies. Periods are bars of the program timeframe. Recursive indicators use bounded history, so values can differ slightly from charts. Validate costs, benchmarks and out-of-sample stability before promotion.",
	}
}

func ruleEngineCatalog() map[string]any {
	return map[string]any{
		"version": rules.Version, "execution": []string{"event_backtest", "live"}, "live_assignment_supported": true,
		"documentation": "RULE_ENGINE.md",
		"capabilities":  []string{"1–500 symbols per program", "per-symbol rules (for_each)", "cross-sectional ranking", "target portfolios (long/short, legs, caps, turnover band)", "pairs and spreads", "OHLC expressions", "multiple timeframes", "SMA/EMA/RSI/ATR/volatility/correlation/beta", "nested series", "persistent state", "reset latches", "session windows", "fixed stop risk", "spot shorts with borrow and Reg T margin (simulator)", "linear contracts", "OCO", "protective exits", "activated trailing stops"},
		"authoring": map[string]any{
			"definition":     "{program: {...}}. The program requires version=trading-rules/1, symbols (1–500), timeframe, IANA timezone and rules. See presets and rule_presets.",
			"symbols":        "Expressions read the rule's current symbol, or the symbol they name ({metric:close,symbol:KO}). A rule with for_each:true runs once per symbol; other rules in a multi-symbol program must name symbols. One-symbol programs need neither.",
			"operand_counts": "and/or accept 2–64 expressions and short circuit left to right. Unary: not abs sign log exp sqrt cs_rank cs_zscore cs_demean cs_count. Ternary: clamp(x,lo,hi), if(cond,a,b). top/bottom/quantile take an expression and a whole-number literal.",
			"cross_section":  "cs_rank (0..1), cs_zscore, cs_demean, cs_count(cond), top(x,n), bottom(x,n), quantile(x,q) evaluate x for every symbol at this moment. Ties go to symbol order; symbols with missing data are left out.",
			"expressions":    []map[string]any{{"value": 100}, {"metric": "ema", "period": 20, "timeframe": "1h"}, {"metric": "close", "symbol": "KO"}, {"metric": "beta", "against": "SPY", "period": 60}, {"name": "my_calculation"}, {"op": "top", "args": []map[string]any{{"metric": "return", "period": 126}, {"value": 5}}}},
			"metrics":        []string{"open", "high", "low", "close", "volume", "range", "body", "close_fraction", "sma", "ema", "stddev", "volatility", "rsi", "atr", "highest_high", "lowest_low", "return", "correlation", "beta", "feature", "price", "bid", "ask", "position_qty", "entry_count", "equity", "minute", "weekday", "day_of_month", "month", "window_high", "window_low", "window_complete"},
			"operators":      []string{"+", "-", "*", "/", "min", "max", ">", ">=", "<", "<=", "==", "!=", "and", "or", "not", "abs", "sign", "log", "exp", "sqrt", "clamp", "if", "crosses_above", "crosses_below", "cs_rank", "cs_zscore", "cs_demean", "cs_count", "top", "bottom", "quantile"},
			"timeframes":     []string{"1m", "5m", "15m", "1h", "4h", "1d"},
			"rule":           "id, on=bar.close|quote|clock|fill|any, optional for_each/timeframe/when/reset/schedule, repeat=once_per_bar|once_per_day|until_reset|always, actions. bar.close rules run once per moment after every symbol's candles are in.",
			"actions":        "enter (symbol, side=buy|sell, order_type=market|limit|stop, price, sizing), flatten (no symbol outside for_each = whole book), cancel, set, target (weight expression per symbol + construct).",
			"target":         "weight is a signed score per symbol. construct.normalize: legs (longs to (gross+net)/2, shorts to (gross−net)/2; default), gross (sum |w| = gross), none. gross default 1, net default 0, max_position, max_gross ≤ 2, min_trade_pct (percent of equity). Symbols without data keep their position. One target action per program; not combined with enter.",
			"sizing":         "{mode:fixed_risk|quantity|notional|equity_pct,amount:number}; fixed_risk requires a stop. equity_pct is a fraction in (0,1].",
			"protection":     "stop/target are absolute-price expressions; stop_pct/target_pct and trail_activation_pct/trail_distance_pct are fractions.",
			"shorts":         "Backtests: pass simulation.shorts {enabled:true, initial_margin:0.5, maintenance_margin:0.3, borrow_bps_annual:25, symbol_borrow_bps, unshortable}. Proceeds go to cash; borrow accrues; maintenance breaches force covers. No dividend events: use total_return adjusted bars for shorts.",
			"live":           "Live assignments evaluate each newly closed bar with the portfolio's cash, positions and marks. Supported: bar.close rules, set, flatten, target (long weights) and market entries. Not yet live: shorts, protective stops/targets/trails, limit/stop entries, features, quote/fill/clock triggers.",
		},
		"mcp_workflow":        []string{"strategy_catalog: discover presets and syntax", "strategy_validate: validate a definition without saving", "strategy_create: save it", "strategy_backtest_create: portfolio_id, strategy_id, start_at/end_at (captured history at the program timeframe) or inputs (a complete sourced tape), optional simulation", "backtest_control: run, then poll until completed; inspect summary.metrics", "strategy_scorecard_update / strategy_scorecard_evaluate: judge results against fixed thresholds", "validation_create/control/report: chronological holdouts, walk-forward, cost and stress scenarios", "strategy_evaluate: preview what the program would do now", "strategy_assign: run it live on a Trading portfolio"},
		"evaluation_guidance": "Synthetic fixtures prove mechanics, not profitability. Preserve a losing verdict; never weaken a policy or relabel in-sample results to manufacture a pass.",
	}
}

func (a *App) toolStrategyCatalog(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	symbols := []string{}
	switch v := args["symbols"].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				symbols = append(symbols, s)
			}
		}
	case []string:
		symbols = v
	case string:
		symbols = strings.Split(v, ",")
	}
	return strategyCatalog(symbols), nil
}
