# GraphQL v0.18.0

Each `database_pipeline` call now records how long each stage took, on the
request log. This only measures: permission filters, totals, cursors and
subscriptions behave as before. No plan changes are needed, and the Database
minimum stays `>=0.8.0`.

## The trace

Traces are stored in `runtime.pipelines` on the request log (`graphql_logs`,
`/admin/logs`). They never appear in a response or in `Server-Timing`.

| Stage | Recorded |
| --- | --- |
| Access reads | wall time, batches, reads |
| Permission and filter resolution | `resolution_ms` |
| Database batch | wall time, split into SDK client stages and `server_wait_ms` |
| Page find | filter hash, rows, `has_more` |
| Count | filter hash, count, Database's execution time and statistics |
| Response | `timings.serialise` and `timings.write` on the log entry |

- `filter_hash` is a truncated SHA-256 of the resolved filter as sent. A page and
  its `count_of` carry the same hash, and a permission change gives a new one.
- A request that hits the execution deadline keeps its trace, marked `in_flight`
  with the stage it was stuck in. Failed calls record their stage and error code.
- A request keeps up to 32 traces of up to 16 operations each. Statistics keep
  only numbers, booleans and short names, so lists of values never reach the log.

## Database statistics

Count steps now send `diagnostics: true`, and the result is removed before
anything can reach a client. The count stays one logical operation in the page's
batch and snapshot.

Released Database versions up to 0.8.0 refuse this option while decoding the
request, before any read runs. GraphQL then resends the same batch once without
it, shows `statistics: unavailable` in the trace, and doesn't ask that project
again for 5 minutes. Statistics appear once a Database with count diagnostics is
installed.

## What it can't separate yet

`server_wait_ms` is the gateway and Database together. Database reports execution
time for counts only, not for a `find` or a batch's queue and snapshot, so
`unattributed_server_wait_ms` holds the gateway and those Database stages
together. It isn't a measure of the gateway alone.

## Verification

- Full test suite, plus the race detector on the short suite and on the real
  trace test.
- Every real-Database test on Database 0.8.0 and on a Database build with count
  diagnostics, on AptevaDB. The relational and trace tests also ran on SQLite and
  Pebble with that build. These cover centre and offer-grant restrictions, mixed
  sale exclusions, optional filters, cursors, and subscription updates after
  permission changes.
- A test confirms requests and results are identical with and without statistics,
  apart from the option itself.
