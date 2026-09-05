# Conversations design

Conversations owns dashboard chat, approvals, reports, alerts, and an optional
Telegram transport. The inbox is a projection of the same durable messages as
the transcript. Agent status belongs to the Status app; historical status rows
remain inert history. This repair is based on `conversations/v0.18.2`.

## Authorization and lifecycle

Every conversation belongs to a project. An ownerless conversation is visible
to project users; otherwise only its owner and explicit user participants may
read it. REST, message and stream SSE events, transport metadata, and delivery
inspection/retry apply that resource rule. Private cards have no project-wide
content delivery. A public *audience* controls visitor-facing agent behavior;
it does not remove authentication or make a private row project-visible.

Creation initializes the roster transactionally. Reusing a key requires access
before returning the row. `app:<name>:inbox` is reserved for authenticated app
callers. Agent-created topic keys are deterministic per agent/title. Archiving
releases those generated topic keys; explicit archived keys require unarchiving
instead of silently creating another conversation. Rooms allow at most eight
agents, and the lead cannot be removed without a separate leadership change.

Archived conversations are read-only, including card actions and settings.
Archiving cancels queued/claimed deliveries atomically; recovery never spins on
archived work. Unarchiving does not replay previously cancelled work. Deletion
cascades message/route data and records retired thread identities so a surviving
Core thread cannot acquire global Conversations access after losing its binding.
Retirement is an authorization policy, not a forced stop of arbitrary Core work.
A provider request already accepted before archive/delete cannot be recalled.

Telegram `/new` copies owner, audience, directive and agent roster in the same
transaction as switching the route. Undelivered messages for the old Telegram
route are cancelled; other surfaces retain the old transcript. A durable command
receipt makes a retried `/new` return the same replacement conversation.

## Message and delivery contracts

`messages` stores content, typed cards, attachments, metadata, authorship and a
request digest. A message and all initial `deliveries` commit in one transaction.
Stable client request IDs reject reuse with different payloads. `inbox_post`
accepts `client_message_id`, scoped to the authenticated source app install;
callers must supply it when retry safety is required. Legacy rows without a
request digest compare their immutable author/content fields.

Content is limited to 256 KiB; the serialized message to 1 MiB; attachments to ten
images and 768 KiB per data URL. These checks apply at the shared store boundary
for HTTP, MCP and transport writes. Images remain inline within these bounds.

The executable runs four delivery workers. HTTP/MCP persists and returns without
waiting for remote providers. Web publication remains immediate. Workers claim
rows with an atomic lease and token; completion checks that token. A heartbeat
extends active leases. Payloads are read after claiming their generation, so an
edit cannot be acknowledged using an older recovery-scan payload. Concurrent
edits requeue the newer generation. Work per recovery pass is bounded.

Telegram routes serialize pending/processing/ambiguous messages in ID order.
Retries back off, then become visible failures after ten attempts; unsupported
payloads fail immediately. Timeout/uncertain send outcomes and expired Telegram
leases become `ambiguous`, blocking automatic replay and later route messages.
Only explicit operator retry can risk a duplicate. Card edits preserve ambiguous
or cancelled state. There is no unconditional exactly-once Telegram guarantee.

`/deliveries?chat_id=...` shows recent delivery states in chat, including queued,
sending, retrying, delivered, cancelled, failed and ambiguous. Failed/ambiguous
rows have authorized retry actions. The same route with `stats=1` reports queue/failure counts, oldest age and mean delivery latency per target/state. Tracked approval execution callbacks are persisted idempotently by increasing sequence; they do not claim business success. SDK HTTP clients bound network requests;
a delivery heartbeat is not an application-level cancellation deadline.
Confirmed non-Telegram ledger history and redundant change revisions are pruned
after 30 days. Telegram links/parts and retirement identities are retained while
needed for deduplication and authorization. Message history is user-owned and
is not automatically deleted.

## Agent threads and approval results

The app persists `(conversation, agent, thread)` ownership before starting Core.
Every inbound user message uses `EnsureThread` with the desired directive/tool
profile and a stable event ID. Accepted or duplicate receipts confirm delivery.
An explicitly unsupported ensure endpoint may use the legacy spawn API with the
same event; other failures remain queued. There is no fallback to a different
thread. Eventless profile caches are bounded and expire after five minutes.

Conversation-thread tools resolve that persisted binding, not a thread-name
prefix. They may operate only on their originating conversation. Main manages
global conversations/reports. Generic workers report to their parent. Public
visitor requests needing operator attention are delegated to main, which writes
into an operator conversation; the public thread cannot write across that boundary.

Approval resolution is an atomic pending-to-verdict transition plus outbox write.
The original requesting agent/thread receives a stable `approval.result` through
SDK `AgentEventClient.SendTrackedAgentEvent`. The event includes the original conversation ID and asks for receipt acknowledgement before further work. Its receipt must match both the
source event ID and original thread and be accepted or duplicate. This API works
for main as well as non-main threads, without replacing their profiles. The
platform may restore the exact thread identity after restart; an unavailable
platform/destination remains a visible retry/failure, never a main-thread reroute.
A platform without tracked-event support cannot confirm approval delivery. The minimum Apteva release is 0.40.6, which introduced tracked agent lifecycle delivery (server 0.23.0 / SDK 0.70.0); this app retains its SDK 0.73.0 pin.
Sibling-app callbacks receive a stable `operation_id`; the recipient must honor
that key for its own effects to be idempotent.

## History, inbox and streaming

`message_changes` journals insert/content/card/action revisions transactionally.
Card changes requeue surface delivery in the same transaction. Each row carries its latest journal revision so delayed older page/SSE responses cannot overwrite a newer edit. Acknowledgement frames carry the triggering message ID so loading historical replies cannot settle current progress. The latest
transcript page and its change cursor are read from one database snapshot.
`/messages?page=1` returns latest messages and backward pagination; `/changes`
replays durable revisions. SSE subscribes before backlog lookup, sends heartbeats,
and closes an overflowing durable subscription to force reconciliation. SSE
message IDs never advance the client's durable replay cursor.

The React controller keeps message rows and active stream bubbles separately.
Only completed change pages advance replay. Drafts and complete pending send
requests are stored per project/conversation. Remounts, failed sends, delayed
responses and text typed during a pending send do not move messages or drafts
between conversations. Read marks require a visible loaded transcript at its
bottom; selection alone does not mark unread history seen.

Inbox priority and dismissal filtering occur in SQL before pagination. Responses
include accurate totals and attention ranks across all matching rows. Conversation
search also filters before limiting, with cursor pagination and direct ID lookup.

Telemetry requires a persisted participating-agent/thread binding and validates
the destination before preview publication. An incremental parser handles escaped
text and Unicode, with bounded buffers, expiration, and publication throttling.
Frames carry agent/thread/call/run identity so reused provider call IDs and
concurrent room responses stay separate. Fallback acknowledgement frames work
without telemetry; durable messages remain authoritative.

The shared transcript displays sender IDs, supported image attachments, report
sections and a readable JSON fallback for arbitrary app components. It does not
execute arbitrary third-party component code. Unsupported images show an explicit
placeholder. UI bundles use the production React runtime and are verified against
the dashboard's import surface.

## Telegram transport

Connections and credentials stay platform-managed. App-side route lookups check
only the requested bound connection and its current project/status. Pairing is
fail-closed; public intake is explicit. Unknown senders' unapproved message bodies
are not retained. Invitation secrets are hashed and expire; webhook secrets are
checked before handling updates.

Inbound updates use heartbeat-renewed processing leases and a separate completion marker. A retry
of unfinished work receives a retryable response while leased, and can recover
after expiry. Only completed duplicates are acknowledged as done. Ordinary
messages have stable update-derived request IDs, and `/new` has transactional
command receipts. Cleanup never deletes unfinished update claims.

Commands explicitly addressed to a different bot and partial username matches
are ignored. Textless inbound media gets an explicit unsupported-content reply.
Long outgoing replies split at 3,500 UTF-16 units with durable per-part IDs;
confirmed parts are reused/edited and obsolete parts are removed. Multipart
messages use plain text to preserve content without broken HTML boundaries.
Single-part messages use the safe Telegram formatter. Unsupported outgoing media
is a visible terminal delivery failure rather than silent truncation or retries.

## Validation

Build with Go 1.26.8 or newer; the module minimum and x/sys 0.44.0 remove known standard-library and Windows dependency advisories found during the audit. Go 1.26.8 is an official supported patch release: https://go.dev/dl/#go1.26.8.

Run from this app with the pinned SDK rather than the workspace overlay:

```
GOWORK=off go test ./... -short
GOWORK=off go test -race -tags integration ./... -count=1
GOWORK=off go vet ./...
GOWORK=off go test -run '^$' -bench BenchmarkAuditStreamSizes -benchmem
```

From the apps repository root:

```
bun test mcp/conversations/ui
bun run scripts/build-panels.ts --app conversations
```

Tier 1 covers store, permissions, retries, concurrency, migrations and regression
invariants. Tier 2 starts actual sidecar binaries and uses real local HTTP with a
stub platform. React behavioral tests exercise snapshot/SSE ordering, switches,
retries, read visibility, draft preservation and multi-agent streams.

Tier 3 requires a real server/Core, a working `openai-codex` connection, this exact
candidate installed, and auto-attachment to newly created test agents:

```
APTEVA_BASE_URL=... APTEVA_API_KEY=... APTEVA_LIVE_PROJECT_ID=... \
  GOWORK=off go test -tags live -run '^TestLive_' -v -count=1 -timeout 30m
```

The tests create/clean temporary agents and conversations. Use a dedicated local
validation server, not a production user's existing conversations. Real Codex
coverage does not substitute for real Telegram/network fault testing. See the
repository audit-fix report for measured results and candidate provenance.
