# CRM v0.9.23

Release `crm/v0.9.23`: save first-outreach drafts for existing contacts and
outbound follow-ups without requiring an inbound conversation. New messages
have Save draft / Save & close and reopen from the contact's Saved message
drafts; outbound-only inbox threads offer Follow up. Saving creates no inbox
conversation or message. All composer sends use the same durable saved-draft
workflow. Recipients stay pinned, send eligibility is rechecked, and uncertain
delivery retries keep the original operation key. Migration 023 preserves every
existing draft field; no message or conversation history is rewritten.
Messaging is unchanged. CRM pins app-sdk v0.99.0.

Release `crm/v0.9.22`: background refreshes preserve the loaded thread,
scroll position and draft shelf instead of replacing them with a loading screen.
CRM/Messaging live updates are coalesced and slow requests get one queued
follow-up rather than repeated cancellation. The selected conversation remains
highlighted as "Currently viewing · outside filters" when it leaves the queue;
choosing another conversation removes that retained row. Queue counts and
pagination exclude the retained row. Failed refreshes keep the last snapshot
with an inline retry, and navigation rejects stale thread responses. UI-only:
no Messaging changes, schema migrations or production data mutations.

Email, WhatsApp and SMS message rows show
explicit Contact/Your team and Incoming/Outgoing labels, distinct left/right
alignment and an outgoing tint in both inbox and contact timelines. Names are
shown only when the recorded From matches the contact or the channel-specific
sender. Actual addresses, delivery status, attachments and template snapshots
remain visible. Integration source is labelled "via", not treated as an author;
unknown historical senders are not guessed. Failed/test sends remain explicit.

Release `crm/v0.9.21`: Conversation messages display immutable template content
captured by Messaging v0.13.60+, together with the template name/ID and whether
the body was confirmed by the provider or captured locally at send time. CRM
records the returned snapshot alongside the sent activity and can retain it
when Messaging is unavailable. Existing snapshots are never replaced by current
template definitions. Incomplete and historical template messages explicitly
report unavailable sent text; no fabricated backfill or historical data repair.
Preserves unsubscribe, typed attributes, drafts and persistent inbox selection.
No CRM schema migration. Messaging adds one nullable snapshot column, without
rewriting legacy messages or changing ContentSid provider request semantics.

Release `crm/v0.9.20`: Corrects the source-install pin to the matching release
tag. Versions 0.9.17–0.9.19 advertised newer features but mistakenly instructed
source installers to build 0.9.16. This release delivers the existing typed
attribute, exact-email unsubscribe, and persistent inbox selection fixes.
A release-contract regression test checks both disk and embedded manifests
against the advertised version, repository and entry; the plugin identity is
also checked by the existing tests. No Messaging changes or database migrations.

Release `crm/v0.9.19`: Replying still changes an open conversation to pending,
but the inbox keeps that conversation selected so its sent message and delivery
status remain visible. Live events, manual refresh and pagination likewise
preserve the viewed thread even when it leaves the current queue. A notice
explains why the conversation is absent from the filtered list. Clicking another
conversation or deliberately changing filters still navigates normally.
Regression coverage includes an empty open queue and deep-link selection.
All v0.9.18 unsubscribe features are retained. No backend status semantics,
Messaging changes, data migration or production data repair.

Release `crm/v0.9.18`: Inbox and contact-conversation headers offer an explicit
"Unsubscribe this email" confirmation and an Unsubscribed/Email blocked badge.
The target is the latest inbound message's recorded From, verified against a
project-owned contact channel; never guessed from To, Reply-To or primary email.
`contacts_unsubscribe_email` previews by default and requires that exact
`expected_address` to apply. Matching HTTP GET/POST endpoints live at
`/contacts/{id}/conversations/{conversation_id}/unsubscribe`.

Messaging v0.13.59+ is required and capability-checked before any mutation.
The action blocks all outbound email to the exact address in this project,
including campaigns and manual replies, while preserving incoming replies and
existing stronger blocks. Confirmed readback updates channel eligibility and
adds an idempotent system audit activity. It does not send, delete, mark spam,
close a conversation, set contact-wide do_not_contact, or alter other projects,
addresses or transports. Failures/uncertain writes are shown, never hidden as
success. Email text requests are not parsed automatically. No migration or
production contact repair is performed by this release.

Release `crm/v0.9.17`: `contacts_set_attribute` recovers numeric and boolean
JSON scalars sent as strings by legacy agent adapters, using the project-owned
attribute definition. Numeric scores such as `"88"` are stored as numbers;
text such as `"00123"` stays text. Invalid/non-finite scalars are rejected before
writing. HTTP and contact-patch validation remain strict. Missing `value` no
longer clears an attribute; explicit JSON null still clears optional values.

The MCP contract documents supported JSON types and copyable examples. Agent
guidance requires `contacts_get` readback of all workflow-required fields before
advancing dossier readiness. No project-specific readiness invariant is imposed
on unrelated contacts. This release performs no data migration or score repair.

Release `crm/v0.9.14`: HTML-only emails no longer inherit huge blank gaps from nested email layouts.
The inbox also guards older inbound email display against repeated blank lines.
`contacts_refresh_message_body` can explicitly normalize formatting when the stored body exactly matches
CRM's former HTML conversion, and fill missing sender metadata from the verified Messaging original.
Both options default off; dry runs default on. Existing content, audit metadata, and workflow state are preserved.
All v0.9.13 inbound recipient ownership safeguards remain included.

Apteva's contact, inbox, audience and opportunity sidecar. The supported dashboard
is `ui/CrmPanel.tsx`, bundled as `CrmPanel.mjs`. `apteva.yaml` is embedded directly
into the binary and is the single manifest source. `MCPTools()` supplies the
executable input contracts, checked against the manifest by tests.

Release `crm/v0.9.12`: inbox rows show the latest message's recorded destination. Individual messages
show From, To, optional CC/BCC, and the receiving address when recorded. These
are message-specific headers, not inferred contact addresses or a union of
thread participants. Missing historical metadata is displayed as Unknown; no
backfill, routing change, or migration is required.

Release `crm/v0.9.11` makes `contacts_resolve_audience` delivery-health lookups
contact-first using existing indexes. Full counts, pages, and source metadata
use the read-only pool with request cancellation. Eligibility, exclusion
precedence, address selection, and pagination remain unchanged. Use
`include_counts: false` for count-free paging (count fields are then zero).
No database migration or record updates are required.

Release `crm/v0.9.10` marks `conversations_inbox` with standard MCP
`readOnlyHint: true` and `destructiveHint: false` annotations, so it is eligible
for Core's strict `access: "read_only"` tool discovery. The SDK dependency emits
these hints in `tools/list`; no Core heuristics, schema changes, or record
updates are needed.

Release `crm/v0.9.9` restores missing inbound email bodies safely, including
on installations with legacy numeric Messaging bindings. HTML-only
mail is converted to readable text even when the plain-text part contains only
whitespace. Re-delivery can fill an empty or subject-only activity body without
creating another activity, changing thread state, or replaying business events.
`contacts_refresh_message_body` previews recovery from the original in Messaging
by default; pass `id`, `activity_id`, and `dry_run: false` to repair a verified
missing body. It checks the project, source install, and RFC Message-ID and
preserves existing complete content.

Release `crm/v0.9.7` made Messaging suppression reconciliation cheaper: a
five-minute worker checks only pending soft-bounce retries, while an
unconditional full safety sweep runs every 30 minutes and applies a Messaging
snapshot with set-based SQL. The sweep leaves unchanged routes untouched and
preserves newer event updates. Email reply threading from `crm/v0.9.6` and the
Customer inbox component from `crm/v0.9.5` remain available.

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
- A configurable Customer inbox dashboard component shows open, pending or all
  conversations for one channel and refreshes from CRM conversation events.

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

## Segment definitions

`segments_create.definition` and `segments_update.patch.definition` are arrays
whose conditions are AND-ed. For example:

```json
{
  "name": "Recent VIP contacts",
  "kind": "dynamic",
  "definition": [
    {"predicate": "tag_in", "tags": ["vip"]},
    {"predicate": "last_activity_within", "days": 30}
  ]
}
```

Synthetic predicates are `tag_in`, `tag_not_in`, `attribute`,
`last_activity_within`, `channel_present`, `in_list`, `not_in_list` and
`not_in_segment`. Core contact fields use `{"field":"company","op":"eq",
"value":"Acme"}`. The MCP input schema contains copyable examples for every
shape and their required arguments.

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

## Dashboard inbox component

The suggested `customer-inbox` component is available in the dashboard home at
half or full width. It reads the existing paged `/inbox` endpoint and shows the
contact, channel, priority, automated flag, subject, latest-message preview and
relative activity time. Settings choose the default status, channel and a limit
from 4 to 20 conversations.

Rows link to `/apps/crm/page?tab=inbox` with the conversation and status encoded.
The full panel opens the Inbox tab and selects that conversation, keeping reply
composition and status mutations in the richer CRM surface. The component is
read-only and does not send messages or change CRM data.

## Messaging and automation

### Saved reply drafts (v0.9.16)

Reply → **Save draft** persists a CRM reply without sending. **Save & close**
saves before closing; existing drafts autosave after edits. Reopen them from
**Saved reply drafts** in Inbox or a contact's conversation. Multiple drafts
are supported, with source labels, last-editor timestamps and pinned From/To.
These are CRM-local drafts, not drafts synchronized to Gmail or another mailbox.

Since v0.9.23, **Send message → Save draft** also saves first outreach without
any incoming message. Reopen from the contact's **Saved message drafts** shelf.
For outbound-only conversations, **Follow up** opens a draft in the same thread,
using the historical recipient/sender instead of today's primary contact route.

MCP examples (all save only):

```json
{"mode":"message","contact_id":42,"channel":"email","subject":"Introduction","body":"Hello"}
{"mode":"message","conversation_id":123,"body":"Following up"}
{"mode":"reply","conversation_id":123,"body":"Proposed reply"}
```

With no conversation, `contact_id` and `channel` are required; mode defaults to
`message`. An optional `to` must be an existing contact channel, otherwise the
primary channel is pinned. With a conversation, mode defaults to `reply` and
requires inbound; explicitly use `message` for an outbound follow-up. Message
drafts cannot change recipient/channel/conversation/mode after creation. Use a
new draft for a different destination. Only explicit send creates a new thread.

`conversation_drafts_create/get/list/update/discard` never send. The separate
`conversation_drafts_send` is a real external send and requires explicit approval.
For example, save with `{"conversation_id":123,"body":"Proposed reply"}`;
after review, send with `{"id":456,"expected_revision":1}`. Editing, discarding
and sending require the returned `revision` as `expected_revision`; a stale
revision returns a conflict instead of overwriting another person's work.
Read-only discovery includes get/list, not save/update/discard/send.

Drafts preserve text, email HTML, attachments, template variables and the exact
inbound reply anchor. SMS/WhatsApp may explicitly switch transport in the same
phone conversation. Closed WhatsApp windows still allow drafting freeform text,
but sending requires an approved template, a live window, or an explicit SMS
switch. Template sends retain any freeform notes in the draft without sending
those notes. Sending rechecks recipient, ownership, Messaging binding, verified
sender, contact eligibility, suppression and WhatsApp window.

REST: `GET/POST /drafts`, `GET/PATCH/DELETE /drafts/<id>`, and
`POST /drafts/<id>/send`. List accepts `conversation_id` OR `contact_id`, `limit`, `offset` and
`include_finished`; it returns summaries, not attachment payloads. Discard is
soft: content is retained for audit. Draft changes publish
`conversation.draft.changed` separately from message/activity events; saves do
not change thread status or create message activities.

Send failures retain content. Known rejection leaves the draft editable.
Uncertain delivery has `status:send_failed` and locks edits/discard; retry the
**same draft** with its new revision and durable operation key, never create a
replacement send. In-progress sends use a five-minute lease; reopen after lease
expiry to retry a crashed send. A committed message is recovered without another
dispatch, and repeated successful sends return the original result. Contact
merges preserve drafts/anchors and reject in-progress or uncertain sends.
The atomic draft-table upgrade preserves all legacy drafts, including their
anchors, status, revision, authorship, attachments and durable retry state;
it does not rewrite existing messages or conversations.

### Delivery and routing

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

Contact-scoped events carry sorted `list_ids` snapshots. Activity and inbound
message events also carry `attributed_list_ids`, which identify only the lists
that directly caused or received that activity. Delete and merge events preserve
explicit before/after list snapshots, while list-scoped segment events expose
their optional `list_id`. Arrays are always present, including when empty.

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
