# Realtime answer preparation race

The race reported against Telephony 0.3.9 is also present in the 0.3.10
preparation code. The fix is included in Telephony 0.3.11.

## Reproduction

A diagnostic runs two signed Twilio inbound webhooks for the same CallSid and
published immediate-answer routing flow. The first spawn is deliberately blocked
after the database claim. On `telephony/v0.3.9`, the second request returns
Say/Hangup and marks the call failed. The first request subsequently loses its
attach and also returns Say/Hangup. The identical diagnostic passes with this fix.

The portable diagnostic is `testdata/answer-claim-race/diagnostic_test.go`.
Copy it to a checkout's `mcp/telephony/answer_race_diagnostic_test.go`, then run:

```sh
GOWORK=off go test -short -run '^TestDiagnosticConcurrentAnswerWebhook$' -count=1 -timeout=1m .
```

The regular regressions are in `realtime_preparation_test.go`.

## Behavior

- Concurrent requests share preparation for one project/call. Each request waits
  at most five seconds; cancellation affects only that request's wait.
- Pending preparation has a distinct error identity. Twilio and Plivo return
  wait/redirect XML, without changing lifecycle state or speaking an error.
  Follow-up wait requests reuse the prepared thread and resume the media stream.
- A failed spawn remains distinguishable from work in progress. The winning
  attempt cleans up its own thread and conditionally releases its claim. The
  webhook keeps the caller waiting for a retry under the existing route deadline.
- A unique pending thread identity identifies each claim. Attach and release
  require that identity to still match. Unique spawned thread names keep late
  cleanup from killing a replacement attempt.
- An incomplete persisted claim without an in-memory owner is observed for a
  bounded interval, never stolen or released. Existing lifecycle expiry handles
  genuinely abandoned calls; no duplicate spawn is issued over an uncertain one.
- Caller hangup remains authoritative. A late successful spawn is cleaned up
  rather than attached to the ended call.
- Answer API calls also share carrier activation, which continues after a
  request wait expires. Concurrent callers cannot issue competing carrier
  answers and then clean up each other's thread.
- Preparation-started/ready logs contain call ID, thread ID and agent ID for
  correlation. They do not print media credentials.

The tests cover concurrent immediate and routed Twilio webhooks, slow spawning
and redirect resumption, failed spawning and retry, stale cleanup, interrupted
requests, incomplete persisted claims, caller status callbacks during spawning,
Plivo wait resumption, and concurrent/slow answer API activation.

## Separate reports

No event-payload or booking-tool changes are included. Routing/offered events
still use Telephony's internal call ID. Realtime requests still use
`RealtimeCapabilitiesInheritAgent`; the regression asserts that setting.

This reproduction confirms a failure path, not which production requests took
it. The reported WebSocket handshake failure and missing booking tools still
require correlated carrier/bridge/Core logs and the effective realtime tool list.
