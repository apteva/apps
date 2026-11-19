# Intelligence Platform / Market Intelligence

Version 0.3 introduces the generic intelligence core. The market and trading
features remain a domain pack, while the core stores source-attributed
evidence and supports date-aware search, timelines, and point-in-time replay.
This keeps historical analysis and backtesting reproducible: a query can ask
what happened in a period, what was published in a period, or what was known
at a specific cutoff.

The app is read-only with respect to external systems. `evidence_record` writes
an immutable, project-scoped evidence record to the app database; duplicate
content is idempotent and corrections are represented with a new observation.

## Generic intelligence tools

- `evidence_record`: record a document, claim, event, measurement, or other
  source observation with event, publication, validity, and observation times.
- `search`: search evidence by text, source, entity, kind, event/publication
  range, or `as_of` cutoff.
- `timeline`: return evidence in chronological order for a topic or entity.
- `replay`: produce a deterministic snapshot hash and records visible at an
  `as_of` cutoff. This is the safe input boundary for analysis and backtests.

All evidence rows carry `event_time`, `published_time`, `observed_at`,
`valid_from`, and `valid_to` where available. `as_of` applies the observed and
valid-time constraints together, preventing future information from leaking
into historical analysis.

## Signal pipeline

Every five minutes the worker:

1. requests the most liquid instruments from each configured provider;
2. loads closed candles only and computes reproducible features;
3. evaluates trend/momentum, mean-reversion, and volume-breakout strategies;
4. rejects stale quotes, thin markets, wide spreads, weak confidence, and
   opportunities whose expected move does not clear fees and slippage;
5. stores accepted signals with entry, stop, targets, expiry, rationale,
   features, and complete provenance;
6. emits `signal.created`; and
7. evaluates matured signals to build an immutable live performance record.

The public `binance-public` provider uses Binance spot REST without credentials.
No synthetic prices are used. `signal_backtest` performs a walk-forward replay
over closed candles with conservative same-candle stop/target handling.

## Paid provider contract

Signal logic depends on `MarketDataProvider`, not Binance. A paid integration
adapter must expose a universe tool and a bars tool returning these normalized
shapes:

```json
{"instruments":[{"symbol":"BTCUSDT","canonical_symbol":"BTC-USD","asset_class":"crypto","bid":100,"ask":100.1,"last":100.05,"quote_volume_24h":50000000,"quote_time":1760000000}]}
```

```json
{"bars":[{"time":1760000000,"open":99,"high":101,"low":98,"close":100,"volume":1200,"closed":true}]}
```

Configure `paid_provider_slug`, `paid_provider_venue`,
`paid_provider_universe_tool`, and `paid_provider_bars_tool`. Credentials remain
inside the bound Apteva integration. A provider that omits `closed=true` cannot
produce a signal.

## Productization

`signal_feeds` separates the computation from the product: each feed has an
asset class, interval, quality gates, visibility (`private`, `delayed`, or
`premium`), and delay. `signals_list` exposes stable public IDs and
`signal_metrics` exposes measured results. Billing/entitlement enforcement can
therefore be added at the feed boundary without changing signal generation.

Backtests are evidence, not a promise. Before charging subscribers, collect a
meaningful live evaluated sample, calibrate confidence against observed hit
rates, document venue/fee assumptions, and have the distribution terms reviewed
for the jurisdictions where the feed will be sold.

## Verification

```sh
env GOCACHE=/private/tmp/codex-go-build go test ./...
env RUN_MARKET_INTEL_LIVE=1 GOCACHE=/private/tmp/codex-go-build \
  go test -run 'Test(BinanceProviderLive|SignalPipelineLive)$' -v
```
