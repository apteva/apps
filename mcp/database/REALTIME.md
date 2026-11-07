# Realtime collection watches

The first implementation uses AptevaDB v0.2.0-alpha.5 native capture. SQLite and Pebble continue to support the portable CRUD/query contract; their persistent change journals are a later phase. `db_realtime_describe` reports `supported:false` for those adapters.

This build writes native format 10. **Before installing it over existing native data, stop the old app and back up its data with the original binary.** Opening older data permanently upgrades it. Release verification uses disposable fixtures and does not replace an installed app.

## Enable capture

A database owner with `database.manage` can call:

```json
{
  "database": "store",
  "enabled": true,
  "retentionBytes": 134217728,
  "retentionSeconds": 3600
}
```

Use `db_realtime_configure` or authenticated admin `POST /operations/realtime_configure`. The dashboard has an **Enable realtime** button for AptevaDB databases. Configuration persists across restarts. Configuring capture drains active watches and reopens the owned database under its write lock. Disabling ends the history; old cursors become invalid if capture is reenabled. It affects all subscribers to that database.

Defaults are 128 MiB or one hour of logical history. Allowed retention is 1–256 MiB and 1–86,400 seconds, with a 1 GiB combined configured budget across the app. Physical WAL storage can exceed these targets because reclamation respects segment boundaries and recovery requirements. Native capture-work limits can reject an oversized write before commit; this setting is left at the native default.

## Watch from MCP

`db_changes` requires both `database.watch` and `database.read` on `database/store`:

```json
{
  "database": "store",
  "collections": ["orders"],
  "waitMs": 20000,
  "initial": {
    "op": "find",
    "args": {"collection": "orders", "limit": 50}
  }
}
```

The first call can read a snapshot and begin watching at its exact boundary. Initial operations support `find`, `count` and `aggregate`; only selected collections may be read. Snapshots are released immediately after the initial query, before waiting or network delivery. Initial query execution is bounded to 30 seconds.

Responses contain `snapshot` when requested, `events` and a signed `cursor`. At most **one complete commit frame** is returned per poll, keeping response memory bounded. A frame lists changed logical collection names; it contains no keys or row values. Repeat with `after` and omit `initial`:

```json
{
  "database": "store",
  "collections": ["orders"],
  "after": "<returned cursor>",
  "waitMs": 20000
}
```

`waitMs` is 0–20,000; zero performs a short nonblocking-style check after opening the cursor. Empty responses retain a safe delivered boundary. Progress events advance through irrelevant commits without requiring a query refresh. Deliveries are at least once within retained history; replay can repeat a commit.

A `history_expired` or `resume_invalid` response requires a fresh initial snapshot. Do not silently continue with an expired cursor. Tokens are opaque, authenticated, and bound to the database UUID/private namespace and sorted collection set. Every MCP call rechecks grants; model-supplied project overrides are rejected.

## Watch from HTTP

Authenticated dashboard clients use **POST `/realtime/watch`** through the existing app proxy with a JSON body. Cursor contents are never put in the URL. Agents and private calling apps use MCP; the admin HTTP route rejects their caller headers.

```json
{
  "database": "store",
  "collections": ["orders"],
  "initial": {"op": "find", "args": {"collection": "orders", "limit": 50}}
}
```

The response is SSE (`text/event-stream`):

```text
event: snapshot
data: {"result":{"records":[],"nextCursor":""}}

id: <signed boundary>
event: ready
data: {"cursor":"<signed boundary>"}

id: <signed commit boundary>
event: invalidate
data: {"type":"invalidate","collections":["orders"],"cursor":"<signed commit boundary>"}
```

Other events are `progress`, `error` and `closed`. Only complete commit boundaries carry resumable cursors. Heartbeat comments every 15 seconds carry no cursor. Clients checkpoint after processing the entire frame; the transport never reads an advanced native cursor to checkpoint frames it has not sent. Resume by sending `after` in a new POST.

HTTP watches reconnect through authentication at least every five minutes. Write-idle deadlines are 30 seconds, with a bounded producer-to-network handoff. Disconnect, shutdown, database drop or capture reconfiguration closes the native cursor and releases quotas. The current platform transport is an observer of parallel work, not a request scheduler; watches are bounded by the app and never hold a database lock while waiting for an event or network write. These leases bound revocation delay; immediate in-flight cancellation on every platform grant change is not implemented.

## Dashboard behavior

The panel subscribes to its selected collection. Writes produce **collection invalidations**, including deletions and schema/index changes. The UI reruns the last submitted filter/aggregate without changing unsaved editor text. Record pagination resets to the first page when invalidated so stale ordering/page boundaries are discarded. Refresh uses 100 ms debounce with a 500 ms maximum delay, and reconnect uses jittered exponential backoff. Selection changes abort the old subscription and reject stale results. Expired history starts a fresh snapshot.

The feature does not patch individual rows or maintain filtered membership locally. It does not publish private namespaces to the project event bus. Internal physical names, `schema/*` keys and DDL intent payloads never leave the adapter. Native selected collection writes are folded to one logical invalidation per commit; selected schema catalog changes can also produce conservative refresh hints while a multistep DDL operation completes. A refresh waits for schema publication through the normal manager lock.

## Resource limits and checks

- Explicitly select 1–16 distinct logical collection names.
- 64 watches per process, 32 per database, 8 per authenticated agent/calling app; admin HTTP watches share the project namespace quota.
- Each native cursor has an 8 MiB work limit, with a 32 MiB shared native reader budget per database.
- Initial query results retain the existing 4 MiB result bound; serialized SSE frames are limited to 5 MiB.
- Poll and stream close paths release quotas even when callers time out. Reader exhaustion/expired retention requires recovery or resnapshot, not unbounded buffering.

Verified cases cover concurrent snapshot handoff, whole-commit replay, failed writes, cross-database/namespace/spec token rejection, catalog hiding, drop/recreation, retention expiry, persistence across restart, disable/reenable invalidation, cancellation, idle/slow readers and process/database/caller quotas. The client decoder tests fragmented frames and reconnect boundaries. See [performance evidence](benchmarks/REALTIME.md).
