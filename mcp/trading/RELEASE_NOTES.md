# Trading v0.11.0

Trading adds validation suites to the shared strategy and agent event simulator.
A captured backtest can now be evaluated across held-out windows, parameter
candidates, and execution scenarios, with persisted progress in the dashboard.

## Changes

- Out-of-sample validation selects candidates on a chronological training window
  and reports performance separately on its held-out test window.
- Rolling or expanding walk-forward validation repeats training-only selection
  across disjoint test windows, with past-only indicator warmup.
- Robustness tests compare strategy parameter grids and execution scenarios.
- Stress tests exercise higher costs, delayed execution and features, reduced
  liquidity, and market-price shocks.
- Seeded Monte Carlo samples execution costs and latency on the captured market
  path, reporting percentiles, drawdowns, and threshold frequencies.
- Validation UI and four MCP tools expose creation, progress, pause/resume/cancel,
  reports, and downloadable suite artifacts with replayable child runs.
- Each case has isolated capital, portfolio state and agent memory. Durable
  checkpoints, inference budgets, restart recovery, and project scope apply.

This release includes v0.10's event-driven strategy/agent simulations, generic
prices/news/sentiment inputs, recorded agent replay, order latency and partial
fills, transaction costs, portfolio risk, benchmarks, and broker hardening.

## Upgrade notes and limits

Migration 021 adds validation suite storage. Create fresh source backtests after
upgrading: simulation artifacts require their matching engine source fingerprint.
The dashboard includes **Validation** under Backtests. Existing running installs
must be upgraded separately after this release is published.

Monte Carlo models execution uncertainty, not generated future market paths.
Walk-forward links independent test-window returns, not a continuously held
portfolio. Stress news remains captured content, not a coherent market/news
scenario generator. Agent suites can incur model costs; exact reproduction uses
recorded decisions. Broker and inference transports are covered by mocked tests;
no real broker order or model inference is part of release validation.

See [Validation suites](VALIDATION.md) for configurations, window semantics,
limits, and API examples, and [Event backtesting](EVENT_BACKTESTING.md) for the
shared strategy/agent simulator.

## Release verification

- Full Go short suite, targeted validation/agent race tests, all 11 UI tests,
  strict TypeScript checks, and both production UI builds passed.
- The release binary captured August 2026 AAPL prices from Yahoo Finance and
  BTC/USDT prices from Binance in an isolated local database.
- All five validation methods completed for both assets: 10 suites and 264 child
  runs, including 100 Monte Carlo samples per asset plus baseline cases.
- An exported Monte Carlo child from each asset replayed with an identical
  result hash. These checks used deterministic fixed-allocation strategies,
  not model inference or broker orders.
