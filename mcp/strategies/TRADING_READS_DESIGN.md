# Design: Strategies reads Trading instead of copying it

Status: proposal, 2026-11-28. Builds on Trading v0.19.0 / Strategies v0.1.5
(branch `codex/finish-strategies-split`).

## Goal

Trading is the only store of account state: portfolios, positions, orders,
fills, marks and policies. Strategies keeps only what it owns: strategies,
assignments, run events, scorecards, backtests, validation suites, and the
frozen inputs captured into each backtest. Every other Trading fact is read
when needed and never persisted.

Non-goals: changing strategy semantics, backtest results or the order gate's
rules; moving any strategy knowledge into Trading.

## What exists today

- Before most research calls and every automated order, `refreshTradingPortfolio`
  (`app_split.go`) pulls `portfolio_research_snapshot` and overwrites 19 local
  tables with it. `dbRebuildStrategyAttribution` then replays every copied
  fill to rebuild per-strategy P&L.
- The snapshot ships the portfolio's full `orders`, `fills`, `execution_costs`
  and `position_accounting` history every time
  (`trading/portfolio_observation.go:280`). That unbounded payload caused the
  paging, size-cap and retry work in 0.1.2–0.1.4.
- About 30 Strategies functions read the copied tables. They fall into five
  groups:

| Group | Readers | Reached from |
|---|---|---|
| Live tick and order sizing | `computeEquity`, `dbListPositions`, `dbGetMark`, `resolveVenueProfile`, `dbPortfolioExecutionSettings`, `dbGetOrder`, `dbListOrdersInternal`, `dbGetPortfolio` | `strategy_tick` worker → `evaluateLiveStrategyAssignments` → `placeStrategyPaperOrders` |
| Automated order gate | `dbGetPortfolio` (execution environment), local scorecard | `toolOrderPlace` |
| Backtest / validation setup | `captureReplayPolicy` (risk policy, venue profiles, instruments), `referenceManifest`, `referenceDataStatus`, `storedUSEquitySessionAt`, `dbReplaceBacktestMarketBars` (listings) | `strategy_backtest_create`, `agent_backtest_create`, `validation_create`, `strategy_validate_backtest`, HTTP backtests |
| Universe checks | `dbGetPortfolioUniversePolicy`, `referenceUniverseContainsSymbol`, `activeListingExists` | `strategy_assign`, backtest create, live tick |
| Strategy P&L | `dbRebuildStrategyAttribution` (fills JOIN orders), `dbListStrategyAccounting` (JOIN portfolios), `dbListPortfolioMarks` | `/strategies/live`, `StrategyLiveWidget` |

- Strategies' own `toolPortfolioGet`, `toolPositionsList`, `toolAccountSummary`,
  `toolOrdersList`, `toolJournalRead` and `toolMarketQuote` are already dead:
  `MCPTools` forwards those calls to Trading. Together with their store
  helpers (`snapshotPortfolioWithMarks`, `dbWatchlist`, day baselines,
  `dbFundingPaid`, …) they can be deleted outright.
- `simulationSourceHash` hashes 19 engine files, including `exec.go`,
  `integrity.go`, `pricing.go` and `strategy.go`. Any change to them stops
  paused simulations and validation suites from resuming, and stops exported
  artifacts from importing.
- In tests, `fixtureTrading` plays Trading by reading the same local tables,
  and 42 calls to `mustCreatePortfolio` write portfolios straight into them.
- Prod (Strategies install 331): 2 active live assignments, 112 completed
  backtests, nothing paused, no validation suites.

## Target design

### Trading: four app-only reads (additive, Trading 0.20.0)

Each read is caller-scoped by the calling app's identity and project, not
by a Strategies-specific check. That replaces `requireStrategiesCaller` with
the generic rule "an app may read state for portfolios in its project and
fills of orders it originated".

1. **`portfolio_state`** `{portfolio_id, symbols[]}`. Returns typed JSON from
   one read-only transaction:
   - portfolio: status, mode, execution environment, cash, execution settings
   - equity and day baseline
   - open positions
   - working orders
   - marks, instruments and venue profiles for `symbols` ∪ held symbols
   - risk policy and universe policy
   - `revision`, from `accountRevision`

   No order or fill history. Size is bounded by positions plus symbols.
   Replaces the account half of `portfolio_research_snapshot`.
2. **`portfolio_policy_check`** `{portfolio_id, symbols[], as_of}`. Returns the
   universe and listing violations per symbol, using Trading's own
   `portfolioUniverseViolation`. Strategies stops re-implementing the
   reference-universe rules.
3. **`research_reference`** `{symbols[], start, end, adjustment}`, paged.
   Returns securities, listings, exchange sessions, corporate actions and
   universe memberships for a backtest window. It is used only when a
   backtest or validation is created, and the result is frozen into that
   run's spec.
4. **`fills_feed`** `{after_fill_id, limit}`. Returns fills of orders whose
   `origin_app` is the caller, with symbol, side, outcome, quantity, price,
   fee, spread/slippage cost and the order's `external_strategy_id` /
   `external_strategy_version`. Keyset-paged and append-only.

Optional, recommended: **`order_place.if_revision`**. If the account revision
has moved since the `portfolio_state` the order was sized from, Trading
rejects with `stale_state`. Today `tradingObservationMu` plus the
snapshot-before-order give that guarantee. Without `if_revision`, two quick
fills between read and submit could size an order against stale cash.

`portfolio_research_snapshot` stays for one release overlap. Trading 0.21.0
removes it once every install runs Strategies ≥ 0.2.0.

### Strategies

- **One reader seam.** A `tradingReader` interface has `State`, `PolicyCheck`,
  `Reference` and `FillsSince` methods. Production calls `CallAppResult`; tests
  use a fake. Engine code takes the returned structs, not `*sql.DB`, for
  Trading facts.
- **Live tick.** Each due assignment makes one `portfolio_state` call. Sizing
  (equity, positions, marks, lot rules) runs on that struct. Orders go out
  with `if_revision`. If the read fails or times out, the tick fails closed:
  it skips the assignment and records a `trading_unavailable` run event, the
  same as today's deferred snapshot. A `stale_state` rejection retries on the
  next tick.
- **Order gate.** `toolOrderPlace` reads the execution environment from
  `portfolio_state`. The scorecard check stays local because it is
  Strategies' own data.
- **Backtest / validation setup.** `captureReplayPolicy` and the reference
  manifest are built from `portfolio_state` and `research_reference`, then
  stored in the run spec as today. Backtests that already exist are untouched,
  since their inputs are already frozen.
- **Strategy P&L.** `strategy_position_accounting` is kept incrementally
  from `fills_feed`, with a `trading_fill_cursor` row per Trading install.
  The worker advances the cursor; a rebuild resets it and replays. Bucket 0
  (manual and agent fills, used to reconcile against the portfolio book) goes
  away, because Strategies only sees fills of its own orders. The widget
  takes portfolio names from `portfolio_list` and marks from
  `portfolio_state`.
- **Binding guard.** The "Trading binding changed" check moves from
  `trading_observation_sources` to a single `trading_binding` row.
- **Deletions.**
  - `refreshTradingPortfolio`, `trading_observation*.go`, the
    `writeResearchRows` path for copied tables
  - the dead local Trading tools and their store helpers
  - `dbRebuildStrategyAttribution`'s full replay
- **Engine fingerprint.** In the same release, narrow `simulationSources` to
  the pure simulation files: `internal/backtest`, `internal/ruleengine`,
  `event_backtest.go`, `agent_event_backtest.go`, `strategy.go` evaluation,
  `strategy_rules.go`, `strategy_replay.go`, `strategy_indicators.go`,
  `validation_runtime.go` and the rule examples. Live data-access files
  (`exec.go`, `integrity.go`, `pricing.go`) leave the hash. The hash changes
  once, here, and later data-layer work stops invalidating checkpoints. The
  upgrade preflight refuses if any simulation or validation suite is paused
  or running. Prod has none today.

### Tests

`fixtureTrading` keeps its role but serves the four new tools from a
separate in-test fixture DB. A test-only copy of Trading's account schema
lives there, so `mustCreatePortfolio` and its 42 callers write to the fixture
DB instead of Strategies' DB. The 113 Strategies tests keep their scenarios.
Contract updates:

- `trading_contract.json` gains the four tools and `if_revision`.
- `portfolio_research_snapshot` leaves the contract in Strategies 0.2.0.

New tests:

- the live tick sizes from one state read
- a stale revision is rejected and retried
- a Trading outage fails closed
- the fill cursor is idempotent across restarts and replays
- a migration test proves the copied tables are unused before 0.2.1 drops them

## Release sequence

| Step | Release | Change | Gate |
|---|---|---|---|
| 1 | Trading 0.20.0 | Add the four reads and `if_revision`; additive | Contract test passes; existing Strategies 0.1.5 unaffected |
| 2 | Strategies 0.2.0 (`requires trading >=0.20.0`) | Switch every read; incremental P&L; narrow the hash; copied tables left in place but unused | No paused/running simulations; both prod live assignments tick and place orders in staging first |
| 3 | Strategies 0.2.1 | Migration drops the 19 copied tables and `trading_observation_*` | 0.2.0 has run on every install |
| 4 | Trading 0.21.0 | Remove `portfolio_research_snapshot` | Every install on Strategies ≥ 0.2.0 |

The legacy export (`research_legacy_export`) is independent and stays until
local dev install 106 and any other un-imported install has imported.

## Size

- Trading: about 500 added lines (four tools, `if_revision`, tests).
- Strategies: about 3,000–4,000 lines removed (copy sync, dead tools, store
  helpers, full-replay attribution) and about 1,000 added (reader, typed
  state, fill feed, fake Trading).
- The largest risk is `placeStrategyPaperOrders` and the live tick. They move
  money, so they get the most new tests and a staging run with real
  assignments before prod.

## Decisions needed

1. **`if_revision` on `order_place`.** Recommended. Without it, live sizing
   loses the consistency the snapshot lock gives today.
2. **One-time engine fingerprint break.** It costs nothing on prod now (nothing
   paused). Old exported backtest artifacts would stop importing.
3. **Drop the bucket-0 reconciliation line** from strategy P&L. Strategies
   would no longer see manual or agent fills. The portfolio-level number
   stays in Trading.
