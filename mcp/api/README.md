# API Gateway

API Gateway exposes project APIs backed by Functions, installed apps, HTTP
origins, or safe AppBus invalidation streams.

## AppBus invalidation routes

Use `target_kind: app_events` to turn events from any installed app into an
authenticated public SSE endpoint. The source app and allowed topics are fixed
in the route. The public client cannot select another AppBus lane.

```json
{
  "api_slug": "sales",
  "method": "GET",
  "path_pattern": "/events",
  "target_kind": "app_events",
  "target_ref": "tables",
  "auth": { "kind": "api_key" },
  "events": {
    "topics": ["row.inserted", "row.updated", "row.deleted"],
    "match": { "data.table": "ventes" },
    "output": { "type": "invalidate", "resource": "ventes" },
    "coalesce_ms": 200
  }
}
```

The endpoint emits an initial `ready` event that tells the client to reload its
atomic endpoint. Matching AppBus events are coalesced and projected to the
static `output` object:

```text
event: ready
data: {"type":"ready","revalidate":true}

id: 42
event: invalidate
data: {"type":"invalidate","resource":"ventes"}
```

Raw AppBus payloads, project IDs, install IDs, row IDs, and other internal data
are never forwarded. `match` accepts at most 16 `data.*` comparisons. A
comparison can be an exact JSON scalar or a bounded `in` allowlist:

```json
{
  "topics": ["row.*"],
  "match": {
    "data.table": {
      "in": ["appels", "ventes", "prospects", "rapports"]
    }
  },
  "output": {
    "type": "invalidate",
    "table": "$data.table"
  }
}
```

An output may project a complete `$data.*` value only when the same path is
constrained by an exact or `in` matcher. This preserves the allowlist boundary:
unmatched fields and arbitrary event payload data cannot be exposed. Projection
tokens embedded inside larger strings are not expanded, and `output.type` must
remain static.

Coalescing is keyed by the rendered output. If several allowed resources change
inside one window, each resource receives one invalidation in AppBus sequence
order; repeated changes to the same resource collapse into one frame. Topic
patterns can be exact, `prefix.*`, or `*`. Streams require `api_key` or
`auth_jwt`; public event routes are rejected. Clients can reconnect with
`Last-Event-ID` or `since`, and the endpoint sends 15-second heartbeat comments.

API Gateway shares one internal AppBus connection per project and source app,
fans it out to public clients, bounds per-client buffers, and treats
invalidations as level-triggered. Slow or reconnecting clients use the initial
`ready` event to recover by reloading current state.

## 0.6.0 upgrade behavior

This release is based on `api/v0.5.0` and pins `app-sdk` to `v0.74.1`.
Its manifest targets Apteva 0.42.3 or newer. The embedded manifest is the same
file used for installation. See [VALIDATION.md](VALIDATION.md) for tested scope
and [PERFORMANCE.md](PERFORMANCE.md) for reproducible measurements.

- Management operations use the authenticated project context. Conflicting
  `project_id` / `_project_id` arguments are rejected. Global installations
  need the selected project in the platform request; a direct management call
  still requires the SDK's sidecar authentication.
- Auth policies must be JSON objects with `kind: public`, `api_key`, or
  `auth_jwt`; `{}` inherits the API policy. Existing malformed policies fail
  closed. JSON strings containing valid policy objects remain supported.
- Credentials in `Authorization`, `Cookie`, `X-API-Key`, internal Apteva and
  forwarding headers, and gateway routing/key query parameters are removed
  before forwarding. Function events receive a verified identity object.
  Upstreams that previously depended on implicit credential forwarding need
  their authentication design updated.
- App targets require an installed, bound app, declared dependency, or the
  optional `upstream` integration binding. Creation checks its `/health`
  callback. Dispatch uses the outbound token and resolved project through the
  platform's bound-app callback. Function routes use the same callback contract.
- Lower route priority wins; at equal priority, literal segments beat named
  parameters, which beat terminal `*`; exact methods beat `ANY` for equal
  path specificity. Exact paths beat a catch-all with an empty suffix. Zero
  priority is valid. Wildcards must be terminal. Root patterns stay `/`.
  Encoded path segments and configured upstream query parameters are preserved.
- HTTP/app redirects are returned to the caller. SSE headers and chunks flush
  immediately. Upgrades (including WebSockets) are explicitly rejected.
  Interrupted responses terminate the client connection and record an error.
- Structured function responses require `statusCode` (200–599), optional
  `headers`, and `body`; objects with a normal `body` field alone remain JSON.
  Header values may be strings or arrays of strings. Invalid envelopes produce
  a gateway error. HTTP errors and empty upstream responses retain their status.

## Resource bounds and lifecycle

HTTP/app request bodies stream with an 8 MiB limit. Buffered function input is
limited to 1 MiB and nonstreaming function output to 2 MiB; overflow fails
explicitly. Management JSON is limited to 1 MiB. Body reads have a 30-second
limit, auth calls five seconds, and upstream response headers ten seconds.
Route timeouts accept integer milliseconds from 1 to 300,000 (default 30,000).
Streaming writes have a 15-second deadline. These are fixed implementation
bounds in this candidate, rather than per-route configuration options.

A sidecar admits at most 256 concurrent gateway requests, including at most
128 public AppBus streams. Each shared source hub retains 256 events, with a
64-event queue per subscriber and 512 KiB maximum aggregate SSE frame. Overflow,
upstream loss, or sequence reset closes affected clients; reconnecting clients
must honor `ready` by reloading current state. `coalesce_ms: 0` means immediate
invalidation. Key revocation, API/route mutation and deletion cancel affected
streams; JWT streams expire with their verified token.

Route snapshots are immutable, invalidated on mutations, and refreshed after
one minute. At most 1,024 API snapshots are retained per database. Response
buffers and identical event projections are reused. Key last-use timestamps
are sampled at most once per minute; key validity itself is checked every time.

Diagnostic request logs use a bounded 512-entry queue and transactions of up
to 64 rows. Reading logs drains queued entries first. Retention removes entries
older than seven days and converges on a 100,000-entry cap through bounded
batches. Saturation drops diagnostic entries and emits a warning; logs are not
a durable billing/audit ledger. Shutdown drains the queue. Logs have request
IDs, credential redaction, and an ID cursor (`before_id`) for stable pagination.
API deletion atomically removes its routes, keys and logs, while preventing a
finishing request from recreating deleted logs.

Browser-origin synchronization and hostname cleanup are persisted and retried
every 30 seconds, including after restart or deletion. The panel preserves
advanced CORS fields and explicit disablement, shows pending/error state,
cancels stale selection loads, and only enables actions on current rows.

DNS automation selects the longest managed zone, uses A/AAAA/CNAME appropriately,
and refuses unrelated conflicting records. Existing compatible records remain
externally owned. Cleanup checks the recorded ID, name, type and value before
removing a gateway-created record; records predating this migration are not
claimed or deleted. External DNS edits can race provider calls: the Domains API
has no compare-and-swap/create-only operation, so use manual mode when other
systems simultaneously manage the same record. Failed cleanup remains visible
and retryable. A hostname cannot be claimed by a second API while its prior
owner or cleanup record exists.
