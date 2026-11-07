package main

import (
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type strategyPreset struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Definition  StrategyDefinition `json:"definition"`
}

func strategyPresets(symbols []string) []strategyPreset {
	symbols = cleanSymbols(symbols)
	if len(symbols) == 0 {
		symbols = []string{"AAPL", "MSFT", "GOOGL", "AMZN", "NVDA", "META", "JPM", "XOM", "JNJ", "WMT", "COST", "UNH"}
	}
	scalar := func(ind, op, compare string, value float64) StrategyCondition {
		return StrategyCondition{Indicator: ind, Operator: op, Compare: compare, Value: value}
	}
	trend := scalar("ema_sma_20", ">", "ema_sma_50", 0)
	longTrend := scalar("price", ">", "ema_sma_100", 0)
	makePreset := func(id, name, desc, by, direction string, conditions ...StrategyCondition) strategyPreset {
		return strategyPreset{ID: id, Name: name, Description: desc, Definition: StrategyDefinition{Universe: symbols, Cadence: "1h", RebalanceEvery: 6, Risk: StrategyRisk{MaxPositionWeight: 0.15}, Rules: []StrategyRule{{Name: name, Rank: &StrategyRank{Symbols: symbols, By: by, Direction: direction, Top: 4, Weight: "equal_weight", Budget: 0.6, Where: &StrategyCondition{All: conditions}}}}}}
	}
	return []strategyPreset{
		makePreset("ema_trend", "EMA trend rotation", "Every 6 completed hourly bars, hold up to four symbols with EMA20 > EMA50 and price > EMA100; rank by 72-bar return. Exit when ineligible. Maximum 60% invested, 15% per symbol.", "return_72", "desc", trend, longTrend),
		makePreset("macd_trend", "MACD trend confirmation", "EMA20 > EMA50 plus MACD(12,26,9) above its signal and zero. Rank by percentage return rather than raw MACD so prices are comparable. Reassess every 6 hours.", "return_72", "desc", trend, scalar("macd_hist_12_26_9", ">", "", 0), scalar("macd_12_26_9", ">", "", 0)),
		makePreset("rsi_pullback", "RSI trend pullback", "Price above EMA100 and Wilder RSI14 between 35 and 50; choose the lowest RSI. These are target filters: exit when the filter fails, not a persistent entry/exit system.", "rsi_wilder_14", "asc", longTrend, scalar("rsi_wilder_14", ">=", "", 35), scalar("rsi_wilder_14", "<=", "", 50)),
		makePreset("bollinger_reversion", "Bollinger + RSI reversion", "Price below the lower 20-bar, 2-standard-deviation band and Wilder RSI14 < 30. Rank by lowest z-score; return to cash when the oversold filter fails. May lose money during sustained downtrends.", "zscore_20", "asc", scalar("price", "<", "bb_lower_20", 0), scalar("rsi_wilder_14", "<", "", 30)),
		{ID: "bollinger_strength_top4_rebalance72", Name: "Bollinger relative strength (experimental)", Description: "Rank by 20-bar z-score and hold the top four at equal weights, targeting 60% invested with a 15% position cap. Rebalance every 72 market bars: about 3 days for hourly crypto, longer for stocks. Research candidate with period-sensitive results; no absolute breakout filter or proven durable edge.", Definition: StrategyDefinition{Universe: symbols, Cadence: "1h", RebalanceEvery: 72, Risk: StrategyRisk{MaxPositionWeight: 0.15}, Rules: []StrategyRule{{Name: "Bollinger relative strength", Rank: &StrategyRank{Symbols: symbols, By: "zscore_20", Direction: "desc", Top: 4, Weight: "equal_weight", Budget: 0.6}}}}},
	}
}
func strategyCatalog(symbols []string) map[string]any {
	return map[string]any{
		"presets":      strategyPresets(symbols),
		"rule_presets": ruleExamplePresets(),
		"rule_engine":  ruleEngineCatalog(),
		"indicators": []map[string]string{
			{"name": "sma_N", "description": "Simple mean of N completed closes."},
			{"name": "ema_sma_N", "description": "EMA with SMA seed; fixed 5×N closed-bar history for live/replay parity."},
			{"name": "rsi_wilder_N", "description": "Wilder smoothed RSI; fixed 5×N+1 closes; flat series = 50."},
			{"name": "macd_F_S_G / macd_signal_F_S_G / macd_hist_F_S_G", "description": "SMA-seeded fast EMA minus slow EMA, signal EMA and histogram. Defaults 12/26/9; history 5×(S+G)."},
			{"name": "bb_upper_N / bb_lower_N / bb_width_N / bb_percent_b_N", "description": "SMA ± 2 population standard deviations. Width in percent; percent B in units where 0=lower and 1=upper; flat B=0.5."},
			{"name": "zscore_N", "description": "(Close − SMA) / population standard deviation; flat = 0."},
			{"name": "return_N / volatility_N", "description": "N-bar percentage return / population standard deviation of log returns (fraction per bar)."},
			{"name": "ema_N / rsi_N", "description": "Legacy formulas preserved: first-close EMA seed / simple-window RSI. Prefer ema_sma_N and rsi_wilder_N for new strategies."},
			{"name": "feature:name.field", "description": "Latest feature known at event availability time (news, sentiment, or custom numeric input)."},
		},
		"conditions":  "Use all or any arrays to combine comparisons. Operators: >, >=, <, <=, crosses_above, crosses_below. Crossovers are one-bar events, not a persistent holding instruction. rank.where evaluates omitted symbols for each candidate. rank.direction is asc/desc; rank.weight also supports inverse_volatility with explicit volatility_period (2–999) and volatility_floor (1e-8–1, per-bar log-return SD). Weights normalize within the eligible top set, then position caps leave excess in cash without redistribution. rank.budget sets total target exposure (0 omitted = 100%). First matching rule wins; no match means cash.",
		"limitations": "Research templates, not proven profitable strategies. Allocation indicators consume completed closes; rule-engine examples separately support OHLC, ATR and volume. ADX is not implemented. Allocation periods are bars; periods are bars, not days. New recursive indicators deliberately use bounded history, so values can differ slightly from charts with longer initialization histories. Validate costs, benchmarks and out-of-sample stability before promotion.",
		"sources": []string{
			"https://www.fidelity.com/learning-center/trading-investing/technical-analysis/technical-indicator-guide/ema",
			"https://www.fidelity.com/learning-center/trading-investing/technical-analysis/technical-indicator-guide/RSI",
			"https://www.fidelity.com/learning-center/trading-investing/technical-analysis/technical-indicator-guide/macd",
			"https://www.fidelity.com/learning-center/trading-investing/technical-analysis/technical-indicator-guide/bollinger-bands",
		},
	}
}

func ruleEngineCatalog() map[string]any {
	return map[string]any{
		"version": "trading-rules/1", "execution": "event_backtest", "live_assignment_supported": false,
		"documentation": "RULE_ENGINE.md",
		"capabilities":  []string{"OHLC expressions", "multiple timeframes", "SMA/EMA/RSI/ATR", "nested series", "persistent state", "reset latches", "session windows", "fixed stop risk", "long/short linear contracts", "OCO", "protective exits", "activated trailing stops"},
		"authoring": map[string]any{
			"definition":       "engine=rules; universe=[program.symbol]; cadence=program.timeframe; program requires version=trading-rules/1, symbol, timeframe, IANA timezone and rules. See complete rule_presets.",
			"operand_counts":   "and/or accept 2–64 expressions and short circuit left to right; comparisons and arithmetic take 2, not/abs take 1. Availability guards precede their dependent indicators.",
			"expressions":      []map[string]any{{"value": 100}, {"metric": "ema", "period": 20, "timeframe": "1h", "offset": 0}, {"name": "my_calculation"}, {"op": ">", "args": []map[string]any{{"metric": "close"}, {"value": 100}}}},
			"metrics":          []string{"open", "high", "low", "close", "volume", "range", "body", "close_fraction", "sma", "ema", "stddev", "volatility", "rsi", "atr", "highest_high", "lowest_low", "return", "feature", "price", "bid", "ask", "position_qty", "entry_count", "minute", "weekday", "window_high", "window_low", "window_complete", "equity"},
			"operators":        []string{"+", "-", "*", "/", "min", "max", ">", ">=", "<", "<=", "==", "!=", "and", "or", "crosses_above", "crosses_below", "not", "abs"},
			"timeframes":       []string{"1m", "5m", "15m", "1h", "4h", "1d"},
			"rule":             "id, on=bar.close|quote|clock|fill|any, optional timeframe/when/reset/schedule, repeat=once_per_bar|once_per_day|until_reset|always, actions. until_reset requires reset. clock schedule names reference program.schedules.",
			"actions":          "enter (side=buy|sell, order_type=market|limit|stop, price for pending orders, sizing), flatten, cancel (optional group), set (declared variable name and value expression). An opposing entry pair must share one nonempty OCO group.",
			"sizing":           "{mode:fixed_risk|quantity|notional|equity_pct,amount:number}; fixed_risk requires a stop. equity_pct is a fraction in (0,1]. Risk and notional use account currency.",
			"protection":       "stop/target are absolute-price expressions; stop_pct/target_pct and trail_activation_pct/trail_distance_pct are fractions (0.005 means 0.5%). Do not combine absolute and percent for the same boundary.",
			"state_and_series": "program.calculations maps names to expressions; initial declares numeric variables. sma/ema/stddev accept input expressions from the same candle timeframe. feature requires feature and field. Historical offset is completed candles; higher frames must be multiples of base.",
			"sessions":         "windows maps names to {start:HH:MM,end:HH:MM}; schedules maps names to HH:MM. Both use program.timezone. Same-day windows require complete base candles.",
			"limits":           "One instrument per program; no implicit pyramiding; bounded indicator seeds; fixed contract FX; no financing or automatic margin liquidation. Source labels are caller-supplied provenance.",
		},
		"mcp_workflow":        []string{"strategy_catalog: discover examples/syntax", "strategy_validate: validate definition without saving", "strategy_create: save definition and return strategy.id", "market_data_import: normalize supplied bid/ask quote and closed-bar CSV exports with explicit provenance; inspect gaps and hashes, then pass returned inputs", "strategy_backtest_create: supply portfolio_id, strategy_id, inputs and simulation", "backtest_control: run then poll status until completed; inspect summary.metrics", "strategy_scorecard_update: define required metric thresholds and evaluation scope before judging results", "strategy_scorecard_evaluate: persist a pass/fail evaluation for the completed backtest; strategy_scorecard_get reads its captured policy, metrics and strategy version", "validation_create/control/report: evaluate captured inputs with chronological holdouts, walk-forward or cost/stress scenarios; no automatic promotion", "backtest_artifact: export results or import a reproducible replay", "strategy_update: revise the definition as a new version; previous runs/evaluations retain their captured version"},
		"evaluation_guidance": "strategy_evaluate returns allocation targets and does not evaluate a trade program's historical performance. Use completed event-backtest metrics, scorecards and validation suites for engine=rules. Synthetic fixtures prove mechanics, not profitability. Preserve a losing verdict; never weaken a policy or relabel in-sample results to manufacture a pass. Rule programs currently cannot be assigned to live portfolios.",
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
