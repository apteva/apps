# Trading v0.20.0

Apps that automate trading can read the account state they need instead of
copying Trading's tables. Each read is scoped to the calling app's project,
and fills are limited to orders the calling app placed.

- Add four app-only tools:
  - `portfolio_state`: cash, equity, day baseline, positions, working orders,
    marks, instruments, venue profiles, risk and universe policy, universe
    violations, the account revision and the Trading install ID for one
    portfolio. No history.
  - `portfolio_policy_check`: universe and listing violations for symbols at
    a point in time.
  - `research_reference`: paged exchange sessions, corporate actions,
    listings and universe memberships for a research window.
  - `fills_feed`: keyset-paged fills of the calling app's orders, with fees,
    spread and slippage cost and the order's external strategy tags.
    Strategies can pass `include_legacy` to also receive fills of Trading-era
    strategy orders, so imported strategies keep their P&L.
- `order_place` takes an optional `if_revision`. If the account revision has
  moved, the order is rejected with `stale_state`. Accepted orders return the
  revision right after their insert, so a caller can chain orders sized from
  one state read.
- Migration 026 bumps the account revision when an order is placed or its
  status or filled quantity changes.
- `portfolio_research_snapshot` stays for one release; it is removed once
  every install runs Strategies 0.2.0 or later.

# Trading v0.19.0

Trading no longer ships the strategy research engine. Strategies has owned
strategies, backtests and validation since the split; Trading only kept a
hidden copy of that code.

- Remove the unreachable strategy, backtest, validation and rule-engine code,
  its tests and CLIs, and the hidden strategy and backtest screens in the
  Trading panel. About 23,000 lines removed.
- Remove the legacy strategy scorecard gate and per-strategy P&L attribution
  from `order_place` and the fill path. `order_place` never advertised
  `strategy_id`; Strategies gates its own orders and tags them
  `external_strategy_id`.
- A working order that still carries legacy Trading strategy provenance now
  fails closed instead of being re-checked against legacy tables.
- Keep the Strategies boundary: `portfolio_research_snapshot`,
  `market_bars_capture`, `backtest_market_step` and `research_legacy_export`.
  Legacy research tables stay until every install has imported into
  Strategies. A contract test checks every tool and `order_place` field
  listed in Strategies' `trading_contract.json`.

No migrations, no new permissions, no changes to public tools.

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

See [Indicator strategies](../strategies/INDICATOR_STRATEGIES.md),
[Validation suites](../strategies/VALIDATION.md), and [Event backtesting](../strategies/EVENT_BACKTESTING.md)
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
