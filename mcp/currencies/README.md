# Currencies

Provider-neutral ISO 4217 metadata, current and historical FX observations,
and deterministic minor-unit conversion for Apteva apps and agents.

Currencies has no app dependencies. It works with manual rates alone and can
optionally fetch rates through bound Alpaca Market Data, Salt Edge, or Alpha
Vantage integration connections. Provider credentials never enter this app.

## Core behavior

- Ships 178 current ISO 4217 definitions from SIX List One, published
  2026-01-01.
- Stores exchange-rate observations append-only.
- Supports current, historical, inverse, and two-edge triangulated rates.
- Converts signed integer minor-unit amounts with exact rational arithmetic.
- Returns rate IDs, provider, effective time, derivation path, staleness, and a
  deterministic quote ID.
- Never assumes parity when a rate is missing.

Rates always mean `1 base = rate quote`.

## Tools

- `currencies_list`
- `currencies_get`
- `currencies_rate_get`
- `currencies_convert`
- `currencies_rates_history`
- `currencies_rate_set_manual`
- `currencies_sources_status`
- `currencies_sync_now`

## Examples

Current EUR/USD rate:

```json
{"base":"EUR","quote":"USD"}
```

Historical conversion of USD 1,250.00 to EUR:

```json
{
  "amount_minor": 125000,
  "from": "USD",
  "to": "EUR",
  "as_of": "2025-12-31T23:59:59Z",
  "rate_kinds": ["reference", "close"],
  "rounding": "half_even"
}
```

Manual rate:

```json
{
  "base": "EUR",
  "quote": "USD",
  "rate": "1.1735",
  "effective_at": "2026-08-25T12:00:00Z",
  "reason": "Contractual treasury rate"
}
```

Consumers must store `rate_snapshot` with any committed invoice payment, tax
calculation, valuation, or materialized report that needs to be reproducible.

## Development

```sh
go test ./...
go build .
cd ../.. && bun run scripts/build-panels.ts --app currencies
```

The SDK pin is `github.com/apteva/app-sdk v0.67.0`, the tag currently pointing
at app-sdk HEAD when this app was created.
