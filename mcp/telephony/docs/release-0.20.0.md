# Telephony 0.20.0

- Record who ended a call. On direct SIP, a carrier BYE now records
  `termination.initiator: "carrier"`, and a Q.850 reason becomes the cause
  (cause 16 is `normal_clearing`). Before, these calls had no initiator.
- Hang-ups and rejects requested through Telephony record who asked: `user`
  (an application user), `operator` (a trusted request with no user identity),
  `agent` or `telephony` (limits, failures, cleanup, dial timeouts). The cause
  says what they did: `hangup_requested`, `rejected`, `agent_completed` or
  `answered_leg_ended`.
- A recorded source is never replaced. A hang-up for a call that already ended
  returns its termination unchanged with `already_ended: true`. If the
  carrier's BYE is already ending the session, the hang-up waits for it and
  reports the carrier's end.
- The call API, the hang-up and reject responses (HTTP and tools) and the
  lifecycle events report the same `termination`.
- Every hang-up and reject request is recorded in `telephony_hangup_audit`:
  call, who asked, action, request ID, time, previous status, outcome
  (`ended`, `already_ended` or `failed`) and the final initiator.
- On direct SIP, an inbound call the caller hung up before it connected now
  counts as `caller_abandoned`, as it already did for Twilio and Telnyx.
