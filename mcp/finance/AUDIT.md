# Finance review — 2026-09-15

Original audit patch: **0.1.17**. The subsequent **0.2.0** payment and Enable Banking implementation is documented in README.md; neither version was released or deployed by this work.
Reviewed against the Finance v0.1.16 source changes on origin/main. Existing local adaptive-icon work was preserved. The SDK pin is now v0.77.0, the tag at local app-sdk HEAD after fetching tags.

## App fixes

- Scope account, transaction, private-instrument, category and budget reads/writes to the current project. Scope holdings, cashflow and performance queries as well. Validate related IDs before writes, including transfer destinations and budget/category parents.
- Reject cyclic category parents and allow clearing a parent or transaction category.
- Reject invalid transaction dates and non-finite holding quantities. Normalize newly written transaction timestamps to UTC.
- Remove external mappings when deleting an account; clear the provider account ID when unlinking.
- Reject bank links to archived accounts, non-cash accounts, mismatched currencies or accounts already attached to a different source.
- Do not serialize bank-link metadata (including the legacy Plaid access token) in tool/HTTP responses.
- Exclude pending bank transactions from new booked imports. Import transaction and external mapping in one database transaction, and distinguish account-local provider transaction IDs.
- Reject imported transactions for a different account/currency. Empty provider arrays no longer create phantom transactions; missing dates no longer use the current time as an unstable transaction identity.
- Recognize Nordigen booking dates and Salt Edge `made_on`; apply TrueLayer debit direction. Coffee purchases are no longer classified as fees because their name contains “fee”; positive refunds stay income.
- Fetch Salt Edge's current account balance instead of reusing discovery metadata. Repeated syncs can update the same day's balance adjustment. Reconciliation adjustments are excluded from cashflow income/expense.
- Preserve balance/import errors and report per-account sync counts. A banking dry run does not write error status. Reject invalid date ranges.
- Reject explicitly selected inactive or foreign-project bank connections.
- Prevent stale Banking-tab account results and credentials from surviving a connection change; prevent overlapping bank actions, match linked accounts by connection/provider, handle connection-load errors and mask the Plaid token input.
- Report, budget-status and account/holding valuation views reject missing FX rates rather than silently reporting a 1:1 conversion.
- Clarify that `txns_transfer` records a ledger transfer and does not move money at a bank.

## Validation

- Full Finance Go test suite, including eight new audit regression tests with multiple subcases.
- `go vet ./...` and standalone binary build.
- Tests/build run with `GOWORK=off` against the pinned published SDK, using installed Go 1.26.8. The workspace's default Go 1.26.6 executable is missing.
- Finance panel rebuilt with Bun and passes the host React import check. The shared checker also scans other apps and reports four existing issues in Instances, 3D Studio and SEO; those files were not changed.
- Integration connector files were restored to their pre-task contents. No provider payment requests or live bank transfers were executed.

## Integration findings from the initial review

The user subsequently fixed the connectors. The current Plaid/Teller fixes and Nordigen capability correction were rechecked before implementing Finance payment support. The entries below preserve the original findings; they are not a current open-bug list.

1. **Teller payment payloads are incorrect.** `create_payment` uses a form content type and `payee_id`. The current official endpoint expects JSON with a `payee` object containing scheme/address. `create_payee` also needs JSON. The connector should expose the payment `Idempotency-Key` header and clients must complete returned `connect_token` MFA through Teller Connect.
   Source: https://teller.io/docs/api/account/payments
2. **Nordigen advertises unavailable payment endpoints.** The configured GoCardless Bank Account Data v2 service's OpenAPI has no payment routes, but the connector declares `/payments/` and `/payments/{payment_id}/`. These are not usable write capabilities for that service.
   Source: https://bankaccountdata.gocardless.com/api/v2/swagger.json
3. **Plaid transaction/balance options are misplaced.** The connector exposes `account_ids`, `count` and `offset` at the input root without a request transform. Plaid expects these under the API's `options` object. This affects account filtering and pagination.
4. **Plaid payment authorization schema is incomplete.** `create_link_token` does not expose `payment_initiation` or Hosted Link configuration, and `create_payment` lacks `user_id`, required for new payment integrations in current docs.
   Sources: https://plaid.com/docs/api/products/payment-initiation/ and https://github.com/plaid/plaid-openapi/blob/master/2020-09-14.yml

The original TrueLayer and Salt Edge connectors remain data integrations. Separate `truelayer-payments` and `saltedge-payments` connectors now provide payment authentication/signing/consent and are wired into Finance v0.2.0.

## Limits and follow-up work

Reviewed payments for Plaid, Teller, Enable Banking, TrueLayer Payments and Salt Edge Payments, plus Enable Banking account imports, are implemented; see README.md for product prerequisites and recovery behavior. Existing transactions are not rewritten or removed automatically, including pending transactions imported incorrectly by earlier versions. Provider adapters still need complete pagination coverage, and existing historical FX/holding valuation behavior deserves a separate accounting review. Validation here uses fixtures and local database tests, not live provider accounts.

## v0.2.0 payment completion

- Added SEPA/instant SEPA Enable Banking payments with capability discovery and durable deferred execution claims.
- Added TrueLayer EUR/GBP hosted payments and Salt Edge SEPA Widget payments using the new connector contracts.
- Added Plaid recipient verification/selection, ACH Link continuation, cancellation eligibility checks and durable event cursor synchronization.
- Added Teller payment capability checks before origination.
- Added optimistic revisions so stale status reads cannot overwrite execution claims, plus random return-state verification.
- Added a read-only background status worker; no payment response books a ledger transaction.
- Regression tests cover concurrent continuation, crash/timeout recovery, event checkpoint failure, mismatched reconciliation and callback URLs, exact amounts, provider payloads and status routing.
