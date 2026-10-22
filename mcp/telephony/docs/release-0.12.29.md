# Telephony 0.12.29

## Automatic SIP playback recovery

The shared inbound/outbound SIP receiver now considers a bounded rolling window
of arrival evidence rather than requiring one uninterrupted run of late packets.
Ordinary threshold crossings no longer erase recovery eligibility. A persistent
live delivery phase can recover automatically, including installations with the
legacy `resynchronize: false` value. That compatibility field is retained in
stored settings and diagnostics but no longer disables recovery.

Recovery changes a separate playout offset, not the original RTP clock estimate.
Diagnostics retain relative source-age evidence, current offset, decision reason,
window length, variation, delivery rate, local residence and time to first output.
RTP-derived age is an estimate relative to an unsynchronized clock, not proof of
absolute carrier sending age. Existing physical residence limits, queue capacity,
sequence/loss handling, tail padding, hold/source resets and concealment remain.

The controller uses 128 fixed observations, evaluates every eight packets and
has a two-second hysteresis. Local processing delays do not authorize a phase
change; consistently draining delivery and offsets above one second remain
ineligible. Retained raw/decoded/concealment audio is discarded once on recovery,
then the reserve rebuilds from new packets. It cannot recover speech that was
never received or already expired.

## Optional carrier jitter buffering

New operator control in inbound SIP media and configured outbound SIP settings,
HTTP `POST /numbers/carrier-media` and MCP `telephony_carrier_media_settings`:

```json
{
  "route_id": "existing-sip-route",
  "carrier_jitter_buffer": {"enabled": false, "min_ms": 60, "max_ms": 200},
  "apply": false
}
```

For outbound, select `carrier_connection_id` and optional `phone_number` instead.
`fresh: true` verifies current carrier settings. Telnyx supports 40–400 ms bounds.
The option is **off by default**, independent of local automatic recovery.
Twilio is reported as unsupported rather than receiving an invented setting.

Carrier buffering affects the shared SIP connection, including every number or
route using it, and adds latency. Saving only records requested policy. Applying
requires an idle resource, including aliases under other configured bindings;
new admission is briefly gated while credentials, identity and readback are
checked. No carrier network request runs under the admission lock. Active calls
return `pending` without a provider change; an operator must retry apply when
idle. There is no automatic background mutation. Effective settings, verification
time and pending/mismatch/unverified states are shown separately. Readback
failure preserves the previous verified value and its timestamp.

## SIP RTCP evidence

RTP and RTCP use an even/adjacent port pair within the existing media range.
SDP advertises the RTCP address; explicit peer addresses use the existing source
allowlist. A separate socket/worker sends standard compound SR/RR + anonymous
CNAME reports approximately every five seconds, with initial staggering. SRTCP
uses separate authenticated/replay-protected contexts. Reports include interval
and cumulative sequence loss, jitter, sender counts, peer loss/jitter and RTT.
They appear separately from RTP in `/audio-health`.

RTCP reporting uses a nonblocking observation lock and an independent bounded
write deadline. RTCP failures never change RTP socket deadlines or become RTP
send/decode failures. RTP+1 is supported; `rtcp-mux-only` is explicitly rejected.
Existing media ranges must permit the adjacent RTCP UDP port. Pair capacity is
bounded by the configured range; no host ports or firewall rules are changed by
publishing this app.

## Reproducible local evidence

The sanitized, immutable header-timing fixture has 1,069 packets over 21.48 s,
with no sequence discontinuities and arrival gaps up to 299 ms. It contains no
call identity, network addresses or caller audio. Known public speech replaces
payloads. A frozen test-only 0.12.28 implementation provides the comparison;
initial relative phase and fitted clock are reconstructed evidence, not a claim
of synchronized wire latency. A separate five-second stable continuation checks
that rejection stops after delivery stabilizes.

Across PCMA, PCMU and G.722, discarded PCM falls from **6.36 s to 2.24 s** in this
replay. Maximum reserve is **139.4 ms**, within the existing 160 ms cap. With the
stable continuation, matched active-speech coverage improves from **75–78% to
85–89%**. Longest damaged speech improves from **560–680 ms to 520 ms**. Estimated
maximum matched-speech delay rises from approximately **160 ms to 300 ms** as
more delayed but locally fresh speech is preserved. This severe burst scenario
is **not classified as smooth speech**, and initial words already absent from
this capture cannot be recovered. Healthy/no-fault acceptance continues to
require exact complete decoded PCM, first-word preservation and zero discards.

On a local Apple M1 Pro, recovery evaluation costs about **31 ns/packet healthy**
and **312–320 ns/packet with an active shifted/jittered window**, with **zero
allocations**. A compound RTCP report costs **430–438 ns / 488 B** per five-second
report, outside the audio owner. These microbenchmarks are not a capacity claim.

Regression coverage includes captured variable delivery, stable recovery, local
stalls, jittered draining backlog, configured bounds, original-clock preservation,
hold/source/timestamp changes, all three codecs, old compatibility values,
provider authorization/project isolation/shared binding aliases, invalid provider
readback, cancellation cleanup, unsupported carriers, SRTCP replay/forgery,
malformed RTCP, allowed source addresses and paired-port cleanup.

A local eight-call, 20-second Chromium/WebRTC to SIP/TLS/SRTP emulator run,
with recording, PCMA/G.722, jitter, a 180 ms phase change and a 570 ms microphone
pause, passed all acceptance gates when run in isolation. An earlier run during
the full CPU-intensive regression suite failed delay-growth checks; that result
is retained and is not a claim of unlimited capacity or immunity to competing
workloads. The repeatable harness remains unchanged.

SDK is pinned to v0.104.0, verified by published tag ancestry. Both manifests,
installer source refs and generated frontend metadata use 0.12.29.

All work and carrier simulations are local. Publishing does not install, restart,
configure or call staging/production, enable TURN or enable Telnyx buffering.
A real Telnyx buffering enabled/disabled comparison remains unperformed. Separate
long browser-to-SIP load/quality limitations from 0.12.27 still require explicit
qualification; passing deterministic recovery is not proof of universal call
quality or unlimited load.
