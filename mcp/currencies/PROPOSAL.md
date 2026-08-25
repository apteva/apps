# Currencies App Proposal

Status: Proposed MVP

Internal name: `currencies`

Display name: **Currencies**

## Decision

Build a small first-party, provider-neutral `currencies` app that supplies
currency metadata, exchange-rate observations, deterministic conversions, and
rate provenance to Finance, Billing, Bills, Taxes, Commerce, Affiliate, Partner
Program, Ads, and other apps.

Currencies has **no app dependencies**. It works in manual/offline mode using
its own database and may use optional FX-provider integrations through platform
connections. It never stores provider credentials and never calls another app.

```text
optional FX connections
  (Alpaca / Salt Edge / Alpha Vantage / future providers)
        |
        v
provider adapters -> immutable normalized rate observations
                           |
                           v
                  select / invert / triangulate
                           |
                           v
             rate + provenance + deterministic conversion
                           |
          +----------------+----------------+
          v                v                v
       Finance        Billing/Bills       Taxes/Commerce/etc.
       snapshots      snapshots           snapshots
```

The app is infrastructure for monetary correctness, not a forex trading app.
It does not place orders, hold balances, issue invoices, determine tax policy,
or choose product prices.

## Why This Should Be A Separate App

Several first-party apps already need currency-aware behavior, but their needs
are not identical:

- Finance converts holdings, cashflows, budgets, and performance into a base
  currency. It currently owns a manual `fx_rates` table and silently falls back
  to 1:1 when a rate is missing.
- Bills needs an auditable rate when payment currency differs from bill
  currency.
- Taxes must normalize source records into a tax-profile currency using a rate
  that is valid for the jurisdiction and accounting date.
- Affiliate, Partner Program, and Ads need cross-currency reporting without
  summing unlike units.
- Billing may need reporting-currency views and manual cross-currency payment
  reconciliation.
- Commerce may show estimates or explicitly generate localized prices, but must
  not silently reprice a cart or canonical Catalog price.

Provider retrieval, decimal arithmetic, inversion, triangulation, staleness,
weekend handling, and provenance should not be implemented differently in each
consumer. A shared app gives these consumers one contract while keeping their
financial records and policies in the apps that own them.

## Goals

1. Return a rate for a currency pair at a requested point in time with complete
   provenance.
2. Convert integer minor-unit amounts deterministically without binary
   floating-point arithmetic.
3. Normalize replaceable FX providers behind one app-to-app contract.
4. Support manual rates when no provider is connected.
5. Preserve immutable observations so past calculations can be reproduced.
6. Make missing, stale, conflicting, inverted, and triangulated rates explicit.
7. Remain useful with no other app installed and no provider connected.

## Non-Goals

- Forex or crypto trading.
- Bank balances, wallets, ledgers, holdings, or transactions.
- Choosing an invoice, settlement, tax, store, or reporting currency.
- Deciding which date or rate type a jurisdiction requires for tax reporting.
- Automatically changing Catalog prices, open carts, invoices, or subscriptions.
- Hiding provider disagreement by silently averaging rates.
- Treating crypto assets or stablecoins as ISO fiat currencies in the MVP.
- Guaranteeing that a market-data rate is acceptable to a tax authority.

## Dependency Boundary

### Required app dependencies

None.

Currencies must not require Finance, Billing, Bills, Taxes, Commerce, Catalog,
Jobs, Workflows, or any other app.

### Required permissions

- `db.write.app`
- `platform.connections.read`
- `platform.connections.execute`

The connection permissions are needed only when an FX integration is bound.
Manual rates and already-cached rates continue to work without a connection.
The app does not require direct `net.egress`; external requests go through the
integrations engine.

### Optional integration connections

The initial compatible providers are already present in the integration
catalog:

| Provider | Current tools | Normalized use |
|---|---|---|
| Alpaca Market Data | `forex_latest_rates`, `forex_rates` | Latest and historical market rates |
| Salt Edge | `list_rates` | Current or dated reference rates |
| Alpha Vantage | `fx_daily` | Daily OHLC history; close can be selected explicitly |

The manifest should expose two optional roles so an install can have a primary
and a fallback connection:

```yaml
requires:
  permissions:
    - db.write.app
    - platform.connections.read
    - platform.connections.execute
  integrations:
    - role: fx_primary
      kind: integration
      required: false
      compatible_slugs: [alpaca-market-data, saltedge, alpha-vantage]
      label: "Primary FX-rate provider"
    - role: fx_fallback
      kind: integration
      required: false
      compatible_slugs: [alpaca-market-data, saltedge, alpha-vantage]
      label: "Fallback FX-rate provider"
```

Provider-specific tool names and response shapes stay inside Currencies
adapters. Credentials stay inside the integration connection. The database
stores only the connection ID needed for execution and safe provenance such as
provider slug and provider record identifier.

Future provider integrations can include ECB/reference-rate sources, Open
Exchange Rates, Fixer, Frankfurter, CurrencyLayer, or jurisdiction-specific
official feeds. Adding one must not change the consumer contract.

## Ownership Boundaries

### Currencies owns

- Versioned currency metadata.
- Immutable normalized rate observations.
- Provider adapters and provider health.
- Tracked pairs and refresh policy.
- Pair inversion and bounded triangulation.
- Decimal conversion and explicit rounding.
- Manual rates and their audit metadata.
- Rate selection results and provenance.

### Consumer apps own

- The business purpose for the conversion.
- The transaction, accounting, settlement, filing, or display date.
- Which rate kinds and providers are acceptable.
- Original and converted monetary fields on their records.
- The immutable rate snapshot used for each committed financial action.
- Jurisdiction-specific tax rules and pricing policy.

Currencies returns facts and calculations. It does not make accounting, legal,
commercial, or investment decisions for callers.

## Currency Model

The MVP supports ISO 4217 fiat, fund, and precious-metal codes from a versioned
table shipped with the app. Each entry includes:

- Alphabetic code.
- Numeric code where assigned.
- Display name.
- Minor-unit exponent.
- Kind: `fiat`, `fund`, or `metal`.
- Active/withdrawn status and effective dates when known.
- Metadata-table version.

Codes are normalized to uppercase. Same-currency conversion is always an exact
identity and needs no stored observation or provider call.

Crypto assets and provider-specific symbols are deferred. They have different
precision, venue, and market semantics and already belong to market-data and
trading domains. A later version may add explicitly namespaced non-ISO units
without weakening the ISO contract.

## Rate Model

Every stored rate means:

```text
1 base currency = rate quote currency
```

The canonical rate is stored and returned as a decimal string, never a JSON
floating-point number. Implementations use exact decimal or rational arithmetic.

Each immutable observation contains:

- `rate_id`
- `base` and `quote`
- `rate` as a canonical decimal string
- `rate_kind`: `spot`, `reference`, `open`, `high`, `low`, `close`, or `manual`
- `effective_at`, or `effective_date` for date-only provider data
- `observed_at` and `ingested_at`
- Provider slug and safe provider reference
- Connection ID used to obtain it
- Original provider base/quote orientation
- Normalization/inversion flags
- Raw-response hash and adapter version
- Quality flags
- Optional `supersedes_rate_id` for corrections

Observations are append-only. A corrected or manually replaced rate appends a
new row; it does not rewrite the observation used by an old financial record.

Daily provider data remains date-based. The app must not invent intraday
precision for a daily close or reference rate.

## Rate Selection

A rate request specifies:

- Base and quote currencies.
- `as_of` timestamp or date.
- Selection mode, initially `latest_on_or_before` or `exact_date`.
- Allowed rate kinds.
- Optional allowed or denied providers.
- Maximum age.
- Whether inverse rates are allowed.
- Whether triangulation is allowed.
- Whether a stale result may be returned with a warning.

Selection rules:

1. Never select an observation after `as_of`.
2. Prefer a direct pair from the configured primary provider.
3. Use the fallback provider only when policy permits it.
4. Invert an eligible reverse pair when direct data is absent.
5. If enabled, triangulate through a configured pivot such as EUR or USD.
6. MVP triangulation is limited to two edges; arbitrary graph walks are
   deferred.
7. Return an explicit missing-rate error when no eligible path exists.
8. Return staleness and provider-conflict warnings as structured fields.

The app never substitutes 1:1 for different currencies.

If multiple providers disagree beyond a configurable tolerance, the app does
not average them. It follows configured priority and returns a conflict warning
with the compared observations. A caller may reject warned results.

## Conversion Semantics

The conversion tool accepts a signed integer amount in the source currency's
minor units. It applies the source and destination exponents, the selected
decimal rate, and an explicit rounding mode.

Supported rounding modes for MVP:

- `half_even`
- `half_up`
- `down`
- `up`

`half_even` is the ergonomic default for interactive use. Financial consumer
apps should pass their required mode explicitly.

The result includes:

- Original amount and currency.
- Converted amount and currency.
- Source and destination minor-unit exponents.
- Rate as a decimal string.
- Rounding mode and whether rounding occurred.
- Rate kind and effective time/date.
- Selected source observations and derivation path.
- Stale/conflict warnings.
- Deterministic `quote_id`, derived from the selected immutable rate IDs,
  request policy, adapter version, and rounding inputs.

The consumer stores this returned snapshot with the invoice payment, tax
estimate, report materialization, or other committed record. Re-running a live
conversion later must not mutate the old record.

## Proposed MCP And App-to-App Tools

### `currencies_list`

List supported currency metadata with filters for active status and kind.

### `currencies_get`

Get one currency's metadata and minor-unit exponent.

### `currencies_rate_get`

Select one direct, inverse, or triangulated rate under explicit time,
staleness, source, and rate-kind constraints.

Example input:

```json
{
  "base": "USD",
  "quote": "EUR",
  "as_of": "2026-08-25T10:00:00Z",
  "selection": "latest_on_or_before",
  "rate_kinds": ["reference", "close"],
  "max_age_seconds": 259200,
  "allow_inverse": true,
  "allow_triangulation": true,
  "allow_stale": false
}
```

Example output shape:

```json
{
  "quote_id": "fxq_...",
  "base": "USD",
  "quote": "EUR",
  "rate": "0.86123456",
  "rate_kind": "reference",
  "effective_date": "2026-08-25",
  "observed_at": "2026-08-25T16:05:00Z",
  "derived": true,
  "path": [
    {"rate_id": 4102, "base": "USD", "quote": "GBP", "rate": "0.7412"},
    {"rate_id": 4117, "base": "GBP", "quote": "EUR", "rate": "1.1620"}
  ],
  "stale": false,
  "warnings": []
}
```

### `currencies_convert`

Convert one signed minor-unit amount using the same selection arguments as
`currencies_rate_get`, plus an explicit rounding mode.

### `currencies_rates_history`

Return normalized observations for a pair and date range. The response keeps
providers and rate kinds separate unless the caller requests a selected
series under a stated policy.

### `currencies_rate_set_manual`

Append a manual rate with effective time/date, reason, and optional source
reference. Manual rates are never silently preferred over providers; selection
policy decides whether they are eligible.

### `currencies_sources_status`

Return bound provider roles, supported capabilities, last success/failure,
tracked-pair coverage, newest observation, and freshness.

### `currencies_sync_now`

Refresh selected tracked pairs or backfill a bounded date range. It returns a
per-provider, per-pair result and is safe to retry.

Read-only HTTP equivalents may be provided for the dashboard, but there are no
public unauthenticated routes.

## Fetching And Refresh

The app uses both lazy fetching and a native SDK worker; it does not depend on
the Jobs app.

- A missing or stale rate request may fetch through a bound connection before
  selection, within a strict timeout.
- Successfully requested pairs become tracked pairs.
- A native worker refreshes active tracked pairs on a configurable cadence.
- Date-based/reference data is refreshed after its provider's expected publish
  window rather than being polled continuously.
- Historical backfills are explicit and bounded.
- Provider rate limits, retry-after responses, and exponential backoff are
  recorded per connection.
- The app does not fetch the full Cartesian product of all currencies.

Suggested MVP worker:

```yaml
provides:
  workers:
    - name: currencies-refresh
      schedule: "@every 15m"
```

Provider freshness defaults are adapter-specific and overrideable. A daily
reference rate is not stale merely because it is older than a spot quote, and
weekends/market holidays use `latest_on_or_before` plus the caller's maximum
age.

## Persistence

Use the app's SQLite database with project/global scope on every mutable table:

- `currency_definitions`
- `provider_bindings`
- `tracked_pairs`
- `rate_observations`
- `sync_runs`
- `manual_rate_audit`

Recommended constraints:

- Currency codes and pair orientation are normalized before insert.
- Rates must be positive canonical decimals within configured bounds.
- Provider observations are idempotent by provider, pair, kind, effective
  time/date, provider reference, and payload hash.
- Observations referenced by a returned quote are immutable.
- Manual corrections append and reference the superseded observation.
- All provider execution and writes are project/scope gated.

The app does not need to persist every conversion. A `quote_id` is
deterministically derived from immutable observations and request semantics;
the consumer persists the returned snapshot. A later version may add optional
quote retention if central audit requirements justify the write volume.

## Consumer Rules

### Finance

- Replace the local `fx_rates` ownership with calls to Currencies.
- Migrate existing manual Finance rates into Currencies with source
  `finance_migration`.
- Store selected rate snapshots when materializing reports or valuations that
  must be reproducible.
- Remove the missing-rate 1:1 fallback. Cross-currency reports must fail or
  return an explicit incomplete result listing missing pairs.

### Billing

- Keep every invoice in one transaction currency.
- Never revalue a finalized invoice.
- Use Currencies only for reporting-currency views or explicitly recorded
  cross-currency manual payments.
- Prefer the payment processor's actual settlement amount/rate when available;
  a market-data estimate must not replace settlement evidence.

### Bills

- Store bill amount/currency and payment amount/currency separately.
- When they differ, snapshot the exact Currencies result or a user-supplied
  manual rate on the payment row.

### Taxes

- Taxes chooses the legally relevant source date, accepted rate kind, and
  source policy for each jurisdiction.
- Each estimate/report stores the rate snapshot and rule version.
- Market rates are not automatically described as official tax rates.

### Catalog And Commerce

- Canonical prices remain authored price-book records.
- A cart remains single-currency and never changes after items are added.
- Live conversion may be used for an approximate display labelled with its
  source/time, or to propose a new localized Catalog price that a user
  explicitly creates.
- Do not convert line items independently and then mix rounding residues into
  an invoice without a documented pricing policy.

### Affiliate, Partner Program, Ads, And Analytics

- Raw monetary facts remain in their source currency.
- Reports may group by original currency or request an explicit reporting
  currency and snapshot the selected rates.
- Historical reports use period-appropriate observations, not today's rate,
  unless the caller explicitly requests current-value reporting.

## How Consumers Depend On Currencies

Currencies itself has no app dependencies. Consumer apps may declare it as an
optional app dependency during rollout:

```yaml
requires:
  apps:
    - name: currencies
      version: ">=0.1.0"
      optional: true
      reason: "Required only for explicit cross-currency conversion and normalized reporting."
```

Same-currency workflows continue without it. A feature that requires a
cross-currency rate must fail clearly when Currencies is absent or cannot
produce an acceptable observation. No consumer may silently assume parity.

Once adoption is universal, apps whose core correctness requires conversion
may make the dependency required. Billing and Commerce should likely keep it
optional because their canonical single-currency flows do not need FX.

## Dashboard

The MVP panel is operational rather than a forex terminal:

- Provider bindings and priority.
- Provider health and last sync.
- Tracked pairs with latest rate, kind, effective date, freshness, and source.
- Pair history and provider comparison.
- Manual rate entry with reason.
- Missing/stale/conflict warnings.
- Manual sync and bounded backfill controls.

There are no trading charts, forecasts, alerts about profitable trades, or
order actions.

## Events

Publish low-volume operational events rather than one event per market tick:

- `currencies.sync.completed`
- `currencies.sync.failed`
- `currencies.source.degraded`
- `currencies.manual_rate.created`

Consumers normally request rates synchronously or from the local cache. They
should not rebuild financial records when a new rate observation arrives.

## Correctness And Safety Requirements

- Different currencies never default to a 1:1 rate.
- Rates and calculations never use binary floating-point arithmetic.
- A historical request never consumes a future observation.
- Identity, inverse, and triangulated paths are distinguishable in output.
- Every non-identity conversion has provider/manual provenance.
- Daily data does not pretend to have intraday precision.
- Staleness and conflicts are machine-readable, not only UI labels.
- Provider failure cannot mutate or invalidate an older observation.
- Financial consumers snapshot results before committing records.
- Same request and same immutable observations produce the same `quote_id` and
  converted amount.

## MVP Test Matrix

- Same-currency identity without providers.
- Direct, inverse, and two-edge triangulated rates.
- USD/EUR two-decimal conversion and JPY zero-decimal conversion.
- Positive, negative, zero, and maximum supported minor-unit amounts.
- Every rounding mode at exact half boundaries.
- Daily rate selection across weekends and holidays.
- `latest_on_or_before` never selecting future data.
- Missing, stale, and conflicting provider observations.
- Primary failure with allowed and disallowed fallback.
- Provider-specific fixture normalization for Alpaca, Salt Edge, and Alpha
  Vantage.
- Retry/idempotency behavior and provider rate-limit responses.
- Manual correction append-only behavior.
- Scope isolation and absence of credentials in rows, logs, errors, events, and
  MCP responses.
- Deterministic quote IDs across restarts.

## Rollout

### Phase 1: Currencies MVP

- Currency metadata.
- Manual rates.
- Immutable observation store.
- Exact decimal conversion.
- Direct/inverse/two-edge selection.
- Provider status and panel.
- Alpaca, Salt Edge, and Alpha Vantage adapters through connections.
- Lazy fetch plus tracked-pair refresh worker.

### Phase 2: Finance And Payables

- Migrate Finance manual rates.
- Replace Finance's 1:1 missing-rate fallback.
- Add Bills cross-currency payment snapshots.
- Add Taxes source normalization with explicit jurisdiction policy.

### Phase 3: Reporting Consumers

- Affiliate, Partner Program, Ads, and Analytics reporting currencies.
- Billing reporting-currency views.
- Commerce approximate display/localized-price proposal flow.

### Deferred

- Official central-bank and tax-authority adapters not yet in the integration
  catalog.
- More than two provider bindings and quorum policies.
- Arbitrary cross-rate graph search.
- Forward rates, bid/ask spreads, and settlement quotes.
- Crypto and custom units.
- Currency redenomination workflows.
- Centrally retained conversion-quote audit records.

## Acceptance Criteria

The MVP is complete when:

1. It installs and serves manual/identity rates with no other app or provider.
2. An optional bound Alpaca, Salt Edge, or Alpha Vantage connection can ingest
   normalized observations without exposing credentials.
3. A caller can request an as-of rate and convert a minor-unit amount with
   deterministic rounding and complete provenance.
4. Missing and stale rates are explicit and different currencies never fall
   back to parity.
5. Direct, inverse, and two-edge triangulated results are covered by tests.
6. The returned rate snapshot is sufficient for a consumer to reproduce and
   audit a committed financial record.
7. Finance can adopt the app without Currencies acquiring any dependency on
   Finance or another first-party app.
