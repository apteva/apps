# Telephony 0.16.2

Fixes a crash that stopped Telephony when ending an inbound direct SIP call.

A Telnyx call on direct SIP that Telephony had answered itself (for example to
play an announcement) but no adviser had answered could not be ended:
`terminateCarrierCall` treated it as unanswered and rejected it, while
`rejectInboundCarrierCall` treated it as carrier-answered and terminated it.
The two called each other until Go reported `fatal error: stack overflow`,
killing the whole app (production incident on 0.12.28).

- `terminateCarrierCall` is the single decision point and calls only leaf
  operations; `rejectInboundCarrierCall` delegates answered and direct SIP calls
  to it and never re-enters. The cycle no longer exists.
- Direct SIP calls end by their live dialog state (`sipGateway.End`): an
  unanswered INVITE is declined, an answered dialog — Telephony's or an
  adviser's — gets a BYE. Stored answer timestamps are not consulted.
- Ending a call that has already ended is a no-op, so duplicate termination
  requests are safe.
- Regression tests over real SIP: Telephony-answered call without adviser,
  ringing call, caller cancellation, duplicate termination, and contradictory
  answer state without a gateway (which reproduces the stack overflow on the
  previous code).
- Twilio, Plivo and Bandwidth termination behave as before.
