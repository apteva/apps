# Credits

Prepaid credit wallets for Apteva apps: lots, holds, caps and an
append-only ledger, with paid top-ups through Billing.

See [skills/how-to-use-credits.md](skills/how-to-use-credits.md) for the
model and the reserve / commit / release flow.

## Invariants

The ledger guarantees, and `credits_reconcile` checks:

- `available + held = SUM(lots.remaining)` for every wallet. A lot's
  `remaining` includes money currently held; `held` is that reserved part.
- `held = SUM(lots.held) = SUM(active holds.amount)`.
- Summing entries' `delta_available` / `delta_held` rebuilds the wallet.
- Each lot's `original + SUM(lot_moves.delta) = remaining`.
- Nothing is ever negative. Money that can't be taken becomes a shortfall.

Every write is idempotent through a lot `source_ref`, an entry
`idempotency_key`, or the hold reference.

## Decisions on edge cases

- **Lot expires while held**: the worker expires only the unheld part. The
  held part expires when the hold settles, instead of returning to the
  balance.
- **Refund of a partly held purchase lot**: only the unheld, unspent part is
  removed; the rest is a `refund_spent` shortfall.
- **Hold across a cap window**: a hold counts against the period it was
  created in, including when it is committed in the next period.
- **Partial refunds**: the refunded share is
  `lot.original × refunded_cents / invoice.total_cents` (both VAT-inclusive),
  capped at what has not been refunded yet.
- **Units**: currency wallets are micro-units; billing is cents.
  1 cent = 10,000 micro-units (`microPerCent`).
- **Naming**: Billing's `credit_cents` on an invoice is customer overpayment,
  unrelated to this app.

## Not in v0.1

- Unit or currency conversion, transfers between billing customers,
  overdrafts, Stripe customer balance.
- A dashboard panel (tools and HTTP only).
- The llm / media-studio hooks: those land in those apps.

## Develop

```sh
GOWORK=off go build ./...
GOWORK=off go test ./...
```
