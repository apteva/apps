# Trading

Trading v0.16 owns portfolio management and execution. Humans, project agents,
and connected apps use the same order and risk pipeline. Agents do not need
Strategies to place trades or inspect performance.

- Portfolios, cash, balances, positions, orders, cancellations, fills and journals.
- Paper execution, broker-paper and broker-live execution, broker reconciliation.
- Market data, reference data, venue constraints, fees and execution costs.
- Portfolio risk limits, tradable universes, objectives, pauses and live arming.
- Portfolio performance and order origin/decision attribution.

Automated strategy authoring, scheduling, backtests, validation and strategy P&L
now belong to [Strategies](../strategies/README.md). Strategies depends on
Trading; Trading has no dependency on Strategies.

## Direct agent usage

Use `portfolio_list` / `portfolio_get`, `market_quote` / `market_history`,
`order_place` / `order_cancel`, `positions_list`, `orders_list`,
`account_summary` and `journal_write` / `journal_read`. Orders require a rationale
and pass the same portfolio risk, cash, venue and live-arming checks regardless
of their source. Stable `idempotency_key` values make retries safe.

App-submitted orders also record the authenticated origin app/install and an
optional `decision_ref`, `external_strategy_id` and `external_strategy_version`.
Those external identifiers are attribution, not a local saved strategy or an
exemption from Trading's execution rules.

For authenticated paper experiments with independent risk approval, durable
stop/target/time exits and a learning scorecard, see [Agent paper desk](PAPER_DESK.md).

REST remains mounted under `/api/apps/trading/*`. The dashboard retains
Portfolios, Trade, Positions, Risk, Agents, Brokers and Journal, plus the
portfolio watch widget and standalone desk.

## Public crypto data

The live public feed uses Coinbase Exchange's exact `BASE-USD` products for
quotes and candles. Product metadata must confirm the base asset, USD quote
currency and online status. Prices never fall back to USDT pairs. Quotes expose
the provider symbol, venue, trade timestamp, bid/ask snapshot and receipt time;
24-hour volume and candle volume are measured in the base asset.

Existing Binance/USDT marks and instrument metadata are replaced when the USD
feed refreshes. Simulation rejects legacy Binance stablecoin references for
USD symbols. Broker adapters retain their own venue-specific wire symbols.

## Upgrading from v0.15

The database upgrade pauses legacy strategy assignments. Trading no longer runs
the strategy scheduler or exposes strategy/backtest/validation authoring routes.
Existing portfolios, broker bindings, orders, fills, performance and research
rows are preserved. Legacy schema and private compatibility helpers remain so
an upgrade does not require deleting or rewriting the existing ledger.

Install Strategies and bind this Trading installation, then use **Import legacy
research** in Strategies, or its `legacy_import` tool. Imported assignments stay
paused; explicitly assign a strategy again when it should run. See the complete
[migration procedure](../strategies/MIGRATION.md).

## Development

```sh
env GOWORK=off go build .
env GOWORK=off go test -short ./...
env GOWORK=off go test -tags integration -run 'TestSidecar_(OrderFillRoundTrip|LimitDoesNotFillUnlessCrossed|PolymarketYesFlow)' .
bun run ../../scripts/build-panels.ts --app trading
```

The source installation builds this directory independently with the pinned
App SDK. No sibling app module or shared database is required.
