---
name: how-to-use-code
description: Build, import, edit and test Code repositories, use native revisions and Workspaces, and build web apps that connect to other Apteva apps with @apteva/web-sdk.
command: /code
triggers:
  - repos_create
  - repos_import_zip
  - repos_git_import
  - code_list_files
  - code_read_file
  - code_write_file
  - code_edit_file
  - code_apply_patch
  - repos_run_command
  - repos_dev_start
  - repos_workspace_changes
  - repos_workspace_apply
  - repos_checkpoint
  - "Apteva web SDK"
  - "@apteva/web-sdk"
---

# Code: repositories and Apteva-connected web apps

Use Code as the source repository and editing surface. Use Workspaces to execute
the code. Use `@apteva/web-sdk` inside the application you are building to call
installed Apteva apps. These are separate roles: the agent's Code MCP tools edit
source; the Web SDK gives the resulting application's users access to app APIs.

## Start from the right repository

Call `repos_list` and `repos_get` to identify the repository in the current
project. Reuse it when continuing existing work. For a new project, choose:

- `repos_create` for a starter (`blank`, `static`, `nextjs`, `go`, or `python`).
- `repos_import_zip` for uploaded source without a remote.
- `repos_git_import` for an HTTPS Git remote; `ref` accepts a branch, tag or
  reachable commit SHA. Public repositories need no provider connection.

For ZIPs, pass the uploaded `blobref://` handle through Core. Core injects its
binary envelope; do not fetch the file's original URL or invent a local path.
Raw base64 is also accepted when the calling environment already has the bytes.
Preview before applying:

```json
{"archive":"blobref://<uploaded-file>","target_mode":"create","name":"Hello Lab","slug":"hello-lab","framework":"go","dry_run":true}
```

Inspect the returned slug, additions, modifications, overwritten paths and
archive checksum. Apply that preview using its actual returned ID:

```json
{"import_id":"zipimp_<returned-id>","confirm":true}
```

For an existing repository, preview with `target_mode: "overlay"` and its
`slug`. Overlay preserves files absent from the ZIP. Previews expire after
30 minutes; changes to the reviewed destination require a new preview. A
failed import must be resolved before proceeding; do not report success from
the preview alone.

## Read, edit, and review

Inspect file names with `code_list_files` or `code_glob`, locate code with
`code_grep`, then use `code_read_file`, `code_read_excerpt`, or
`code_file_outline` for the relevant source. Read the repository's own guidance
and build/test configuration before choosing commands.

Use `code_write_file` for new files and small replacements. Use the returned
SHA with `expected_sha256` when replacing a file you previously read; use
`create_only: true` when it must be new. `code_edit_file` performs an exact
unique replacement; `code_multi_edit` groups replacements within one file.

For changes across files, `code_apply_patch` supports both unified diffs and
Codex `*** Begin Patch` envelopes. Preview with `dry_run: true`, inspect the
result, then call it with the returned `patch_id` and the same `slug`. This
applies the reviewed patch. If source/context changed, reread and rebuild the
patch. Do not enable fuzzy matching just to hide a failed context check.

## Execute through Code's workspace tools

For finite commands, use `repos_run_command` with `runtime: "workspace"` and
the appropriate `profile` (`bun`, `go`, or `python`). Code prepares and links
the workspace, transfers source, executes the command, and returns output and
exit status. The Workspaces app must be bound for this path. A binding failure
is a setup issue, not evidence that the source code is broken.

```json
{"slug":"hello-lab","runtime":"workspace","profile":"go","command":"go test ./...","timeout_seconds":120}
```

For JavaScript/TypeScript projects use Bun and the scripts declared in the
repository, for example `bun install`, `bun run typecheck`, `bun test`, and
`bun run build`. Check the command's exit status; output alone is not a pass.

Commands can edit the workspace copy. To bring those changes back, first call
`repos_workspace_changes`, inspect its changed paths and diff, then call
`repos_workspace_apply` with the returned `workspace_digest` as
`expected_workspace_digest`. A workspace edit does not automatically change
Code's repository. A stale digest requires a new review.

Use `repos_dev_start` for a long-running preview, then `repos_dev_status` and
`repos_dev_logs` to confirm readiness. Connected Workspaces provide the web
preview runtime; `repos_dev_stop` stops it. Custom web servers must listen on
`0.0.0.0:$PORT` inside the workspace. A localhost preview URL belongs to the
Docker host; use an authorized preview route when the browser is elsewhere.
Local execution requires the repository's explicit execution permission; do
not silently select it after a workspace failure.

## Revisions are optional during ordinary work

Editing and running use the working tree without requiring a checkpoint or Git
remote. Native Code revisions are independent of Git. Use
`repos_version_status` and `repos_diff` to inspect changes; create a
`repos_checkpoint` to save a meaningful immutable revision when appropriate.
`repos_history`, native branches, tags, and `repos_export_ref` operate on that
history. `repos_restore` normally makes a new revert revision; use its
`working_tree_only` option only when an uncommitted restore is intended.

External Git remains optional. The `repos_git_*` tools connect, fetch, commit,
pull and push provider history; they are separate from native checkpoints.
Do not push or publish just because tests pass when the requested task only
covers local editing. `repos_export` captures a working-tree source snapshot;
use `repos_snapshot_read` for bounded chunks when it is too large to inline.

## Build an application with the Apteva Web SDK

Install the published shared SDK in the repository:

```sh
bun add @apteva/web-sdk@^0.11.0
```

The SDK works with React, other frameworks, or plain TypeScript. Read its
installed README/types and the target app's current tool/HTTP contract before
writing calls. Use the Apteva server's origin as `baseURL` (without `/api`),
and real project/installation IDs from the platform. A workspace preview is
the frontend origin, not automatically the Apteva API origin.

### Authenticate the application's users

For an application using the Auth app (v0.12.0 or newer), configure one shared
client with its public Auth client ID. The operator configures that Auth client,
role bindings, and the permitted app policies. The SDK handles login and token
renewal; a client ID or `projectId` is routing/configuration, not permission.

```ts
import { AptevaClient } from "@apteva/web-sdk";

export function createAptevaClient(
  baseURL: string,
  projectId: string,
  publicAuthClientId: string,
) {
  return new AptevaClient({
    baseURL,
    projectId,
    auth: { clientId: publicAuthClientId },
  });
}
```

Call `client.auth.login({ email, password })` from the login form and
`client.auth.logout()` on logout. Reuse the client for the application session.
Sessions default to memory; enable `auth.persistence: "local"` and call
`auth.restore()` at startup only when persistent login is wanted. Render login,
loading, and recovery states from the SDK instead of managing refresh tokens.

With managed Auth, select `credential: "platform"` for platform-authorized app
calls such as Conversations, and `credential: "auth"` for an app's documented
Auth-user routes (for example API Auth-policy routes or Telephony `/user/`).
The selected app and tool must be allowed by its policies. Keep the credential
choice explicit when mixing apps; do not substitute another credential after
a denied call.

An existing operator dashboard can instead use its platform session cookie by
omitting `auth` and bearer credentials. A host with an existing trusted token
flow can provide `accessToken` instead. Managed `auth` cannot be combined with
`apiKey` or `accessToken`. Keep private administrator API keys and app-sidecar
tokens out of browser source, environment variables bundled into frontend
assets, URLs, and logs.

### Connect to another app through HTTP or MCP

Select the installed app by name and scope its handle to the actual project
and installation. The handle routes through `/api/apps/<app>/...`; it does not
talk directly to a sidecar port. The SDK unwraps MCP results for you:

```ts
import { AptevaClient } from "@apteva/web-sdk";

export async function searchContacts(
  client: AptevaClient,
  projectId: string,
  crmInstallId: number,
  query: string,
) {
  const crm = client.app("crm", {
    projectId, installId: crmInstallId, credential: "platform",
  });
  return crm.tool<{ contacts: Array<{ id: number; display_name: string }> }>(
    "contacts_search", { q: query, limit: 25 },
  );
}
```

For documented HTTP APIs, use the same handle's `get`, `post`, `put`, `patch`,
or `del` with an app-relative path beginning with `/`. For example
`client.app("conversations", scope).get("/chats")` reads that app's chat list
when the credential has access. Check the app's contract for arguments, response
fields, pagination, and whether a route uses Auth-user or platform credentials;
do not infer them from a display name. Use `client.apps.list()` for installed
app discovery only when the caller has its platform permission; otherwise have
the authorized host supply the installation metadata.

### Write CRM data

Use `contacts_create` for a new contact and `contacts_update` to patch its core
fields. Email/phone values belong in `channels`, not an invented top-level
`email` field. For example, after login and with CRM write permission:

```ts
import { AptevaClient } from "@apteva/web-sdk";

export async function createAndUpdateContact(
  client: AptevaClient,
  projectId: string,
  crmInstallId: number,
) {
  const crm = client.app("crm", {
    projectId, installId: crmInstallId, credential: "platform",
  });
  const { contact } = await crm.tool<{ contact: { id: number } }>(
    "contacts_create", {
      first_name: "Alex",
      last_name: "Smith",
      channels: [{ kind: "email", value: "alex@example.com", is_primary: true }],
      source: "web-app",
    },
  );
  await crm.tool("contacts_update", {
    id: contact.id, patch: { company: "Example Company" }, source: "web-app",
  });
  return contact.id;
}
```

This example writes actual records when called. Use appropriate test data for
verification. Read back with `contacts_get` to verify the stored fields; use
the returned ID for subsequent updates. Updating `channels` replaces the channel
list, so send the intended complete list. `contacts_create` is not an idempotent
retry operation. When the intended action is find-or-create by email or phone,
use `contacts_upsert_by_channel` with `kind`, `value`, and create-only `defaults`,
then update the returned contact when necessary.

Handle SDK `AptevaError` failures in the UI. Treat unauthorized/forbidden calls
as login or policy problems, and missing routes/tools as compatibility issues.
HTTP writes are not automatically replayed. For uncertain outcomes, reconcile
server state and reuse an app-supported idempotency key before retrying.

### Reuse another app's frontend

When a trusted installed app publishes `/ui/frontend.json`, load its headless
client and optional UI with `client.apps.load`. A separate npm package for each
app is unnecessary. For example, a host can load Conversations' chat component:

```tsx
import * as React from "react";
import { AptevaClient } from "@apteva/web-sdk";

export async function loadConversationChat(
  client: AptevaClient,
  projectId: string,
  conversationsInstallId: number,
  agentId: number,
) {
  const loaded = await client.apps.load<unknown, React.ComponentType<{
    conversations: unknown; agentId: number;
  }>>("conversations", {
    projectId, installId: conversationsInstallId,
    credential: "platform", react: React,
  });
  const Chat = loaded.components["conversation-chat"];
  if (!Chat) { loaded.dispose(); throw new Error("Chat component unavailable"); }
  return {
    element: <Chat conversations={loaded.client} agentId={agentId} />,
    dispose: loaded.dispose,
  };
}
```

Retain the loaded client/components across renders, unmount consumers before
calling `dispose()`, and close subscriptions on cleanup. Omit `react` for a
headless client. Loading requires the correct installation, authenticated asset
access, HTTPS or localhost, a compatible React major, and a CSP that permits the
loader's blob modules and styles. The SDK verifies asset hashes. Only load
frontends from trusted installed apps; an arbitrary source/message URL is not
an app frontend contract. Installing the SDK alone does not make every app
publish this contract.

If the backend runs in an Apteva Go sidecar, declare its app dependencies and
`platform.apps.call` permission in its own manifest, and use
`ctx.PlatformAPI().CallAppResult(...)` for inter-app MCP calls. This backend SDK
workflow is separate from the browser Web SDK; browser requests use the user's
credentials and app policies.

## Verify and hand over

Run the project's typecheck, tests, and build in its workspace. Verify the
preview's actual UI and at least one authorized app read; for writes, verify
the resulting record and retry behavior with appropriate test data. Exercise
login, denied access, loading, and failure states as relevant to the application.
Explain which repository changed, whether workspace changes were applied back,
what passed, which dependencies or policies still need setup, and the usable
preview/export or native revision. A build pass does not prove another app's
authorization or live connection works.

SDK reference: https://github.com/apteva/web-sdk/blob/v0.11.0/README.md
