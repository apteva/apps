# Billing release notes

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
