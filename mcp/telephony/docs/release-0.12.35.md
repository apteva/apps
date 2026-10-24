# Telephony 0.12.35

## Generic continuous-agent carrier continuity

Negotiated continuous AI bridges now recover their Core connection independently
of the carrier stream. This applies to JSON WebSocket carriers, Twilio, binary
Vonage/Sinch media and direct SIP, inbound and outbound. Human, external and
legacy/traditional bridges keep their existing transport path.

Core-only recovery retains carrier ownership, codecs, resampler/packetizer state,
recording and media pacing. It renews only the authenticated same-project,
same-agent/thread Core capability, verifies the renderer contract, and makes at
most three connection-renewal attempts per recovery episode. A 30-second
connection with received media restores the retry budget for a later incident. Terminal calls, ownership changes,
protocol/policy rejection and cancellation are fenced. Exhaustion does not issue
carrier streaming restart commands. No carrier resources or settings change.

A separate bounded output reader/delivery path permits 500 ms of PCM read-ahead plus one in-flight batch capped by the
configured playback backlog, without adding a mandatory playback wait. This
preserves valid 600/800 ms provider chunks; the carrier queue and 200 ms pressure
grace remain independently bounded.
Interrupts fence their IDs before local delivery, even behind queued PCM.
Staging exhaustion is explicit scoped overflow; it cannot grow an unbounded
queue or silently remove words.

The relay forwards complete audio metadata/payload pairs and does not replay
partially written messages. Caller PCM is normalized to 20 ms admission units;
a 12-frame queue and 250 ms age bound prevent recovery catch-up backlogs. Audio
omitted during recovery is counted separately from writes whose delivery is
ambiguous. Old receipts are coalesced away during disconnection; readiness,
current speech state and scoped overflow controls have defined renewal behavior.
Connection renewal and actual output-media resumption are distinct evidence.

## Playback pressure and transitions

Continuous-agent playback gives a rejected batch up to 200 ms to enter the
existing bounded carrier queue before reporting overflow. Successful short
pressure recovery preserves ordered speech, quiet audio and silence; no carrier
clear is issued. Real interruptions are observed independently by the Core-link
reader, fencing canceled playback while the consumer waits. Sustained pressure
retains the explicit overflow/cancellation behavior and existing queue limits.

A 3 ms linear start ramp is applied to locally controlled audio immediately after
an explicit continuous-agent clear. Normal packet boundaries, ordinary silence,
codec settings and the SIP receive jitter buffer are unchanged. No special
first-segment or greeting policy is introduced.

Audio health retains bounded Core connection events, connection/media recovery
state, omitted/ambiguous input durations and playback pressure wait/outcome
counters. Steady-state bookkeeping adds no periodic worker or database writes.
The message relay uses serialized direct writers rather than extra per-frame
writer requests. Audio health also exposes maximum output staging and omitted
output duration; these are not handset playback measurements.

## Verification and limits

Regression tests cover three Core failures with one actual Telnyx carrier socket
and zero carrier commands, terminal/policy rejection, canceled recovery, partial
messages, fragmented PCM/pings, stale input, receipt bursts, cancellation during
pressure, healthy 32-connection PCM isolation and natural-speech preservation
under near-limit queue pressure. Existing codec, recording, routing, softphone
and continuous-agent carrier tests remain required. The compiled local
Core/Server/carrier harness covers both directions across Telnyx, Twilio, SIP
PCMA/G.722, Vonage and Sinch.

The relay adds processing and allocations; its local duplex benchmark measures
that overhead separately from the zero-allocation pressure fast path. These
checks are not a production-capacity or physical handset quality certification.

Core is not released or changed by this app release. Its input admission,
provider-input scheduling and producer-side flow-control changes remain separate.
Telephony can contain a Core reconnection but cannot recover speech never
accepted upstream, remove model/backend response time or make a sustained network
outage inaudible. SDK v0.106.0. Publishing does not install or restart any instance.
