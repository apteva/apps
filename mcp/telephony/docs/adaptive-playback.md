# Live PCM playback reserve and micro-cut verification

Local Telephony work based on 0.11.0. No staging or production installation changed.
This runs in the shared/headless browser engine for every PCM/WebSocket carrier.
WebRTC/Opus retains the browser's native jitter buffer. Microphone DSP, carrier
routing, call permissions, recording and the private-coaching mixer are unchanged.

## Policy

- Keep the initial/minimum target at 60 ms. Learn from arrival variation and
  underruns, with a configurable maximum target of 280 ms (previously 160 ms).
- Build actual reserve during playback using waveform-matched expansion. Search
  3–20 ms windows against upcoming samples, reject poor matches, and overlap joins
  over 2 ms. Repeat matching periods without changing sample rate/pitch or pausing
  to fill the buffer.
- Adjustment uses 2% of active rendered time, with at most 20 ms accumulated
  credit. Searches are limited to ten per second; buffers are preallocated.
  Healthy streams without a reserve deficit preserve original playback samples.
- Expansion requires fresh arrivals and remaining original-source age budget.
  It does not conceal a delivery outage or reconstruct missing speech.
  Credit/history reset on underrun, stale discard and flush. Caller history is
  recorded before the independent private-coaching overlay.
- Preserve reserve through repeated jitter. Matched compression is allowed after
  ten seconds of stable playback. The target decreases by 10 ms after 30 stable
  seconds. Compression shortens matched segments and is accounted separately
  from stale/overflow loss.
- Retain 320 ms hard source-age and backlog limits, with packet/render headroom
  before expansion. The adaptive ceiling is a target, not a bound on every
  packet-sized queue fluctuation or physical PSTN latency.

Headless hosts may specify `playbackMaxMs:160`, `220`, or `280`, and may set
`playbackAdaptive:false` to retain startup/rebuffer-only behavior. Finite 40–280 ms
bounds require `min <= target <= max`; the hard cap cannot be increased through
these options. Built-in panels inherit the shared defaults.

## Diagnostics

Underrun intervals exclude startup and count missing rendered samples through
rebuffering. `call_ended` distinguishes call ending from successful recovery;
incomplete final reports do not invent recovery. `timing.playback` records
`reserve_expanded_ms`, `reserve_compressed_ms`, `reserve_adjustments` and
`reserve_match_rejections`. These are adaptation measurements, not additional
transport loss. `played_ms` accounts for source consumption including matched
compression; expansion is separately reported as added rendered duration.

## Repeatable local verification

```sh
bun test ui/adaptive-playback.test.ts ui/playback-underrun.test.ts
bun benchmarks/softphone/adaptive-playback.ts /tmp/telephony-adaptive-playback.json
bun run benchmark:softphone --profiles all --seconds 20 --output /tmp/telephony-adaptive-network
```

The actual worklet simulation preserves original source timestamps at 24, 44.1
and 48 kHz, with voiced harmonics, silence, nonperiodic noise and a 200 Hz reference.
Tests check actual reserve growth, pitch, RMS level, splice continuity, normal
sample identity, source accounting, latency caps, repeated 280 ms gaps, progressive
gaps, missing frames, constrained bandwidth and slow reserve drain. The matrix
compares 160/220/280 ms ceilings with adjustment enabled/disabled. Chromium tests
use the shared production client, Worker, worklets, Go bridge and local carrier
substitute with independent directional shaping. Three new profiles replay
280 ms TCP recovery at the three ceilings.

Tests demonstrate behavior under their controlled conditions. An unpredicted gap
can exceed the starting reserve; missing packets cannot be reconstructed. A
sustained PCM link below its approximately 384 kbps payload requirement cannot be
fixed by buffering. Synthetic tones/markers do not establish MOS/PESQ, subjective
natural-speech quality, or production VPN behavior. Representative real-call
listening is a separate validation step requiring authorization.

## Results recorded on 8 October 2026

- 52 VM comparisons. Progressive 40/80/120/200/280 ms voiced gaps: startup-only
  160 ms policy produced 3 underruns / 240 ms missing; live 280 ms policy produced
  2 underruns / 74.6 ms missing, zero stale/overflow discard, and approximately
  289 ms p95 source-to-render age. This is about 69% less missing playback in
  this scenario, not a guarantee for other gap distributions.
- Repeated 280 ms delivery gaps settled after the initial unpredictable gap;
  24/44.1/48 kHz checks and a ten-minute voiced simulation had no later underruns.
- Normal tone/noise/silence remained sample-exact. Growth tests retained the
  200 Hz pitch, RMS level and bounded join discontinuity at all three rates.
- Chromium: 27/28 first-pass profile gates passed. The ten-second missing-carrier
  case lost 2/38 adviser markers while local Go/simulation work overlapped;
  this does not establish the cause. That case, 48k fallback and Wi-Fi jitter
  passed isolated repeats with unchanged gates. 160/220/280 ms recovery profiles
  passed, alongside the reconnect, mute and constrained WebRTC checks. PCM links
  below the payload budget and very low Opus budgets honestly report degradation.

Detailed summaries: `benchmarks/softphone/adaptive-playback-verification-2026-10-08.json`
and `benchmarks/softphone/adaptive-network-verification-2026-10-08.json`.
The first run is retained with its failure and the isolated repeats separately.

Final isolated Go suite: 744 passing checks/subtests, two optional live tests skipped.
Frontend/audio suite: 195 passing tests. Focused race tests, vet, build and both
typechecks passed. Two pre-existing Go wall-clock pacer checks failed during a
concurrent load run, then passed in the complete isolated suite; their thresholds
were retained. Renderer timing fields measure elapsed VM callbacks, including
scheduling effects, and are not a production CPU guarantee.
