> Historical baseline: this report describes untouched v0.9.1. The checkout now contains fixes; source line references below are historical. See [remediation status](CRM-FIX-PROGRESS.md) for the current state and passing evidence.

**CRM v0.9.1 audit — 5 September 2026**

The highest-priority fixes are reply addressing, merge integrity, activity ownership, and suppression reconciliation. These have concrete failure cases in the released code. I would repair these behaviors before adding CRM features or undertaking a broad rewrite.

Audited release: `crm/v0.9.1`, commit `aced4f5d9c00e915e90b2105dbd4b761b7424375`. The audit uses a separate detached worktree at `/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1`. Application source and the original modified checkout were not changed. Only this report, evidence files, and opt-in reproductions were added.

The review covered contact persistence and identity, migrations, HTTP/MCP handlers, attributes, lists, segments, audience resolution, pipelines/opportunities, inbound/outbound messaging, attachments, email verification, deliverability workers/events, and the React panel. Relevant pinned SDK and Messaging code was also inspected to check integration assumptions.

Validation:

- Existing Go tests pass with `GOWORK=off`, using the release's pinned SDK instead of the workspace overlay.
- Existing tests also pass under the race detector; statement coverage is **51.6%**. `go vet ./...` passes.
- All four `TestSidecar_*` integration tests pass, including project/global scope cases.
- The React panel bundles successfully with Bun; the shipped source map contains the same TSX source as the release.
- **16 added Go regression tests fail against v0.9.1**, demonstrating missing behavior. They are gated behind the `audit` build tag and do not change the default suite.
- Two JavaScript reproductions execute the release's actual helpers/callback with controlled inputs: segment type corruption and out-of-order contact loading.
- An SQLite query plan and a suppression microbenchmark support the performance findings below.

Messaging calls in reproductions use local doubles. No real messages were sent. Production data, provider delivery, and a full interactive browser session were not exercised. Source-level concurrency and integration concerns are labeled separately from executed reproductions; this audit does not establish that all possible bugs have been found.

**Prioritized findings**

1. **P1 — Replies can go to a different address than the incoming message. Reproduced.**

   A contact has primary `work@example.test` and secondary `private@example.test`. An email arrives from the private address. `contacts_reply` selects the thread's transport but `sendMessageImpl` independently resolves the contact's preferred address, sending the reply to the work address. The original recipient identity is also not automatically used as the reply sender: without an explicit `from`, list/install defaults apply. This is particularly problematic for contacts spanning several brands or personal/work addresses.

   **Fix:** store message-level From, To, Reply-To, and receiving identity; derive reply recipients/sender from the selected message or conversation. Do not silently switch a reply to another address when the original is suppressed. Validate that an explicitly supplied conversation uses the selected transport. Show the resolved route in the composer.

   Evidence: [toolReply](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/messaging.go:1862), [address resolution](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/messaging.go:738). Test: `TestAuditReplyUsesThreadAddress`.

2. **P1 — Successful merges leave identity lookup broken and can replace the winner's primary address. Reproduced.**

   The loser keeps its denormalized email/phone, and `dbGetByPrimary` accepts `status='merged'`. Looking up the loser's old email returns the merged record even though its channels now belong to the winner. Subsequent inbound mail can therefore attach to the wrong record. Both original channels also retain `is_primary=1`; when the loser was created first, the winner's primary switches to the loser's address. A later channel edit can fail because there are now two primaries.

   **Fix:** retain exactly one primary per kind, prefer the winner's existing primary, clear loser mirrors, and resolve merged IDs/addresses through an explicit survivor mapping. Reject repeated/cyclic merges and merges into deleted records. Repair existing merged data in a migration.

   Evidence: [merge implementation](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:3224), [address lookup](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:2573). Test: `TestAuditMergeLookupAndPrimary`.

3. **P1 — Merging two contacts with SMS or WhatsApp history fails. Reproduced for SMS.**

   The merge directly rewrites conversation ownership. Migration 009 enforces one persistent conversation per project/contact/transport. When both contacts have an SMS conversation, that update violates the unique index and rolls back the entire merge. The same constraint applies to WhatsApp.

   **Fix:** consolidate the persistent conversations inside the merge transaction, repoint activities and participants, reconcile status/priority and recency, then delete the redundant conversation. Preserve email threads individually.

   Evidence: [conversation move](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:3292). Test: `TestAuditMergePersistentConversations`.

4. **P1 — Activity writes do not verify contact ownership. Reproduced.**

   Calling `contacts_log_activity` for project A with a contact ID belonging to project B succeeds. It inserts an A-partition activity referring to B's contact. The foreign key checks only the contact ID; the contact timestamp update affects zero rows and does not reject the write. The HTTP activity handler reaches the same database function. This demonstrates cross-project relational corruption, not a proven cross-project read leak.

   **Fix:** validate a live, project-owned contact within the write transaction or use an ownership-checked `INSERT ... SELECT`. Add composite project/contact foreign keys where practical. Apply the same invariant to routing rules and segment list references, which currently accept foreign-project IDs without ownership validation.

   Evidence: [activity handler](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:2020), [database write](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:3362). Test: `TestAuditActivityProjectOwnership`.

5. **P1 — Reconciliation can erase hard-bounce evidence. Reproduced.**

   After a hard-bounce event, an empty Messaging suppression snapshot changes the channel from `hard_bounced` to `active` and makes it messageable. The code similarly resets `complained` and `unsubscribed`. Absence from one suppression snapshot is not evidence that an independent delivery failure was reversed; this matters with event/snapshot lag, missing suppressions, or a changed binding.

   **Fix:** separate delivery evidence from the effective suppression overlay. Refreshing suppressions should update only suppression fields. Require an explicit, attributable recovery operation to clear durable bounce/complaint state. Add out-of-order delivery/suppression and stale-snapshot tests.

   Evidence: [reconciliation reset](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/deliverability.go:728). Test: `TestAuditReconcilePreservesDeliveryEvidence`.

6. **P1 — Thread matching can attach one contact's message to another contact's conversation. Reproduced.**

   The first In-Reply-To lookup checks contact ownership, but the root-message fallback and References lookups only check the project. A message from contact B referencing A's root message is stored with B's contact ID and A's conversation ID. Conversation reads and reply actions then operate on conflicting ownership. This can happen with forwarded/shared threads; supplied headers are sufficient to trigger it locally.

   **Fix:** either consistently require the same contact, or explicitly model conversations as multi-contact objects with message-level senders and correct reply targets. Do not combine the current single-owner model with unchecked cross-contact threading.

   Evidence: [root/reference fallback](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/messaging.go:3073). Test: `TestAuditCrossContactThreadLink`.

7. **P1 — A delayed request can show/edit the wrong selected contact. Reproduced with controlled promises.**

   Select A, then B; let B's requests finish before A's. The selected ID remains B, but A's late response replaces the detail pane. Actions based on `detail.id` target A while the selected list row indicates B; conversation status actions use `selectedId`, making the inconsistency worse. Contact searches and several refresh callbacks also lack stale-result guards.

   **Fix:** use an AbortController and/or generation token for all contact/detail loads, keyed by project, install, and selected contact. Clear dependent state on project/install changes. Guard mutation-response refreshes too. The Inbox already has sequence guards that can inform the implementation.

   Evidence: [selectContact](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/ui/CrmPanel.tsx:837). Reproduction: [audit-ui-repro.ts](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/audit-ui-repro.ts).

8. **P2 — Archiving makes contacts inaccessible while reserving their addresses. Reproduced.**

   The UI promises archived contacts remain viewable through the Status filter. The archive endpoint sets both `status='archived'` and `deleted_at`; every search requires `deleted_at IS NULL`. The record cannot be fetched for restoration, and upserting the same email fails against the channel uniqueness constraint. An inbound message from that address can consequently fail ingestion.

   **Fix:** distinguish archival from deletion. Make archived contacts readable, provide restore, and define whether inbound activity restores or remains attached to an archived contact. Keep true deletion as a separate operation with explicit address-reservation rules.

   Evidence: [archive handler](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:2288), [search conditions](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:2612). Test: `TestAuditArchivedContactRecovery`.

9. **P2 — Updating primary_email/primary_phone can make the displayed address disagree with the send address. Reproduced for email.**

   Scalar patches permit writes to the denormalized primary columns without changing `contact_channels`. After changing primary_email to a new address, lookup/display use that address while sending still uses the old channel. This also bypasses the channel-based email verification path.

   **Fix:** make primary mirrors read-only, or translate such patches into normalized channel updates in the same transaction. Return a validation error for ambiguous changes.

   Evidence: [scalar patch allowlist](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:2833). Test: `TestAuditPrimaryScalarMaintainsChannels`.

10. **P2 — Date-based saved segments generate invalid SQL. Reproduced.**

    Saving a segment with `created_at >= ...` passes validation. Evaluation generates `c.datetime(created_at)` by prepending `c.` to an already-built expression; SQLite rejects it. Other timestamp comparison fields follow the same path. Audience resolution and materialization inherit the failure.

    **Fix:** pass a table alias into the field compiler and produce `datetime(c.created_at)`. Validate by preparing the generated query, and test every supported core-field/operator combination through search, segment evaluation, and audience resolution.

    Evidence: [alias concatenation](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/segments.go:112). Test: `TestAuditDateSegmentExecutes`.

11. **P2 — Typed attribute filters return incorrect audiences. Reproduced.**

    A stored date is in `value_date`, but any string filter chooses `value_text`, so date equality never matches. `is_null` also chooses `value_text`: a populated numeric attribute matches as null, while a contact with no attribute row does not match at all because the expression uses EXISTS. Both search and segments share this code.

    **Fix:** resolve the attribute definition and compile against its declared type. Define missing-row/null semantics explicitly and use NOT EXISTS or equivalent logic for unset values. Cover date, number, bool, multi-select, empty, missing, and null values.

    Evidence: [attribute column choice](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/segments.go:280), [search EXISTS](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:2720). Test: `TestAuditTypedAttributeFilters`.

12. **P2 — Editing a segment silently changes number/bool predicates to strings. Reproduced.**

    `predicateToDraft` stringifies values; `draftToPredicate` persists attribute values as strings. Opening and saving a predicate with value `42` or `true` changes it to `"42"` or `"true"`. The current backend then searches the text column and changes the audience.

    **Fix:** use attribute definitions in the segment editor and preserve the original JSON type. Reuse the typed field editor. Add a round-trip invariant for all supported predicates. Also reject incomplete predicates instead of silently removing them and potentially saving an all-contacts definition.

    Evidence: [segment conversions](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/ui/CrmPanel.tsx:4851). Reproduction: `audit-ui-repro.ts`.

13. **P2 — Events from several global-install HTTP mutations have no project. Reproduced.**

    A request to create a list for project A writes the correct database partition but emits `list.created` with an empty project. Lists, segments, archive, and some other paths use the mount-time `globalCtx.Emit` instead of explicit project emission. The pinned SDK sends these events to the wildcard lane, so project-specific UI listeners and consumers miss them.

    **Fix:** consistently call the existing `emitCRMEvent(ctx, pid, ...)` helper, or derive an immutable request-scoped context. Audit all direct Emit calls and test HTTP mutations in a global installation.

    Evidence: [HTTP list event](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/lists.go:634), [project-aware helper](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/messaging.go:1227). Test: `TestAuditGlobalHTTPEventProject`.

14. **P2 — Membership retries emit duplicate business events. Reproduced.**

    Adding an existing membership is a database no-op but still emits `list.member.added`. Inbound routing emits the event on every matching message, and removing a missing membership similarly emits removal. Consumers that start workflows on membership changes can run repeatedly even though membership did not change.

    **Fix:** return a changed flag from add/remove operations using RowsAffected, and emit only on transitions. Preserve project IDs and add durable event delivery for business-critical workflows; current SDK emission is asynchronous and best-effort.

    Evidence: [add membership](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/lists.go:433), [routing actions](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/db_routing.go:102). Test: `TestAuditMembershipEventsAreIdempotent`.

15. **P2 — CRM conversation reads omit the tail of long messages. Reproduced.**

    Inbound and outbound message bodies are truncated at 4,000 bytes before storing the activity. A 4,528-byte test message becomes 4,003 bytes including the ellipsis. Conversation reads return that shortened body; Messaging enrichment fetches status/attachments, not the full body. HTML-only inbound payloads also have no text fallback because `BodyHTML` is not used in ingestion. Operators and agents cannot rely on this surface to read the complete message.

    **Fix:** keep snippets separate from full content. Persist full text or lazily fetch it from Messaging, and expose explicit truncation/full-content links when returning bounded context. Convert HTML-only messages safely to text or render sanitized content.

    Evidence: [inbound activity construction](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/messaging.go:2910), [message enrichment](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/messaging.go:462). Test: `TestAuditFullMessageBody`.

16. **P2 — Contact timestamps are unreliable for ordering and incremental sync. Reproduced.**

    Logging an older activity unconditionally moves `last_contact_at` backwards. Message logging does the same to conversation recency. Custom-attribute writes do not bump contact `updated_at`, so incremental consumers filtering on that field can miss changes. Writes mix RFC3339 and SQLite CURRENT_TIMESTAMP; ordering raw timestamp strings can misorder updates on the same day.

    **Fix:** define one timestamp representation, use chronological MAX/MIN for activity recency, set first-contact time consistently, and update contact modification metadata for attribute/channel/tag/list changes according to a documented contract. Use a stable ID tiebreaker for paginated ordering.

    Evidence: [activity recency](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:3362), [attribute write](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:3452), [search ordering](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/main.go:2694). Tests: `TestAuditRecencyDoesNotRegress`, `TestAuditAttributeWriteUpdatesContactTimestamp`.

17. **P2 — The opportunities board silently omits non-default pipelines and records beyond its page limit. Source-confirmed.**

    The panel fetches at most 200 opportunities across all pipelines, then renders only the default pipeline's stages. Opportunities from other pipelines consume the page budget but have no rendered column. There is no pipeline selector or opportunity pagination. Lists similarly display only the first 100 members; their database endpoint has no offset/cursor.

    **Fix:** select a pipeline explicitly, filter before pagination, expose total/has_more, and page or virtualize the board. Add list-member pagination. Clearly distinguish an empty result from a failed request; current opportunity loaders turn errors into empty arrays.

    Evidence: [opportunity fetch](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/ui/CrmPanel.tsx:668), [board rendering](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/ui/CrmPanel.tsx:2805), [list-member fetch](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/ui/CrmPanel.tsx:4429).

18. **P2 — Concurrent opportunity edits can violate lifecycle consistency. Source-level race; not deterministically reproduced here.**

    `dbOpportunityUpdate` reads the existing stage/status before beginning its transaction. A title edit that reads an open opportunity always schedules `closed_at=NULL`. If another request closes the opportunity before that edit commits, it can clear the close timestamp while leaving status won/lost. Two concurrent stage moves can also log history from the same stale previous stage. Stage archival/category changes similarly check for active opportunities separately from their write.

    **Fix:** perform state reads and invariant checks inside the write transaction, or use a revision column and compare-and-swap with a conflict response. Read the post-change snapshot consistently for event emission. Add barrier-controlled concurrency tests; a passing Go race detector cannot detect these SQL logical races.

    Evidence: [opportunity update](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/opportunities.go:734), [stage mutations](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/opportunities.go:474).

19. **P2 — Inbound ingestion is only partly idempotent and atomic. Source-level failure window.**

    Contact creation, routing, conversation creation/reopening, activity insertion, participant insertion, and list attachment commit separately. A concurrent duplicate email can create an extra conversation before losing the activity uniqueness race. A failure after activity insertion makes retry take the early dedup return, so it does not repair missing participants, list assignment, or events. Attachment reconciliation is handled, but the rest of ingestion is not.

    **Fix:** use a transaction for local ingestion state and a durable idempotency record; commit business events to an outbox. On retry, repair/replay all pending work. Retain source Messaging install identity with message/attachment IDs: current activity dedup keys only contain project plus a Messaging-local integer ID, which can collide after rebinding to a different install.

    Evidence: [ingestion ordering and early return](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/messaging.go:2779), [activity uniqueness handling](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/messaging.go:564).

20. **P2 — Suppression reconciliation scales as routes × suppressions. Measured.**

    For every channel route, `effectiveSuppression` scans the suppression list twice and normalizes addresses repeatedly. The worker then rewrites every route in one transaction every five minutes, even when nothing changed. The pinned SDK uses a single SQLite connection, so that transaction blocks the app's other database work.

    Local Apple M1 Pro microbenchmark, one unmatched route: 100 suppressions ≈24.7 µs; 1,000 ≈165 µs; 10,000 ≈1.86 ms. At that last rate, 100,000 routes would imply roughly 186 seconds of lookup CPU alone. That is an extrapolation, not a production load-test result; SQL writes add further cost.

    **Fix:** pre-index suppressions by transport/address/domain once per snapshot, calculate only changed rows, and use short bounded write batches. Protect against a stale snapshot overwriting newer event state. Add a realistic benchmark budget and worker duration/queue metrics.

    Evidence: [suppression matching](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/deliverability.go:655), [worker transaction](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/deliverability.go:696). Benchmark: `BenchmarkAuditSuppressionScan`.

21. **P2 — Inbox previews scan an activity index, and event refreshes amplify cross-app calls. Query plan verified; network amplification source-confirmed.**

    The preview subquery filters only by conversation ID, while the useful index begins with project ID. EXPLAIN reports `SCAN a USING INDEX ix_act_conv` and a temporary B-tree sort. This work is repeated for conversation rows. Thread reads separately call Messaging `message_get` once per activity, up to 200 calls with eight workers. A single inbound message emits multiple CRM events; the Inbox refreshes on each, and list refresh also triggers a selected-thread reload. Sequence guards discard stale responses but do not cancel the underlying work.

    **Fix:** include project ID in the snippet predicate, align indexes with project/conversation/chronological order, and select a bounded page before expensive enrichment. Batch/cache message status lookup. Coalesce events, refresh only affected threads, and cancel superseded requests.

    Evidence: [inbox query](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/messaging.go:2535), [per-message enrichment](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/messaging.go:462), [Inbox refresh listeners](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/ui/CrmPanel.tsx:3423). Diagnostic: `TestAuditDiagnosticsInboxQueryPlan`.

22. **P2 — Shipped system attributes are never initialized. Reproduced.**

    Migration 002 creates ten templates, including lifecycle, lead score, timezone, and do-not-contact. No application code reads that template table. Creating a project's first contact leaves its attribute definitions empty. The README and code comments describing lazy initialization are therefore inaccurate.

    **Fix:** seed per-project definitions idempotently before the first relevant read/write. Define and test actual do-not-contact behavior across sends and audience resolution; currently no eligibility code checks that attribute, so simply exposing the field would not enforce it.

    Evidence: [attribute templates](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/migrations/002_seed_system_attributes.sql:13). Test: `TestAuditSystemAttributeSeeding`.

**Additional changes worth making after the correctness fixes**

- Validate channel kinds, transport values, scalar statuses, positive IDs, dates, finite numbers, and body shapes uniformly across HTTP and MCP. Several handlers trust casts or ignore unknown fields; SQLite constraints are then the only validation. Keep syntax validation separate from optional deliverability verification.
- Make snapshot semantics explicit. Static-segment creation does not populate a snapshot; changing kind alone can produce an empty audience. `not_in_segment` silently uses only snapshots even for dynamic references. Reject unsupported references or implement them, and make create/materialize/update behavior clear in the UI.
- Bound all collection endpoints consistently. Static snapshot reads do not apply the dynamic path's 5,000 maximum; list/segment eval APIs described as paged have no cursor, while the newer audience resolver does. Reuse the audience cursor contract where appropriate.
- Limit email-verification work per request and per install. The semaphore limits active calls per request, but one goroutine is still created per submitted email, and several concurrent requests multiply that limit. Pass request cancellation through long-running verification, enrichment, and database work.
- Keep routing settings behind CRM's bound Messaging backend. Sender/template reads use that backend, but Settings calls Messaging directly without an install ID and automatically writes catch-all routes on mount. This can configure a different install in multi-install setups and unexpectedly recreate routing choices. Make setup explicit and idempotent; avoid three routing writes before every send/retry.
- Strengthen read errors. Many loops silently skip failed scans or omit `rows.Err()`, and some mutation responses ignore failed relation reloads. Return a clear partial/error state rather than apparently complete data.
- Protect unsaved edits and full-array channel updates. Switching contacts drops drafts; simultaneous channel edits can overwrite one another. Use dirty-state handling and record revisions or narrow channel mutation endpoints.
- Review automatic-mail handling as a product policy. Ingestion drops all classified automated messages before even checking for an existing contact. Some automated replies and confirmations are useful conversation history. Separate lead creation suppression from message retention, and allow a configurable review lane.
- Remove obsolete ContactsPanel assets or clearly identify the supported UI, update the v0.1 README, and generate manifest/tool contracts from one source. The release contains duplicate embedded/disk manifests and outdated descriptions of features that now exist.

**Recommended implementation order**

1. Repair identity and ownership: findings 1–7 and 9. Add merge cleanup migrations, reply route invariants, and controlled concurrent UI tests.
2. Repair record lifecycle and audience correctness: findings 8, 10–12, 15–16, and 22. Include archived-record recovery and existing segment/data migration tests.
3. Make events and concurrency dependable: findings 13–14 and 18–19. Use project-aware events, transaction boundaries, revisions, and a durable outbox for automation triggers.
4. Fix visibility and performance: findings 17, 20–21. Add pipeline/member pagination, indexed queries, batching, and event refresh coalescing.

Acceptance should include all reproductions passing after their corresponding fixes, plus real CRM↔Messaging tests using local companion sidecars, multi-project/multi-install cases, migration fixtures with existing merged/archived data, and browser tests for selection, replies, and segment editing. Keep the current race/vet/integration checks. Prefer these targeted changes over a rewrite: the release already has useful transactional create/update logic, scoped reads, message idempotency, and bounded context reads that should be preserved.

**Reproduction files and commands**

- [Go regression cases and performance diagnostics](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm/audit_repro_test.go)
- [Recorded Go failures](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/audit-evidence/go-reproductions.txt)
- [UI reproduction script](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/audit-ui-repro.ts)
- [Recorded UI output](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/audit-evidence/ui-reproductions.jsonl)
- [Integration test output](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/audit-evidence/integration-tests.txt)
- [Coverage profile](/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/audit-evidence/coverage.out)

Run from `/Users/marcoschwartz/Documents/code/apps-crm-audit-0.9.1/mcp/crm`:

```sh
env GOWORK=off go test ./... -count=1
env GOWORK=off go test -race -cover ./... -count=1
env GOWORK=off go vet ./...
env GOWORK=off go test -tags integration -run '^TestSidecar_' -v -count=1
env GOWORK=off go test -tags audit -run '^TestAudit' -v -count=1
env GOWORK=off go test -tags audit -run '^$' -bench '^BenchmarkAudit' -benchmem -benchtime=100ms
```

Run the UI reproduction from the audit worktree root with `bun audit-ui-repro.ts`. The audit-tagged tests deliberately assert desired behavior and therefore fail on the untouched release; they are not failed modifications to application code.
