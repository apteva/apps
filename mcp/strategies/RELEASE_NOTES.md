# Strategies v0.3.2

- `strategy_catalog` presets with shorts list only `{"shorts": ...}` under
  `simulation`, not every simulator field at zero.
- The adjustment warning in backtest execution notes reads "price adjustment
  is provider_adjusted, not total_return" instead of
  "provider_adjusted-adjusted".
- Validated rule actions no longer echo an empty `sizing` object on
  `target` actions.

# Strategies v0.3.1

- The manifest describes the single rules engine. The app description and
  the `strategy_catalog`, `strategy_create`, `strategy_evaluate`,
  `strategy_assign` and `strategy_backtest_create` summaries in 0.3.0 still
  referred to allocation strategies and research-only rule programs. No code
  changes.

# Strategies v0.3.0

One rules engine for backtests, validation and live runs. Every strategy is
now a `trading-rules/1` program; the allocation engine is gone. Requires
Trading 0.20.0.

- **One format.** A definition is `{"program": {...}}`. A program trades
  1–500 `symbols`; its symbols and timeframe are the strategy's universe and
  cadence. Rules can name a symbol, run `for_each` symbol, or read
  cross-sectional operators (`cs_rank`, `cs_zscore`, `cs_demean`, `cs_count`,
  `top`, `bottom`, `quantile`). New metrics: `correlation`, `beta`,
  `day_of_month`, `month`. New operators: `sign`, `log`, `exp`, `sqrt`,
  `clamp`, `if`.
- **Target books.** The `target` action holds a signed weight per symbol,
  normalized as long/short legs, to a gross, or as given, with per-symbol
  and gross caps and a turnover band. It replaces allocation rules.
- **Shorts in backtests.** `simulation.shorts` enables spot shorts. Short
  proceeds go to cash and borrow accrues (25 bps a year by default, with
  per-symbol overrides). Reg T margin is 50% initial and 30% maintenance,
  with forced covers. Short metrics come separately: long/short exposure,
  long/short P&L, borrow cost and margin calls.
- **Presets** for the research strategies: trend rotation, forecast decile
  spread, volatility-scaled time-series momentum, factor momentum, news
  sentiment and KO/PEP pairs. Long/short presets carry the shorts
  configuration they assume.
- **Live runs use the same engine.** Each closed bar is evaluated with the
  portfolio's cash, positions and marks. Variables and latches persist per
  assignment (migration 030, `strategy_rule_state`). Live runs still refuse:
  - shorts and protective stops, targets and trails;
  - limit and stop entries;
  - features;
  - triggers other than `bar.close`.

  Validation flags these programs "Backtest only".
- **Captured backtests for any program.** The captured backtest path builds
  the rule tape from captured history at the program timeframe. Bars for
  symbols the program doesn't trade are dropped. US-equity daily bars complete
  at the session close.
- **Fixes:**
  - The validation crash scenario now scales the whole OHLC bar.
  - The feature-delay scenario moves only feature events.
  - Resumed validation runs keep their warmup.
- **Scale.** A 500-symbol × 2,500-bar cross-sectional backtest runs in about
  17 s. Candles aren't checkpointed: they're rebuilt from the tape. Resolved
  orders are pruned.

Breaking: definitions in the old allocation format (`universe`, `rules` with
`when`/`rank`/`allocate`, `risk`, `rebalance_every`) and `engine: rules`
wrappers no longer validate. Recreate them as programs. The catalog presets
cover the old templates.

# Strategies v0.2.2

- Remove three symlinked scenario aliases (`scenarios/05`–`07`) left by the
  split. They duplicated files in `scenarios/strategies/`, and the source
  installer refuses packages that contain links, so no Strategies release
  could install on current servers.

# Strategies v0.2.1

- Migration 029 drops the 19 Trading tables 0.1.x copied, the rest of the
  pre-split Trading schema that came with them (journal, alerts, corporate
  actions, objectives and the like) and the old `trading_observation_*`
  bookkeeping. Strategies' schema now holds only its own tables.
- `backtest_market_bars` keeps `security_id` as plain text; its foreign key to
  the dropped `securities` table goes.
- Install 0.2.1 only after 0.2.0 has run on that install.

# Strategies v0.2.0

Strategies reads Trading instead of copying it. Requires Trading 0.20.0.

- Stop copying 19 Trading tables before research calls and automated orders.
  Strategies keeps only its own data: strategies, assignments, run events,
  scorecards, backtests, validation suites and the inputs frozen into each
  run.
- Live ticks read one `portfolio_state` per assignment and size orders from
  it. Orders go out with `if_revision`; if the account moved, Trading
  rejects with `stale_state` and Strategies re-reads and re-sizes, up to two
  times. If Trading is unreachable, the tick fails closed with a
  `trading_unavailable` run event and backs off.
- Backtests and validations build their replay policy and reference manifest
  from `portfolio_state` and `research_reference`. Universe checks use
  Trading's `portfolio_policy_check`.
- Strategy P&L is kept incrementally from Trading's `fills_feed`, with a
  cursor per project that advances in the same transaction as the lots. The
  book is rebuilt from the feed on first read after the upgrade, and again
  after a legacy import so Trading-era strategy fills are counted.
- Manual and agent fills no longer appear as an unattributed row in strategy
  P&L; `include_unattributed` is gone from `/strategies/live`. The
  portfolio-level figure stays in Trading.
- The "Trading binding changed" guard reads a single `trading_binding` row,
  seeded from the old observation sources.
- Migration 028 rebuilds Strategies' own tables without foreign keys to the
  copied `portfolios` table and recreates `strategy_position_accounting`
  empty. The copied tables stay in place, unused, until 0.2.1.
- Legacy import no longer writes Trading securities; imported market bars keep
  their symbol and drop `security_id`.
- Remove the local portfolio, position, order, journal and market read tools
  (they already forwarded to Trading) and their store helpers.
- The simulation fingerprint now covers only the files that decide a result.
  Live data-access files (`exec.go`, `integrity.go`, `pricing.go`) leave the
  hash; the pure policy, cost and symbol rules move to the hashed
  `simulation_kernel.go`. The hash changes once in this release: paused or
  running simulations and validation suites from 0.1.x cannot resume, and
  exported 0.1.x artifacts no longer import. Check that none are paused or
  running before upgrading.

# Strategies v0.1.5

- Remove unreachable Trading execution and pricing code carried over by the
  split. Test fixtures that replay old Trading behaviour keep their helpers in
  test-only files.
- Add `trading_contract.json`, the list of Trading tools and `order_place`
  fields Strategies depends on, with a test that fails when the source calls
  anything outside it.
- No hashed engine source changed, so the event-simulation fingerprint is
  unchanged: paused simulations, validation suites and exported artifacts
  still resume and import.

# Strategies v0.1.4

- Request symbol-scoped Trading observations and apply a complete revision in one transaction.
- Back off oversized responses from older Trading releases during rollout.

# Strategies v0.1.3

- Persist Trading snapshot failures separately from the evaluation schedule.
  Transient failures back off from 30 seconds to 15 minutes.
- Block an oversized snapshot request until its arguments or protocol version
  change. A fresh snapshot clears the failure and allows order submission.
- Pin the App SDK to v0.115.1.

# Strategies v0.1.2

- Fetch Trading portfolio snapshots when a strategy is due for a cadence check
  or has a pending rebalance.
- Skip unchanged snapshot rows and keep security IDs out of metadata updates
  so SQLite avoids scanning backtest bars for foreign-key checks.
- Pin the App SDK to v0.115.0.

# Trading v0.14.0

Realized and open P&L is now attributed to the strategy that traded it, and two dashboard widgets read that attribution.

- Add a strategy-own average-cost lot book (`strategy_position_accounting`). A strategy closes against its own lots rather than the portfolio's blended average, so two strategies holding the same symbol no longer report each other's cost basis. Where a symbol has a single owner the strategy and portfolio books agree exactly.
- Attribute manual, agent and imported broker fills to strategy 0, so the strategy books plus that bucket reconcile against the portfolio book.
- Rebuild the strategy book from the append-only fills ledger on mount and after broker imports, recovering history from `orders.strategy_id` instead of starting at zero. The rebuild is a full, idempotent replay and matches incremental accrual exactly.
- Add `GET /strategies/live`, a per-(strategy, portfolio) rollup of realized, open and total P&L, execution costs, open lots marked at current prices, run health, and a shared-symbol count.
- Add the `portfolio-watch` and `strategy-live` dashboard widgets. Live and armed portfolios are badged and tinted; a strategy sharing a symbol with another book carries a note that its P&L follows the strategy-own close convention.

Migration 022 adds one table and two indexes. No new permissions. The rebuild cannot recover imported opening balances or corporate actions, which never pass through fills; those stay in the portfolio book and appear as the gap between it and the strategy books.

# Trading v0.13.1

- Quantize automated strategy buys and sells to each symbol's effective venue lot size, including after cash-budget scaling. This prevents valid allocations such as BNB from repeatedly failing the execution quantity check.
- Regression coverage exercises accepted and filled buys, sells, and cash-constrained orders through the virtual execution pipeline.

No migrations or new permissions. Fixed strategy definitions are unchanged.

# Trading v0.13.0

Strategies can now size eligible assets by inverse volatility, allowing the fixed SMA300 crypto strategy to run in virtual portfolios with the same weights used in research.

- Add explicit inverse-volatility weights, validated lookback and volatility floor, required warmup, and position caps that preserve excess cash.
- Fix decimal-lot rounding in event simulations so floating-point residue cannot leave a whole permitted lot unfilled and block later rebalancing.
- Cover inverse-volatility ratios, flat-series floors, caps, invalid parameters, warmup, and decimal buys/sells/partial fills.

No database migrations or new permissions. Existing strategies retain their definitions. Historical artifacts still require their original engine fingerprint; create fresh backtests on this release. The adaptive research controller remains outside this production release.

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
