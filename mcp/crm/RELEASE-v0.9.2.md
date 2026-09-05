# CRM v0.9.2

Repairs contact identity, reply routing, audience correctness, concurrent edits,
and inbox performance issues identified in the v0.9.1 audit.

## Changes

- Replies retain the original sender/recipient route and never silently switch a blocked address.
- Contact merges preserve primary channels, resolve survivor IDs, and consolidate SMS/WhatsApp history. Archives remain readable and restorable.
- Inbound ingestion commits all local state and events atomically, with deduplication scoped to the source Messaging installation.
- Delivery evidence survives suppression reconciliation; explicit recovery is audited. Do-not-contact now blocks sends and messageable audiences.
- Typed segment filters preserve values and null semantics. Static snapshots materialize atomically and collection evaluation supports bounded cursor pages.
- Contact editing rejects stale updates and protects drafts. Opportunity stage/status consistency is enforced transactionally.
- The panel supports pipeline selection and pagination, list-member paging, correct reply previews and explicit routing repair.
- Suppression reconciliation uses indexed matching and bounded changed-row batches. Inbox reads use indexed previews, bounded status caching and refresh coalescing.
- Events carry project IDs and stable outbox IDs. Automated inbound mail has an explicit ignore-new/review-new policy. System attributes initialize idempotently.
- SDK pinned to v0.73.0; obsolete UI removed and documentation updated.

## Validation

All original audit regressions and the full Go suite pass, including race detection.
Go vet, TypeScript checks, UI regression checks and the production panel build pass.
Five sidecar integration tests cover HTTP/MCP, project/global isolation and real
CRM-to-Messaging calls through a local test gateway. No real provider message was sent.

## Upgrade notes

Back up the CRM SQLite database before upgrading. Migrations 014–020 apply
forward data repairs and add ownership constraints, source-aware message IDs,
the event outbox and recovery records. Rollback requires the old app and database
backup together. Updating the release/registry does not upgrade running installs.

Legacy messages lacking source-install provenance retain local content but skip
remote enrichment; a redelivery can create a new source-scoped activity. Already
truncated historical text cannot be reconstructed by migrations.

Events are at least once; consumers must deduplicate `event_id`. Membership,
inbound and recovery events commit with their data. Other CRUD events retain a
narrow crash window between the database commit and outbox enqueue. Backend
cross-app calls already started cannot be cancelled through the current SDK.

Browser checks covered replies, pipeline/member pagination, settings and numeric
segment edits. The revised draft-discard dialog passed its source callback test;
a final interactive pass was prevented by an embedded-browser dialog stall.
