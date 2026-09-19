# GraphQL app

The standalone `graphql` app owns GraphQL schemas, resolver bindings,
aggregation-aware source adapters, and realtime subscriptions. It is
deliberately independent from the REST/API gateway app.

## Typed filtering, identity row policies and selection pushdown (0.5.x)

Tables resolvers accept typed GraphQL input objects as `where` arguments. API
authors define the input types in ordinary SDL; the adapter lowers scalar
operators (`eq`, `neq`, comparisons, `contains`, `in`, `notIn`, `between` and
null checks), nested `and`, invertible `not`, and same-column equality `or` to
native Tables predicates. Existing `[{col,op,value}]` inputs remain compatible.
Use source/resolver `filter_columns` to map GraphQL input field names to table
columns. Unsupported disjunctions fail closed because Tables' native predicate
list is conjunctive; the adapter never broadens a query or filters a truncated
page in memory.

GraphQL pagination conventions map without changing the schema: `first` maps to
Tables `limit`, `after` to `cursor`, and `includeTotal` to `include_total`.
`limit`, `cursor`, and snake-case names remain compatible.

Auth policies can enforce non-overridable identity-derived predicates before a
Tables read:

```json
{
  "mode": "auth",
  "tenant_id": "default",
  "environment": "production",
  "claims": ["centre_ids"],
  "row_filters": {
    "Query.prospects": [
      {"column":"commercial_id", "identity":"subject", "value_type":"string"},
      {"column":"centre_id", "op":"in", "identity":"claim.centre_ids", "value_type":"string"}
    ]
  }
}
```

Identity sources are `subject`, `tenant`, or an explicitly requested safe
`claim.<name>`. Configured source filters, identity filters, client filters and
relationship keys are always ANDed. Query arguments can no longer replace a
source's fixed `where` scope.

For Tables `find`, `list`, `search`, and `get`, the validated GraphQL selection
is automatically pushed down as the native `select` list. Aliases and fragments
are resolved first, configured select allowlists are retained, and hidden parent
keys needed by selected relationships are added. This reduces inter-app JSON
encoding without changing the GraphQL response or completion rules.

Version 0.6.0 adds reusable, versioned Resolver Modules behind ordinary
GraphQL fields. Modules use a validated typed expression tree rather than
scripts, can compose pinned module versions, map parent/argument/identity
inputs, and remain compatible with Tables selection pushdown and fast
projection. Version 0.5.1 added bounded ordered distinct selection for Tables `find`/`list`
resolvers. Configure `distinct_by` with up to eight columns, optionally
`distinct_defaults` for null/empty key normalization, and
`distinct_scan_limit` (maximum 1,000). Tables applies fixed/client filters and
ordering first; GraphQL keeps the first row for each key tuple and then applies
the schema's `first`/`limit`. Distinct keys are automatically included as hidden
source projections when the client does not select them. This supports standard
GraphQL fields for “latest row per group” without returning all candidates.

```json
{
  "order_by": "starts_at desc",
  "distinct_by": ["offer_id", "sale_type"],
  "distinct_defaults": {"sale_type": "standard"},
  "distinct_scan_limit": 1000
}
```

## Standard execution and direct Tables fields (0.4.0)

Schemas and client requests use ordinary GraphQL SDL and operations. There are
no special `me`, workspace, pipeline, expression, or SQL constructs in this
release. API authors choose their own field names and input/output types.

The execution layer uses graphql-go for general field collection and result
completion, with gqlparser validation and an explicit input-coercion boundary
to preserve modern null/default semantics. It supports named/inline fragments,
aliases and merged fields, `@skip`/`@include`, strict variables, input objects,
enums, lists, introspection, interfaces/unions (source rows must provide
`__typename`), and serial mutation roots. Errors contain field paths and source
locations; nullable field failures preserve successful sibling data, while
non-null failures propagate to the appropriate parent.

HTTP supports POST and query-only GET. GET mutations are rejected before any
resolver runs. Request `extensions` metadata is accepted but does not enable
persisted queries or affect trusted identity. Execution errors with partial
data normally return HTTP 200; admission/authentication failures retain their
4xx responses. Clients must inspect `errors`, not just the HTTP status.

### Example: ordinary fields backed directly by Tables

```graphql
type Query { customers(limit: Int = 10): [Customer!]! }
type Customer {
  id: ID!
  name: String!
  orders(limit: Int = 10, cursor: String, include_total: Boolean = false): OrderPage!
}
type OrderPage {
  rows: [Order!]!
  total: Int
  has_more: Boolean!
  next_cursor: String
}
type Order { id: ID! customer_id: ID! amount: Float! }
```

Create Tables sources with fixed `table` configuration, then bind
`Query.customers` to the customers source with operation `find`. Bind
`Customer.orders` to the orders source with operation `search` and:

```json
{
  "order_by": "id asc",
  "relation": {"parent_key": "id", "foreign_key": "customer_id"}
}
```

`relation` is an adapter column mapping, not part of GraphQL syntax. It adds a
mandatory equality filter from the actual parent record. Client arguments
cannot replace the fixed table or this relationship predicate. Configured and
client filters are combined for relationships. Include parent join keys in any
source `select` configuration; missing keys fail closed. Relationships support
`find`, `list`, `search`, `count`, and `aggregate`, not unfiltered `get` or writes.
When the Tables foreign-key column deliberately uses a different primitive type
than its parent value, set `value_type` to `string`, `number`, or `boolean` in
the relation mapping. Coercion is explicit so the adapter never guesses from
identifiers or silently changes ordinary same-type relationships.

```graphql
query CustomerOrders($limit: Int = 10) {
  customers(limit: $limit) {
    id
    name
    orders(limit: 5, include_total: true) {
      rows { id amount }
      total
      has_more
      next_cursor
    }
  }
}
```

`find`/`list` return row lists; `search` preserves the native Tables result
envelope, including its opaque scoped cursor. Pass `next_cursor` back as
`cursor` on the same relationship. Counts are opt-in via `include_total`, not
inferred from field selection. Relay connections are an API design convention,
not a GraphQL requirement; this adapter does not impose them or remap page keys.

All reads go directly to Tables, with no Function invocation. A request-local
loader deduplicates identical reads and batches small independent reads across
siblings and list parents through `tables_batch`; larger reads run concurrently
with a bound of eight source tasks. A relationship batch still contains one
filtered operation per distinct parent; it is not a single SQL join. Per-parent
pagination stays independent. Unselected/skipped fields do not load data.
The loader permits at most 1,000 source jobs per request and never shares row
results across users or requests. Mutation source calls are never deduplicated.

Use the existing Resolvers UI (operation and JSON configuration), MCP
`graphql_resolver_set`, or HTTP `/admin/resolvers`; they share the same storage
and relation validation. No server, SDK, or Tables changes are needed; Tables
0.1.22+ is required for batching.

### Boundaries and upgrade notes

- This is not a claim of complete support for every GraphQL specification
  revision: `@oneOf`, interface inheritance, incremental delivery, and custom
  executable directive behavior are not implemented. Unsupported `@oneOf` and
  interface inheritance schemas are rejected at publication. Custom scalars
  retain JSON passthrough semantics, not automatic DateTime/UUID validation.
- Existing Auth admission and field permissions remain. Relationships alone
  are not authorization; authenticated Tables roots can now declare explicit
  identity-derived row filters. The app does not infer an application's
  owner/team/centre model or invent identity-to-entity joins.
- Field permissions are conservatively preflighted before execution, including
  fields behind conditional directives, and checked again at resolution.
- Built-in scalar completion now follows the execution engine, including ID
  serialization as strings. Schemas that previously relied on omitted required
  fields may now produce non-null errors. Nullable errors may return partial
  data rather than failing the entire operation.
- Existing platform-only WebSocket event transport is unchanged; protected
  subscriptions remain disabled. This release does not claim graphql-transport-ws
  protocol compliance.

Real-process integration tests are opt-in with `GRAPHQL_TEST_TABLES_DIR`,
`GRAPHQL_TEST_AUTH_DIR`, and `GRAPHQL_TEST_FUNCTIONS_DIR`. Tests create isolated
databases and never alter an installed project.

### Guarded fast completion (0.4.1)

Ordinary GraphQL inputs and validation are unchanged. Concrete Tables query
selections now use a compiled projection plan for built-in scalar fields,
including aliases, merged selections, fragments, directives, nested
relationships, page envelopes, counts and aggregates. It calls the same scalar
serializers as the standard engine and retains field permission checks.

Unsupported selections (including abstract output types, enums, custom scalars,
introspection and non-Tables sources) use the standard engine. Failed source
reads, invalid output values and non-null failures also fall back to standard
error completion, sharing the same request-local read cache so completed reads
are not repeated. Mutations always use the standard ordered executor. A query
does not lose a capability just because it is ineligible for fast completion.

An isolated Apple M1 Pro real-process test stored 10,000 rows in each of three
Tables tables and returned 1,000 rows per table, five scalar fields each.
After five warmups, 30 rotating trials with exact data parity measured:

| Path | Median | p95 |
| --- | ---: | ---: |
| GraphQL 0.4.0 | 29.71 ms | 33.94 ms |
| GraphQL 0.4.1 | 14.44 ms | 16.78 ms |
| Direct parallel Tables calls | 5.67 ms | 8.63 ms |

This uses a forwarding test gateway, not the installed management server, and
does **not** establish native-call parity or predict production latency. A
separate preloaded-row microbenchmark isolates projection/execution overhead:
about 1.4–1.5 ms versus 15.4–19.6 ms, with roughly 94% fewer allocated bytes.
Reproduce real-process timings by also setting `GRAPHQL_BENCH_BASELINE_DIR` to
the v0.4.0 GraphQL source and running `go test -run TestRealThreeTablePerformance -v`.

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
- Authentication and field permissions alone are not row-level authorization.
  Configure `row_filters` for authenticated Tables fields. Multi-hop identity
  mapping and business authorization still require an appropriate schema field,
  trusted resolver, or source-side model. HTTP sources execute using installation
  permissions and must not expose unrestricted resource selectors to users.
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

This remains available through standard typed GraphQL input objects and enums,
not custom query syntax. Counts, sums, averages, minima/maxima, grouping,
filtering and ordering are retained. Tables aggregates can also be scoped to
each parent with the same `relation` mapping as row reads. Define the result
fields and metric names in your schema; GraphQL itself does not standardize
aggregation field names. Aggregate queries return native aggregate rows, while
the `count` operation returns the scalar count.

Regression tests exercise real Tables grouped/filtered and per-parent metrics
against direct native results; Database adapter forwarding/result mapping;
mixed Tables/Database/HTTP/Function queries; pagination; API/project/environment
isolation; realtime hub scoping; Auth and trusted Function invocation.

## Execution performance

Independent root query fields execute concurrently (up to eight); mutation
fields remain ordered. Compiled schemas and validated documents are cached,
and resolver/source bindings are loaded once per request with a bounded
one-second metadata cache. Scalar row projections avoid per-cell metadata
lookups. Query results themselves are not cached.

Small Tables read fan-outs use `tables_batch` (requires Tables 0.1.22 or newer),
in groups of at most five operations with individual requested limits up to 100.
Larger reads use parallel individual calls, bounded to eight source tasks.
Rows-only queries skip total counts unless explicitly configured otherwise.
Batching uses `best_effort`, not cross-table snapshot consistency.

Version 0.3.0 pins SDK v0.82.0, including cancellable trusted Function calls and
negotiated inner JSON results. Platform-only read optimizations are preserved.

## Realtime transport

`/realtime` retains its existing platform-only event transport with
`graphql-transport-ws`-style frames, not full protocol/spec compliance.
Protected subscriptions remain disabled. A subscription resolver
can set `config.topic`; Tables row events are bridged automatically and trusted
source adapters or the `graphql_event_publish` tool can publish matching
events. Database-native change-feed integration is the next step for full
collection CDC delivery.
