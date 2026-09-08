# Gateway upstream budget validation — 2026-09-08

Issue: API 0.6.1's transport cuts off response headers after 10 seconds even
when route `timeout_ms` allows 30 seconds. This patch removes that independent
cutoff and uses the existing route deadline, including queue and execution time.

## Real elapsed-time comparison

Tests use the real Go HTTP transport and an upstream that delays response
headers. Each route allows 30,000 ms. The baseline is the exact published
`api/v0.6.1` source, with only the portable regression test added.

| Upstream delay | Released API 0.6.1 | Patched Gateway |
|---|---|---|
| 5 seconds | HTTP 200 at 5.002 s | HTTP 200 at 5.003 s |
| 12 seconds | HTTP 504 at 10.002 s | HTTP 200 at 12.002 s |
| 25 seconds | HTTP 504 at 10.003 s | HTTP 200 at 25.002 s |

This is a correctness improvement: permitted requests now finish. It is not an
underlying calculation speedup or evidence for a server/SQL-driver upgrade.

## Other checks

- Cancellation reaches upstream for Function, HTTP, and app routes; disconnected
  requests are classified `client_cancelled` without attempting an error body.
- Exhausting a route budget returns `gateway_timeout`; queue plus execution
  consumes one budget, with no reset between stages.
- Functions queue expiration is distinguishable from immediate resource
  rejection; execution deadlines return HTTP 504 with their existing error code.
- Gateway maps Functions deadline codes into `queue_timeout` or `upstream_timeout`.
- Real Functions and Tables SDK sidecars confirm the Gateway-generated ID reaches
  the actual Tables MCP call. Client-supplied IDs do not override it.
- Functions persists the same ID in invocation resources, and Tables removes
  diagnostic metadata before operation validation.

Release versions: API 0.6.2, Functions 1.11.4, Tables 0.1.19.
No production configuration was changed. This patch introduces no database
migrations or SQL-driver changes.

All three complete package suites passed with the race detector: API (including
real-duration cases) 49.434 s; Functions 125.352 s; Tables 11.221 s. The final API
suite including the real sidecar correlation chain passed in 13.972 s (the long
5/12/25-second cases were separately confirmed with the same transport change).
`go vet ./...` passed in all three modules. Functions' original resource-limit
checks still pass: queue timeout classification preserves the original capacity
constraint in resource details.
