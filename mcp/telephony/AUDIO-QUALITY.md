# Live audio reliability and verification

## Scope

Local changes for the 29 September 2026 Target KPI report. No staging,
production, live number, carrier account or integration JSON was modified.
Routing successfully connecting a call is separate from audio quality. The
reported minute-long adviser-to-caller delay remains unverified by carrier
traces. These changes address demonstrated buffering and observability gaps;
they do not establish which system caused the production ingress pauses.

## Behavior

- Playback keeps the default 60 ms startup target, adaptive range 60–160 ms,
  and 320 ms hard queue limit. Arrival jitter raises the target; drops reset
  the clean-playback interval. A higher target does not discard queued speech.
  The former `playback_soft_limit` trimming is removed. Over-limit bursts are
  trimmed to 160 ms, with the existing 5 ms crossfade. Frames older than the
  playback residence limit are discarded before rendering.
- Microphone timestamps are mapped from the AudioContext clock into the worker
  monotonic clock using MessagePort probes and an uncertainty allowance.
  Capture frames demonstrably over 250 ms old, including frames from before a
  mute/reconnect gate, are discarded. Existing WebSocket backpressure handling
  remains. This cannot retract bytes already transmitted into the network.
- Negotiated APT2 frames carry sequence, source timestamp, send timestamp and
  local residence time. The server accepts legacy APT1 capture and raw PCM;
  playback stays raw unless that browser negotiates version 2. A new worker
  remains compatible with an old server. The internal carrier-peer protocol
  remains raw PCM16 mono at 24 kHz.
- Server live-audio queues allow at most 120 ms of unsent PCM, plus at most one
  active write. Frames expire at 250 ms; active writes use the remaining age
  budget. Human/external carrier media writes use a 250 ms timeout. A stalled
  write closes the affected transport through the existing error/reconnect
  path; it does not leave an indefinitely blocked writer.
- Hub locks protect ownership and nonblocking enqueue only. Network writes run
  outside them. Hold flushes both directions; replaced browsers cannot inject
  microphone audio. Already transmitted audio cannot be recalled by a local
  hold or mute operation.
- Human JSON, Twilio and SIP pacers discard aged speech as well as excessive
  queue depth. Buffered AI output retains its existing policy.
- APT2 capture transport delay is also checked against the fastest transit
  observed in the current browser connection. Excess above 250 ms is dropped.
  This is an offset-independent *increase*, not absolute one-way latency; a
  connection that is slow from its very first frame needs carrier/network
  measurements to establish its absolute delay.

## Diagnostics

`browser_audio_diagnostics.timing` contains worker capture/sending counters,
playback receipt, sequence gaps, worker tick gaps, cumulative drop totals by
reason, playback sample duration and maximum residence time. The last events
remain bounded; their eviction does not reset the cumulative reason totals.

`browser_audio_diagnostics.server` contains a server observation epoch,
per-stage frame/byte counters, maximum inter-arrival gaps, timestamps for the
last 32 gaps above 100 ms, decode/write work durations, capture timestamps and
excess transit delay, and separate live queue/pacer counters. It includes
carrier read/decode/send, carrier-to-hub forwarding, hub-to-browser forwarding
and microphone server receipt. Queue bytes are PCM bytes; carrier stage bytes
are bytes at that stage and may be encoded. Successful carrier socket writes
are not proof of remote playback.

The server snapshots every five seconds while a browser is attached, even if
its UI diagnostics stop arriving, and at browser teardown. No per-frame database
write is added. Queue counters survive ordinary browser/peer replacement while
the hub remains alive; a fully recreated hub has a new epoch. Browser counters
belong to that audio session. Do not subtract counters across different epochs
or assume they are durable all-call totals after a complete process restart.

Carrier event sequence gaps and microphone capture sequence gaps are exposed
separately; the old combined field remains for compatibility. Carrier sequences
are observed across start/media/mark events. A missing carrier event has no
assumed 20 ms audio duration. Provider media timestamps/chunks are retained for
correlation where available, alongside the call's existing carrier identifiers.

Worker/server clock probes report estimated playback transit with RTT-derived
uncertainty. Frames are dropped on network age only when the conservative lower
bound exceeds 320 ms and the clock estimate is recent. These estimates do not
prove PSTN mouth-to-ear latency. AudioContext time, worker time and server time
are not directly interchangeable.

Process diagnostics sample goroutine count, heap usage and cumulative scheduler
and GC pause p99 once per five seconds across all calls. They provide context,
not proof of the cause of a historical audio gap. Read/decode probes describe
arrival at Telephony's process, not packet arrival at the carrier or network
edge.

## Local verification

Commands (from this directory):

```sh
bun run build:frontend
bun run typecheck
bun run test:frontend
GOWORK=off go test -short ./...
GOWORK=off go test -race -short -timeout 8m \
  -run 'TestLiveAudio|Test.*Audio|TestSoftphone|TestWebSocket|TestAIHandoff|TestCarrierActivation' .
env -u TELEPHONY_DEPLOYED_FRONTEND -u RUN_TELEPHONY_LIVE_CARRIER \
  GOWORK=off go test -tags integration -run TestTier2 -count=1 .
```

The integration command uses local protocol substitutes and real compiled
sidecars/Chromium. It excludes the separately build-tagged live-carrier profile.
The existing untracked synthetic integration fixture was preserved unchanged.

New regression evidence:

- Real playback worklet at 24, 44.1 and 48 kHz; fixed and variable packet sizes.
- Steady playback has zero drops/underruns; PCM waveform samples are bit-exact.
- Repeated 40–250 ms ordered transport stalls have zero playback drops in the
  specified three-minute simulations. Rebuffering is bounded; it is not a claim
  of zero underruns under all jitter.
- Repeated 200 ms stalls in the original three-minute audit: previously 424 ms
  dropped / 10 underruns; now 0 ms dropped / 2 underruns.
- A 30-minute simulation with 200 ms stalls and variable packets: 0 ms dropped,
  2 underruns, maximum marker source age about 211.4 ms, maximum queue 212 ms.
- 500 ms and 6.7-second outages: sample conservation, bounded queue/residence
  and recovery checks. Some speech is deliberately lost; retaining seconds of
  queued speech would violate the live-call latency requirement.
- Worker pauses, delayed MessagePort delivery, WebSocket backpressure, mute and
  reconnect gates, legacy/new framing and server sequence propagation.
- Server duration/age bounds, exact drop accounting, a blocked socket, hold,
  stale ownership, diagnostics persistence and human-vs-AI pacer policy.
- 24 concurrent bidirectional calls through the actual server hub/writer path:
  100 frames per direction per call, exact PCM payloads and no unexpected drops.
- Existing resampler/limiter, WebSocket, human, routing and AI regression tests.

The integration suite also exposed a pre-existing AI fallback journal race:
late admission could reopen the marker for an already committed terminal/menu
routing effect. Admission now preserves that effect. A deterministic regression
covers the interval between committing fallback and completing its carrier
command; the integration test waits for the complete durable outcome.

## Practical limits

These tests establish behavior for their defined waveforms, schedules and load.
They cannot guarantee zero audio loss on every browser, network or carrier, or
prove that the original production incident is eliminated. A multi-second media
outage necessarily causes silence or discarded audio if latency stays bounded.

A separately authorized real-call check should correlate two-ended audio markers
with browser/server stages and carrier session/leg traces, in both directions.
That is required to measure actual mouth-to-ear delay and locate frames absent
before Telephony. No such call was made for this change.

## Browser network benchmark

The [softphone benchmark](benchmarks/softphone/README.md) measures both directions
through actual Chromium and a compiled sidecar under nine network profiles.
It supplements deterministic regression tests with timed audio markers and
retained browser/server diagnostics. Unsupported bandwidth and interruption
loss are reported explicitly; this is not a perceptual speech-quality score.

The real-browser benchmark also found fractional worker drop-event times were
rejected by the Go integer diagnostics schema, discarding entire snapshots.
Drop-event decoding now accepts fractional milliseconds, rounds and bounds them,
and preserves the existing integer public API. Wire-format regression tests and
browser benchmark protocol checks cover this; it does not alter audio samples.

## 1 October carrier delivery follow-up

[Source timing and bounded recovery](docs/carrier-media-delivery.md) extends
APT2 with negotiated APT3 human playback metadata, source-aware stale filtering,
directional carrier reception notices and a clock-independent growing-delay
guard. Human carrier-peer traffic may now be framed internally; AI peers retain
raw PCM. The benchmark matrix now also shapes carrier input and tests a ten-second
catch-up, absent frames, intentional mute and forced browser reconnect.
