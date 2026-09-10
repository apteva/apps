# API Gateway authorization and Function requests

API owns authentication at the public HTTP boundary. Configuration selects both
an authorizer and the destination Function; no application-specific rules are
built into the gateway. Functions apply their business authorization rules using
the verified principal and permitted claims.

## Configure an API or route

Pass `auth` to `api_create`, `api_update`, or `api_route_add`, or use the API
panel's authentication controls. An empty route policy `{}` inherits the API
policy. Any nonempty route policy replaces it completely, including provider,
tenant restriction, and claim allowlist.

Existing `public`, `api_key`, and `auth_jwt` policies remain supported.
`auth_jwt` is the compatibility name for the Auth provider. New configuration:

```json
{
  "kind": "authorizer",
  "provider": "auth",
  "tenant_id": "example-organization",
  "claims": ["roles", "permissions", "authorization_version"]
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
  "auth": {}
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
  "claims": ["roles", "permissions"]
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

## Trusted Function envelope

API constructs a new event and invokes the existing protected Functions
`/fn/<name>` interface through the authenticated platform bound-app proxy.
Browser JSON is nested under `body`; it is never merged into the outer event.
Non-JSON text is passed as a string body; an empty body is `null`.
The outbound app credential stays in the transport, outside the event.

```json
{
  "method": "POST",
  "path": "/items/42",
  "query": { "view": "summary" },
  "params": { "id": "42" },
  "headers": { "Content-Type": "application/json", "Last-Event-Id": "cursor-7" },
  "body": { "name": "Updated item" },
  "raw_body": "{\"name\":\"Updated item\"}",
  "principal": {
    "issuer": "apteva:auth:example-organization",
    "subject": "123",
    "project_id": "project-id",
    "tenant_id": "example-organization",
    "claims": { "roles": ["editor"] }
  },
  "auth": { "kind": "authorizer", "subject": "123" },
  "request_id": "gateway-generated-request-id",
  "deadline": "2026-09-10T17:00:30Z",
  "received_at": "2026-09-10T17:00:00Z"
}
```

`principal`, `auth`, `request_id`, and `deadline` belong to API. Identically
named browser fields remain untrusted data inside `body` or `query`. Read
identity from `event.principal`, never `event.body.principal` or request headers.
Existing `event.auth.kind` / `event.auth.subject` are retained for compatibility.
Public routes have `principal: null`. API-key principals use issuer `apteva:api`,
subject `api_key:<key-id>` and the gateway project; the actual key is never copied.

Authorization, cookies, API keys, hop-by-hop, forwarding and reserved identity
headers are removed. Platform routing and API-key query parameters are removed.
Other body and query data are passed as application input. The deadline covers
authorization and upstream work for ordinary routes; streaming and cancellation
continue through the existing transport.

This envelope is trusted because API constructs it on an authenticated
invocation path. It is not a signed assertion valid on arbitrary entry points.
A Function exposing an additional public Function URL must separately secure
that entry point and must not accept a browser-supplied principal as verified.
No changes to Functions are required to consume the event.

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
