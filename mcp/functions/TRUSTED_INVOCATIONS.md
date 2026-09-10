# Trusted invocation context

Functions 1.14.0 adds trusted invocation context on top of 1.13.1. It supplies execution
admission and context integrity without embedding an Auth provider or application
roles. Authentication is performed by a configured caller before admission.

## Configure each Function

Use `functions_create`, `functions_update`, or their existing HTTP management
endpoints to set `invocation_policy`:

```json
{
  "name": "orders-handler",
  "invocation_policy": {
    "require_authenticated": false,
    "authenticated_callers": [
      {
        "installation_id": 42,
        "issuers": ["https://identity.example"]
      }
    ]
  }
}
```

Installation IDs identify actual platform installations, not app names. Issuers
are exact strings, with no wildcards. Configure each nested target separately.
With no policy or no configured caller, principal admission is denied. Ordinary
HTTP, public Function URLs, MCP, jobs, agents, and service calls retain their
existing paths by default. Set `require_authenticated: true` to close those paths
for a particular Function; the setting applies centrally to every invocation,
including nested calls. Set `invocation_policy: null` to restore legacy admission.
Updates replace the entire policy. Source deploys and rollbacks preserve it.

The existing outbound `access.apps` and `access.integrations` policies still
apply. A Function needs permission to call `functions.functions_invoke` before
it can invoke a nested Function.

## Admit a bounded request

The authenticating app invokes `functions_invoke_authenticated` through the
platform's authenticated app-to-app MCP callback, after authenticating the
browser request. This tool is `app_only`, hidden from ordinary agent tool lists.
The gateway must validate the caller installation token, `platform.apps.call`,
the target binding and project scope, then mint `X-Apteva-Bound-Caller-*` headers
and replace `_project_id`. Functions checks the verified installation against the
target policy. A payload containing an installation ID is never sufficient.
This uses the existing platform bridge and SDK v0.77.0; a platform without these
verified caller headers fails closed for this entry point.

Example callback input (replace IDs and deadline with values for that request):

```json
{
  "tool": "functions_invoke_authenticated",
  "input": {
    "name": "orders-handler",
    "_project_id": "project-123",
    "principal": {
      "subject": "customer-456",
      "issuer": "https://identity.example",
      "project_id": "project-123",
      "function_ids": [12, 34],
      "claims": {"tenant": "tenant-789", "permissions": ["orders.read"]}
    },
    "event": {"path": "/orders", "body": {}},
    "request_id": "request-abc",
    "deadline": "2026-09-10T14:00:30Z"
  }
}
```

`function_ids` is an explicit bounded list including the root and all permitted
nested targets. Project scope must agree with the gateway's project. `deadline`
is required and must be in the future; execution is bounded by the earliest of
this original deadline, upstream context deadline and Function timeout. Each
nested call can only shorten that budget. `request_id` is optional; Functions
generates one when absent. Supplied IDs use 1–128 letters, digits, `_` or `-`.
The return shape is the existing `functions_invoke` result.

The caller is trusted to assert the principal and restrict the Function scope
for that request. Functions does not resolve users, interpret roles, refresh
sessions, or contact an Auth installation. The principal schema has no credential
field. Callers must omit all session credentials from claims and events. Standard
credential keys (including nested authorization/cookie/session/access-token/
refresh-token fields) are rejected; arbitrary opaque claim strings cannot be
classified as secrets by Functions. Principal and policy input are capped at
32 KiB and scope at 100 Function IDs.

## Handler contract and nested calls

For authenticated object events, Functions constructs:

```js
const { principal, claims } = event.requestContext.authorizer;
// principal.subject, principal.issuer, principal.project_id,
// principal.function_ids, principal.claims
// claims: the caller's opaque application claims
const auditIdentity = context.invocation;
```

Go handlers receive the same event JSON and `ctx.Invocation` as a
`json.RawMessage` containing audit metadata. Authenticated events must be JSON
objects. Legacy scalar, null, array and object events remain supported.
`requestContext.authorizer` is now a reserved field: ordinary object events have
it removed, including raw JSON inputs. This is the intentional compatibility
change for handlers that previously trusted a payload-supplied authorizer.
Other request context fields are preserved.

Nested calls use the existing API:

```js
await context.call("functions", "functions_invoke", {
  id: 34,
  event: {orderId: "order-1"}
});
```

The sidecar carries the original principal in its internal Go context. It
checks outbound access, exact target Function scope, project, the target's
trusted-caller policy, existing depth/cycle limits, and the remaining deadline.
An argument named `principal`, `_project_id`, `deadline`, or a forged authorizer
cannot replace this identity or widen its scope. Edits to `event` or
`context.invocation` affect only handler-local data. Workers cannot call the
principal admission tool to mint a replacement identity. Principal forwarding
is specific to local nested Functions invocations; ordinary calls to other apps
remain service calls and receive no automatically forwarded user principal.

## Isolation, audit, and session lifecycle

Invocation identity is per request and never stored in worker environment
variables. Each worker request gets separate metadata; existing protocol sequence
checks reject late calls. Authenticated workers are retired on completion,
including success, failure and cancellation. This prevents arbitrary handler
globals and background tasks from retaining a prior principal for a later
request. Existing idle workers may serve an authenticated invocation once;
subsequent execution requires another worker. Prepared artifacts remain cached.
Ordinary invocations continue to reuse warm workers. This deliberately trades
some authenticated-call startup latency for process-level context cleanup.

Each invocation's `identity` audit record contains kind, verified subject and
issuer (when present), project, verified caller installation/app, request ID and
parent invocation ID. It is committed atomically with the invocation row before
execution. Detail, history and `functions_logs` expose it. Opaque claims and
credentials are not included in the identity record; authenticated request bodies
are omitted from stored event logs. Handler responses and explicit logs retain
their existing behavior, so handlers should avoid logging sensitive claims.
Existing historical rows have no identity record.

Other sources have explicit identities: `service` with a verified installation
ID and app name (including Jobs), `agent` with its platform agent ID,
`function_url` for token-gated URLs, and `anonymous` when there is no verified
caller. Trigger kind continues to distinguish HTTP, manual and nested paths.
A service's identity never becomes an authenticated browser principal implicitly.

Authentication admits that bounded request. A later browser request must be
authenticated again by the caller. Logging out does not automatically cancel
already-admitted execution; normal request cancellation, deadlines and runtime
shutdown still apply. Functions neither holds session credentials nor polls
session validity during a running invocation.
