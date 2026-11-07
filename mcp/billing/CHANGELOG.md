# Billing release notes

## 0.14.0

- Mark the 11 tools that never write with the MCP `readOnlyHint`, so the
  platform can run them across every project an agent's grant allows
  (`project_id: "*"`). `invoices_render_pdf`, provider session tools and every
  create, update, void, refund, send or collect tool stay unmarked.
- Pass `_project_id` on catalog price and product lookups. A global Billing
  install previously sent none, so `price_id` line items failed with catalog's
  "project_id required".
- Point `runtime.source.ref` at `billing/v0.14.0`. It had stayed on
  `billing/v0.13.0`, so source installs of 0.13.1 and 0.13.2 built 0.13.0
  code; this release ships their changes too.
- Read `payment_methods_list` from the read pool. No new tools, arguments or
  database migrations.

## 0.13.2

- Return Storage’s signed invoice PDF URL and expiry instead of constructing an
  authenticated relative path. Preserve Storage’s URL and project routing.
- Upload new invoice PDFs privately through the existing `invoices_render_pdf`
  tool; the linked Storage install supplies the signed-link TTL.
- Preserve PDF bytes when upload fails and distinguish a missing Storage
  binding from other failures. If signing fails after upload, retain the saved
  file ID and report `shareable=false` so callers can retry `files_get_url`.
- Clarify linking, expiry, partial failures, and authenticated direct PDF routes.
- Correct the startup version log and pin app-sdk v0.95.0. No new tools or
  database migrations.

Validation: race-enabled Tier 1 tests, real Billing/Storage integration tests
(including missing, tampered, and expired signatures), go vet, and two live-agent
Tier 3 scenarios covering signed delivery and missing bindings, three runs each,
plus three customer-search regression runs. All checks use the clean release
checkout and published SDK modules without the workspace overlay.

## 0.13.1

- Improve the existing `customers_search` tool with exact-match ranking,
  literal phrase matching, and labelled broader token candidates when no phrase
  matches. Preserve project isolation, deleted-customer filtering, and pagination.
- Add optional `include_context=true` for invoice totals and balances by currency,
  invoice status counts, and recent payments in the same call. Context pages are
  capped at 20 customers; broader candidates require identity confirmation.
- Clarify customer-first account reviews and invoice text-search semantics.
  Make integer-cent units explicit to prevent incorrect currency amounts.
- Pin app-sdk v0.93.0. No new tools or database migrations.

Validated with the complete Tier 1 suite using the published SDK without the
workspace overlay, plus three live-agent OpenCode Go scenario runs. Each review
used exactly two customer searches, no invoice/context follow-up calls, and no
billing writes; amounts and candidate identity were checked.

## 0.13.0

- Recover ambiguous Stripe operations with durable idempotent requests, a verified
  webhook inbox, invoice payment coordination, and a transactional event outbox.
- Record immutable provider amounts, overpayment credits, allocated refunds,
  monotonic statuses, and explicit collection holds after commercial refunds.
- Enforce saved-method ownership, preserve detached-method tombstones, snapshot
  issued identities, and allocate invoice numbers through transactional sequences.
- Correct currency precedence, historical settlement dates, validation, lifecycle
  guards, mixed-currency totals, draft metadata retention, and global install scope.
- Add paginated server search/history, stale-response protection, install-aware
  actions, refund status display, and bounded PDF pagination.
- Split backend/UI modules, use read pools and indexed timestamps, and reduce
  repeated catalog calls and event refreshes.

Requires app-sdk v0.75.0 (pinned) and Apteva server v0.27.4 or newer.
Migration 011 flags duplicate emails without merging financial histories; existing
issued snapshots preserve the current identity with migration provenance. Unknown
legacy provider outcomes need review. Consumers must deduplicate event_id and use
payment.recorded for individual payments; invoice.paid is a settlement transition.

Validated with unit, race, local-sidecar integration, SDK migration, Stripe HTTP
contract, component, type-check, PDF layout, and 10,000-invoice benchmark checks.
No live Stripe transactions were used. PDF fonts remain CP-1252; HTML print is
available for other scripts. This release does not add a credit-note system.
