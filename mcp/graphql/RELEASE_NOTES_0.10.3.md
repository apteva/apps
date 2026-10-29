# GraphQL v0.10.3

Makes Database sources usable on real installs, completes their read/write
path, and adds realtime subscriptions driven by Database's native change feed.
Requires Database >=0.5.3 when Database sources are used.

## Project databases

App-to-app calls always carry the caller's install, so Database served GraphQL
its own empty private namespace and a source such as `{"database":"prod"}`
returned "does not exist". Database sources now send `scope: "project"` by
default and reach a project database once an operator grants `graphql` access
with `db_access_grant` (read, or write for mutations). Source config
`"scope": "app"` opts back into the private namespace; clients cannot set it.

## Database adapter

- Writes work: `records`, `set`, `increment` and `ifVersion` are forwarded, so
  insert, update and upsert resolvers no longer fail with "records is required".
- Only record operations bind: find, list, search, get, count, aggregate,
  insert, update, delete and upsert. Schema, index, drop, batch and explain
  tools are rejected when the resolver is saved and when it runs.
- A resolver's `where` is combined (and) with the client's instead of being
  replaced; clients may lower `limit` and `maxAffected` but not raise them;
  `all`, `conflictIndex` and `requireIndex` come only from resolver config.
- New `search` operation returns `{rows, has_more, next_cursor}`, matching the
  Tables search page shape. `find`/`list` still return a plain list.

## Performance

Database reads now use the guarded fast projection path Tables reads already
used. At 100,000 records, GraphQL's overhead on a 1,000-group aggregate fell
from 5-8 ms to about 2 ms over a direct Database call; small results add
0.5-1 ms. Rows on this path no longer count toward `max_nested_resolvers`.

## Database subscriptions

A `Subscription` field bound to a Database read follows `db_changes` and
re-runs when a commit touches the source collection (`watch_collections`
overrides the set). Requires an `aptevadb` database with capture enabled
(`db_realtime_configure`) and a read grant; otherwise subscribing fails with
Database's reason.

- One shared watcher per database and collection set (Database allows 8
  watches per app); it stops with the last subscriber.
- Subscribers with the same document, variables, environment and caller
  identity form a group: each change runs the query once, after the
  invalidation, and every member receives it. Each member keeps only the newest
  pending result, so a slow connection delays only itself.
- Listening starts before the initial result, so commits during it are not
  missed. Failed re-runs retry with backoff (250 ms to 5 s). Expired history
  reopens the watch and re-runs everyone once.
- `/realtime/<api_slug>` now reaches named APIs; only `/realtime` was routed.

Load test (Database 0.5.3, AptevaDB, 5 s per scenario): 50 subscribers at 100
commits/s run 502 Database queries for 502 commits with 1.0 / 1.5 ms p50/p95
lag; ten subscribers on a 100,000-record aggregate use about 5x less Database
CPU than per-subscriber re-runs.

## Validation

Full Go suite, race suite and vet with `GOWORK=off`; unit tests for adapter
inputs, operation allowlist, project scope, watcher lifecycle, lost history,
coalescing, group fan-out, caller isolation and retry; opt-in real-sidecar tests
against Database 0.5.3 (aggregation parity and benchmark, subscriptions on
AptevaDB with grants and an unsupported-adapter refusal, and a load test).

SDK remains pinned to v0.99.0. The app-sdk gateway connection-pooling fix
(untagged at release time) removes per-call TCP connections under concurrent
app-to-app calls and will be picked up by a later release. Tables >=0.2.16 is
unchanged. No running installation or production deployment is updated by this
release.
