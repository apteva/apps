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
are never forwarded. `match` accepts at most 16 scalar `data.*` comparisons.
Topic patterns can be exact, `prefix.*`, or `*`. Streams require `api_key` or
`auth_jwt`; public event routes are rejected. Clients can reconnect with
`Last-Event-ID` or `since`, and the endpoint sends 15-second heartbeat comments.

API Gateway shares one internal AppBus connection per project and source app,
fans it out to public clients, bounds per-client buffers, and treats
invalidations as level-triggered. Slow or reconnecting clients use the initial
`ready` event to recover by reloading current state.
