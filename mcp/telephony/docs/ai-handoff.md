# AI startup recovery

Telephony 0.7.2 coordinates inbound realtime startup across all carriers, routing
workers, AI ring offers, immediate-answer webhooks and answer tools. No carrier
connector or production configuration change is required.

## Default policy

- One call-wide budget: three total startup attempts within 15 seconds, capped
  by the remaining state/call deadline. Retry delays are 2 then 4 seconds.
- `ai_startup_max_attempts` accepts 1–5; `ai_startup_timeout_seconds` accepts
  1–120. Values are snapshotted on the first attempt. Zero clamps to one and
  invalid/out-of-range settings use the defaults.
- Explicit temporary HTTP 429/503 responses may retry after cleanup succeeds.
- Configuration/access failures (400/401/403/404/422) stop immediately.
- All other failures, including unknown 500/502 errors and ambiguous network
  timeouts, stop conservatively. Telephony does not assume a failed HTTP response
  means Core never accepted the thread. It cleans up the unique thread ID and
  cannot start another AI thread if that cleanup failed.
- A request's five-second wait expiring is **not** a failed startup. It observes
  the existing attempt. Duplicate callbacks cannot increment its attempt count.

The currently pinned SDK serializes platform failures into a fixed string
wrapper. Telephony decodes that wrapper and the server's nested Core HTTP status;
it does not match provider-specific error messages. A typed `HTTPStatusCode()`
error is also supported. Unrecognized formats stop safely. Richer structured
platform error codes can later broaden transient recovery without changing this
budget or requiring a server deployment for the current fix.

## Failure routing

A destination node can specify an optional branch:

```json
{
  "id": "assistant",
  "type": "destination",
  "config": { "destination_id": "support-ai" },
  "branches": { "ai_startup_failed": "unavailable-notice" }
}
```

The branch is resolved from the call's saved published flow. It can lead to a
human/external destination, a routing decision, a menu, supported voicemail, or
an announcement followed by hangup. Normal carrier capability validation still
applies. A configured announcement uses the existing durable media executor.

Without a valid branch, Telephony commits a terminal hangup plan with
`routing_resolution=ai_startup_failed`. Established calls hang up; unanswered
calls use the carrier's rejection adapter. This does not use lifecycle expiry
as a substitute for routing completion.

The call-wide AI budget cannot be reset by another node. An immediate fallback
to AI uses the safe terminal plan; AI/agent members are removed from a selected
fallback ring group. Already eligible browser/external offers and newer routing
nodes are preserved. A human claim or caller hangup always wins over AI cleanup.

Startup failure is not AI handling. `media_connected_at` remains the evidence
of connection; an unhandled terminal call has one stable `callback:<call_id>`
opportunity. Existing closed-hours and suppression classifications are unchanged.

## Persistence and diagnostics

Migration 034 adds `ai_handoffs` and `ai_handoff_attempts`, with no changes to
existing call history. The journal records each attempt before the platform
request. The state and deadlines survive sidecar restarts. Attach and readiness
are atomic; caller termination fences in-flight work, and late success is cleaned
up instead of attached. A recovered uncertain attempt waits only to its saved
deadline and then uses the failure fallback, without spawning over it.

Administrative `GET /calls/{id}` includes `call.ai_startup`: status, attempt
count/limit, deadline, next retry, stable error code, fallback disposition and
per-attempt timestamps/outcomes. No directives, credentials, bridge URLs or raw
provider errors are returned there. Adviser visibility/answer permission and the
frequently polled call-list query are unchanged. Startup `ready` means the bridge
was prepared; it does not claim that media connected.

## Verification

`ai_handoff_test.go` covers permanent failure under 20 replayed deliveries,
backoff, budget persistence across coordinator restart, transient recovery,
concurrent callers, deadline fencing, caller cancellation, configured fallback
announcements, preserved adviser offers, recursive AI fallback, classification,
and bounded diagnostic output. Existing preparation, routing, media and browser
integration suites protect the surrounding paths. These tests use local doubles;
no real carrier or AI-provider availability is asserted by them.
