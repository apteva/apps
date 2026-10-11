# Telephony 0.12.2

Cumulative release retaining every previous Telephony change. Publication only:
no running installation, carrier settings, route or SIP listener is modified.
SDK v0.99.0 remains pinned and was verified as the latest released commit on
app-sdk origin/main. No Server, SDK, Core, integration or client-app source changed.

## Carrier errors and recovery admission

The shared JSON carrier envelope accepts event-specific object/string payloads.
Telnyx's documented error payload retains numeric code, title, sanitized detail
and stream identity; legacy provider `error` envelopes and Bandwidth string PCM
remain supported. Valid errors enter the existing bounded same-leg recovery.
Malformed JSON increments exact counters and emits at most one sanitized decoding
event per bridge per second. Media bytes and credential-bearing URLs are not logged.

An uncanceled continuous-media bridge with at least two seconds without decoded
media can admit one authenticated replacement candidate. The original bridge
continues owning the call while the candidate identifies its call, stream and
format. Candidate reads have a three-second forced-close budget and four small
JSON message slots; no carrier/Core network request holds a call-ownership lock.
Same logical stream IDs are allowed because carrier reconnects can retain them.
A small bounded set of media frames may precede start; they are replayed only
after the subsequent start proves the same call and stream identity.

Before committing, ownership, call termination, candidate cancellation and the
old bridge's renewed media delivery are checked again. Only a successful claim
transaction replaces/cancels the old sockets and any in-flight recovery command.
Claim failure, wrong identity/format, healthy duplicates and intentional hold do
not disturb the existing bridge. Replacement inherits the bounded recovery
budget; absent replacement audio must not wait indefinitely. Late cleanup and
callbacks retain their evidence without releasing or reviving another generation.
The carrier call, adviser session/token and call thread are preserved.

## Audio health and reconnect batches

Carrier snapshots separate socket state, expected media delivery, media age and
recovery state. A continuous-media gap is degraded even without a fresh browser
report. Intentional hold and discontinuous media do not become stall failures.
Current media age and recovery/batch indicators are available to Audio health.
Call-wide arrival gap measurements survive generations; canceled generations
cannot update arrival timing with a late decoded frame.

Each new source epoch explicitly starts with uncalibrated relative timing.
Fast initial batches retain timestamped bounded burst evidence and a confidence
label instead of claiming known original audio age. Existing bounded PCM queues
retain the newest audio, preserve its samples and account for every overflow;
queue and stale-audio budgets are not increased. A new stream's relative source
clock cannot establish absolute age of its first frame or restore delayed speech.
SIP transport, codecs/DSP, browser PCM/WebSocket and WebRTC audio algorithms are
unchanged. Candidate checks operate only on carrier stream establishment/recovery.

## Validation

New regressions exercise documented Telnyx errors through a real local WebSocket,
all documented code categories, legacy/Bandwidth media, verified replacement and
both audio directions with the same browser session, same-stream-ID reconnect,
wrong identity, old-stream recovery during candidate admission, caller hangup,
claim database failure, candidate timeout, hold, live health without browser
reports, bounded decoding evidence and bounded first-batch loss accounting.
CI includes these tests in its focused race gate. Complete Go/frontend suites,
typecheck, vet, generated panel import validation and native/Linux builds are
required before publication. Local network/performance results are recorded in
`verification/stream-replacement-0.12.2.txt`.

The October 11 incident's initial upstream media stall remains unattributed.
Local simulations establish these recovery defects and their regression coverage;
they do not prove Telnyx/network interoperability or uninterrupted real calls.
