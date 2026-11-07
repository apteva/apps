# Telephony 0.12.37

## Single startup for immediate XML-carrier calls

The background automatic-answer worker now prepares Core without issuing a
carrier REST redirect for immediate inbound Twilio and Plivo WebSocket calls.
Their authenticated inbound/wait XML response owns media activation. Direct
SIP and push-carrier activation retain their existing answer path. Explicit
agent-decided pickup and human softphone activation are unchanged.

Previously, the worker could win the pending-call preparation claim while the
initial provider webhook was still open. The worker sent streaming XML through
the carrier API, then the webhook returned the same streaming XML a second
time. Twilio replaced the first stream, and Telephony's duplicate-connection
guard correctly rejected the competing connection with HTTP 409. Twilio could
then end the call with media handshake error 31920. The local 24 October call
confirmed both stream starts, the stop/replacement, REST update and rejection.

The correction preserves the single shared Core preparation, independent
request cancellation, bounded startup/retry handling, media ownership and
duplicate-connection protection. It does not relax authentication or permit
two concurrent carrier bridges. No codecs, audio queues or Core settings change.

## Verification

A deterministic reproduction starts the actual automatic-answer tick before
the authenticated Twilio webhook and holds Core preparation while both join.
Both legacy/direct and published-flow cases fail on 0.12.36 with one unwanted
REST activation alongside the XML response. The corrected cases require one
Core spawn, no carrier REST activation, one Connect/Stream response and the
same answered call/thread. Additional Twilio/Plivo checks verify that worker-only
preparation remains answering until a callback activates media. Push-carrier
success/failure cleanup tests retain positive REST-activation coverage using
Bandwidth. Existing preparation, retry, cancellation, SIP activation and
inbound-protection regressions remain required; the new startup checks are
included in lifecycle race CI.

Publishing does not deploy to any installation. A local installation upgrade
and another physical phone call are needed to validate the complete carrier
path; local fixtures do not prove handset listening quality.
