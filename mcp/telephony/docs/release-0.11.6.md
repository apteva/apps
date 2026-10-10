# Telephony 0.11.6

Cumulative release retaining all Telephony 0.11.5 source and earlier work.
Only Telephony source and its public registry entry are release targets.

## Critical activation correction

The 0.11.5 media-generation claim introduced a shared call lock into carrier
socket admission. AI activation and human inbound answering held that same lock
while waiting for the carrier API. A carrier that opened its media socket before
returning its HTTP command response could therefore time out waiting for itself.

- Reserve the command under the short call lock, release it before carrier I/O,
  and recheck the reservation token, thread/peer ownership and terminal state
  before applying the response. The new command reservation is durable and
  expires after ten seconds; programmable activation commands use an eight-second
  request deadline, capped by the existing AI total deadline.
- Carrier media admission and callbacks remain free to progress during API
  dispatch. Concurrent worker ticks/browser reattachments cannot dispatch a
  second answer command while its reservation is active. Human claim release
  waits for the dispatch outcome without holding the socket admission lock.
- A bridge error no longer terminates AI activation after one attempt. Failed
  commands and absent media confirmation use the existing three-attempt,
  thirty-second activation budget. Stream recovery defers to initial activation
  instead of issuing competing commands with a separate retry budget.
- Actual decoded media can prove connection before an HTTP error arrives.
  Provider command acceptance alone does not prove media connection. Late
  responses cannot revive caller termination or overwrite a changed peer/thread.
  Failed media state remains available to the bridge URL resolver for Core lease
  renewal when a replacement connects.

The generic human dispatch change applies to programmable carriers and direct
SIP; actual socket-before-response regressions exercise Telnyx and Twilio.
No carrier integration JSON or Core/Server/SDK change is required.

## Inventory performance

- Deduplicate Telnyx application details between webhook verification and
  outbound readiness; reuse credentials/signing-key validation within a request.
- Four application workers per account refresh and an installation-wide ceiling
  of thirty-two carrier reads; every nested read uses the inventory deadline.
- Cache raw successful carrier reads for twenty-five seconds, share simultaneous
  reads, retain provider/application failures and recompute permissions, local
  routes, admission and binding scope on every request.
- Invalidate at configuration mutation boundaries and when scoped configuration
  changes. Bound cache storage; canceled/invalidated late completions cannot
  repopulate it. Fresh checks and `verified_at` are available in admin, MCP and
  shared headless APIs, and the Numbers panel.

Local 200 ms simulated carrier responses, eleven numbers and ten enabled routes:
previous checks **4.633 s / 23 reads**, cold inventory **1.010 s / 13 reads**,
cached **4.071 ms / zero carrier reads**, twelve simultaneous cold requests
**1.041 s / thirteen total reads**. This is approximately 78% less cold elapsed
time and 43% fewer carrier reads. These are local latency simulations, not
production carrier measurements or evidence of CPU savings. See
`number-inventory-performance.md` for scope, allocation costs and commands.

## Verification

Local regression coverage includes actual authenticated carrier sockets opened
before activation returns, audio reaching Core while the command is in flight,
Telnyx/Twilio human answering, duplicate ticks, successful media with a late HTTP
error, caller hangup/ownership change during dispatch, three media setup failures,
recovery-worker exclusion, and existing answer-claim release serialization.

Inventory tests cover unchanged result fields, request counts, concurrency,
cancellation, partial failures, invalid signing keys, permission/project scope,
configuration invalidation, expiration and simultaneous fresh refreshes.

- Complete Go suite with the published SDK v0.99.0 and `GOWORK=off`: **940
  passing tests/subtests, zero failures**, three opt-in skips.
- Frontend/audio suites: **225 passed, zero failures** (102 client, 123 audio).
- TypeScript client/benchmark checks, Go vet/build and whitespace checks pass.
- Focused race validation is the final publication gate.

Raw evidence: `/private/tmp/telephony-0116-release-go.jsonl`,
`/private/tmp/telephony-0116-release-frontend.log`,
`/private/tmp/telephony-0116-release-race.log`.

## Limits and installation

No staging or production API, configuration, installation or real carrier call
was used. Local socket tests establish the command/callback ordering against
carrier substitutes; a controlled real-carrier check remains unperformed.
PCM/WebRTC media algorithms, codecs and latency caps are unchanged.

Publication makes the version available; it does not activate installations.
Installing an app update replaces its sidecar process. Drain active calls before
a separately authorized installation.
