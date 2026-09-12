# Conversations frontend

This app-owned package contains the existing Conversations UI and a headless
client over the app's HTTP API. Dashboard entry points in `../ui` are thin
wrappers around the same source. Manifest names remain `agent-conversations`
and `inbox-overview`.

External applications install only the shared SDK and React:

```sh
bun add @apteva/web-sdk@^0.7.0 react react-dom
```

```tsx
import * as React from "react";
import { AptevaClient } from "@apteva/web-sdk";

const client = new AptevaClient({ baseURL, accessToken, refreshAccessToken });
const loaded = await client.apps.load("conversations", {
  projectId, installId, react: React,
  clientOptions: { storageKey: signedInUserId }, // optional non-secret identity
});
const Chat = loaded.components["conversation-chat"];
const Inbox = loaded.components["inbox-overview"];
<Chat conversations={loaded.client} agentId={agentId} />
<Inbox conversations={loaded.client} agentId={agentId} />
// After unmounting consumers:
loaded.dispose();
```

This JSX example uses JavaScript; TypeScript hosts can supply client and component
contracts through `apps.load<ClientContract, React.ComponentType<HostProps>>`.
The included `example/main.tsx` demonstrates loading, errors, cancellation and cleanup
with no imports from this app. Omit `react` for a headless client. The loaded client
uses public audience by default; the backend still enforces the token's permissions.

Retain the loaded client across renders. On logout or a subject/scope change,
unmount the old UI, dispose it and load a fresh instance. Use a unique non-secret
`storageKey` per user when drafts should survive remounts. Tokens never enter draft
storage. A parent must supply a usable height. Styles are loaded automatically,
scoped under `.apteva-conversations`, and removed after the final consumer disposes.

Run `bun run scripts/build-panels.ts --app conversations` from the apps repository.
Native dashboard panels embed the same generated, scoped CSS that the Web SDK loader mounts for external hosts. They do not depend on a dashboard Tailwind source scan or a separate dashboard release. Host theme tokens (for example `--font-base`, `--bg`, `--text`, and `--accent`) are inherited; unthemed hosts get local defaults.

It builds dashboard bundles and `/ui/frontend.json` plus content-addressed client,
UI and style assets. The normal app release includes these files. No npm app
publication or platform upgrade is needed. The old npm package 0.20.0 remains
available for existing consumers; this directory is now private and future normal
app releases use the platform-served frontend.

The SDK requires HTTPS or localhost and a host CSP allowing `script-src blob:`
and inline styles. It checks asset integrity and uses authenticated fetches with
explicit project/install scope. Pass the host's React 19 instance; no extra React
copy or import map is loaded. Explicit loading trusts this app's code in the host.

Component names include `conversation-chat`, `agent-conversations` (browser layout),
`conversation-thread`, `conversations-panel`, `inbox-overview`, `approval-card`,
`report-card`, and `alert-card`. The original manifest names stay intact. Inject
the trusted `conversations` client separately from untrusted message props.
Agent chat components accept `agentId`; dashboard wrappers translate their
existing `instanceId` prop. Only an explicit `apps.load` call loads app code.

The client exposes list/get/create/update/remove, history/changes/send/subscribe,
markSeen/unread, agents, inbox/act/dismiss, and delivery status/retry. Sends require
a stable `client_message_id`; preserve the entire request when retrying an
uncertain response. REST change pages own the durable replay cursor. SSE stream
frames remain ephemeral and cannot advance that cursor or suppress later edits.

## Localization and host wording

All eight exported surfaces accept optional `locale`, `timeZone`, and `messages`
props. This includes `conversation-chat`, `agent-conversations`,
`conversation-thread`, `conversations-panel`, `inbox-overview`, and the three cards.
No Web SDK change or extra frontend dependency is required:

```tsx
const Chat = loaded.components["conversation-chat"];
<Chat
  conversations={loaded.client}
  agentId={agentId}
  locale="fr-FR"
  timeZone="Europe/Paris"
  messages={{
    "chat.empty": "Aucun message pour le moment. Comment puis-je vous aider ?",
    "chat.placeholder": "Écrivez votre message…",
  }}
/>
```

English remains the default. Bundled dictionaries cover English, French, and
Spanish, including dialogs, inbox/card chrome, accessibility text, and Telegram
administration. Regional locales such as `fr-CA` use the French dictionary while
retaining regional number/date formatting. Unsupported languages fall back to
English copy; invalid locale tags fall back to `en`. Absolute timestamps use the
supplied IANA timezone, or the browser timezone when omitted/invalid. Relative
times and plurals use `Intl` with the selected locale.

`messages` overrides individual stable keys; missing keys use the selected
language, then English. An empty string is a valid override. Strings are rendered
as text, never HTML. Keys and bundled translations are in `src/locales.ts` and
`src/telegramLocales.ts`. Common host overrides include `chat.empty`,
`chat.placeholder`, `chat.reconnectingPlaceholder`, `chat.history`, `chat.new`,
`chat.send`, and `inbox.caughtUp`.

Messages with parameters use `{name}` or `{count}`. For plural copy, provide an
object with `other` and optional CLDR categories (`zero`, `one`, `two`, `few`,
`many`), for example:

```tsx
messages={{
  "inbox.count": { one: "{count} demande", other: "{count} demandes" },
}}
```

The host owns the current language and its override dictionary. Pass updated
props when the user changes language; keep `loaded.client` and component keys
stable. Updating localization does not reload the app, reset drafts, or restart
chat subscriptions. The example hosts demonstrate live language switching.
Settings are scoped to each mounted surface, so separate users/embeds do not
share a global locale. Source consumers can also use
`ConversationLocalizationProvider` or the localization props on
`ConversationsProvider`; nested component props override inherited settings.
The dashboard wrappers accept the same props. Connecting them to the dashboard's
language preference is a separate host change.

Localization applies to UI chrome. Authored chat content, conversation titles,
reports, custom approval action labels, and server-provided error details are
preserved. It does not change agent instructions, generated response language,
API enum values, or the default stored Telegram conversation-title prefix.

## Application-user boundary

Application-user credentials are platform-issued tokens, not arbitrary Auth JWTs.
For an external host, the issuer policy must grant `app_user` on `conversations`,
explicit `agent_ids`, and only the required actions:

- Chat: `chat.read`, `chat.create`, `message.read`, `message.send`, `stream.read`,
  `chat.seen`, `delivery.read`.
- Optional owner operations: `chat.update`, `chat.delete`, `delivery.retry`.
- Own inbox and cards: `inbox.read`, `inbox.dismiss`, `approval.act`.

External subjects are isolated by project, issuer app/install, organization and
subject type/ID. Migration 010 creates the app-local identity map; negative local
user IDs cannot overlap positive platform user IDs. Existing ownership and data
are unchanged. External users cannot see ownerless project-wide conversations,
operator conversations, another subject's history, or Telegram administration.
Creation is restricted to permitted agents and public audience; directives come
from issuer policy. Multi-agent tokens must provide an allowed agent projection
for aggregate reads. Scope parameters and the component registry grant no access.

External hosts require a platform with application-user token minting, trusted
subject headers, scoped app routing and allowed-origin CORS. Configure the host
origin in the issuer policy; dashboard session cookies stay on the dashboard.

## Validation

```sh
bun test mcp/conversations/ui mcp/conversations/frontend/tests/client.test.ts mcp/conversations/frontend/tests/localization.test.ts
bunx --no-install tsc -p mcp/conversations/tsconfig.json
bun run scripts/build-panels.ts --app conversations
bunx --no-install playwright test -c mcp/conversations/frontend/playwright.config.ts
```

The browser fixture deliberately supplies no app stylesheet to the dashboard: its panel must carry its own. It compares computed typography and layout with SDK-loaded chat, checks theme inheritance and isolation, and renders real Markdown replies, multiple speakers, delivery failures and streaming text. It tests both hosts at desktop/mobile widths and exercises
report/approval UI over a controlled HTTP/SSE service. Real platform/token and
real-Codex evidence is recorded separately; a fixture token does not establish
backend isolation.

### Tool activity

Dashboard panels and exported chat use the same port of the original `ChatToolActivity` renderer, grouping rules and scoped styles, including stacked icons, animated activity text, expandable groups, parallel calls, failure indicators and timing. Data and translations are adapted to Conversations. First-party app icons are bundled from public app metadata; custom sources use the original fallback resolver when no matching metadata is available.

The shared transcript displays conversation-bound tool starts and results. Activity is stored separately from messages and replayed through `/activity` on load, reconnect and periodic reconciliation. Live updates travel in `stream` frames under `tool_activity`, with stable IDs and revisions. The endpoint uses the same conversation and delegated `message.read` access checks as history. It carries display metadata only, never tool arguments or raw results, and does not create unread messages or outbound deliveries. Activity collected after this feature is installed survives refreshes; older platform telemetry is not imported automatically.

The main composer action pauses an active reply or tool when the input is empty, and switches to send as soon as a draft is entered. Pausing remains an advisory request.

### Composer attachments

Every chat surface uses the same attachment composer and message renderer. Its `+` menu includes files/photos and screenshot capture; paste and drag/drop work too. Attachments can be sent with or without text while an agent is active. Completed uploads remain in the per-conversation session draft; a pending send retains its original idempotency key. Unsaved uploads interrupted by a reload must be selected again.

Configure `composer` on `ConversationChat`, `ConversationThread`, `AgentConversations`, `ConversationsPanel`, or `ConversationsProvider` (and the native dashboard wrappers):

```tsx
<ConversationChat conversations={conversations} agentId={agentId}
  composer={{
    files: true,
    screenshot: true,
    accept: "image/*,.txt,.pdf",
    maxFiles: 5,
    maxFileBytes: 10 * 1024 * 1024,
    // Optional native capture bridge; return null when canceled.
    captureScreenshot: async () => nativeHost.captureScreenshot(),
    actions: [{ id: "notes", label: "Attach notes", run: async () =>
      new File(["My notes"], "notes.txt", { type: "text/plain" }) }],
  }} />
```

Browser screen capture uses the browser's screen/window/tab picker in a secure context, captures one frame and stops the media tracks immediately. Unsupported browsers can still paste or upload a screenshot. Native callbacks are trusted host code; serialized message content cannot register actions.

The headless client offers `upload(chatId, uploadId, filename, contentBase64)` and `attachment(chatId, attachmentId)`. Upload IDs must be 16–80 characters and reused on retries. Send references as `attachments: [{id, type}]`; the server resolves canonical metadata and rejects cross-conversation or cross-sender references. Reads require `message.read`; uploads and sends require `message.send`. Exported hosts use the same delegated Conversations permissions, without granting visitors Storage access.

Uploads are limited server-side to 10 MiB each, ten attachments per message, and 100 MiB of pending originals per conversation. PNG/JPEG/WebP/GIF images are decoded with a 40-megapixel limit and resized to a bounded JPEG for preview and Core vision. Originals remain available for authenticated downloads. Messages persist previews and attachment IDs; originals live in the app database and follow its backup/restore and conversation deletion. Unsent local uploads older than one day are reclaimed on the next upload.

To also retain files in Storage, bind a Storage app (v0.12.3+) and enable **Copy attachments to Storage** (`attachment_storage=true`) in Conversations settings. Sending then creates private copies and includes `storage_app: "storage"` and `file_id` on the returned message attachments. This is optional and uses the binding's selected install. Conversations retains originals for reliable authorized history; Storage copies have independent retention. Storage failure prevents sending and allows retry with the same IDs.

Images are delivered as actual Core `image_url` content parts in the existing conversation event. File IDs and names accompany the event; a file ID alone never substitutes for vision data. Agents can use `conversations_read_attachment` for bounded text (64 KB), image data, or binary chunks (48 KiB, `offset`/`next_offset`). PDF/Office content is not automatically extracted: the agent must use document tools, optionally against the Storage file ID. File contents remain user-provided data.
