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

For relationships that need more than one equality key, configure the
server-owned `relation_filter` plan. This does not add syntax to GraphQL: clients
still send standard fields, arguments and typed `where` inputs. The plan is
validated when the resolver is saved and again when an atomic API release is
published. Client filters are ANDed outside the plan and cannot replace fixed,
identity-derived, or relationship predicates.

```json
{
  "relation_filter": {
    "and": [
      {
        "or": [
          {"eq":[{"column":"prospect_id"},{"parent":"id"}]},
          {"eq":[{"column":"prospect_business_id"},{"coalesce":[{"parent":"business_id"},{"parent":"legacy_business_id"}]}]}
        ]
      },
      {
        "exists": {
          "source": "calls",
          "correlate": [{"outer":"id","inner":"prospect_id"}],
          "where": {
            "and": [
              {"eq":[{"column":"status"},{"const":"open"}]},
              {"gte":[{"column":"created_at"},{"argument":"since"}]}
            ]
          }
        }
      }
    ]
  },
  "relation_scan_limit": 10000
}
```

Boolean operators are `and`, `or`, `not`, `exists`, and `not_exists`.
Comparisons are `eq`, `neq`, `lt`, `lte`, `gt`, `gte`, `contains`, `in`,
`between`, `is_null`, and `is_not_null`. Values can come from `column`,
`parent`, `outer`, `argument`, or `const`, and can use `coalesce` or an explicit
`cast` to `string`, `number`, or `boolean`. Dotted parent and argument paths are
supported. Correlated sources are immutable, active Tables sources in the same
API release; source/table names are never supplied by clients.

With Tables v0.1.25+, GraphQL compiles the saved plan to native `filter_ast` v1.
Tables performs recursive and correlated filtering before pagination, totals,
and aggregation; native filtered reads retain request-local deduplication and
`tables_batch`. Capability negotiation is cached after a successful probe.

For an older Tables runtime, GraphQL retains a compatibility fallback: simple
top-level AND predicates are pushed down and the remaining plan is evaluated
over ordered candidate pages. Its default candidate and correlated-row budget
is 10,000 (configurable up to 100,000). Hitting it returns
`relation_filter_scan_limit_exceeded`; partial data is never returned. Resolvers
without `relation_filter` keep the ordinary Tables batch, projection, and fast
completion paths on every supported Tables version.

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

For a server-defined multi-table aggregation, use the separate
`aggregate_pipeline` operation. This does not add syntax to GraphQL: clients
still select an ordinary typed field. The immutable resolver configuration
declares active Tables sources, one read-only `SELECT`/`WITH` query using
`{table_name}` placeholders, and positional parameters derived from coerced
`$args`, `$parent`, verified `$identity`, or fixed literals:

```json
{
  "version": 1,
  "engine": "tables_query",
  "sources": ["prospects", "calls", "sales"],
  "sql": "SELECT p.centre_id, COUNT(*) AS total FROM {prospects} p LEFT JOIN {calls} c ON c.prospect_id=p.id WHERE p.centre_id=? GROUP BY p.centre_id",
  "params": [
    {"from": "$identity.claim.centre_id", "type": "string", "required": true}
  ],
  "result": "rows",
  "max_rows": 100,
  "on_truncated": "error"
}
```

All resolver versions (1, 2 and 3) permit 1 to 64 declared Tables sources.
The list is a fixed SQL access allowlist, including configured table/projection
mappings. Every declaration must be active, valid and unique; the bound source
must be included. Extra declarations do not fetch extra data or add queries.
Stage, SQL, row, byte, time and concurrency limits are independent and unchanged.

`result` is `rows`, `single`, or `envelope`. Truncation fails closed by
default. SQL, identifiers, source names, and ordering can never come from the
client. Automatic `row_filters` and pre-scoped Tables sources are rejected for
pipelines because silently applying them outside the fixed SQL would be
unsafe; put authorization predicates directly in fixed SQL and bind only
verified `$identity.*` values. Each pipeline is one Tables call, sibling
pipelines share the existing batch transport, and Tables executes joins,
window functions, grouping, and conditional metrics in SQLite.

Regression tests exercise real Tables grouped/filtered and per-parent metrics
against direct native results; Database adapter forwarding/result mapping;
mixed Tables/Database/HTTP/Function queries; pagination; API/project/environment
isolation; realtime hub scoping; Auth and trusted Function invocation.

### Database writes and access guards

`update` and `delete` resolvers on a Database source forward the client's
`key`, `set`/`increment` and `ifVersion`. A `where` in the resolver config is
the access guard: it is always sent, and a client `where` is combined with it
(`and`), so a client can only narrow it. `maxAffected` can be lowered but not
raised; without a resolver value it is 1. `all`, `conflictIndex`,
`requireIndex`, `lookups` and `buckets` come only from resolver config.

Read resolvers (`find`, `list`, `search`, `aggregate`) forward configured
`lookups` (`[{as, collection, localField, select?}]`, at most 4) and, on
`aggregate`, `buckets` (`[{name, field, unit, timezone?, weekStart?}]`).
Both are validated when a release is published, and a subscription on such a
resolver also watches the lookup targets.

With Database 0.6.5 or later, a key plus a guard edits the record only if it
also matches the guard. A miss reads like an absent record: `affected: 0`, or
`version_conflict` with `ifVersion`. Earlier Database versions refuse a key
combined with a guard with `invalid_argument`.

Database's failure classes reach clients as `errors[].extensions.code`, with
Database's message: `version_conflict` (stale revision, or a record outside
the guard), `unique_conflict`, `not_found`, `invalid_argument`,
`permission_denied`, `resource_limit`, `query_too_expensive`, `schema_conflict`,
`unsupported`, and the realtime codes. Other failures stay `execution_error`.

`TestRealDatabaseGuardedEdits` checks all of this against a real Database: a
client filter with `or`, `all: true` or a raised bound cannot reach another
centre's record; stale, cross-centre and missing keys give `version_conflict`
and change nothing; of two concurrent edits with the same revision exactly
one wins.

### Database pipelines (`database_pipeline`)

`database_pipeline` answers a whole report from Database without SQL. It is a
resolver operation on a `database` source and needs Database 0.6.0 or later (the
manifest requires it)
(read batches, `count_distinct`, metric filters, lookups and buckets). A run
has up to three phases:

1. **access** (optional): checks permissions from the verified identity and
   resolves the values the caller may read, such as teams, before any data is
   read. Requested values must be a subset of what the caller may read; refusals
   fail the field with `permission_denied`.
2. **steps**: 1 to 100 `aggregate`, `find`, `count` or `get` operations, sent
   to Database as ONE read-only `db_batch`, so every number comes from the
   same snapshot.
3. **combine** (optional): joins grouped step rows on their keys, adds a
   total row from ungrouped steps, and computes fields with resolver-module
   expressions (`add`, `subtract`, `divide`, `safe_divide`, `round`, `if`, ...).

```json
{
  "params": {
    "from":  {"from": "$args.from", "type": "datetime", "required": true},
    "to":    {"from": "$args.to", "type": "datetime", "required": true},
    "teams": {"from": "$args.teamIds", "type": "string_list"},
    "user":  {"from": "$identity.subject", "type": "string", "required": true}
  },
  "access": {
    "require_any_permission": ["reports:read"],
    "values": {"teams": {
      "requested": "$param.teams",
      "unrestricted_if_permission": "reports:all_teams",
      "default":   {"collection": "teams", "field": "id", "where": {"field": "active", "op": "eq", "value": true}},
      "otherwise": {"collection": "members", "field": "team_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}},
      "single": true
    }}
  },
  "steps": [
    {"id": "calls", "op": "aggregate", "max_rows": 100, "args": {
      "collection": "calls",
      "where": {"and": [
        {"field": "team_id", "op": "in", "value": "$scope.teams"},
        {"field": "started_at", "op": "gte", "value": "$param.from"},
        {"field": "started_at", "op": "lt", "value": "$param.to"}]},
      "groupBy": ["agent_id"],
      "metrics": [
        {"name": "callers", "op": "count_distinct", "field": "caller"},
        {"name": "answered", "op": "count_distinct", "field": "caller", "where": {"field": "answered", "op": "eq", "value": true}}]}},
    {"id": "calls_total", "op": "aggregate", "args": {"collection": "calls", "where": {"field": "team_id", "op": "in", "value": "$scope.teams"},
      "metrics": [{"name": "callers", "op": "count_distinct", "field": "caller"}, {"name": "answered", "op": "count_distinct", "field": "caller", "where": {"field": "answered", "op": "eq", "value": true}}]}}
  ],
  "combine": {"byAgent": {
    "join": ["calls"], "on": ["agent_id"], "missing": "zero",
    "total": {"key": "__total__", "steps": {"calls": "calls_total"}},
    "fields": {
      "callers": "calls.callers",
      "unanswered": {"op": "subtract", "args": ["calls.callers", "calls.answered"]},
      "answer_rate": {"op": "safe_divide", "args": ["calls.answered", "calls.callers"]}
    }
  }},
  "result": {"agents": "$combine.byAgent", "teams": "$scope.teams"}
}
```

Rules:

- Collections, fields, metrics, groupings, lookups, buckets and ordering are
  resolver configuration. Clients reach a plan only through declared `params`
  (`$args`, `$parent`, verified `$identity`, or fixed values; types `string`,
  `number`, `boolean`, `datetime`, `json`, `string_list`, `exact_string_list`).
  `$param.<name>` and `$scope.<name>` are accepted only as filter values,
  metric-filter values and `get` keys. `string_list` trims values and drops
  blanks, which suits authorization lists; `exact_string_list` keeps identifiers
  as sent and only removes exact duplicates.
- Access values: an unrestricted caller (`unrestricted_if_permission`) gets
  exactly the values it requested, or the `default` read when it requested
  none. Every other caller gets the `otherwise` read (`single: true` requires
  exactly one distinct value, `single_record: true` exactly one matching
  record), narrowed to its request. `require_any_permission` on a value refuses
  callers holding none of the listed permissions. Permissions come from the
  verified Auth identity. Access reads run before the steps.
- A client can narrow a value with `requested`, and anything derived from it. In a
  positive filter that only returns fewer rows, so that's the only place it's allowed.
  A plan is refused if such a value reaches an `access_when` condition or filter, a
  result's `$if`, a relationship's `where`, `not_in`, `ne` or a `combine` input, or an
  `expr` that decides on it, because a shorter list could return more rows or lift a mask.
  An `expr` can still pass the list through as an `if` result.
- Access values can chain. A read's `where` may use `$scope.<value>`, and
  `{"union": [...]}` or `{"intersect": [...]}` combines earlier values without
  a read. Values run in dependency order, one read batch per level, for at
  most 4 levels; cycles are rejected. An array field contributes each element.
  A read with `orderBy` and `limit` keeps its first records instead of failing
  past `max` (for example "the latest enabled grant"). An empty value refuses
  the request before anything that depends on it is read, unless the value is
  `optional: true`: then it stays an empty scope, and any filter using it
  matches nothing (an empty `in` is sent as a contradiction, never dropped).
- Access values trim identifiers, drop blanks and remove duplicates by default,
  at every stage. `"type": "exact_string_list"` on a value keeps identifiers as
  stored and only removes exact duplicates, so `[]`, `[""]` and `["   "]` stay
  different and `" a "` doesn't match `"a"`. A blank or padded identifier still
  matches a record that really holds it, and numbers still become strings. The
  type is resolver configuration; clients can't set it.
  - Set it on each read, `union`, `intersect` and `expr` that must keep
    identifiers. A derived value reading an exact one must be exact too, or
    publishing is refused, because it would normalise them again. An exact value
    can read a normalised one, which stays normalised, and a normalised read can
    filter on an exact value.
  - `requested` on an exact value must name an `exact_string_list` param. Each
    requested identifier must be in the caller's access exactly as sent, and `""`
    is a request for that identifier, not an empty request.
  - `intersect` and `requested` compare strings exactly. Inside an `expr`, `eq`
    and `in` still compare numeric-looking strings as numbers, so `"01"` equals
    `"1"` there.
  Read failures (truncation past `max`, `single`/`single_record`,
  `require_any_permission`, Database errors) still refuse an optional value.
- `{"expr": ...}` derives a value from verified params and earlier values
  with `if` (`condition`/`then`/`else`), `and`, `or`, `not`, `in`, `length`,
  `eq`, `neq`, `lt`, `lte`, `gt`, `gte`, `coalesce` and `{"const": ...}`.
  It may reference `$scope.<value>` and `$param.<name>` only for params with a
  fixed value or a `$identity.` source, never client `$args`. It must return a
  flat list (null is empty). Derived values (`union`, `intersect`, `expr`) run
  in their dependency's level and do not count towards the 4 read levels.

  ```json
  "grantOffers": {"optional": true, "otherwise": {"collection": "grants",
                  "field": "offer_ids", "where": {"field": "user_id", "op": "eq", "value": "$param.user"},
                  "orderBy": [{"field": "id", "direction": "desc"}], "limit": 1}},
  "catalogue":   {"optional": true, "otherwise": {"collection": "offers", "field": "id"}},
  "offers":      {"optional": true, "expr": {"op": "if",
                  "condition": {"op": "in", "args": [{"const": "reports:all"}, "$param.permissions"]},
                  "then": "$scope.catalogue", "else": "$scope.grantOffers"}}
  ```

- `"distinct": true` on an access read fetches the field's distinct values as
  a Database aggregate (`groupBy` on the field, null values excluded), so
  `max` bounds the distinct values instead of the matching records: 5,000
  sales in one centre are one centre. More than `max` distinct values fail
  with `source_result_truncated`; nothing is silently dropped. It cannot be
  combined with `orderBy`/`limit` or `single_record` (which counts records),
  and the field must be a scalar (Database rejects grouping on JSON fields).

  ```json
  "saleCentres": {"optional": true, "otherwise": {"collection": "sales",
                  "field": "centre_id", "distinct": true, "max": 999,
                  "where": {"field": "offer_id", "op": "in", "value": "$scope.grantOffers"}}}
  ```

- `in` filters with more than 100 values are split into `or` chunks of 100
  (Database's limit per `in`); a find or count whose filter matches nothing
  is answered locally without a Database call.

  ```json
  "values": {
    "grant":  {"otherwise": {"collection": "grants", "field": "id",
               "where": {"and": [{"field": "user_id", "op": "eq", "value": "$param.user"},
                                 {"field": "enabled", "op": "eq", "value": true}]},
               "orderBy": [{"field": "granted_at", "direction": "desc"}], "limit": 1}},
    "offers": {"otherwise": {"collection": "grant_offers", "field": "offer_id",
               "where": {"field": "grant_id", "op": "in", "value": "$scope.grant"}}},
    "grids":  {"otherwise": {"collection": "offers", "field": "grid_ids",
               "where": {"field": "id", "op": "in", "value": "$scope.offers"}}},
    "offerCentres": {"otherwise": {"collection": "offers", "field": "centre_id",
               "where": {"field": "id", "op": "in", "value": "$scope.offers"}}},
    "home":   {"otherwise": {"collection": "profiles", "field": "centre_id",
               "where": {"field": "user_id", "op": "eq", "value": "$param.user"}},
               "single_record": true},
    "centres": {"intersect": ["offerCentres", "home"]}
  }
  ```

  Each level reads its own snapshot; the steps still read one snapshot.
  Database sources do not use `security.row_filters`, which are Tables-only
  (setting one on another source is rejected): access values are the Database
  authorization mechanism, and client params can only narrow them because they
  appear only as filter values.
- Conditional filters: a child of a step or metric filter's `and` group may be
  `{"when": <condition>, "filter": <filter>}`, which lets an empty client value
  mean "no filter" without changing what an empty `in` means:

  ```json
  "where": {"and": [
    {"field": "centre_id", "op": "in", "value": "$scope.authorisedCentres"},
    {"when": {"op": "gt", "args": [{"op": "length", "args": ["$param.centres"]}, {"const": 0}]},
     "filter": {"field": "centre_id", "op": "in", "value": "$param.centres"}},
    {"when": {"op": "neq", "args": ["$param.status", {"const": ""}]},
     "filter": {"field": "status", "op": "eq", "value": "$param.status"}}]}
  ```

  A false condition removes the child; a true one replaces it with its filter.
  If nothing is left the read has no `where`. The page and its `count_of` total
  resolve the same conditions, and subscriptions evaluate them again, with the
  access values, on every refresh.

  They exist to make client filters optional, never authorization:
  - `$scope` and identity-sourced params are rejected in both `when` and
    `filter`, so a permission predicate is always an unconditional sibling.
    Conditional filters are also rejected inside `or` and inside each other, except
    in a literal search group (see below), at the root of a filter, in access reads
    and in plain `find` resolvers.
  - Conditions read `$param.<name>` and `{"const": ...}` with `length`, `eq`,
    `neq`, `lt`, `lte`, `gt`, `gte`, `and`, `or`, `not` and `coalesce`. Params
    must be fixed or a top-level `$args.<name>`, of type `string`, `number`,
    `boolean` or `exact_string_list`; for an optional date, use a `string`
    param, because `datetime` refuses `""`. `string_list` is refused, because
    dropping a blank could switch the filter off.
  - Types are checked when the resolver is set: the result must be Boolean,
    comparisons need matching types, and nothing is decided by truthiness. A
    value that is null at run time fails the request unless `coalesce` handles
    it. Every operand is evaluated, so the failure doesn't depend on the others.
  - Inside `filter`, `in` needs a list or an `exact_string_list` param. The
    filter is validated in full whatever the condition would evaluate to, and
    the text search rules below still apply.
  - Publishing requires each argument used to exist with the matching shape and
    to be non-null or have a default (`centres: [String!]! = []`,
    `status: String = ""`), and reads one record selecting the conditional
    fields, so a mistyped field refuses the release. Aggregates with `lookups`
    or `buckets` skip that probe.

  An `exact_string_list` over an integer field fails with `invalid_argument` on
  a non-numeric value; nothing is dropped to make it fit.
- Relational filters (Database 0.8.0+): a step filter may hold Database's
  correlated `exists` and `not_exists`, so a permission that depends on related
  records is enforced by Database, with no list of eligible ids:

  ```json
  {"exists": {"collection": "sales",
              "anyOf": [{"on": [{"localField": "sale_ref", "foreignField": "id", "comparison": "text"}]},
                        {"on": [{"localField": "local_id", "foreignField": "call_id"}]}],
              "where": {"field": "offer_id", "op": "in", "value": "$scope.offerRefs"}}}
  ```

  `on` holds 1 to 8 pairs that must all match. `anyOf` holds 1 to 16 such lists,
  any of which may match. `comparison: "text"` compares a text field with an
  integer one; it is the only comparison mode. Collections and fields are
  configuration: placeholders are refused, and a client can only supply values.
  - `where` binds params and access values like any filter, with long `in` and
    `not_in` lists chunked. It can't hold a client `when` or
    `like_contains_ascii`, at any depth, apart from the search clauses described
    under literal search below. Under a `not_exists`, at any depth, it can't hold
    client values either, because negating a client filter would widen the result.
  - With Database 0.10.0+, `where` can hold further relationships, up to 4
    levels. A nested `localField` reads from the record of the clause holding
    it, so a chain goes report to call to sale:

    ```json
    {"exists": {"collection": "calls",
                "on": [{"localField": "call_ref", "foreignField": "local_id"}],
                "where": {"exists": {"collection": "sales",
                                     "on": [{"localField": "local_id", "foreignField": "call_id"}],
                                     "where": {"field": "offer_id", "op": "in", "value": "$scope.offerRefs"}}}}}
    ```

    Every level needs an index on a related field, and each record still counts
    once. A page and its total each resolve the chain, inside the batch's one
    snapshot. Database gives nested filters about 30 s of read work and fails the
    query past it, under `not_exists` too.
  - A filter holds at most 24 relationships on Database 0.11.1+ and 8 before it,
    counting every branch and level of it, optional ones included, because one
    request can use them all. Publishing refuses a plan past the installed
    Database's limit. They are
    rejected in metric filters, access reads and plain `find`, `count` and
    `aggregate` resolvers, and in client `when` filters unless they search. A plain
    resolver's client `where` argument can't hold them, in any letter case.
  - Publishing asks Database to plan each relationship with its values removed,
    whichever way a selection would go, and a nested relationship is planned as
    part of the one holding it. An unknown collection or field, field types that
    can't be compared, a related field with no index and a Database that's too
    old all refuse the release: 0.8.0 for relationships, 0.10.0 for nested ones.
  - A `count_of` total uses the page's whole resolved filter in the page's
    snapshot. Subscriptions watch every collection a relationship names, at any
    level, so a change to a related record re-evaluates visibility. A cursor belongs to the
    resolved filter: if it changes, the cursor is refused, and every page is
    authorised again.

  Relationships cost far more than an indexed scope, so keep a selective
  `centre_id`-style scope beside them.
- Policy selection: when a permission applies only to some users, a child of an
  `and` group may be `{"access_when": <condition>, "filter": <filter>}`. It is
  built from verified values only, and it is separate from `when`:

  ```json
  "where": {"and": [
    {"field": "centre_id", "op": "in", "value": "$scope.authorised"},
    {"access_when": {"op": "and", "args": [
        {"op": "gt", "args": [{"op": "length", "args": ["$scope.grantOffers"]}, {"const": 0}]},
        {"op": "not", "args": [{"op": "in", "args": [{"const": "calls:all_centres"}, "$param.permissions"]}]}]},
     "filter": {"and": [
       {"exists": {"collection": "sales", "on": [{"localField": "local_id", "foreignField": "call_id"}],
                   "where": {"field": "offer_id", "op": "in", "value": "$scope.offerRefs"}}},
       {"not_exists": {"collection": "sales", "on": [{"localField": "local_id", "foreignField": "call_id"}],
                       "where": {"field": "offer_id", "op": "not_in", "value": "$scope.offerRefs"}}}]}},
    {"when": {"op": "gt", "args": [{"op": "length", "args": ["$param.centres"]}, {"const": 0}]},
     "filter": {"field": "centre_id", "op": "in", "value": "$param.centres"}}]}
  ```

  The condition decides on the grant itself, so a grant naming only unknown
  offers still selects the restriction and matches nothing, rather than falling
  back to the normal scope. That holds for a value of type `exact_string_list`,
  where a blank grant is still a grant. A grant row whose value is null, or no
  row at all, counts as no grant, so removing the last row lifts a restriction
  written this way.
  - Conditions read `$scope.<name>`, `{"const": ...}` and params that are fixed
    or come from `$identity`, with the ops `when` allows plus `in` (a value and a
    list). Client params and `$args` are refused. Types are checked when the
    resolver is set, and a missing identity list fails the request rather than
    reading as "not listed".
  - `filter` takes only those same trusted values, including inside
    relationships. It can't hold `when`, a text search or another `access_when`,
    except inside a relationship's `where`, where a nested `access_when` can make
    one predicate optional.
  - `access_when` is rejected inside `or` and at the root of a filter. A
    selection that leaves nothing out of an `and` group behaves as `when` does.
- Text search (Database 0.7.0+): a step or metric filter may hold
  `{"fields": ["resource_key", "opportunity_key"], "op":
  "like_contains_ascii", "value": "$param.q"}`, which matches the fields joined
  by one space with SQLite's default `LIKE` (`%` and `_` stay wildcards, ASCII
  case folds, an empty value matches everything). Fields and op are
  configuration; the value is a literal, a fixed string param, or a top-level
  string `$args.<name>`. Because an empty search matches every record, it can
  only narrow: it is rejected inside `or` and in access reads, so it always
  sits beside the resolved scope:

  ```json
  "where": {"and": [{"field": "centre_id", "op": "in", "value": "$scope.centres"},
                    {"field": "status", "op": "eq", "value": "$param.status"},
                    {"fields": ["resource_key", "opportunity_key", "commercial_id"],
                     "op": "like_contains_ascii", "value": "$param.q"}]}
  ```

  Publishing also requires the argument to be `String!` or to have a default
  (`q: String = ""`) and asks Database to validate the search, so a missing
  field, a non-text field or an older Database refuses the release instead of
  failing at query time. Aggregates with `lookups` or `buckets` may search
  derived fields, so they skip that probe and any error surfaces when they
  run. A `count_of` total uses the page's filter in the page's snapshot.
- Literal search (Database 0.11.0+): a filter may hold `{"field": "label", "op":
  "contains_ascii", "value": "$param.q"}`. It tests one text
  field for a literal substring: `%`, `_` and `\` are ordinary characters, ASCII
  letters fold case, other characters compare exactly, and null reads as an empty
  string. The field and op are configuration. The value is a literal, a fixed
  string param, or a top-level string `$args.<name>`, sent as the client wrote it.

  An empty value matches every record. Beside other filters that changes nothing,
  so no guard is needed there. Inside an `or` or a relationship it would match
  everything, so the search must sit in a client `when` that reads its value, and
  a request that still gets a blank there fails rather than running:

  ```json
  "where": {"and": [
    {"field": "centre_id", "op": "in", "value": "$scope.centres"},
    {"when": {"op": "neq", "args": ["$param.q", {"const": ""}]}, "filter": {"or": [
      {"field": "source_name", "op": "contains_ascii", "value": "$param.q"},
      {"exists": {"collection": "sales", "on": [{"localField": "sale_ref", "foreignField": "local_id"}],
        "where": {"and": [
          {"field": "centre_id", "op": "in", "value": "$scope.centres"},
          {"access_when": {"op": "gt", "args": [{"op": "length", "args": ["$scope.offers"]}, {"const": 0}]},
           "filter": {"field": "offer_id", "op": "in", "value": "$scope.offers"}},
          {"or": [
            {"field": "contract", "op": "contains_ascii", "value": "$param.q"},
            {"when": {"op": "gte", "args": [{"op": "length", "args": ["$param.phone"]}, {"const": 6}]},
             "filter": {"field": "phone_digits", "op": "contains_ascii", "value": "$param.phone"}}]}]}}}]}}]}
  ```

  A blank `q` removes the whole group, so a report with nothing to search still
  appears under its scope. The rules keep a search from standing in for a
  permission:
  - A client `when` may hold a relationship only if it is a positive `exists` that
    searches. Any other relationship is policy and is refused there, so a client
    can't remove it.
  - Inside such an `exists`, `$scope` values, identity params and `access_when`
    still work, so linked records keep their own checks. A match on a linked
    record that fails them doesn't count. `not_exists` works there with trusted
    values only.
  - An `or` holding a search can't also read `$scope` or an identity param at its
    own level, or hold a relationship that doesn't search: one alternative matching
    would satisfy the whole `or`. Put those checks beside it.
  - Inside the search group, and only there, a branch of an `or` may be a client
    `when`. A false one drops that alternative, and the `or` must keep one branch
    without a condition.
  - Access reads can't search at all, and client values stay out of `not_exists`.

  The page and its `count_of` total send the same filter. Publishing checks the
  argument as for `like_contains_ascii`, and asks Database to validate each search
  on the collection that owns its field, through the relationship for related
  fields. A missing or non-text field, or a Database without the operator, refuses
  the release. `contains_ascii` never selects an index, so keep an indexed scope
  beside it on large collections.
- Filter operators are checked on publish: `eq`, `ne`, `gt`, `gte`, `lt`,
  `lte`, `in`, `not_in`, `starts_with`, `is_null`, `is_not_null`,
  `like_contains_ascii`, `contains_ascii`. An empty `not_in` matches every non-null value, because
  Database's `not_in` never matches a null field. Past 100 values it is sent as
  an `and` of chunks, as `in` is sent as an `or` of chunks.
- Paging: a `find` step with `"page": {"size": "$param.first", "cursor":
  "$param.after"}` reads one page (size defaults to `max_rows`, and larger
  requests are capped at it) and never fails on `hasMore`. Results can select
  `$steps.<id>.hasMore` and `$steps.<id>.nextCursor` (null on the last page).
  A count step with `"count_of": "<find step>"` counts the same collection and
  filter in the same snapshot. A `find` page uses Database cursors; joined reads
  also take offsets (see "Joined reads" below).
- Field masking: params can bind `$identity.permissions` (a `string_list` of
  the verified permissions), and combine expressions can read `$param.<name>`
  and `$scope.<name>`. The `in` op tests membership. `keep` copies a joined
  step's columns unchanged, so a combine over a `find` step on its key can
  null one field for some callers:
  `{"join": ["rows"], "on": ["id"], "keep": ["rows"], "fields": {"amount":
  {"op": "if", "condition": {"op": "in", "args": [{"const": "finance:read"},
  "$param.perms"]}, "then": "rows.amount", "else": {"const": null}}}}`.
  Resolver modules accept `in` and `$identity.permissions` too.
- `result` maps output fields to `$steps.<id>` (rows, records, a count or a
  record), `$steps.<id>.first`, `$combine.<id>` or `$scope.<name>`. It can
  also be one of those references on its own (`"result": "$steps.rows"`), and
  the field then returns that value directly, for list fields such as
  `commercials: [Commercial!]!`. Declare the field non-null in your SDL so any
  failure nulls the whole report.
- Configuration is strict: unknown keys, mistyped values (`"single": "yes"`,
  `"max": 2.5`) and malformed lookups, buckets or metrics fail validation
  instead of being ignored.
- Failures are never partial: a step over its `max_rows` (default and maximum
  1000) fails with `source_result_truncated`; Database budgets fail with
  `resource_limit`; exceeding `timeout_ms` (default 15000, maximum 60000)
  fails with `timeout`. A `find` step marked `top_n: true` returns its first
  `max_rows` records instead (for "latest N" lists).
- Integer counts and sums that Database returns as decimal strings become JSON
  numbers while they stay exact (within 2^53); larger values stay strings.
- A `Subscription` bound to a pipeline watches every collection the plan reads
  (steps, lookup targets and access reads) through one Database watch, and
  each change re-runs the whole report once per subscriber group.

What Database cannot compute while reading, such as window-function
deduplication ("first call per caller") or one-to-many existence checks,
belongs in fields stored on the record when it is written. Joins are
many-to-one lookups by primary key.

Measured end to end (`TestRealDatabasePipeline`, Database 0.6.0 on AptevaDB,
50,000 calls and 5,556 sales, 7 steps): every figure equals an independent Go
computation over the same records, including Paris day buckets across the
October DST change and a chained lookup; GraphQL adds well under 1 ms over
replaying the same `db_batch` directly; a sale commit reaches two subscribers
with one batch. Batch cost equals the sum of its steps: there, a chained lookup
(about 410 ms) and an unindexed latest-5 sort (about 78 ms) dominated; the
other steps took 1 to 17 ms each.

#### Joined reads (Database 0.12.0)

Steps can use Database's relational reads: `related_read`, `joined_page` and
`joined_aggregate`. Their `args` are Database's request, as its README describes
under "Relational reads", so a plan reads like the request it sends. Older Database
versions refuse them at publish.

```json
{"id": "page", "op": "related_read", "max_rows": 100, "args": {
   "collection": "reports",
   "where": {"and": [
     {"field": "centre_id", "op": "in", "value": "$scope.centres"},
     {"when": {"op": "neq", "args": ["$param.centre", {"const": ""}]},
      "filter": {"field": "centre_id", "op": "eq", "value": "$param.centre"}}]},
   "select": ["id", "local_id", "score_total"],
   "orderBy": [{"field": "modified_time", "direction": "desc"}, {"field": "id", "direction": "desc"}],
   "count": true,
   "relation": {"as": "display_context", "limit": 40, "sources": [...]},
   "match": {"and": [{"when": {"op": "neq", "args": ["$param.q", {"const": ""}]},
                      "filter": {"field": "client_name", "op": "contains_ascii", "value": "$param.q"}}]}},
 "page": {"size": "$param.limit", "offset": "$param.offset", "cursor": "$param.cursor"},
 "shape": {"total_as": "total_count", "json_text": ["display_context"], "numbers": ["score_total"]}}
```

**What a client can reach.** Client arguments only reach filters that narrow a
result: the root `where`, the joined-row `filter`, `match`, a branch's `where` and
`filter`, and the `where` of an outer metric. An `or` that mixes client values with
`$scope` or identity values is refused, because a client value could widen access
there. So is an `or` where only some alternatives read client values, since they could
add rows the fixed alternatives leave out; a client `when` around the whole `or` is
fine, because that group is optional anyway. Everything else is fixed in the plan:

- `collection`, join definitions and keys, `select`, `orderBy`, `group`, `groupBy`,
  metric definitions, `computed`, `derived`, `distinct` and aliases hold no
  placeholders at all.
- A join's `where`, a source's `filter`, `pick.where`, `rollup`, a join metric's
  `where` and a computed `if` decide eligibility. They can read identity and access
  values, but no client values and no client `when`. Use `access_when` when one
  applies only to some viewers.
- A relation's `limit` is a literal from 1 to 1000. Database fills it source by
  source, and a row past it can never match.

Conditions have their own rules on joined reads:

- A root or branch `where` holding an `access_when` needs a term that always applies.
  With only conditional terms, a viewer none of them applies to would read every record.
- An `access_when` condition and a result's `$if` can only decide on
  `exact_string_list` values. A normal list drops blanks, so a blank grant would read
  as no grant.
- A computed `if` can't hold an `access_when`.

**What decides a winner.** A `pick`, a `rollup` and a `distinct` by field choose rows,
so a client value before them does more than narrow. It can change which row wins a
pick, or make a mixed record look single to a rollup. Client params, including a value
a client can narrow, are refused in the `where` and `filter` that feed them. A plan
that means it, such as a report's date window, lists those params on the step:

```json
{"id": "facts", "op": "joined_aggregate", "stage_params": ["from", "to"], "args": {...}}
```

A `distinct` by alias only collapses the joined rows of one record, so it chooses nothing
and needs no list.

**Publish.** Publishing sends each joined step to Database once with a root that
matches nothing, every conditional branch present and `limit` 1. Database then
checks the operation, collections, fields, join keys, expressions, and indexes, and
reads no rows. Values the plan fixes stay in the request, so Database also checks them
against their fields' types. A refusal leaves the previous release active. Publish also
checks that each `$args` param reads a declared field argument of a fitting type.

Two things only fail at run time, with no partial rows: a read that exceeds its work
budget on real data, and a param whose type doesn't suit its field. A sample value could
refuse a good plan, so publish doesn't send one.

**Paging.** `page.size` works as on `find`; a size of 0 returns no rows and keeps the
total. `page.offset` passes up to 100000, past which the request fails. A negative
offset starts at the first record, like SQL. A request takes a cursor or an offset,
not both. Results can select `$steps.<id>.total` (a `joined_page`, or a
`related_read` with `count: true`), `.hasMore`, `.nextCursor` and `.first`.

**Cursors.** A joined page's `nextCursor` wraps Database's and is encrypted with a key
kept per project, so a client can't read order values or record keys from it. It's
bound to the project, the resolver's definition, the step, the viewer, the effective
scope, and the request's filters and order, so it fails with `invalid_cursor` under
any other. Changed grants end a held cursor. A refreshed token with the same permissions
keeps it. Cursors on `find` steps are unchanged.

**Shape.** `shape` turns Database's rows into a field's existing contract:

- `rename` maps output keys, such as `root.id` to `id`. Two values can't end up with
  one name, and `total_as` can't reuse one.
- `total_as` adds the page's total to every row.
- `json_text` turns a value into JSON text, for fields typed `String`.
- `numbers` turns decimal text into numbers where that's exact.
- `relation_source` keeps the reserved `source` field on related rows, which is
  otherwise removed.

A grouped `joined_page` flattens its groups into rows, so a page of 1 interaction
can return two legs while `total` keeps counting interactions.

`result` can also build a payload: `{"$json_text": <template>}` encodes a part as
JSON text, `{"$const": <value>}` holds a literal, and `{"$if": <condition>, "then":
..., "else": ...}` chooses on identity and access values only:

```json
"result": {"$if": {"op": "in", "args": [{"const": "admin"}, "$param.perms"]},
           "then": [{"payload": {"$json_text": {"total": "$steps.calls.total", "items": "$steps.calls"}}}],
           "else": []}
```

**Lists in a String argument.** A `string_list` param with `"encoding": "json"` reads
a JSON list from a `String` argument, as in `centreIds: String = "[]"`. Numbers in it
keep their decimal text, and anything else fails the request.

**One view of grants and rows.** A plan with a joined read re-reads its access values
in the batch that reads the rows. If they differ from what its scope was resolved
from, it resolves the scope again and retries, up to 3 times, then fails with
`access_changing` rather than return rows it can't confirm. Other plans keep their
two batches.

#### Measuring a pipeline

Each pipeline call leaves a trace on its request log, in `runtime.pipelines`
(`graphql_logs`, `/admin/logs`, or `runtime_metrics_json` by `request_id`). The
trace is for operators: it never appears in a response or in `Server-Timing`.
It only observes, so filters, totals, cursors and subscriptions behave the same
with or without it.

```json
{"field": "Query.calls", "outcome": "ok", "total_ms": 7.4, "filter_hash": "7fc948d66fa5cf1b",
 "access": {"ms": 3.2, "batches": 3, "reads": 6, "wire": {"server_wait_ms": 2.6}},
 "resolution_ms": 0.4,
 "database_batch": {
   "wall_ms": 3.7,
   "wire": {"prepare_ms": 0.07, "connection_wait_ms": 0, "request_write_ms": 0.02,
            "server_wait_ms": 3.55, "response_read_ms": 0.07, "decode_ms": 0.02, "response_bytes": 1055},
   "database_reported_ms": 1.46, "unattributed_server_wait_ms": 2.09, "statistics": "reported",
   "operations": [
     {"step": "rows", "op": "find", "filter_hash": "7fc948d66fa5cf1b", "rows": 2, "has_more": true},
     {"step": "total", "op": "count", "count_of": "rows", "filter_hash": "7fc948d66fa5cf1b", "count": "6",
      "execution_ms": 1.46, "statistics": {"strategy": "documents", "documentFetches": 12, "exists": {"probes": 52}}}]},
 "assemble_ms": 0.04}
```

| Field | Covers |
| --- | --- |
| `access` | Every access read batch: wall time, batches, reads |
| `resolution_ms` | Validation, params, permission checks, derived values, conditions and filter binding |
| `database_batch.wall_ms` | The batch holding the page and its count, including any resend |
| `wire.server_wait_ms` | Request written to first response byte: the gateway and Database together |
| other `wire` fields | This process: the SDK encoding the request, getting a connection, reading and decoding the response |
| `execution_ms`, `statistics` | What Database reported for a count, kept beside that count's `filter_hash` |
| `assemble_ms` | Decoding the steps and building the resolver's result |
| log `timings.serialise`, `timings.write` | Encoding the GraphQL response and writing it to the client |

- **`filter_hash`** is a truncated SHA-256 of the resolved filter as sent, scope
  values included. A page and its `count_of` must carry the same hash. Requests
  with the same hash asked Database the same question, and a permission change
  shows up as a new hash.
- **`count_of` stays one operation.** The trace adds `diagnostics: true` to count
  steps only, so the count is still one logical request in the page's batch and
  snapshot. Index ranges Database expands it into stay inside that request.
- **`outcome`** is `ok`, `error` or `in_flight`, and `stage` says where a call
  stopped (`access`, `resolution`, `database_batch`, `assemble`). `in_flight`
  means the execution deadline passed while the call was still running: the
  stage it was stuck in keeps counting until the log entry is taken.
- **`statistics`** is `reported`, `unavailable` (this Database refuses the option),
  `missing` (asked, but nothing came back, as on a failed batch), `requested`
  (still running) or `not_requested` (nothing was counted in Database). A
  Database that refuses the option rejects the batch while decoding it, before
  any read runs, so the same batch is resent once without it
  (`retried_without_statistics`). That project isn't asked again for 5 minutes.

What the trace can't separate yet: Database reports execution time for counts
only. It reports nothing for a `find`, and nothing for a batch's queue or
snapshot. So `unattributed_server_wait_ms` holds the gateway, those Database
stages and the page's execution together, and it isn't a measure of the gateway
alone. `database_reported_ms` is the longest reported read rather than a sum,
because reads in a batch can overlap. Each request keeps up to 32 traces, each
up to 16 operations (`pipelines_omitted`, `operations_omitted` count the rest),
and statistics keep only numbers, booleans and short names.

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
Default batching uses `best_effort`. Version 0.8.0 adds opt-in native batch snapshots,
request snapshots for capable upstream adapters, admission limits and in-flight
read sharing; see [RUNTIME.md](RUNTIME.md) for the contract and consistency limits.

Version 0.3.0 pins SDK v0.82.0, including cancellable trusted Function calls and
negotiated inner JSON results. Platform-only read optimizations are preserved.

## Realtime transport

`/realtime` (or `/realtime/<api_slug>`) retains its existing platform-only
event transport with `graphql-transport-ws`-style frames, not full
protocol/spec compliance. Protected subscriptions remain disabled. A
subscription resolver can set `config.topic`; Tables row events are bridged
automatically and trusted source adapters or the `graphql_event_publish` tool
can publish matching events. Every event re-runs the subscription's query.

### Database subscriptions

A `Subscription` field bound to a Database read (`find`, `list`, `search`,
`get`, `count` or `aggregate`) follows Database's native change feed. GraphQL
long-polls `db_changes` with `scope: "project"` and re-runs the field whenever
a commit touches the source collection; `watch_collections` in the resolver
config watches other or additional collections instead. A `database_pipeline`
watches every collection it reads, including joins, relation sources, branches,
`exists` targets and its access reads, and `watch_collections` can only add to those.
Watches follow the active release's plan, and the draft only where nothing is published.
Publishing refuses a subscription that depends on more than 16 collections, Database's
limit for one watch:

```json
{
  "parent_type": "Subscription",
  "field_name": "orderStats",
  "source": "orders",
  "operation": "aggregate",
  "config": {"groupBy": ["region"], "metrics": [{"name": "count", "op": "count"}], "watch_collections": ["orders", "refunds"]}
}
```

Requirements, all set by a project operator in Database (0.6.0 or later): the
database uses the `aptevadb` adapter, capture is enabled with
`db_realtime_configure`, and `graphql` has read access via `db_access_grant`.
Otherwise the subscribe message fails with Database's reason.

Change frames carry no row data, so updates are always a fresh read. GraphQL
starts listening before the initial result, so a commit landing during that
read still produces an update. Subscribers with the same document, operation,
variables, environment and caller identity form one group: each change runs
the query once, after the invalidation arrived, and every member receives that
result. Different callers or variables never share a result. Bursts collapse
into one re-run per group, and each member keeps only the newest pending
result, so a slow connection delays only itself. A re-run that fails is
retried with backoff (250 ms up to 5 s) instead of waiting for the next commit.

A result is only delivered while it's the newest run, so a revoked row can't come
back, however late its read returns. A commit replaces a running refresh, so a result
read before it is dropped. To keep constant writes from starving delivery, that stops
after 3 replacements in a row, or 16 for a collection the plan reads its access values
from, a dropped change event, or one that can't say what changed. The refresh then
finishes and is delivered, and the next one starts at once to cover what arrived
meanwhile. A refresh that finishes at the limit can predate the latest grant change, but
no newer result has been delivered. Each refresh may only share a read that started
after the last change it covers.

A subscriber's first read gives way the same way: if a refresh started since it joined,
that refresh's result is sent instead, and a member never receives a refresh older than
its own first read. Every refresh and reconnect re-reads saved grants. Token permissions
are read at connection and checked again when the token expires. A subscriber connected
across a publish keeps watching what the old release read until it reconnects, though
its refreshes run the new plan.

One native watcher per database and collection set is shared by every
subscriber, because Database admits at most 8 concurrent watches per calling
app. When retained history expires the watcher reopens and every subscriber
re-runs once.

## Request health and dashboard widget

The **Logs** tab and the **GraphQL request health** dashboard widget share the
same server-side filters. Choose **Errors** to include HTTP failures and GraphQL
errors returned with HTTP 200, **Slow** for requests over the threshold, or
**Large** for responses of at least 1 MB. Search matches operation names/hashes,
request IDs, and every retained error message. Advanced filters include error
code, exact operation, environment, operation type, HTTP status, duration,
response bytes, rows, and resolver counts; each numeric dimension has lower and
upper bounds. Time windows include custom local dates, converted to UTC.

Expand a request to read every error, its field path and source location, the
request ID, authorization scope, API release, response statistics, execution
phases, and source timings. Copy details exports the stored request metadata.
Query text, variables, request bodies, and authentication tokens are not stored.

Summary counts and averages cover **all matching stored requests**, independently
of the list's limit (at most 500). They describe the selected filters, rather than
an unfiltered global error rate. Logging is asynchronous and may drop entries
under queue saturation or a storage failure; it is operational telemetry rather
than a durable audit trail. Older entries retain their existing details, with an
unknown environment where none was recorded. Use **All environments** to include
them.

HTTP observations include decoding, public-route authentication, execution and
response writing. Malformed requests, authentication rejections, and method
rejections are logged too, when a project can be established. WebSocket
subscriptions are not included in this request view.

The app exports `graphql-telemetry` in `provides.ui_components` for
`dashboard.home`, with project visibility and half/full sizes, following the
Tasks widget convention. Add **GraphQL request health** from the dashboard widget
gallery. Per-widget settings select the API slug, window in minutes, slow
threshold in milliseconds, initial view (all/errors/slow), and list limit. The
widget supports changing APIs, filtering and expanding details directly; live
refresh runs every 30 seconds while the page is visible. The app pins SDK v0.95.0.

Build and validate this UI without rebuilding other apps:

```sh
bun build mcp/graphql/ui/GraphQLTelemetryWidget.tsx --target browser --format esm \
  --external react --external react/jsx-runtime --external react/jsx-dev-runtime \
  --define 'process.env.NODE_ENV="production"' \
  --outfile mcp/graphql/ui/GraphQLTelemetryWidget.mjs
bun test mcp/graphql/ui/telemetry.test.ts
```

## Reusable runtime capabilities

Version 0.8.0 adds opt-in authorized in-flight sharing, real Tables point-read
batching, generic upstream adapters with bounded read snapshots, and configurable
admission queues. Deploy controls configure the immutable release. Logs and the
existing dashboard widget show queue/shared execution views, source metadata,
loader/backend counts and per-resolver timing details. See [RUNTIME.md](RUNTIME.md)
for configuration, capability contracts and backend consistency boundaries.

See [STAGED_PIPELINES.md](STAGED_PIPELINES.md) for version 3 separate-statement snapshot stages and legacy version 2 stages,
executed through one Tables snapshot batch with intermediate data kept inside SQLite.
