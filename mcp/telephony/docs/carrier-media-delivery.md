# Carrier media delivery: source timing and bounded recovery

Implemented locally following the 1 October 2026 Twilio reception-gap report.
No staging, production, carrier account, route, number or integration JSON is
changed. This work does not identify the origin of those production gaps.

## Media behavior

Twilio normalizes `media.timestamp`, `media.chunk`, `media.track` and
`streamSid`. Shared JSON adapters normalize their millisecond source timestamp
and chunk when available. Unknown timing falls back to receipt time; metadata
is never invented for untimed binary/SIP providers.

Each human call has an independent source timeline. The earliest observed
arrival-minus-source offset maps the relative carrier clock to the server's
monotonic clock. Excess age is delay above the fastest observed delivery, not
absolute carrier latency. Stream/bridge changes and timestamp resets with a
continuing sequence create an epoch. Duplicate/non-increasing media chunks in
an existing timed stream are discarded. Timestamp jumps may represent absent
frames or omitted silence; they are not counted as received PCM loss.

The source age budget is 320 ms. Delayed packets wholly older than that budget
are discarded before human processing/forwarding; boundary packets can reach
the next stage, which checks the same source clock again. Internal APT3 frames
preserve receipt time, original timestamp/chunk, mapped source time and epoch
through the authenticated loopback peer. Source expiry drops audio rather than
closing a healthy socket. In-flight writes retain the existing transport
deadline; the next stage validates source age after transport.

The browser negotiates APT3 using a version-2-compatible capabilities request
with `versions: [3, 2]`. Old browsers receive APT2 or raw PCM. New browsers keep
APT2/APT1 fallback with old servers. AI peers still receive plain PCM and keep
their buffered speech policy. Primary routing and carrier answer operations are
unchanged.

Browser filtering uses clock uncertainty, then carries the accepted source age
into the worklet's render clock. When a slow link prevents clock probes, growing
delay relative to the fastest delivery on that socket is still bounded. That
fallback cannot measure unknown fixed latency on its first packet. The current local engine keeps 60 ms startup and the 320 ms hard backlog/source-age
limit; generic live reserve adaptation has a 280 ms ceiling and waveform matching
(see [adaptive-playback.md](adaptive-playback.md)). No buffer is increased to accommodate ten seconds of stale speech.

## Reception monitoring and notices

After media starts, a two-second missing-media interval is a delivery stall for
adapters explicitly declaring continuous input (currently Twilio and Telnyx).
The monitor is disabled while ringing, held or disconnected. Untimed or
discontinuous adapters do not assume that absent media is a transport failure.
Silent PCM counts as delivery. Adviser microphone traffic and intentional mute
do not influence the caller-direction timer.

`media.delivery` controls contain `direction: carrier_to_operator` and
`state: stalled | flowing`. The shared softphone surfaces interruption and
recovery via `onNotice`, leaving microphone readiness, carrier sessions and
answer ownership untouched. Notifications use a bounded queue and cannot block
the media reader. WebSocket pong replies also use the sole writer asynchronously;
a blocked pong no longer prevents receiving the next caller audio frame.

## Diagnostics and accounting

`browser_audio_diagnostics.server.carrier_reception` includes stream/epoch,
received duration, source timestamp/chunk, reception gap, maximum excess age,
rapid-batch duration, stale/duplicate discard totals and stall/recovery counters.
The last 32 incident events carry UTC correlation time and monotonic receipt
time. A rapid batch means adjacent arrivals less than 5 ms apart; it is an
observed delivery pattern, not a claim about carrier packetization.

The server timeline distinguishes completed socket-frame receipt, JSON parsing,
media receipt, decoding, hub receipt and carrier output. Queue snapshots retain
overflow, local age expiry, source expiry, flush and write failure separately.
Browser timing records source age, growing delivery delay, transport drops,
playback drops and played duration. Browser connection, carrier peer,
AudioContext and microphone/device states are separate fields. Explicit
`operator` interrupts retain their source in logs and carrier diagnostics.

Only actual received/discarded samples contribute to discard durations. General
carrier sequence gaps, missing media and downstream sequence gaps are evidence,
not additional lost-audio milliseconds. Do not add gap lengths to sample discard
totals or count a downstream observation of an upstream discard again.

## Local verification

- Real Twilio JSON/G.711 bridge, loopback hub and browser socket preserve metadata,
  discard simulated ten-second catch-up, retain fresh speech and continue the
  operator-to-carrier direction.
- Source tests cover delayed batches, absent frames, silent PCM, hold, duplicate
  chunks, invalid timestamps, stream/reset epochs and 24 independent calls.
- Actual WebSocket tests prove stale expiry leaves media usable and a blocked
  pong does not block caller reception.
- Worker tests cover APT3, legacy negotiation, clock uncertainty, source expiry
  through playback and an undersized download without clock probes.
- Existing steady PCM, 24-call duplex, mute/reconnect, AI, routing, carrier,
  listener and 30-minute audio regressions remain part of the full suites.
- The Chromium matrix additionally tests carrier ten-second catch-up, missing
  frames without catch-up, intentional microphone mute and forced reconnect.

See `../benchmarks/softphone/README.md` for reproduction and the accompanying
verification report for measurements. No local test can restore absent speech,
prove zero perceptual quality loss on every real network, or attribute the
production stalls to Twilio. Authorized real carrier traces remain necessary
to establish the origin and validate physical PSTN audio timing.
