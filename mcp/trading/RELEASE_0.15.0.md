# Trading 0.15.0

Trading can now describe individual trades as portable, generic rule programs.
Completed OHLC bars, multi-timeframe indicators, named calculations, persistent
state and reset policies drive market/limit/stop entries and protected exits.
The same simulator handles fixed monetary stop risk, explicit linear contracts,
long/short positions, native OCO groups and activated trailing stops.

The visual editor and MCP catalog expose the same definition format. Agents can
validate, create and version programs, run backtests, persist scorecards, evaluate
cost robustness and export reproducible artifacts. Earlier evidence keeps the
strategy version and policy actually evaluated. Allocation strategies and
existing per-strategy P&L attribution remain available.

## Three strategy examples

The Donchian, ATR candle and session-range examples from the supplied screenshots
are ordinary JSON programs. Nine controlled cases cover both trade directions,
stops/targets, trailing activation and midpoint rearming, OCO, daily entry limits,
18:00 liquidation and cancellation. Every case independently reproduces the
complete input/output hashes through the offline runner. Synthetic cases prove
mechanics, not an investment return.

A full-year 2025 BTCUSDT/ETHUSDT study tested all three examples under two cost
settings using archived Binance minute candles. All twelve runs lost money after
the assumed costs. Execution used sampled minute-open prices, which can miss
intraminute stops and trailing events. Perpetual funding was excluded. These
results do not establish profitability on XAUUSD or DE40.

## Historical export import

The new agent-visible `market_data_import` tool and offline `market-import` CLI
normalize CSV bid/ask quotes and completed OHLC bars for any instrument. They
retain source metadata and byte fingerprints, report gaps, enforce timestamp and
numeric validity, and reject ambiguous local DST timestamps. CSV column maps
and explicit price bases support different export formats without adding
provider-specific strategy logic. The same inputs feed the existing replay,
backtest and validation tools.

The importer accepts supplied exports; an authenticated broker-history connector
and automated Dukascopy downloader are not included. Public download probes for
the original XAUUSD and Germany 40 instruments returned HTTP 429. Real historical
profitability for those instruments remains unverified.

## Validation and limits

- Go unit, registered-MCP workflow and race checks; spawned-binary integration
  checks including the CSV importer.
- Fifteen UI tests, TypeScript checks and rebuilt panel bundles.
- Nine independent screenshot-case replays.
- Tier 3 real-LLM scenarios for screenshot strategy authoring, a new EMA/SMA
  research lifecycle and JSON-text definition transport. They cover
  winning/losing scorecards, cost robustness, artifacts and versioned revision.
- Embedded and install manifests share one source, report 0.15.0, and pin the
  install source to `trading/v0.15.0`; the public SDK pin is v0.99.0.

Rule programs currently run in research simulation. Broker/live rule assignment
is disabled. Each program manages one instrument; windows are same-day;
indicator seeds are bounded. FX is fixed per contract. Overnight financing and
automatic margin liquidation are excluded. Quote replay models fills and native
OCO, not guaranteed real broker execution. Broker-specific contract settings,
chart price basis, costs, timezone and data coverage require explicit selection.
See [RULE_ENGINE.md](RULE_ENGINE.md) for the complete contract and commands.
