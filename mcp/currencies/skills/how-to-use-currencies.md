# How to use Currencies

Use Currencies whenever an answer or another app needs an exchange rate,
historical conversion, ISO currency metadata, or provider provenance.

## Choose the operation

- Use `currencies_rate_get` to answer “what is/was the rate?”
- Use `currencies_convert` to convert an integer minor-unit amount.
- Use `currencies_rates_history` for a time series or audit trail.
- Use `currencies_sources_status` when a rate is missing or stale.
- Use `currencies_rate_set_manual` only when the user supplies or authorizes a
  contractual, official, or offline rate. Always include a reason.

## Current and historical rates

Omit `as_of` for the latest eligible observation. For historical accounting or
tax work, pass the relevant RFC3339 timestamp or `YYYY-MM-DD` date and specify
accepted `rate_kinds` when the distinction matters.

Provider history and granularity differ. A daily close/reference observation
is labelled as daily; it is not presented as an intraday rate.

## Conversions

Amounts use signed integer minor units. USD 12.50 is `1250`; JPY 125 is `125`.
Currencies uses the seeded ISO exponent for both sides and returns the original
amount, converted amount, rounding information, and `rate_snapshot`.

Use an explicit rounding mode for financial records:

- `half_even`
- `half_up`
- `down` (toward zero)
- `up` (away from zero)

## Safety

- Never treat different currencies as 1:1 when a rate is unavailable.
- Do not silently accept `stale: true` or provider-conflict warnings for a
  committed financial action.
- Taxes decides the legally relevant date, source, and rate kind. Currencies
  does not claim a market rate is an official tax rate.
- Billing and Commerce keep canonical prices and finalized invoices in their
  original currency. Live rates are not permission to reprice them.
- Store the returned `rate_snapshot` on committed consumer records. A later
  market observation must not rewrite the original calculation.
