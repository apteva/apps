# Media session resilience (local changes)

The adviser softphone, headless controller, Calls panel, listening and coaching
use the same bounded media lease renewal helper. No installation is activated.

## Renewal and authorization

- Retry failed network requests, server failures and rate limits with backoff
  from 250 ms to 4 seconds while the current lease remains valid.
- Bound each renewal request to five seconds and maintain an independent expiry
  timer. Timer delays cannot extend a grant; late results after expiry or stop
  cannot restart audio. Successful renewal uses request-start time rather than
  response-arrival time, retaining the server's second-resolution safety margin.
- Stop immediately for confirmed denial (401/403/404/410) and expose expiry
  separately. Renew returns the server's current lease duration.
- Explicit and automatic adviser reconnection call authenticated
  `/softphone/attach/<call>` to obtain fresh credentials for the existing call.
  Concurrent recovery is coalesced; stale credential replies are ignored. Mute,
  current ownership, call completion and cancellation remain authoritative.
- New HTTP/media authorization failures from session or policy lookups return
  503, rather than impersonating revocation. An established socket may retain
  only its last verified lease deadline during temporary lookup errors. A
  known revocation, ownership change or expiry still closes it immediately.
- Listening and coaching retain their sessions through temporary renewal
  failures. Coaching does not automatically reconnect or change its pinned
  adviser. During a server permission lookup outage, stop private talk while
  retaining the listening session within its verified lease. Talking requires
  a new explicit command after permission checks recover.

## Audio clocks and bounded buffering

The worker invalidates its AudioContext mapping on suspension/resumption and
requires a fresh, low-uncertainty clock sample after it becomes stale. It flushes
old playback and gates unsynchronized capture, avoiding false multi-second age
calculations after a paused render clock. Fresh resumed speech is accepted once
calibrated; genuinely old frames still expire. The 250 ms capture and 320 ms
playback budgets and existing DSP/resampler remain unchanged.

Playback measures ingress, transit and source age before selecting a drop
reason. Accepted PCM retains the existing `playback_received_ms` meaning; the
new ingress counter includes rejected frames. Timestamped directional drop
samples and aggregate totals account for rejected frames without double counting.
Server capture rejection also retains timestamp, age, sequence and duration.

## Diagnostics and host APIs

`SoftphoneOptions.onSessionEvent` and `CallListenerOptions.onSessionEvent`
expose bounded session events: timestamp, operation, outcome, HTTP status/code,
remaining lease, and available audio/connection error details. Adviser diagnostics
persist up to 50 session events, including WebSocket close code/reason and clean
close state. Events contain no session tokens or media URLs. Drop samples remain
bounded at 100 while aggregate counters retain total loss.

The benchmark now reconnects through real local attach authorization and records
source/playback render-clock progress against wall time. A stalled synthetic
source is distinguishable from nominal end-to-end marker delay. Its audio quality
gates are unchanged.

## Scope

Only Telephony source, tests, generated assets and its dependency pin are edited.
The app SDK pin is v0.95.0, confirmed as a descendant of the prior v0.91.0 pin;
standalone tests use `GOWORK=off`. Production, staging, carrier settings, IVR
assignments and live calls remain untouched. These changes are not a release.
