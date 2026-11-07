# Indicator strategies

## Research and what it supports

Reviewed 13 September 2026 after a web search for technical indicator strategy guides. These sources describe established tools, not evidence of a universally best or profitable hourly strategy. The presets are testable hypotheses; daily-period examples in an educational guide do not establish that the same parameters work on hourly stocks or crypto.

- [Fidelity: EMA](https://www.fidelity.com/learning-center/trading-investing/technical-analysis/technical-indicator-guide/ema): trend following, buying dips near a rising moving average, more sensitivity and whipsaws than SMA. Specifies SMA initialization.
- [Fidelity: RSI](https://www.fidelity.com/learning-center/trading-investing/technical-analysis/technical-indicator-guide/RSI): Wilder's momentum oscillator, traditional 30/70 thresholds; can remain overbought/oversold throughout a strong trend. This motivates testing a trend filter, not blindly buying low RSI.
- [Fidelity: MACD](https://www.fidelity.com/learning-center/trading-investing/technical-analysis/technical-indicator-guide/macd): default 12/26 EMA difference and 9-period signal; crossovers and zero-line confirmation; whipsaws in trading ranges.
- [Fidelity: Bollinger Bands](https://www.fidelity.com/learning-center/trading-investing/technical-analysis/technical-indicator-guide/bollinger-bands): default 20-period SMA ± 2 standard deviations, reversion and trend continuation interpretations; combine with other indicators.
- [Fidelity: ATR](https://www.fidelity.com/learning-center/trading-investing/technical-analysis/technical-indicator-guide/atr): Wilder-smoothed true range and volatility-based stops. ATR requires highs/lows and previous close. It is not implemented here: the strategy pipeline currently retains closes only. Do not substitute close volatility and label it ATR. ADX and volume indicators likewise remain outside this catalog.

## Available in the app

The Strategies screen offers five hourly multi-symbol templates: EMA trend rotation, MACD trend confirmation, RSI trend pullback, Bollinger + RSI reversion, and experimental Bollinger relative strength. Templates use the current watchlist; the catalog defaults to 12 stocks when no symbols are supplied. Supply crypto symbols to use the same definitions for crypto. Loading a template only edits the draft. Saving, assigning and executing remain separate operations.

Discover the complete formulas, periods, limitations and templates with MCP `strategy_catalog` (optional `symbols` array), or `GET /strategies/catalog?symbols=BTC-USD,ETH-USD` using normal project scoping.

New indicators:

| Name | Definition |
| --- | --- |
| `ema_sma_N` | EMA, alpha 2/(N+1), seed = SMA of first N closes in a fixed 5N-bar window |
| `rsi_wilder_N` | Wilder smoothing, alpha 1/N, seed average of first N changes in a fixed 5N+1-close window; flat series 50 |
| `macd_F_S_G` | SMA-seeded fast EMA minus slow EMA; defaults 12,26,9 |
| `macd_signal_F_S_G` | G-period SMA-seeded EMA of valid MACD values |
| `macd_hist_F_S_G` | MACD minus signal |
| `bb_upper_N`, `bb_lower_N` | SMA ± 2 population standard deviations |
| `bb_width_N` | Band width / SMA × 100 |
| `bb_percent_b_N` | (Close − lower band) / band width; 0 lower, 1 upper; flat series 0.5 |
| `zscore_N` | (Close − SMA) / population standard deviation; flat series 0 |

MACD uses a fixed 5×(slow+signal) close history. Bounded initialization deliberately makes new indicators identical between live evaluation and replay regardless of total replay length. It can differ slightly from charting packages using a longer historical seed. Missing warmup gives no allocation and an explicit evaluation warning. Periods requiring more than 1000 closes are rejected. All periods are **completed strategy-cadence bars**, even when execution uses finer data.

Legacy `ema_N` (first-close seed) and `rsi_N` (simple-window gains/losses, including the old flat-series behavior) are preserved to avoid silently changing saved strategies. New templates use the explicit standard-formula names. Existing SMA, return, log-return volatility and availability-timed generic features remain supported.

## Combining indicators

Conditions accept nested `all` / `any`, scalar comparisons, and `crosses_above` / `crosses_below`. Crossovers compare the previous completed candle to the current completed candle. Generic feature crossovers are rejected because the engine stores the latest available feature, not a historical feature series.

`rank.where` filters each candidate symbol (omit `symbol` to refer to that candidate); `rank.direction` chooses `asc` or `desc`; `rank.budget` sets total target exposure before per-position caps (positive fraction, default 1). `rank.min` retains its legacy convention: zero disables the minimum; use `where` with `>= 0` for an actual zero threshold. Rankings use stable input-order tie breaking.

```json
{
  "universe": ["BTC-USD", "ETH-USD", "SOL-USD", "LINK-USD"],
  "cadence": "1h",
  "rebalance_every": 6,
  "rules": [{
    "name": "Trend and momentum",
    "rank": {
      "symbols": ["BTC-USD", "ETH-USD", "SOL-USD", "LINK-USD"],
      "where": {"all": [
        {"indicator": "ema_sma_20", "operator": ">", "compare": "ema_sma_50"},
        {"indicator": "macd_hist_12_26_9", "operator": ">", "value": 0}
      ]},
      "by": "return_72", "direction": "desc", "top": 4,
      "weight": "equal_weight", "budget": 0.6
    }
  }],
  "risk": {"max_position_weight": 0.15}
}
```

First matching rule wins, no matching rule means cash. These are **target allocation filters**, not latched entry/exit orders: the RSI and Bollinger examples stop holding when their filters cease to pass. A crossover is a one-bar event; it does not mean "hold until the opposite crossover." A separate persistent entry/exit state machine and ATR stops are future work. No profit claim follows from adding indicators.

## Execution and validation

Hourly bar inputs provide execution quotes at opens and reveal closes at completion. Zero submission/cancellation/jitter latency gives an idealized next-open approximation. Any positive latency can miss that opening quote, defer execution for a full source interval, and permit cancellation at the following signal. This is an execution-data resolution limitation; inventing intrabar fills would not fix it.

Simulation settings now explain the assumption, offer an explicit zero-latency timing button, and preserve fees/slippage when applying it. Generated run summaries and exported artifacts describe the tape resolution and latency limitation. Quote-event latency behavior remains unchanged. Use finer captured execution bars or a custom quote tape to study timing accurately; compare delayed-quote sensitivity rather than assuming millisecond precision on hourly data.

Research should compare active candidates to an equal-exposure diversified portfolio and a market benchmark, include fees/slippage, report turnover/drawdown, and validate on separate dates. July–August 2026 has already been inspected in this session and is not a fresh unseen holdout for the new indicator search.


## Experimental relative-strength candidate

`bollinger_strength_top4_rebalance72` ranks symbols by descending `zscore_20`, holds the top four equally at a total target budget of 60%, caps each position at 15%, and rebalances every 72 market bars. It has no absolute breakout threshold. On hourly crypto this is three days; stock market closures make the elapsed time longer. Positions can drift between rebalances.

A September 2026 research search compared 41 definitions per market, including a 60%-invested equal-weight reference, across 12 stocks and eight crypto assets. The selected definition beat that reference in July–August for both markets. After selection, it beat the reference in September 1–12 for crypto but failed for stocks; both markets underperformed in fresh-start August tests. These are exploratory results with period and start-date sensitivity, not evidence of a durable edge. The template is offered for further validation with the user's own universe, costs and unseen dates. The original research artifacts retain their original engine fingerprint; adding this catalog entry changes the release fingerprint and does not make old artifacts compatible.


## Inverse-volatility sizing

`rank.weight: "inverse_volatility"` sizes the selected assets inversely to the population standard deviation of their completed per-bar log returns. Set `volatility_period` (2–999 returns, requiring one additional close) and `volatility_floor` (1e-8–1, a fraction per bar; 0.005 means 0.5% daily SD on daily candles). The floor prevents a flat or nearly flat series receiving unbounded weight. Scores normalize to `rank.budget` before existing position caps; capped weight stays in cash and is not redistributed. This is relative sizing, not a portfolio volatility target, and it does not estimate correlations. Eligibility and ranking are evaluated before sizing. Missing history rejects that evaluation, without substituting volatility from future bars.
