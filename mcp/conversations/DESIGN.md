# conversations — design

One app owning the replacement conversation surfaces: internal dashboard
chat, the inbox (approvals / reports / alerts / status), and Telegram as an
optional connection-backed transport. Slack and other transports remain
behind the same adapter seam for later releases. Developed in
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

External transports are not conversation *types*. A Telegram chat is an
explicit binding to an ordinary conversation. Pairing and public intake create
the same generic conversation rows as web or app gateways. The bot token stays
in a platform integration connection; Conversations stores the connection id,
chat route, webhook verification secret, and external participant identity.

## Model

- `conversations` — id, project, owner user, lead agent, audience
  (operator|public), kind (direct|room), origin (web|agent|app), and an
  optional unique conversation key.
- `messages` — role, content, `component_kind` (approval|report|alert|
  status — a real indexed column, replacing channel-chat's
  `LIKE '%"approval-card"%'` scans), components/attachments/metadata
  JSON, `client_message_id` idempotency key.
- `participants` — agent, platform user, or externally keyed identity with
  an optional contact URI. External identities never become platform users
  implicitly.
- `deliveries` — the ledger. One row per (message, target); pending rows
  are redelivered on mount (crash-safe sends, the Hermes pattern).
- `telegram_connections` / `telegram_bindings` — one verified webhook per
  bot connection and project-scoped chat-to-conversation routes. Routes may be
  created by pairing, public intake, one-time invitation, or advanced recovery.
- `transport_intake_policies` / `transport_access_requests` /
  `transport_invites` — provider-neutral onboarding state. Unapproved content
  is never retained; invitations store only a token hash.
- `telegram_updates` / `telegram_message_links` / `telegram_action_tokens`
  — inbound deduplication, provider message editing, and opaque callbacks.

## Surfaces

- **MCP tools** (agents): `conversations_send`, `_request_approval`,
  `_report` (inbox-only), `_alert`, `_set_status`, `_list`, `_history`.
- **Inter-app**: `inbox_post` — any sidecar raises inbox items via
  `CallAppResult("conversations", "inbox_post", …)`, with an optional
  callback tool invoked on action. This makes the app the platform's
  notification provider; no other app needs its own inbox.
- **HTTP** (dashboard): /chats /messages /stream (SSE) /inbox
  /message-action /seen /telegram-connections /telegram-bindings
  /telegram-intake /telegram-access /telegram-invites, plus the secret-verified
  /telegram-webhook/*. (Reserved prefixes /health /manifest
  /mcp /events /ui/ are avoided — guarded by a test.) `/chats`, `/inbox`, and
  `/unread-summary` accept an optional `agent_id` projection. It is intersected
  with the authenticated user/project scope and matches explicit participants,
  never merely `lead_agent_id`.
- **Adapters**: `web` (the SSE hub), authenticated agent/app callbacks, and
  `telegram`. Telegram executes Bot API tools through the bound platform
  connection; raw bot credentials never enter app config or SQLite.

## Telegram onboarding (v0.10.0)

Webhook activation calls `getMe`, `setWebhook`, and `getWebhookInfo` through
the bound platform connection. The default intake mode is fail-closed pairing:
an unknown sender receives one rate-bounded access-request code, while the app
stores only their Telegram identity metadata. An operator approves, dismisses,
or blocks the request in the dashboard. One-time `t.me` links bind a private
chat or group without copying numeric ids; their random token is single-use,
expires after 15 minutes, and is stored only as SHA-256.

Public intake is an explicit alternative. The first private message creates or
reuses a normal public conversation keyed by connection and Telegram chat, adds
the external participant, and forwards the first message to the chosen lead
agent. `/new` rotates only the active route; the old transcript stays in the
generic store. Groups never auto-open and default to mention-only activation.
Webhook drift is displayed but never fought automatically.

## Thread-per-conversation

Every conversation gets its own core thread ("chat-<conversation id>",
channel-chat's naming so thread identities survive the data migration),
spawned lazily on first inbound message via the SDK's `ThreadClient`
(`platform.threads.write`). The directive suffix carries the
conversation's context and reply contract; composition with the agent's
main directive happens core-side. MCP is left nil so the platform
supplies the agent's spawnable set; the immediate tool preload contains only
the conversation-facing Conversations tools plus its Core coordination tools.
The app persists each `(conversation, agent, thread)` relationship before Core
can execute a newly spawned thread. That stored relationship—not an inferred
platform role—binds every call from the thread to the exact conversation and
refuses conversation management or reports there. Spawn is idempotent; a changed
suffix re-spawns (the sidecar-visible approximation of channel-chat's
drift update). The first inbound message is an idempotent initial event
inside that same spawn request, so Core persists it before starting the
thread. Conversations requires an accepted-or-duplicate event receipt;
later messages use `SendThreadEvent`. A spawn transport failure degrades
to the main thread. A successful spawn without a receipt is surfaced as
a visible delivery failure and is not rerouted, because its outcome is
ambiguous and a second route could duplicate a message Core persisted.

The remaining parity gap that needs an SDK addition is an
UpdateThread/ThreadTools surface for true in-place drift correction.

## Approval round-trip

message-action mutates the card in place, publishes the updated row to
every current surface (one row — transcript, inbox widget, and Telegram
message agree), and
forwards an `approval.result` event to the owning agent's
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
4. Add Slack and other transports through the same connection-backed seam.
5. Deprecate the `channels` sidecar and shrink `channel-chat`.

## Testing in isolation

Everything runs against the SDK testkit with a recording platform stub —
no apteva-server involved. `APTEVA_GATEWAY_URL=… go run .` boots it
standalone against a dev server when wanted, but no test requires one.
