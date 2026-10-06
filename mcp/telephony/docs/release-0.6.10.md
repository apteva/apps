# Telephony 0.6.10

This release extends the generic inbound decision loop to direct SIP calls on
Twilio, Telnyx, and DIDWW, and to programmable Bandwidth inbound calls. Carrier
capability checks reject unsupported media nodes at ingress, including in older
published flows. Bandwidth routing requires a dedicated Voice Application and
Location configured manually; Telephony does not repoint a shared Location.

Inbound caller filtering now supports explicit project-wide or
destination-specific E.164 displayed-caller rules through
`inbound_spam_blocked_callers`. The default is empty. A matching call is
rejected before adviser delivery, classified as `spam_suppressed`, and omitted
from the missed-call pool. On Telnyx, the initial webhook is still received
before the `reject_call` command; a failed command receives a retryable webhook
response. Destination-wide bursts across rotating caller IDs remain alert-only
so the public IVR number stays reachable. See
[inbound-spam-filtering.md](inbound-spam-filtering.md).

Local verification covers the Go short suite, focused race tests, frontend
typecheck and tests, and generated client build. No live carrier call or
production installation was performed. Direct SIP does not support every media
node available to programmable carriers.
