# Conversations frontend

This app-owned package contains the existing Conversations UI and a headless
client over the app's HTTP API. Dashboard entry points in `../ui` are thin
wrappers around the same source. Manifest names remain `agent-conversations`
and `inbox-overview`.

Install the published packages with Bun:

```sh
bun add @apteva/conversations@0.20.0 @apteva/web-sdk@^0.6.0 react react-dom
```

To build from the apps repository, run `bun install`, then
`bun run mcp/conversations/frontend/build.ts`. Dashboard bundles include the SDK;
the dashboard itself does not need another dependency or SDK initialization.

The build produces separate headless and React entry points, TypeScript
declarations and an opt-in stylesheet. React and Web SDK remain peer dependencies.
Consumers import `@apteva/conversations`, `@apteva/conversations/react`, and
`@apteva/conversations/styles.css`. The headless entry does not load React.

```tsx
import { AptevaClient } from "@apteva/web-sdk";
import { conversationsExtension } from "@apteva/conversations";
import { ConversationChat, Inbox } from "@apteva/conversations/react";
import "@apteva/conversations/styles.css";

// The host owns login, token renewal, identity and authorized routing metadata.
const client = new AptevaClient({ baseURL, accessToken, refreshAccessToken });
const conversations = client.use(conversationsExtension({
  audience: "public",
  storageKey: signedInUserId, // optional stable, non-secret identity key
}), { projectId, installId });

<ConversationChat conversations={conversations} agentId={agentId} />
<Inbox conversations={conversations} agentId={agentId} />
```

Retain the client and extension in your host (for example with `useMemo`). Create
a new extension when the signed-in subject changes, even within the same project.
Without `storageKey`, drafts persist for that client instance and cannot collide
with another instance. With a stable key, drafts also survive host remounts;
never share the key across users. Tokens are never written to draft storage.
A parent must supply a usable height. Import the stylesheet only in external
hosts; the dashboard keeps its existing theme. Override the `.apteva-conversations`
CSS variables to theme an external surface.

Other exports: `AgentConversations` (browser layout), `ConversationThread` (an
explicit authorized conversation), `ConversationsPanel` (operator panel),
`ApprovalCard`, `ReportCard`, and `AlertCard`. `conversationsComponents` maps the
two existing manifest names to local React exports. Hosts resolve these names
with the SDK registry and inject the trusted `conversations` client separately
from untrusted message props. The `agent-conversations` export accepts `agentId`;
dashboard wrappers translate their existing `instanceId` prop. The SDK does not
fetch or execute components from URLs automatically.

The client exposes list/get/create/update/remove, history/changes/send/subscribe,
markSeen/unread, agents, inbox/act/dismiss, and delivery status/retry. Sends require
a stable `client_message_id`; preserve the entire request when retrying an
uncertain response. REST change pages own the durable replay cursor. SSE stream
frames remain ephemeral and cannot advance that cursor or suppress later edits.

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
bun test mcp/conversations/ui mcp/conversations/frontend/tests/client.test.ts
bunx --no-install tsc -p mcp/conversations/tsconfig.json
bun run scripts/build-panels.ts --app conversations
bunx --no-install playwright test -c mcp/conversations/frontend/playwright.config.ts
```

The browser fixture tests both hosts at desktop/mobile widths and exercises
report/approval UI over a controlled HTTP/SSE service. Real platform/token and
real-Codex evidence is recorded separately; a fixture token does not establish
backend isolation.
