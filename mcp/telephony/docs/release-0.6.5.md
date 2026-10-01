# Telephony 0.6.5

This release includes all local Telephony changes that were pending after
0.6.4, plus the inbound incident fixes. It does not modify a customer's
published routing flows or install the app into a project.

## Inbound call handling

- A published flow ending in an announcement followed by `hangup` keeps the
  selected announcement text on the call. Telnyx answers, speaks it, waits for
  `call.speak.ended` (or the equivalent playback completion), and then hangs
  up. Twilio and Plivo render the announcement before `<Hangup/>` in their
  response XML. Duplicate completion callbacks do not hang up twice.
- Calls intentionally routed through a closed schedule record
  `handling_reason=closed_hours`. Calls suppressed by the burst guard record
  `handling_reason=burst_suppressed`, with the specific guard in
  `error_message`. Lifecycle events and call reads include the reason and a
  `missed_pool_eligible` flag so projections can exclude handled calls.
- A new inbound guard counts distinct carrier call IDs, not repeated webhooks.
  It limits calls per displayed caller and destination and also limits all
  callers to one destination, so rotating caller IDs do not bypass it. The
  policy has configurable windows, thresholds, cooldowns, and trusted caller
  numbers. Trusted callers remain subject to the destination limit. Suppressed
  calls are recorded, never offered to advisers, canceled at the carrier, and
  emit `telephony.burst.suppressed` for alerting. Guard state survives app
  restart and is pruned after its useful window.

## Included pending work

- Adds Bandwidth and Sinch call and webhook adapters, and DIDWW outbound SIP
  trunk support. The install picker includes all three providers. DIDWW needs
  separate SIP trunk credentials and the TLS/SRTP gateway configuration in
  [DIDWW-OUTBOUND.md](../DIDWW-OUTBOUND.md).
- Keeps the pending application-user policy and media-session improvements:
  current provider/action and caller grants are rechecked when authenticating
  or issuing media access, while unrelated policy edits do not disconnect an
  authorized active call.
- Includes the pending carrier number inventory, recording, SIP dialog, and
  media fixes with their tests.

## Verification and rollout

Run `GOWORK=off go test ./... -short`, then `apteva test --tier 1,2 .` from
`mcp/telephony`. The deterministic suite covers the carrier command order,
duplicate carrier IDs, one-caller and rotating-caller bursts, adviser offer
suppression, and Saturday open/closed route selection. Real Telnyx audio and
signaling still require a controlled live call and carrier call-detail access.
The new carrier adapters have mocked protocol tests; make a staging call on
each provider before enabling it for customer traffic.

The burst guard is enabled by default at 12 new IDs per displayed caller and
number, or 60 new IDs per number, in 60 seconds. Suppression lasts 300 seconds.
Tune these values for an installation's legitimate traffic and add trusted
caller exceptions where justified. Displayed caller ID is not authenticated;
the destination-wide limit remains in force.
