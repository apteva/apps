# Telephony 0.12.5

Cumulative release retaining all changes in 0.12.4. SIP improvements apply to
Telephony's shared inbound/outbound SIP bridge, including Telnyx, Twilio and
DIDWW. Browser WebRTC/PCM transports, carrier WebSocket processing and outbound
carrier pacing are unchanged.

## Bounded adaptive SIP playback

Startup requires a real reserve. The initial target remains 60 ms, adapts up to
100 ms and gradually builds/releases reserve during playback using bounded
single-sample adjustments. A short utterance may start with at least 40 ms after
120 ms of playback ticks; a lone 20 ms packet cannot start on the timer alone.

The default capacity is 160 ms, including retained decoded samples; the age
limit is 200 ms. Capacity can be configured up to 200 ms. Relative source age
uses the fastest observed RTP clock baseline, independent of time spent in our
queue. Delayed catch-up speech remains subject to the age limit. These are SIP
stage limits, not a measurement or guarantee of total conversation latency.

Source timestamps distinguish contiguous DTX and telephone-event sequences
from missing speech. Empty queues are not counted as confirmed packet loss.
Long missing-packet runs resynchronize rather than indefinitely delaying new
speech. RTP source changes reset the clock baseline and retained PCM.
Intentional remote hold resets the playout baseline and discards held
reserve separately; a frozen source clock during hold does not poison resume.

Stable ten-second RTP-clock windows estimate drift, bounded to 1,000 ppm, with
small sample corrections. Jittery windows do not establish clock-drift estimates.
Healthy decoded PCM is unchanged when no correction is needed.

## Short-gap concealment

Bounded pitch-matched PCM continuation fades to silence within 60 ms and
crossfades recovery over 5 ms. This is shared across PCMU, PCMA and G.722 and
never injects fabricated compressed samples into the G.722 decoder. Concealment
softens gaps; it cannot reconstruct missing words or recover long outages.

## Configuration for future calls

Use the existing `telephony_local_media_settings` tool or
`POST /numbers/local-media` with optional `route_id`:

```json
{
  "config": {
    "rtp_playback": {
      "target_ms": 80,
      "max_target_ms": 100,
      "capacity_ms": 160,
      "max_age_ms": 200,
      "concealment": true,
      "clock_drift": true
    }
  }
}
```

The example explicitly selects 80 ms; omission retains the 60 ms initial target.
Save the full desired local-media config, including existing prompt/codec
settings, because this endpoint replaces the config. Route overrides apply to
inbound calls; project defaults also apply to outbound SIP. Settings are copied
at admission, never fetched in the audio loop or changed on an existing call.
`concealment: false` retains zero filling; `clock_drift: false` disables the drift
estimate's sample corrections. Adaptive reserve management remains independent.

## Diagnostics and performance

Live `/audio-health` evidence exposes separate overflow, expired, late-arrival,
concealed, zero-filled and intentional-hold counters, target/capacity, relative
delivery age, drift and bounded timestamped incidents. Source age is explicitly
labelled `relative_rtp_baseline`, not an absolute carrier timestamp. Raw RTP
sequence loss remains separate from local drops and replacement audio.

The media path performs bounded in-memory bookkeeping. No diagnostic network or
database writes run on the packet/framing path. Event allocation/serialization
is outside the reader lock and existing background reporting remains bounded.

## Verification

Deterministic replays compare the frozen 0.12.4 buffer against 60/80/100 ms
targets and 160/200 ms capacities across all three codecs. Profiles include
continuous delivery, ordinary jitter, 80/180 ms batches, packet loss, scheduling
pauses, DTX and ten-second catch-up batches. Clock tests replay +/-300 ppm over
120 seconds. Optional locally synthesized French speech exercises all codecs.

Tests also cover healthy PCM identity, short-packet PLC, fade bounds, voiced-gap
error, packet/timestamp wrap, expired partials, bounded history/concurrency,
DTMF, hold with a frozen clock, scoped settings and actual simulated SIP media.
CPU benchmarks compare legacy/current queue and framing work, concurrent calls
and worst-case diagnostic-history serialization. See
[replay and CPU evidence](sip-playback-performance.md): 146 cases; repeated
180 ms batches discarded 960 ms with 0.12.4, 700 ms with defaults, and 220 ms
with optional 200 ms capacity. Healthy G.722 framing measured 14.72 µs versus
14.58 µs previously, with fewer allocations; cheaper codecs incur a small
bookkeeping increase. Complete Go/frontend suites,
SIP race checks, vet, typecheck and builds are publication gates.

No real staging/production call or installation is changed or exercised by this
release. Real network results remain necessary to assess audible improvement on
the affected deployment; local replays cannot identify the incident's carrier
or network cause.
