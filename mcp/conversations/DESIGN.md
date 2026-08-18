# conversations — design

One app owning every conversation surface: internal dashboard chat, the
inbox (approvals / reports / alerts / status), and external channels
(Telegram, Slack, …) as optional integration bindings. Developed in
parallel with — and without touching — apteva-server's built-in
`channel-chat` and the deprecated `channels` sidecar. Both are donors;
neither is modified.

## Why one app

The inbox has no data of its own. Every inbox item IS a message: an
agent's `request_approval` appends a message with a typed card into its
conversation, and the inbox is a priority-ordered query over those rows.
Splitting inbox from conversations would mean mirrored stores, duplicated
unread bookkeeping, and a cross-app hop inside the approval round-trip —
the single most valuable behavior in the system.

External channels are not conversation *types*. A Telegram thread is an
ordinary conversation with a transport binding: an `origin` label, a
`conversation_key` carrying the external identity
(`telegram:<binding>:<chat_id>`), and an externally-keyed participant.
Features (rooms, approvals, inbox) never branch on origin; per-transport
differences (buttons? edits? attachments?) live in the adapter.

## Model

- `conversations` — id, project, lead agent, kind (direct|room), origin
  (web|telegram|slack|app), conversation_key (external identity, unique).
- `messages` — role, content, `component_kind` (approval|report|alert|
  status — a real indexed column, replacing channel-chat's
  `LIKE '%"approval-card"%'` scans), components/attachments/metadata
  JSON, `client_message_id` idempotency key.
- `participants` — agent, platform user, or external identity
  (`telegram:12345`) with an optional contact URI. Strangers never become
  platform users implicitly.
- `deliveries` — the ledger. One row per (message, target); pending rows
  are redelivered on mount (crash-safe sends, the Hermes pattern).
- `pairing_codes` — DM policy for unknown external senders: pairing
  (default) / allowlist / open / disabled per binding.

## Surfaces

- **MCP tools** (agents): `conversations_send`, `_request_approval`,
  `_report` (inbox-only), `_alert`, `_set_status`, `_list`, `_history`.
- **Inter-app**: `inbox_post` — any sidecar raises inbox items via
  `CallAppResult("conversations", "inbox_post", …)`, with an optional
  callback tool invoked on action. This makes the app the platform's
  notification provider; no other app needs its own inbox.
- **HTTP** (dashboard): /chats /messages /stream (SSE) /inbox
  /message-action /seen /pairing. (Reserved prefixes /health /manifest
  /mcp /events /ui/ are avoided — guarded by a test.)
- **Adapters**: `web` (the SSE hub) is channel zero and always on;
  `telegram` ships as a compiling stub gated on its optional binding.

## Thread-per-conversation

Every conversation gets its own core thread ("chat-<conversation id>",
channel-chat's naming so thread identities survive the data migration),
spawned lazily on first inbound message via the SDK's `ThreadClient`
(`platform.threads.write`). The directive suffix carries the
conversation's context and reply contract; composition with the agent's
main directive happens core-side. MCP is left nil so the platform
supplies the agent's spawnable set. Spawn is idempotent; a changed
suffix re-spawns (the sidecar-visible approximation of channel-chat's
drift update). Every failure — stopped agent, no ThreadClient, spawn
error — degrades to the main thread via SendEvent and is retried on the
next message; a message is never lost to a thread problem.

Known parity gaps that need SDK additions (not app work): atomic
spawn-with-first-event, and an UpdateThread/ThreadTools surface for
true in-place drift correction.

## Approval round-trip

message-action mutates the card in place, publishes the updated row to
every surface (one row — transcript, inbox widget, external chat all
agree), and forwards an `approval.result` event to the owning agent's
thread via the platform API. Writer, store, and forwarder stay in one
process.

## Streaming (v0.2.0)

Ephemeral bubbles ride a named `stream` SSE channel, produced by a port
of channel-chat's streamer. Two feeds: the platform telemetry bridge
(`platform.telemetry.read` → llm.tool_chunk/tool.call/tool.result for
chat-* threads) gives token-level text; without it the app emits
acknowledgement/done phase frames around forward/reply — same bubble
lifecycle, no token text. The panel renders either without knowing
which. Frames never persist; the durable row always wins, and settled
call ids are tombstoned so a late frame cannot resurrect a bubble.

RELEASE DEPENDENCY: v0.2.0 requires an app-sdk release carrying
TelemetryClient (>= the version that ships sdk/telemetry.go) and a
server carrying /api/apps/callback/telemetry. On older platforms the
app runs with phase-frame streaming only.

## Later phases (not in this repo yet)

1. Server-side relay: the frozen per-agent `channels`/`agent-output`
   MCPs forward into this app (running cores never notice).
2. Dual-write from `channel-chat`, then flip dashboard reads.
3. One-time data migration of `channel_chat_*` rows (idempotent,
   refuses-to-guess — same discipline as the providers migration).
4. Telegram adapter goes live (long-poll + webhook), then Slack.
5. Deprecate the `channels` sidecar and shrink `channel-chat`.

## Testing in isolation

Everything runs against the SDK testkit with a recording platform stub —
no apteva-server involved. `APTEVA_GATEWAY_URL=… go run .` boots it
standalone against a dev server when wanted, but no test requires one.
