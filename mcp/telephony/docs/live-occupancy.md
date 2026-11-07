# Live human call occupancy

`telephony_live_occupancy` returns current human call ownership for the
authenticated project in one read. It is an operator/app MCP tool with the same
permission wrapper as `telephony_calls_list`; application-user credentials must
continue to use the authorized softphone API. Project arguments cannot override
the authenticated project context.

Optional arguments:

```json
{"direction":"outbound"}
```

Omit `direction` to include both directions. `inbound` and `outbound` are the
only accepted values. There are no pagination parameters.

Response:

```json
{
  "snapshot_at": "2026-10-22T08:13:36.834Z",
  "complete": true,
  "calls": [{
    "call_id": "example-call-id",
    "direction": "outbound",
    "status": "answered",
    "owner_identity": {
      "issuer_app": "auth",
      "issuer_install_id": "18",
      "subject_type": "user",
      "subject_id": "145",
      "organization_id": "1"
    }
  }]
}
```

## Meaning and consistency

- A single SQL statement joins live human calls to their current owner, with
  both rows scoped to the same project. Concurrent commits cannot mix a call
  from one snapshot with an owner from another.
- Owned connecting calls count, including `initiated`, `ringing`, `pending`
  and `answering`. No browser attachment or media lease is required: temporary
  media disconnection does not free the adviser.
- As in the existing capacity rules, `completed`, `failed`, `busy`, `no-answer`
  and `canceled` are terminal. Released ownership, unowned calls and AI-only
  calls are excluded. Offers and capacity reservations are not ownership.
- Every matching call is returned, even when one adviser owns several calls.
  Compare the complete identity tuple, never only `subject_id`.
- A successful empty snapshot is `complete: true, calls: []`. Database errors,
  cancellation and invalid owner identities produce a handler/MCP error without
  a partial snapshot, including with the SDK's direct JSON result format. Treat
  these as unavailable information, not zero occupancy.
- `snapshot_at` is a UTC observation timestamp. The snapshot is not a lock or a
  reservation, and ownership may change as soon as the read finishes.

The query uses a covering partial index of live human calls followed by owner
primary-key lookups. It selects only call ID, direction, status and the owner
identity. There is no pagination, full call loader, carrier lookup, diagnostic
decode, recording/history lookup, cache or background polling. Request
cancellation is passed to SQLite. Structured request logs contain project,
direction, query duration in milliseconds and returned call count; identities
and credentials are not logged.

## Routing adoption

A routing function can replace status-list and per-call owner lookups with one
`telephony_live_occupancy` call. Check the MCP error status and `complete` before
using the result. Retain the router's reservation ledger and Telephony's atomic
capacity checks: two routing decisions can observe the same free adviser.
Installing the tool does not modify existing routing functions automatically.

## Repeatable verification

Run from `mcp/telephony`, without Docker:

```sh
GOWORK=off go test -run 'TestLiveOccupancy|TestLifecycleManifestDeclarationsMatchDisk|TestApplicationUserMCPAndIncompleteScopesDenied' -count=1 -v .
GOWORK=off go test -race -run 'TestLiveOccupancy|TestDecisionCapacityConcurrentAliasesOutboundAndRelease' -count=1 .
GOWORK=off go test -run '^$' -bench '^BenchmarkLiveOccupancy$' -benchmem -count=3 .
```

Tests cover lifecycle and released ownership, AI exclusion, full identity,
project isolation, direction filters, more than 200 active calls, concurrent
transactional answer/takeover/hangup/release changes, cancellation, read errors,
malformed identities and empty results. Query-plan tests seed 50,000 historical
calls with retained owners and verify indexed live-call reads before and after
`ANALYZE`; large invalid diagnostic payloads are never decoded. Benchmarks compare
12 live calls with zero and 50,000 historical calls. These measure local database
and owner-identity decoding cost, not cross-host RPC latency.

## Local results — 22 October 2026

On Darwin/arm64 (Apple M1 Pro), with 12 live calls:

| Retained historical calls/owners | Snapshot duration, three benchmark runs | Allocations |
| --- | --- | --- |
| 0 | 72–74 µs | 336/op, about 16.8 KB/op |
| 50,000 | 78–107 µs | 336/op, about 16.8 KB/op |

Other local tests were running during measurement. These are local snapshot
costs, not a latency promise for the deployed RPC. Both query variants use the
covering live-call index and owner primary-key probes, without a temporary sort,
before and after `ANALYZE`.

The full Go suite passed: 1,796 test cases/subtests passed, 11 opt-in tests
skipped, no failures. Targeted occupancy and capacity race tests passed. After
tightening the handler error contract, the occupancy tests and focused error-path
race tests passed again. `go vet ./...` and the application build passed. No
production configuration or routing function was changed; this implementation is
local and has not been released.
