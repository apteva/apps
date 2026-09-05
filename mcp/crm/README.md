# CRM v0.9.2

Apteva's contact, inbox, audience and opportunity sidecar. The supported dashboard
is `ui/CrmPanel.tsx`, bundled as `CrmPanel.mjs`. `apteva.yaml` is embedded directly
into the binary and is the single manifest source. `MCPTools()` supplies the
executable input contracts, checked against the manifest by tests.

Release `crm/v0.9.2` repairs the correctness, concurrency and performance issues
identified in the `crm/v0.9.1` audit.

## Capabilities

- Contacts with multiple channels, typed custom attributes, tags, lists, activity
  history, archival/restoration, and merges with survivor IDs.
- Email, SMS and WhatsApp conversations through a bound Messaging installation;
  attachments, message status, reply routing, triage and delivery eligibility.
- Dynamic filters and static audience snapshots, cursor evaluation, and explicit
  do-not-contact enforcement in sends and messageable audience resolution.
- Multiple pipelines, stages, opportunities, lifecycle history and a paged board.
- HTTP endpoints mounted at `/api/apps/crm/*`, MCP tools, event subscribers and
  workers. Project installations use their own partition; global installations
  require an explicit project on every request.

## Data contracts

Primary email/phone are derived from `channels`. Patch the channels array to
change them; scalar primary patches are rejected. Exactly one channel per kind
may be primary. Contact PATCH accepts `expected_updated_at` from the last read;
if another writer changed the contact, the update fails. The panel sends this
field for core and channel edits. API clients should do the same to prevent lost
updates. Attribute, tag and list changes also advance contact modification time.
Dates are compared chronologically even when legacy rows use SQLite timestamps.
First/last contact timestamps track the earliest/latest recorded activity.

Archive changes status to `archived`; it does not delete the record or free its
addresses. Archived contacts remain readable and can be restored with an active
status patch. Inbound history stays attached without silently restoring them.
A merged record remains readable with `merged_into_id`; channel lookup returns
the survivor. Merging preserves the winner's existing primary channels and
combines persistent SMS/WhatsApp conversations.

Ten system attributes initialize idempotently per project. `do_not_contact=true`
blocks sends and messageable audiences. Custom values and segment operands retain
their declared number, boolean, date, string or multi-select types. Missing and
unset attributes match `is_null`. Multi-select equality compares sets;
`contains` tests membership.

Static segments populate a snapshot on creation, conversion to static, and
explicit definition updates. Materialization replaces the snapshot atomically.
Static evaluation returns frozen membership IDs; resolve an audience to filter
those IDs by current active/contact/delivery eligibility. `not_in_segment`
requires an active static segment from the same project. Dynamic references are
rejected, including when evaluating legacy definitions.

## Paging

| Surface | Contract |
| --- | --- |
| Contact search | `limit` up to 200, `offset`, `total` |
| List members | `after_contact_id`, up to 500 per page; panel loads 100 at a time |
| `lists_eval` | `after_contact_id`, default/max 5,000 |
| `segments_eval` | `after_contact_id`, default 200/max 5,000; `count` is total membership |
| Audience resolution | `after_contact_id`, default 1,000/max 5,000; eligibility counts and exclusions |
| Opportunities | Select/filter pipeline before paging; panel pages 100 records |

For list/segment evaluation, pass the returned `next_after_contact_id` into the
next request; stop on an empty `contact_ids` page. Do not use page length as the
full audience size. Static membership does not itself guarantee messageability.

## Messaging and automation

Bind Messaging in the platform's integration settings. Reading CRM Settings is
read-only. Explicit routing repair uses CRM's `/messaging/routes` backend to set
catch-all routes in that bound installation; sending does not recreate routes.

Replies use the selected inbound message's Reply-To/From and original receiving
identity. `reply_to_activity_id` selects a specific inbound message within the
conversation; `from` explicitly overrides the sender. The composer displays the
resolved route. A blocked reply address produces an error instead of switching
to another contact address. `/messaging/reply-route` previews this resolution.
New messages may choose another healthy channel according to normal preference.

Inbound ingestion commits contact/channel creation, routing, thread state,
activity, attachments, participants and its events in one SQLite transaction.
Deduplication uses project + source Messaging install + message ID. Full text is
stored; HTML-only messages get a text fallback that omits script/style content.

Existing-contact automated messages are retained. `automated_inbound_policy`
defaults to `ignore_new`; `review_new` creates contacts tagged `automated`, visible
for review and excluded from default audiences. Operators explicitly opt into
including automated contacts. Verification concurrency is bounded across the
process, with at most 100 channels accepted per write. Optional verifier policy
is independent of basic address syntax validation.

Durable bounce/complaint evidence is separate from the suppression overlay.
Suppression snapshots cannot clear it. An explicit authenticated POST to
`/messaging/delivery-recovery` with `channel_id`, `transport` and a nonempty
`reason` records a recovery and clears delivery evidence/quarantine. Messaging
suppressions remain effective until separately removed at their source.

Events carry explicit project IDs and stable `event_id` values. A local outbox
retries gateway failures; delivery is **at least once**, so downstream workflows
must deduplicate `event_id`. Membership events are inserted transactionally by
triggers and only on actual transitions. Inbound and recovery events also commit
with their mutations. Other CRUD handlers enqueue after their database commit;
there remains a process-crash gap between those two operations. Do not treat
these latter events as an exactly-once replication feed.

Workers log suppression duration/routes/changed rows/retries and event batch and
pending counts. Suppressions are indexed per snapshot and written in batches of
100, skipping unchanged state. Event delivery batches 100 and retains delivered
rows seven days, pruning 1,000 per minute; pending rows are never discarded.
Status reads use a bounded three-second cache keyed by database/project/install.

## Upgrade and operational limits

Back up the CRM SQLite database before rollout. Additive migrations 014–020
repair archive/merge/primary state, seed attributes, enforce ownership and
opportunity invariants, add source-scoped message IDs, and create the event
outbox and recovery audit. Tests apply them to a populated 001–013 fixture.
These migrations are forward changes; rollback requires restoring the backup
and previous app build together.

Legacy messages without source-install provenance retain their bodies and local
attachments but **do not fetch status/attachments from the current binding**.
This prevents attaching another installation's data after rebinding. Their old
IDs cannot be safely attributed automatically, and a provider redelivery after
upgrade may create a new source-scoped activity. Recover provenance only from a
verified Messaging backup or provider metadata. Already truncated historical
bodies cannot be reconstructed by a database migration.

Browser aborts and sequence guards cancel superseded fetches and reject stale UI
results. SDK cross-app calls already started on the backend finish within their
SDK timeout; the current PlatformClient interface has no per-call context.
Production provider delivery, historical-data completeness and host dashboard
styling need rollout checks; local tests do not establish those conditions.

## Development and checks

Run Go commands inside `mcp/crm`. `GOWORK=off` checks the pinned SDK rather than
the workspace overlay. The consumer is pinned to SDK v0.73.0, verified against
the latest tag at SDK HEAD by commit topology.

```sh
env GOWORK=off go test ./... -count=1
env GOWORK=off go test -race -cover ./... -count=1
env GOWORK=off go vet ./...
env GOWORK=off go test -tags integration -run '^TestSidecar_' -v -count=1
```

From the apps repository root:

```sh
bun audit-ui-repro.ts
bun run scripts/build-panels.ts --app crm
```

The browser fixture is opt-in: `go test -tags browser -run '^TestBrowserHarness$'
-v -timeout 30m`. It serves synthetic CRM data on localhost and stubs Messaging;
it does not use production data or send real messages. Audit regressions run in
the default suite. The root audit report records the original release failures;
`CRM-FIX-PROGRESS.md` records remediation and current evidence.
