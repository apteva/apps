# GraphQL app

The standalone `graphql` app owns GraphQL schemas, resolver bindings,
aggregation-aware source adapters, and realtime subscriptions. It is
deliberately independent from the REST/API gateway app.

## Minimal setup

Create and publish a schema:

```json
{
  "sdl": "type Query { orderCount: Int! }",
  "environment": "staging"
}
```

Bind `Query.orderCount` to a Database source:

```json
{
  "name": "orders",
  "kind": "database",
  "config": {"database": "staging", "collection": "orders"}
}
```

```json
{
  "parent_type": "Query",
  "field_name": "orderCount",
  "source": "orders",
  "operation": "count"
}
```

The public endpoint is the app's own `/graphql` route. Queries use the
published schema for the selected server-side environment; clients cannot
choose an arbitrary database or source.

Existing `/graphql`, `/admin/`, MCP and `/realtime` routes retain the normal
Apteva platform/app-token gate. Version 0.3.0 adds a separate Auth-only endpoint
at `/public/graphql/{api_slug}` for project-scoped installations. It is disabled
unless that API has an explicit Auth policy. There is no anonymous fallback.

## Trusted authenticated Functions (0.3.0)

GraphQL authenticates independently of the API app. Apteva Auth validates the
bearer token, session revocation and project via `/me`; GraphQL verifies the
configured tenant, takes only server-managed authorization claims, and bounds
execution by both credential expiry and a 15-second request deadline.
Authentication responses are not cached. Browser credentials go only to Auth,
never into Function identity claims or resolver headers. API policies and
protected resolver bindings are read fresh on every request.

Configure in **Authentication**, or use `graphql_security_set`:

```json
{
  "api_slug": "workspace-test",
  "security": {
    "mode": "auth",
    "tenant_id": "default",
    "environment": "production",
    "claims": ["roles", "permissions", "authorization_version"],
    "permissions": [],
    "fields": {"Query.reports": ["reports:read"]}
  }
}
```

The HTTP equivalent is `PUT /admin/security?api_slug=workspace-test` with
`{"security":{...}}`. `GET /admin/security`, `graphql_security_get`,
`POST /admin/security/validate`, and `graphql_security_validate` provide inspection
and read-only Function trust checks. Security changes apply immediately, not
only when a schema is published. `{"mode":"platform"}` disables public execution.
The public environment is pinned in policy; browser overrides cannot select a
different schema. API permissions and additional `Type.field` permissions are
all-required and checked for the entire operation before any resolver runs,
including nested scalar projection and Tables batch paths.

Create a Function source, then configure its resolver in **Function security**:

```json
{
  "authenticated": true,
  "function_id": 95,
  "function_ids": [95],
  "contract": "http"
}
```

IDs are illustrative. Configure the actual project Function IDs. The allowlist
must contain the root and only its permitted nested Function targets. The
adapter invokes `functions_invoke_authenticated` through the platform with a
verified principal, absolute deadline and correlation ID. Functions verifies
the platform-bound caller installation and issuer, then injects trusted
`event.requestContext.authorizer.principal` and `context.invocation`. Failed
admission never falls back to ordinary invocation.

Each target Function must explicitly allow this GraphQL installation and
`apteva:auth:<tenant>` issuer in its invocation policy. GraphQL never grants
itself trust or weakens `require_authenticated`. Publishing an Auth API checks
configured Function trust; runtime admission remains authoritative even if the
policy changes after publication. The generic resolver JSON editor remains
available for other source options.

- `contract: "graphql"`: receives `arguments`, `parent`, and `project_id`;
  returns the GraphQL field value directly.
- `contract: "http"`: receives arguments in `event.body`; requires a 2xx
  `{statusCode,body}` response and decodes a JSON string body when necessary.
  This compatibility mode requires authenticated invocation. Function 401/403
  responses become sanitized GraphQL errors, not successful data. Arbitrary
  Function response headers are not forwarded.

Call `/api/apps/graphql/public/graphql/workspace-test?project_id=<project>` with
`Authorization: Bearer <user Auth token>` and the usual GraphQL JSON body.
Responses are `private, no-store`. Internal execution and the admin endpoint
cannot impersonate a user or bypass an Auth API's identity requirement.
Existing platform-only APIs keep their behavior. App/API keys are not treated
as user identities.

### Boundaries of this release

- Apteva Auth bearer tokens only; OIDC and custom authorizers are not included.
- Public authenticated execution requires a project-scoped installation.
- Protected subscriptions are denied, including through internal entry points,
  until subscription/event-level authorization is available.
- Authentication and field permissions are not row-level authorization. Keep
  domain/team/centre filtering in Functions. Direct Tables/HTTP sources execute
  using installation permissions and must not expose unrestricted resource
  selectors to untrusted users.
- Cross-origin access still requires platform-managed CORS configuration.
- This release does not change any existing Function trust policy, Flexylead
  endpoint, or installed app. A GraphQL Function wrapper is not a speedup by itself.

### Tests

Run `GOWORK=off go test -race ./...` and `GOWORK=off go vet ./...`.
For an isolated three-process integration test, also set `GRAPHQL_TEST_AUTH_DIR`
and `GRAPHQL_TEST_FUNCTIONS_DIR` to source directories of compatible releases
(tested with Auth 0.12.0 and Functions 1.14.1). The test creates disposable
databases, signs up a test user, checks nested trusted identity, rejected caller
installations, scope removal, forged inputs and internal-entry-point bypasses.

## Aggregation

Use `operation: "aggregate"` with a Database or Tables source and configure
`groupBy`/`group_by` and `metrics`. The adapter delegates aggregation to the
native source instead of scanning records in the GraphQL process.

## Execution performance (0.2.2)

Independent root query fields execute concurrently (up to eight); mutation
fields remain ordered. Compiled schemas and validated documents are cached,
and resolver/source bindings are loaded once per request with a bounded
one-second metadata cache. Scalar row projections avoid per-cell metadata
lookups. Query results themselves are not cached.

Small Tables read fan-outs use `tables_batch` (requires Tables 0.1.22 or newer).
Reads whose summed requested limits exceed 500 use parallel individual calls.
Rows-only queries skip total counts unless explicitly configured otherwise.
Batching uses `best_effort`, not cross-table snapshot consistency.

Version 0.3.0 pins SDK v0.82.0, including cancellable trusted Function calls and
negotiated inner JSON results. Platform-only read optimizations are preserved.

## Realtime transport

`/realtime` speaks the `graphql-transport-ws` framing. A subscription resolver
can set `config.topic`; Tables row events are bridged automatically and trusted
source adapters or the `graphql_event_publish` tool can publish matching
events. Database-native change-feed integration is the next step for full
collection CDC delivery.
