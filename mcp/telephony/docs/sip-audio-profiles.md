# SIP audio profiles and adaptive receiving

Audio profiles apply to **new SIP calls**, inbound and outbound. Active calls keep
their resolved policy and `profile_revision` until they end. Saving a profile
does not restart Telephony or change carrier provisioning. Browser WebRTC/Opus,
TURN and Telnyx's optional carrier jitter buffer remain separate settings.

## Receiver behavior

The default `adaptive` receiver separates three clocks:

1. Original RTP-to-arrival timing is retained as diagnostic evidence. RTP is an
   unsynchronized sampling clock, so relative delivery age is not measured
   one-way network latency. G.722 retains its 8 kHz RTP clock and 16 kHz decoder.
2. Playback adapts to changed delivery conditions without repeatedly flushing
   otherwise fresh audio. Recovery qualification still controls phase updates,
   but does not prevent playing locally fresh packets.
3. Physical residence in the socket, raw/decoded reserve and forwarding queue
   remains bounded. Forwarding preserves the oldest real sample's arrival;
   padding and recovery never renew that age. The browser leg retains its own
   320 ms playback-age cap, even if the SIP policy allows a larger local budget.

After playback starts, the default receiver continues regular output during
missing input, using the existing bounded concealment and then silence. It does
not generate output before priming or during intentional hold, or invent RTP
sequence-loss counts for continuity silence. Actual received sequence gaps remain
separate. Old queued audio, excessive relative lateness and capacity overflow
still cause discard.

For stateful G.722, overflow can retain at most four skipped encoded packets
(640 RTP samples) for ordered decoder-only warm-up on the playback worker. A
larger skipped backlog resets decoder state with a diagnostic counter. Ordinary
rebuffering preserves ADPCM state; a real source restart or hold transition
invalidates it. No decoding runs on the socket reader.

Linux kernel realtime stamps are converted to monotonic residence. Detected
wall-clock steps make boundary stamps uncertain; those packets are discarded
before entering the source-clock estimator and counted explicitly. This is
conservative handling of an ambiguous clock boundary, not evidence of a clock
step in the reported production incidents.

## Recovery after an empty receive queue

Initial playback still builds the configured reserve. Once the adaptive receiver
has started, starvation is a separate state: admissible returning audio can
render on the next consumer tick without rebuilding that initial reserve.
Intentional hold and source/clock resets still start a new playback epoch.
The reference receiver keeps its previous priming behavior.

The existing target remains an operating target for bounded sample correction;
capacity and age limits do not increase. Locally generated concealment does not
own real-audio admission capacity. Recovery removes retained synthetic PCM but
preserves fresh received PCM and G.722 decoder continuity. The configured
crossfade also applies when returning from long silence.

Diagnostics distinguish `receive_starved`, `receive_audio_returned` and
`receive_playback_resumed`. The SIP jitter snapshot exposes
`receive_playback_state`, `receive_starved_at`, `receive_returning_at`,
`receive_rendered_at`, `receive_resume_ms`, `receive_max_resume_ms`,
`receive_recoveries`, `encoded_queue_ms` and `decoded_queue_ms`.
Rendering means the receive framer produced real PCM, not that a browser or
remote listener heard it. Generated silence cannot complete recovery. Optional
packet timing records the first real rendering, not a synthetic frame or a later
partial continuation. Event history remains bounded by the existing 64 slots.

Repeat the constructed 140/180/220 ms gap scenarios and targeted guards with:

```sh
GOWORK=off TELEPHONY_RECEIVE_RECOVERY_RESULTS=/tmp/sip-receive.json \
  go test -run '^TestSIP(Starvation|ReceiveRecovery|ShortSourceGapContinuity)' -count=1 -v .
```

The scenarios use recorded human speech through G.722, PCMA and PCMU, with three
repeated delivery interruptions, followed by either a stable delivery offset or
catch-up bursts. They are not an exact replay of the reported call, which lacked
packet timing. Tests check next-tick recovery, decoded speech, initial-word
preservation, continuous 20 ms output, combined capacity, local residence,
G.722 decoder state, hold/source reset and truthful recovery diagnostics.

The severe recovery guards allow bounded damage from missing delivery and
capacity trims. The exported speech `Accepted` field separately uses the existing
strict smooth-speech budget (99% active coverage, 80 ms maximum interruption,
100 ms added delay); passing recovery tests does not make a degraded network
scenario smooth. Catch-up bursts exceeding capacity still require trims.

## Configuration

Operator UI: **SIP media → SIP audio profile** on an inbound route, or the
outbound SIP transport dialog. Explicit PCMA-only, PCMU-only and G.722-only
restrictions are available. Codec changes apply at the next negotiation. Carrier
capabilities still apply; Twilio outbound does not support G.722.

The administrator HTTP route is `POST /numbers/sip-audio-profile` (under the
installation URL); the MCP tool is `telephony_sip_audio_profile`. Both use the
authenticated project and existing administration permissions. Delegated users
and agent principals cannot administer these settings.

Read by sending only scope arguments. Save with `config`; delete an override
with `reset: true`. Scope precedence is:

1. Project (no scope arguments)
2. `direction: "inbound"` or `"outbound"`
3. Inbound `route_id`, or outbound `carrier_connection_id`
4. Outbound `phone_number` together with direction and carrier binding

An override replaces the complete playback policy at its scope. Omitted codecs
inherit the less-specific codec configuration. Existing local media settings
form the base beneath these profiles. At call setup a single indexed query reads
at most four scope rows; audio workers never read policy from the database.

Example: temporary inbound PCMA test on one route:

```json
{
  "route_id": "example-route",
  "ttl_seconds": 3600,
  "config": {
    "preset": "resilient",
    "codecs": ["PCMA"],
    "rtp_playback": {"diagnostic_window_ms": 10000}
  }
}
```

`ttl_seconds` is evaluated on the server: 0 means persistent, 1–86400 gives an
expiring test. Alternatively use `expires_at` within the next 24 hours. Expiry
reveals the **inherited policy**, not the previous value at the same scope. No
timer or restart is needed. Responses include `active_override`, saved revision,
effective settings and the fingerprint pinned to new calls.

Presets:

| Preset | Initial/adaptive max target | Capacity | Local age budget | Receiver |
| --- | --- | --- | --- | --- |
| balanced (default) | 60 / 100 ms | 160 ms | 200 ms | adaptive |
| resilient | 80 / 120 ms | 200 ms | 200 ms | adaptive |
| reference | 60 / 100 ms | 160 ms | 200 ms | previous Telephony source-age admission |
| custom | default values plus explicit fields | | | adaptive unless specified |

The reference preset is a comparison with the prior Telephony receiver, **not**
the test-only SpeexDSP implementation. A more specific supplied field overrides
its preset default. Resilient trades a larger reserve for additional latency; it
does not recover missing input or guarantee first-word preservation in a burst.

| `rtp_playback` field | Default | Accepted bounds / meaning |
| --- | --- | --- |
| `receiver` | adaptive | adaptive or reference |
| `target_ms` | 60 | 40–120 |
| `max_target_ms` | 100 | target–160 |
| `capacity_ms` | 160 | max target + 40 through 400; raw and decoded combined |
| `max_age_ms` | 200 | capacity through 400; original physical residence |
| `max_relative_lateness_ms` | 1000 | local age budget through 2000; separate relative RTP guard |
| `recovery_window_ms` | 1200 | 200–2400 |
| `recovery_wait_ms` | 600 | 80 through recovery window |
| `recovery_cooldown_ms` | 2000 | 200–5000 |
| `concealment` | true | boolean |
| `concealment_ms` | 60 | 20–120 |
| `crossfade_ms` | 5 | 1–20 |
| `continuous_output` | true | continuity after real playback begins |
| `clock_drift` | true | apply fitted sample-rate correction; clock observation and queue stabilization remain active |
| `diagnostic_window_ms` | 0 | 0–20000; optional bounded header ring |

Invalid fields/types/bounds fail rather than silently changing scope. Legacy
`resynchronize` remains deprecated; it is not an enable switch for recovery.

## Bounded diagnostics

Normal one-second SIP telemetry includes effective policy/revision, decoder
warm-up/reset counters and process runtime evidence. Runtime evidence is cached
once per second across all calls, collected lazily by the existing diagnostics
worker. GC pauses, scheduler latency histogram bounds and GC CPU deltas describe
the **process interval**, not proof that a specific call was blocked. There is no
new per-call ticker or runtime collection on packet ingress.

Header timing capture is off by default. When requested it has at most 1,000
slots per call and 64 enabled calls per process. It contains sequence, RTP
timestamp, SSRC, payload type/duration, arrival/render time, processing delay,
relative delay, queue depth and disposition. It contains no PCM, SDP, addresses,
tokens or credentials. Normal polling does not serialize the packet ring.

Retrieve live header evidence explicitly through operator
`/audio-health?call_id=...&include_packet_timing=true` (full or incremental
history mode). Ordinary lightweight `history=none` remains counters-only.
Background history retains at most the first incident ring, a post-incident ring
about two seconds later, and one final ring. Snapshots use a best-effort media
lock and allocate outside it; contention may omit a snapshot. Playback timestamps
describe decoded frame release, not verified browser rendering. Header capture
supplements existing browser diagnostics, rather than proving end-to-end latency.

## Reproducible verification

Run from `mcp/telephony`, without Docker or live carrier access:

```sh
GOWORK=off go test ./...
GOWORK=off go test -race -run 'TestSIP(AudioProfile|Adaptive|KernelClock|PacketTrace|RuntimeEvidence|G722|TimestampRestart|HumanHeader|Forwarding)' .
GOWORK=off go test -run TestSIPMatureJitterReferenceCapturedSpeech -v .
bun run test:frontend
bun run typecheck
bunx --no-install playwright test -c frontend/sip-profile.playwright.config.ts
bun run benchmark:outbound-sip --seconds 20 --calls 8 --recording true --pause-ms 570 --output /private/tmp/new-sip-soak
```

The mature comparison builds pinned BSD-licensed SpeexDSP 1.2.1 source offline
using a local C compiler. It is test-only and introduces no production native
dependency. It uses the same packet timings, speech, Go decoders and concealment,
but its own buffering policy; it is not a complete independent SIP stack or NetEq.

Keep healthy/moderate speech acceptance separate from severe-fault ceilings. The
captured severe trace improves with adaptive admission, but still has overflow
and damaged opening audio; the mature reference performs better on that trace.
Passing bounded-fault regressions is not a claim of uniformly smooth speech.
Use staged matched G.722/PCMA listening and load comparisons before production
adoption. Source phase and modeled added delay are not synchronized carrier-to-ear
measurements. Native Linux timestamp handling requires Linux execution beyond a
macOS cross-build and injected-clock tests.

## Optional receive delay estimator

`rtp_playback.delay_estimator` accepts `ewma` (existing default) or
`distribution` for the adaptive receiver. The latter uses a bounded rolling delay
distribution without increasing configured capacity or age limits. See
[receive timing recovery](receive-timing-recovery.md) for configuration, measured
tradeoffs, WebRTC recovery, and local verification.
