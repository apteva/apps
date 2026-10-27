# Strategies

Strategies v0.1 owns automated strategies and research. It is a project-scoped
Apteva app with its own database and a required integration with Trading >=0.16.

- Versioned allocation strategies and generic trading rule programs.
- Strategy evaluation, assignments, scheduling and promotion scorecards.
- Event backtests, agent simulations, validation suites and portable reports.
- Sourced CSV market-data import and strategy performance attribution.
- A Strategies / Backtests panel and the live-strategy performance widget.

Trading owns all authoritative portfolios, balances, positions and orders.
Strategies reads portfolio observations and market data through the Trading
integration, then submits automated orders through Trading's `order_place`.
It has no broker connections, broker credentials, paper-fill worker or
portfolio-management routes. Project agents can use Trading directly without
installing Strategies.

## Execution boundary

Research has private portfolio/market snapshots to capture reproducible inputs
and compute strategy attribution. These are observations, not another broker
account or execution ledger. No order is submitted when a fresh Trading
observation cannot be obtained. Trading independently enforces its current
risk, cash, venue and live-arming rules on every submission.

Exact order submissions are persisted before the cross-app call and retried
with a stable key. Trading namespaces those keys by the authenticated caller
installation and records the originating strategy/version and decision.

Allocation strategies can be assigned for automated execution. Generic rule
programs retain v0.15's research-only restriction; splitting apps does not add
protected live execution or establish that a strategy is profitable.

Agent replay uses isolated Strategies environments and replay-only order
intents. The normal strategy worker is disabled in those environments.

## Install and migration

Install Trading first, then Strategies, and select Trading in the integration
binding. The manifest declares the required app dependency for automatic
resolution. Existing users should follow [MIGRATION.md](MIGRATION.md).

Detailed research documentation:

- [Rule engine](RULE_ENGINE.md)
- [Indicators and allocation strategies](INDICATOR_STRATEGIES.md)
- [Event backtesting](EVENT_BACKTESTING.md)
- [Validation suites](VALIDATION.md)

## Development

```sh
env GOWORK=off go build .
env GOWORK=off go test -short ./...
env GOWORK=off go test -tags integration -run TestSeparateAppsStrategyExecutesThroughTrading -v .
bun run ../../scripts/build-panels.ts --app strategies
```

The integration test runs separate Trading and Strategies binaries with separate
SQLite databases and checks submission, fills, attribution and portfolio reads.
The module builds independently; no relative Go-module replacement is needed.
