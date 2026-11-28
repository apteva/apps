# Design: one rules engine for backtests and live

Status: building as Strategies 0.3.0, 2026-11-28. Builds on Strategies v0.2.2 / Trading v0.20.0.
Supersedes the two-engine (allocation + rules) design and the v1/v2 split.

## Goal

Strategies has one strategy engine. A strategy is a rule program. The same
program, evaluated by the same code, drives:

- **event backtests** in the deterministic simulator, and
- **live assignments** on a Trading portfolio.

The rule language is powerful enough that allocation rotations, cross-sectional
long/short books, time-series momentum, pairs and signal-driven strategies are
each one program, with no engine code per strategy:

| Strategy | Program shape |
|---|---|
| Momentum / trend rotation (old allocation presets) | `target` with `weight = if(top(return(126), 3), 1, 0)`, `normalize: gross` |
| Decile spread on a model forecast (Gu-Kelly-Xiu, Jiang-Kelly-Xiu) | per-symbol feature score, `top`/`bottom`, `target` with `legs, gross 1, net 0` |
| Time-series momentum with vol scaling (Moskowitz-Ooi-Pedersen, Lim-Zohren-Roberts; equities, FX, crypto) | `weight = sign(return(252)) × clamp(0.4 / volatility(60) / cs_count(1), 0, 0.2)`, `normalize: none` |
| Factor momentum (Ehsani-Linnainmaa) | factor ETFs ranked by `return(252)`, long top 2, short bottom 2 |
| LLM news sentiment (Lopez-Lira-Tang) | `weight = clamp(feature(sentiment.score), -1, 1)`, `legs, gross 1` |
| Pairs (Gatev-Goetzmann-Rouwenhorst) | `ratio = close(KO)/close(PEP)`, z-score, `enter` short/long, `flatten` on cross |
| Single-instrument trading rules (breakouts, sessions, stops) | `enter` with stops, targets, trailing stops, OCO |

## Program format

One format, `trading-rules/1`:

- `symbols`: 1–500 symbols. A one-symbol program is a list of one.
- `timeframe`, `timezone`, `calculations`, `initial` variables, `windows`,
  `schedules` and `rules` work as before.
- Expressions may carry `symbol` to read a specific symbol (`close` of `KO`).
  Without it, a metric reads the rule's current symbol.
- A rule with `for_each: true` runs once per symbol. A rule without it runs
  once. Its metrics must name a symbol, unless the program has one symbol.
- Operators:
  - **Scalar:** `+ - * / min max`, comparisons, `and or not abs`, `sign log
    exp sqrt`, `clamp(x, lo, hi)`, `if(c, a, b)`, `crosses_above/below`.
  - **Cross-sectional:** `cs_rank`, `cs_zscore`, `cs_demean`, `cs_count`,
    `top(x, n)`, `bottom(x, n)`, `quantile(x, q)`. They use one total order
    (value, then symbol), so `top` and `bottom` never share a symbol. A symbol
    with missing data is left out of that moment's cross-section.
- Metrics:
  - OHLCV and the indicators `sma ema stddev volatility rsi atr return
    highest_high lowest_low`.
  - Rolling `correlation` and `beta` against another symbol.
  - `feature`; account metrics `price bid ask position_qty entry_count
    equity`; clock metrics; session windows.
- Actions: `enter`, `flatten`, `cancel`, `set`, and `target`.

`bar.close` rules run once per moment, after every symbol's candles for that
moment are in and before the next quotes. The tape carries a `rules.evaluate`
event for this.

## `target`: hold a portfolio

`weight` is a per-symbol signed score. `construct` turns the scores into a
book:

- **normalize:**
  - `legs` (default) scales longs to `(gross + net)/2` and shorts to
    `(gross − net)/2`.
  - `gross` scales the sum of |w| to `gross`.
  - `none` uses the scores as weights.
- **Caps:** `max_position` caps |w|; `max_gross` (at most 2) caps the total.
- **Turnover band:** `min_trade_pct` skips trades smaller than that percent of
  equity.

Orders go in this sequence: closes and reductions first, then new shorts, then
buys. A weight that crosses zero is split into a close and an open. If a
symbol's weight or quote is unavailable, the symbol keeps its position. A
program has at most one `target` action and cannot mix it with `enter`.

## Simulator: spot shorts

Spot shorts are opt-in per run (`shorts`). Without that setting, spot
positions stay long-only and cash-funded.

- A sell beyond the holding opens a short, and the sale proceeds go to cash.
- **Borrow:** 25 bps a year by default, with per-symbol overrides. It accrues
  continuously on |short value| and is reported as `borrow_cost`.
- **Reg T margin:**
  - 50% initial: orders that would breach it are rejected.
  - 30% maintenance: a breach submits reduce-only covers, back to halfway
    between the maintenance and initial levels, logged as margin calls.
- An `unshortable` list holds those symbols flat. Borrow availability is
  otherwise not modelled.
- Risk limits apply to any order that grows |position|.
- **Dividends:** the simulator has no dividend events. Use total-return
  adjusted bars for shorts; runs holding shorts on other adjustments warn.
- **Reported metrics:** long and short exposure, net exposure, long-leg and
  short-leg P&L, borrow cost, margin calls, margin requirement and the maximum
  margin used.

## Live

A live assignment runs the same evaluator on each due bar:

1. Read the portfolio from Trading: cash, positions, marks and profiles.
2. Load the program's symbols' completed bars at its timeframe, enough for
   the warmup.
3. Replay them through the rule runtime, then evaluate the latest closed
   moment with the portfolio as account state. Variables and fired triggers
   persist per assignment.
4. Execute:
   - `target` weights go through the existing rebalance path: sells first,
     revision-chained orders, and a pending rebalance carried across ticks.
   - `enter` and `flatten` become Trading market orders.

Limits until Trading supports them:

- Short weights and short entries are refused live.
- Protective stops, targets and trailing stops are refused at assignment.
- Feature metrics are unavailable live.

## Removed

- The allocation engine (`indicators` / `when` / `rank` / `allocate`
  definitions) and its step-based backtest runner. Catalog presets become
  rule programs.
- The trading-rules/1 single-`symbol` field and the v1/v2 split.

Nobody runs Strategies in production yet, so there is no migration. Stored
allocation strategies fail validation and must be recreated as rule programs.

## Scale

Candles are rebuilt from the tape and are not written to every step's
checkpoint. Resolved orders older than two days are pruned from the
simulator checkpoint. The cap is 500 symbols × 2,500 daily bars per run, and
a scale test measures it.

## Tests

- Cross-sectional operators, ties and missing-data exclusion; scalar operators.
- `target` construction: dollar-neutral spread P&L equals long leg plus short
  leg, net of fees and borrow; caps; turnover band.
- Shorts: proceeds to cash, zero crossing, borrow accrual and per-symbol
  overrides, initial margin, maintenance-margin forced cover.
- Pairs round trip; correlation and beta.
- Checkpoint resume gives identical outputs; warmup carries into the run.
- Live: a target program rebalances a Trading portfolio through the fake;
  short and protective programs are refused at assignment.
- Scale: 500 symbols × 2,500 daily bars.
