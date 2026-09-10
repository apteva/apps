# API Gateway authorization and Function requests

API owns authentication at the public HTTP boundary. Configuration selects both
an authorizer and the destination Function; no application-specific rules are
built into the gateway. Functions apply their business authorization rules using
the verified principal and permitted claims.

## Configure an API or route

Pass `auth` to `api_create`, `api_update`, or `api_route_add`, or use the API
panel's authentication controls. An empty route policy `{}` inherits the API
policy. Any nonempty route policy replaces it completely, including provider,
tenant restriction, claim allowlist, and permitted Function IDs.

Existing `public`, `api_key`, and `auth_jwt` policies remain supported.
`auth_jwt` is the compatibility name for the Auth provider. New configuration:

```json
{
  "kind": "authorizer",
  "provider": "auth",
  "tenant_id": "example-organization",
  "claims": ["roles", "permissions", "authorization_version"],
  "function_ids": [12, 34]
}
```

Auth verifies the bearer token through its project-scoped `/me` endpoint. The
principal uses the verified user ID, the organization slug as `tenant_id`, and
`apteva:auth:<organization-slug>` as its issuer. Claims are selected exclusively
from Auth's server-managed `authorization` object, never from user-editable
metadata or browser-supplied claims. `tenant_id` is optional; setting it rejects
identities from other organizations. Older Auth responses without an
organization use issuer `apteva:auth` and omit `tenant_id`.

No claims are forwarded unless explicitly listed. Claim names must be simple
identifiers; credential/session/token/password fields and reserved identity
names are rejected. Values may be scalars or flat scalar arrays, with bounded
sizes. Nested objects are rejected. An authorizer must return authorization
claims only, and must never place credentials in identity or claim values.

A route chooses its Function independently:

```json
{
  "api_slug": "example",
  "method": "POST",
  "path_pattern": "/items/:id",
  "target_kind": "function",
  "target_ref": "update-item",
  "timeout_ms": 30000,
  "auth": {
    "kind": "authorizer",
    "provider": "auth",
    "tenant_id": "example-organization",
    "claims": ["roles", "permissions"],
    "function_ids": [12, 34]
  }
}
```

## Other authorizers

Bind an installed app through API's `authorizer` integration (or another existing
app binding). It must implement this contract; arbitrary HTTP origins are not
authorizers. The platform enforces authenticated app-to-app calls and bindings.

```json
{
  "kind": "authorizer",
  "provider": "app",
  "app": "identity-provider",
  "path": "/authorize",
  "issuer": "https://identity.example.com",
  "tenant_id": "tenant-a",
  "claims": ["roles", "permissions"],
  "function_ids": [12, 34]
}
```

API sends a POST through the platform's project-scoped bound-app proxy, using
its outbound app credential. The provider receives:

```json
{
  "project_id": "project-id",
  "credential": { "type": "bearer", "token": "browser-bearer-token" }
}
```

Only the authorizer receives that credential. It must verify the credential,
revocation/expiry and project/tenant membership, then return:

```json
{
  "authenticated": true,
  "expires_at": "2026-09-10T18:00:00Z",
  "principal": {
    "issuer": "https://identity.example.com",
    "subject": "person-123",
    "project_id": "project-id",
    "tenant_id": "tenant-a",
    "claims": { "roles": ["reader"], "permissions": ["items:read"] }
  }
}
```

The configured issuer and gateway-selected project must match exactly. If a
tenant is configured it must also match. The subject must be nonempty and the
RFC3339 expiry must be in the future. Return HTTP 401/403 or
`{"authenticated":false}` to deny access. Invalid responses, unavailable
providers and timeouts fail closed. Authorizer requests have a five-second
maximum and honor earlier request deadlines and browser cancellation.
Authenticated `app_events` streams also close at the returned identity expiry.

## Trusted Function admission

Authenticated Function routes (Auth, custom authorizer, or API key) invoke
`functions_invoke_authenticated` through
`POST /api/apps/callback/apps/functions/call`. API sends its outbound app token;
the platform validates the installation, binding, permission, and project before
minting verified caller headers. API never supplies caller-installation headers
or accepts a browser-supplied caller ID.

`auth.function_ids` is an explicit list of 1–100 unique positive IDs including
the named root Function and every permitted nested target. It comes only from
configuration, never from the authorizer response or browser input. Route
policies replace the whole API auth policy; `{}` inherits it. Empty scope is
not a wildcard or an automatic root grant. Missing scope fails closed, and an
out-of-scope root is rejected by Functions before execution.

Each target Function needs an `invocation_policy` configured through Functions:

```json
{
  "require_authenticated": true,
  "authenticated_callers": [
    {
      "installation_id": 42,
      "issuers": ["apteva:auth:example-organization"]
    }
  ]
}
```

Use the real API installation ID and exact verified issuer. Configure nested
Functions separately. `require_authenticated: true` closes alternative ordinary
invocation paths for that Function. Functions still enforces its outbound access
policy for nested calls. See [Functions' trusted invocation contract](../functions/TRUSTED_INVOCATIONS.md).

API sends identity separately from the request event:

```json
{
  "tool": "functions_invoke_authenticated",
  "input": {
    "name": "update-item",
    "_project_id": "project-id",
    "principal": {
      "issuer": "apteva:auth:example-organization",
      "subject": "123",
      "project_id": "project-id",
      "function_ids": [12, 34],
      "claims": {"tenant_id": "example-organization", "roles": ["editor"]}
    },
    "request_id": "gateway-generated-request-id",
    "deadline": "2026-09-10T17:00:30Z",
    "event": {
      "method": "POST",
      "path": "/items/42",
      "query": {"view": "summary"},
      "params": {"id": "42"},
      "headers": {"Content-Type": "application/json", "Last-Event-Id": "cursor-7"},
      "body": {"name": "Updated item"},
      "raw_body": "{\"name\":\"Updated item\"}",
      "auth": {"kind": "authorizer", "subject": "123"},
      "request_id": "gateway-generated-request-id",
      "deadline": "2026-09-10T17:00:30Z",
      "received_at": "2026-09-10T17:00:00Z"
    }
  }
}
```

Functions constructs the authoritative handler context after admission:

```js
const { principal, claims } = event.requestContext.authorizer;
// principal.subject, issuer, project_id, function_ids
// claims.tenant_id and the explicitly allowed authorization claims
```

API maps verified tenant identity to reserved `principal.claims.tenant_id`
because the Functions principal schema carries tenant information in claims.
An arbitrary provider claim cannot overwrite this field. Application claims
still require an explicit allowlist. API-key identity uses issuer `apteva:api`,
subject `api_key:<key-id>`, and the gateway project, without copying the key.

Browser JSON remains nested under `body`; it is never merged into the event or
invocation arguments. Non-JSON text is a string body; an empty body is `null`.
Identically named browser fields such as principal, function_ids, deadline or
requestContext remain untrusted body/query data. Read identity exclusively from
`event.requestContext.authorizer`, not `event.body` or the legacy `event.auth`.
The old top-level `event.principal` is not sent on authenticated invocations.

Authorization, cookies, API keys, hop-by-hop, forwarding and reserved identity
headers are removed. Platform routing and API-key query parameters are removed.
Business body fields remain opaque application data, including fields named
password or token; they never become invocation identity. Functions rejects
credential fields in identity claims and omits authenticated bodies from its
invocation history.
The original deadline bounds authentication, transport and execution; browser
cancellation cancels the MCP callback. API unwraps the tool result and preserves
nonstreaming Function responses, including structured status, headers and body.
Tool/admission errors fail closed and never fall back to `/fn`.

### Migration and streaming

This path requires Functions **1.14.1 or newer** and a platform that supplies
verified bound-caller headers. Existing authenticated Function routes must add
`auth.function_ids` and configure their targets' trusted callers before use.
Handlers that used `event.principal` must switch to the authoritative context
above. No Function scope is inferred from a name or expanded automatically.

The current authenticated MCP tool returns a buffered result; it does not expose
live HTTP streaming. Authenticated handlers should return ordinary or structured
nonstreaming responses. Public Function routes continue using the existing
`/fn/<name>` path, including streaming, and `app_events` remains unchanged.
There is no legacy fallback when an authenticated invocation fails.

## CORS and resumable streams

CORS remains API/route configuration. For example:

```json
{
  "enabled": true,
  "origins": ["https://console.example.com"],
  "allow_methods": ["GET", "POST"],
  "allow_headers": ["authorization", "content-type", "last-event-id"]
}
```

Preflight is handled before authentication. `Last-Event-ID` is an ordinary
allowed request header and survives sanitization. Adding it does not change
authentication or create special identity/streaming behavior.
