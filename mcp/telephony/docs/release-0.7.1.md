# Telephony 0.7.1

This release strengthens default inbound termination and burst protection while
preserving all changes in 0.7.0. No special burst announcement mode is included.

## Changes

- A shared termination helper distinguishes unanswered inbound calls from
  established calls. Unanswered routing completion and lifecycle expiry use the
  carrier rejection adapter; answered calls use hangup. Configured routing
  announcements still execute before termination.
- Telnyx rejection supplies `CALL_REJECTED`. Direct SIP rejection sends 603
  Decline, including burst suppression. Initial Twilio suppression returns
  `<Reject reason="rejected"/>`. Other adapters retain their supported carrier
  termination operations; identical upstream SIP behavior is not assumed.
- Duplicate initiated webhooks wait for confirmation of an accepted answer
  rather than prematurely exhausting its command retry budget.
- Expiry handling locks and reloads calls, rechecking deadlines so stale worker
  snapshots cannot interrupt a renewed adviser claim or routing transition.
- Burst checks stop after the threshold plus one indexed rows, keeping counting
  work bounded as the number of attempts in the window grows.

The existing default guard suppresses the 13th distinct call ID from one
caller to one destination within 60 seconds. Suppressed calls receive no adviser
offer or special announcement and do not enter the missed-call pool. Explicit
caller blocks apply immediately. Destination-wide rotating-caller bursts remain
alert-only, and the IVR number stays enabled.

## Verification and compatibility

Local unit/integration suites and focused race tests cover generic provider
suppression, the threshold boundary, unrelated callers, callback classification,
answer confirmation deduplication, stale expiry protection and direct SIP 603
signaling. Release metadata and generated frontend assets identify 0.7.1.

There are no additional database migrations or new configuration requirements
compared with 0.7.0. The experimental local announcement mode and its migration
were never released and are excluded from this release.

This publishes source only. No production instance is updated and no live call
is placed. Actual forwarding-provider retry behavior still requires a controlled
call and carrier traces; application tests cannot guarantee external retries stop.
