# CRM v0.9.1 remediation

Implemented in isolated branch `fix/crm-0.9.1-audit`, based on release
`crm/v0.9.1` (`aced4f5d`). The original modified `apps` checkout is untouched.
No production data was changed, no real messages were sent, and nothing was
published or deployed. The original audit/evidence is retained as a baseline.

## Audit findings addressed

| Finding | Change |
| --- | --- |
| 1. Reply address/identity | Persist inbound route metadata; reply to selected message; preview recipient/sender; reject blocked or mismatched routes. |
| 2. Merge lookup/primary | Clear loser mirrors, expose survivor ID, preserve winner primary, reject invalid merges. |
| 3. SMS/WhatsApp merges | Consolidate persistent threads and move activities/participants transactionally. |
| 4. Relational ownership | Transaction checks and SQLite triggers protect activity/channel/list/routing/opportunity ownership. |
| 5. Lost delivery evidence | Durable evidence separated from suppression overlay; explicit audited recovery. |
| 6. Cross-contact threading | All inbound email matching requires the conversation's contact owner. |
| 7. Stale selected contact | Abort superseded requests, use generation and scope guards, protect mutation refreshes. |
| 8. Inaccessible archives | Archive preserves readability and addresses; active status restores; migration repairs old archives. |
| 9. Primary scalar divergence | Reject primary mirror patches; update normalized channels. |
| 10. Invalid date segments | Correct qualified timestamp SQL and validate generated definitions. |
| 11. Typed attribute audiences | Compile against declared types; correct missing/null and multi-select behavior. |
| 12. Segment editor type loss | Preserve typed operands and reject incomplete conditions; typed null operators work without values. |
| 13. Unscoped events | Explicit project-aware emissions across HTTP/MCP, including attribute and activity writes. |
| 14. Duplicate membership events | Emit on actual transitions using transactional outbox triggers. |
| 15. Truncated messages | Store full bodies and derive safe text from HTML-only messages. |
| 16. Incorrect timestamps | Monotonic activity recency, first-contact time, chronological ordering, relation modification metadata. |
| 17. Hidden board/list records | Pipeline selection before paging, board totals/pages, list-member cursors/load-more, visible load errors. |
| 18. Opportunity update races | Read/write/snapshot inside one transaction, stage/status invariants enforced in SQLite. |
| 19. Partial inbound commits | One transaction for all local ingestion state and events; source-install-aware deduplication. |
| 20. Suppression scaling | Indexed matching, changed-row writes, batches of 100, stale-snapshot guards, worker metrics. |
| 21. Inbox amplification | Project-scoped indexed preview, bounded status cache, refresh coalescing and aborts; source-isolated enrichment. |
| 22. Missing system attributes | Idempotent project initialization and enforced do-not-contact behavior. |

## Additional recommendations implemented

Channel kinds/syntax, contact statuses, timestamps, finite numeric values and
opportunity dates are validated; fractional IDs are rejected rather than
truncated. JSON bodies reject trailing values. Static segment creation/updates
materialize atomically, unsupported dynamic exclusions are rejected, and eval
APIs have bounded cursor pages. Verification work is bounded across requests.
Routing setup is explicit through the bound CRM backend. Collection scan errors
are propagated in the changed read paths. Unsaved contact edits use the CRM
confirmation dialog; optimistic timestamps protect whole-array channel edits.
Automated mail has an explicit ignore-new/review-new policy while retaining
existing-contact history. Obsolete ContactsPanel assets are removed; the README
and contracts are updated, and the manifest is embedded from its disk source.

The SDK pin was updated to v0.73.0 after fetching tags and checking that the tag
points to SDK HEAD by commit topology. CRM's built panel and source map were
regenerated. The panel verifier now respects `--app`, matching the build scope;
this avoids an unrelated pre-existing Campaigns bundle failure during CRM-only
builds. Full-repository verification still reports unrelated problems normally.

## Verification

- All 16 original Go behavior regressions now pass in the default suite; the
  additional query-plan diagnostic passes too.
- New tests cover ingestion rollback/retry and concurrent duplicates, source ID
  collisions, outbox acknowledgment/retry, stale edits, snapshot paging, an
  existing-data migration, concurrent opportunity edits, do-not-contact,
  automated review, audited recovery, stale delivery events, unchanged snapshot
  writes, invalid numeric/datetime inputs, and status-cache source isolation.
- The full suite passes with the Go race detector and 53.7% statement coverage; exact
  final output is in `audit-evidence/fixed-race.txt`. `go vet` passes.
- All five real-sidecar integration tests pass: boot, full HTTP/MCP flow,
  project isolation, global scope, and real CRM-to-Messaging routing/sender calls
  through a local gateway. No provider is configured in that integration test.
- TypeScript checking passes; the CRM production bundle and host React import
  verification pass. The source-executed UI tests check numeric/boolean/date/
  multi-select round trips, typed nulls, out-of-order loads, and discard callbacks.
- Browser interaction checks verified correct reply recipient/receiving identity,
  the non-default pipeline including page two, all 103 list members, read-only
  Settings load, and saving/reopening a numeric segment. See browser notes below.
- Suppression lookup benchmark at 10,000 entries: linear scan about 370,457 ns/op
  versus indexed lookup 97.83 ns/op on this machine. This measures lookup CPU
  after indexing, not full reconciliation or production throughput.

Evidence files prefixed `fixed-` describe this branch. Unprefixed files describe
the original release and intentionally include its failing reproductions.

## Explicit limits and rollout notes

The browser fixture has synthetic data and does not reproduce full host styling.
A native browser confirmation dialog stalled embedded-browser automation during
an unsaved-edit check. The implementation was changed to the CRM dialog and its
actual callback passed the controlled test, but the revised dialog did not get a
successful final interactive browser pass. The test server has been stopped.

This work does not reconstruct already-truncated historical messages. Legacy
message IDs without install provenance retain their local content but skip remote
enrichment; after upgrade, a redelivery may create a new source-scoped activity.
Do not guess which installation owns ambiguous old IDs. Upstream provider behavior
and production data need rollout validation.

The outbox guarantees retry after enqueue, with at-least-once delivery and stable
IDs. Membership, inbound and recovery events commit with their business writes;
other CRUD events still enqueue immediately after commit and retain a narrow
crash window. The current SDK cannot cancel an already-started cross-app call
through its PlatformClient interface; browser requests are aborted and backend
work remains bounded by existing SDK timeouts. These limits are documented in
the app README rather than being presented as solved guarantees.

Back up the CRM database before applying migrations 014–020. Rollback requires
the prior app and database backup together. The manifest remains at 0.9.1 because
this is a review branch, not a published release.
