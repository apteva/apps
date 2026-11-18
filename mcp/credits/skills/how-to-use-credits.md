# Credits

Credits keeps prepaid balances. Use it whenever an app must check, set aside
or take money (or minutes, points, tokens) before doing work.

## Model

- **Wallet**: one per `(subject_type, subject_id, unit)`, e.g.
  `(saas_account, acct_42, usd)`. Every amount is an integer in the wallet's
  unit. Currency units are **micro-units**: `usd` 1000000 = $1.00. Units never
  mix and are never converted.
- **Lot**: one batch of credit with a source (`purchase`, `promo`, `reward`,
  `adjustment`, `transfer`), an expiry and a priority. Spending draws from
  the lowest priority number first, then the soonest expiry, then the oldest.
  Promo lots default to priority 10 (spent first), everything else 100.
- **Hold**: money set aside for an unfinished operation, keyed by a
  reference `app/type/id`. It ends exactly once: commit, release, or expire.
- **Entry**: append-only ledger row for every change. `credits_entries_list`
  is the customer statement.
- **Cap**: optional ceiling per window (day, week, month, lifetime), for the
  whole wallet or one scope such as `project:proj_prod`. A wallet can have $50
  and still be stopped by a $15 monthly cap.

## Spending flow

1. `credits_reserve(subject_type, subject_id, unit, amount, reference, scope?, ttl_seconds?)`
   - success: `{hold_id, available_after}`
   - denial: `{denied: true, reason}` with reason `insufficient_balance`,
     `cap_reached` or `frozen`. Nothing is spent. Map it to HTTP 402/429.
   - the same reference always returns the same hold, so retries are safe.
2. Work succeeded: `credits_commit(hold_id, amount)` with the actual cost.
   Less than the hold releases the rest. More takes the extra from the
   balance; anything not covered is recorded as a shortfall (never negative).
3. Work failed: `credits_release(hold_id)`.
4. A crashed caller is fine: the hold expires (default 15 min, max 7 days)
   and the worker releases it.

`credits_spend` does reserve and commit in one call for operations that
can't fail halfway (a single SMS).

## Adding credit

- `credits_grant` for promo, reward or adjustment credit. Pass `source_ref`
  (e.g. `saas:signup:acct_42`) so a retry never credits twice.
- `credits_topup_create(price_cents: 5000, customer_id, idempotency_key)` for
  paid credit. Billing invoices it (VAT applies here) and returns a payment
  session. The purchase lot appears only when billing emits `invoice.paid`.
  Nobody can grant a `purchase` lot directly.
- Auto-reload: `credits_wallet_update(auto_reload: {enabled: true, price_cents: 2000}, low_threshold: 5000000)`.
  When the balance crosses the threshold, billing charges the saved card
  off-session; one attempt in flight per wallet.

## Admin

- `credits_adjust(amount, reason)` — signed correction; reason required.
- `credits_transfer` — same unit, same billing customer only.
- `credits_wallet_freeze` / `credits_wallet_unfreeze`.
- `credits_shortfalls_list` — commit overruns and refunds of spent credit.
- `credits_reconcile` — checks every invariant; run it after anything odd.
- `credits_liability_report(as_of)` — unspent paid credit at a date, for the
  accountant. Promo credit is not a liability and is excluded.

## Refunds

When billing emits `invoice.refunded` for a top-up, credits removes the
refunded share of that purchase lot (VAT-inclusive proportion). Credit that
was already spent or is currently held is recorded as a `refund_spent`
shortfall instead of making the balance negative.
