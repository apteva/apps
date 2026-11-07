# Telephony 0.12.36

## Renderer capacity for continuous AI calls

Telephony now consumes Core's optional `playback_flow_control` capability and
advertises the call's pinned `backlog_ms` as `capacity_ms` in `playback.ready`.
Matching Core versions pace output against existing generation/item-scoped
playback receipts instead of sending large provider bursts into Telephony at
once. The readiness message and capacity survive Core connection renewal.

This adds no playback reserve, timer, or queue expansion. The existing
200–2000 ms configurable backlog, short-pressure handling and bounded relay
remain authoritative. Older Core versions, traditional AI mode and human calls
retain their previous behavior. Audio health exposes the advertised capacity
alongside the negotiated capability.

Generation-scoped cancellation already shipped in 0.12.33 and is retained:
cancelled frames/receipts cannot revive old speech, while subsequent replies
remain playable. SIP clears local queued playback only. Twilio/Telnyx remote
clear commands affect the carrier stream; collateral items and the evidence
scope remain explicit rather than being described as selective remote clears.

## Verification

Regression checks cover capability/legacy negotiation, pinned minimum/default/
maximum capacities, traditional/human exclusions, capacity replay after three
Core connection failures, stale generation fences and cancelled playback marks.
The compiled Core/Server/carrier harness adds 2.5-second provider bursts against
the 1-second Telephony backlog and interrupts while Core has further output to
deliver, across inbound/outbound Telnyx, Twilio, SIP PCMA/G.722, Vonage and Sinch.
It checks complete carrier frame delivery, no overflow/reconnect/omission, and
successful replies after repeated interruption. The full Telephony suite and
focused media race checks remain release gates.

Core and Server are not changed or released here. Core must advertise the
matching capability; deployment to a local installation is separate from
publishing. Simulated carrier receipts and successful RTP transmission do not
prove physical handset audibility or certify production capacity.
