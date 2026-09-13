# Trading v0.12.0

Trading now provides explicit technical indicators, composable strategy conditions,
and five multi-symbol templates in the strategy editor and API. Backtest results
also explain how captured quote frequency affects simulated execution timing.

## Changes

- SMA-seeded EMA, Wilder RSI, MACD/signal/histogram, Bollinger bands, and z-score
  indicators with documented initialization and completed-candle semantics.
  Legacy EMA and RSI formulas remain compatible with saved strategies.
- Nested all/any conditions, closed-bar crossovers, per-symbol ranking filters,
  ascending/descending ranking, and total allocation budgets.
- A strategy catalog available through HTTP and MCP, with five editable hourly
  templates: EMA trend, MACD confirmation, RSI pullback, Bollinger reversion,
  and experimental Bollinger relative strength from the stock/crypto search.
- Execution-resolution notes in simulation summaries and exported artifacts.
  An explicit next-open timing control clears latency while preserving costs.
- Formula, live/replay parity, feature availability, checkpoint, catalog, and UI
  coverage, plus opt-in reproducible indicator and strategy-search harnesses.
- App SDK updated to v0.81.0, the latest tag by commit ancestry at release time.

Includes the existing event-driven strategy/agent simulator, generic news and
sentiment inputs, recorded agent replay, order/portfolio modeling, broker
integrations, benchmarks, reproducible artifacts, and out-of-sample,
walk-forward, robustness, stress, and seeded Monte Carlo validation suites.

## Upgrade notes and limits

Create fresh source backtests after upgrading. Artifacts require the exact engine
source fingerprint that created them; preserve the matching older release or
research binary for older artifacts. This release adds no database migration.
Existing running installations must be upgraded separately after publication.

Templates are research candidates. The relative-strength candidate's results
varied by period and market; it has no established durable edge. Conditions are
target allocation filters, not persistent entry/exit latches. ATR/ADX, volume
indicators, and ATR stops are not included. Recursive indicators use fixed bounded
history for live/replay parity and may differ slightly from long-seeded charts.

Hourly bar-open quotes cannot resolve millisecond fills: positive latency can
wait until the next available quote, including overnight. Zero latency is an
idealized next-open approximation. Execution behavior is unchanged; use finer
captured quotes to study latency. Monte Carlo samples execution uncertainty on
the captured path, not synthetic future market paths.

See [Indicator strategies](INDICATOR_STRATEGIES.md),
[Validation suites](VALIDATION.md), and [Event backtesting](EVENT_BACKTESTING.md)
for formulas, examples and simulation semantics.

## Release verification

- Full Go short suite, targeted indicator/strategy/validation/agent race tests,
  all 13 UI tests, strict TypeScript checks, and production panel/desk builds passed.
- The release binary ran the experimental indicator template on hourly August
  2026 AAPL/MSFT and BTC/ETH data in an isolated database.
- All five validation methods passed for both markets: 10 suites, 316 child runs,
  including 100 Monte Carlo samples plus a baseline for each market.
- Exported Monte Carlo children from both markets replayed with identical result
  hashes. The crypto suite also resumed successfully from its saved checkpoint
  after the smoke harness's initial time limit stopped the process.
- Research-tagged harnesses compile; their long searches remain opt-in. No real
  broker orders or model inference were part of release validation.
