# Softphone audio health and connection telemetry

Local changes based on Telephony 0.10.1. Applies to supported carrier bridges
with human browser audio, the shared headless client and bundled Calls panel.
No carrier-specific routing, DSP/resampling, latency budgets, call classification,
answer permissions or carrier-call termination behavior is changed.

## What is recorded

- `browser_audio_diagnostics.server.browser_socket`: accepted connections,
  reconnects, disconnects, maximum RTT/buffered bytes, cumulative browser counters
  and the latest 64 connection events. Server-side detach events survive a client
  that cannot send its final report. Client runtime counters use a random epoch
  to avoid recounting the same Worker after reattachment; reconnect attempts and
  successful upgrades are separate. Browser-tab/session replacement is also
  counted as a new connection, not assumed to be a network failure.
- Socket peer address uses a process-scoped HMAC hash, without IP or port storage.
  `peer_hash_epoch` must match before hashes are compared. The process key is not
  exported; restart changes hashes. `address_source` distinguishes socket peer,
  trusted forwarded peer and unavailable. By default an app behind a proxy hashes
  the proxy. Optional `audio_telemetry_trusted_proxy_cidrs` permits a strictly
  parsed X-Forwarded-For chain from trusted immediate peers only, walked right to
  left up to the first untrusted hop. Client-provided prefixes are not trusted.
- `timing.transport`: Worker-measured application ping RTT (latest/max and latest
  32 timestamped samples), maximum WebSocket `bufferedAmount`, reconnect attempts/
  successes, worker scheduling-gap count and maximum tick gap. The ping crosses
  the real socket and server reader/writer, so it includes server/transport queue
  work; it is not a carrier RTT or an absolute one-way speech delay.
- `timing.runtime` and `session_events`: AudioContext suspension/interruption/
  resumption and duration; main-thread scheduling gaps. Main and Worker observers
  compare a nominal one-second timer and report delays of at least 250 ms. These
  are scheduling observations: background throttling is not proof of a stalled
  transport. Measurements are rounded to milliseconds at integer JSON boundaries.
- Existing per-stage queue/write residence, source timestamps, sequence gaps and
  directional discard counters remain. Browser totals survive reattachment.
  Worklet and transport sequence gaps are separate and must not be added together
  as if they were disjoint loss. Socket write completion is not browser playback
  acknowledgment, and queue/drop metrics are not an intelligibility/MOS score.

Reports are coalesced in memory and persisted by the watcher (pending reports at
most once per second, server-only snapshots every five seconds), plus detach.
Database writes and event publication are outside the media frame read path.
Samples are bounded; cumulative totals survive sample eviction. A process crash
can lose telemetry since the latest successful database write. Temporary write
failures retry on the next watcher tick and never terminate the carrier call.

## Separate pipeline stages

`server.audio_health.stages` exposes these independent stages:

| Stage | Evidence |
|---|---|
| `carrier_to_telephony` | Carrier-source age/discards, continuous-stream gaps over the 320 ms source budget, and current reception stalls |
| `telephony_to_browser` | Server browser queue expiration/overflow and browser transport/worklet late-frame discards |
| `browser_to_telephony` | Worker capture-age/backpressure discards and server capture transit-age rejection |

`server.stages`, `server.carrier_reception`, `server.to_browser`,
`server.capture_*`, `timing.transport`, `timing.playback`, and directional drop
samples supply the detailed measurements. A stage identifies the observation
boundary, not which carrier, network or device caused it. Clock/source-age
measurements retain their existing uncertainty and excess-delay limitations.

Health is sampled once a second. Lifetime maxima never keep a recovered call in
an incident. A new counter increment or current carrier stall sets the recoverable
`audio_degraded` reason; after ten seconds without fresh impairment the stage
recovers. Mute/device mute, hold, disconnected sockets and suspended contexts
baseline inactive stages rather than treating them as new delivery incidents.
Carrier gap detection requires a continuous stream contract, not speech amplitude;
providers that omit silence frames do not get false silence-gap incidents.

`audio_degraded` is **not** a call status, hangup cause, missed-call classification
or automatic reconnection command. Existing stale-frame rejection remains enabled
(250 ms server/capture age and 320 ms source/playback budgets). Audio that already
arrived too late cannot be reconstructed by these diagnostics.

## Shared backbone API

```ts
const phone = client.createSoftphone({
  onSessionEvent: event => recordSessionEvent(event),
  onDiagnostics: diagnostics => renderDiagnostics(diagnostics),
  onAudioHealth: health => renderAudioHealth(health),
});
```

The server sends `audio.health` updates on the existing media socket. The Calls
panel consumes shared backbone notices; headless hosts can use the callback and
`diagnostics.audioHealth` without changing call state. Browser RTT is measured in
the Worker so a slow main thread does not inflate the sample.

A brief media WebSocket drop keeps the carrier leg alive. The existing Worker
retry budget/backoff and fresh `/softphone/attach` authorization recover the same
call, retain mute, flush stale queued audio and cannot revive an ended call or
bypass permission revocation. Exhausted recovery stops browser audio; it does not
issue a carrier hangup. Existing carrier duration/media watchdogs still apply.
Private coaching is stopped on adviser-session replacement and never retargeted.

## Correlated alerts

Defaults: three **distinct calls**, same project/provider/stage, with fresh
impairment in a rolling 30-second window; 120-second alert cooldown. Set
`audio_alert_min_calls` to 0 to disable or 2–256 to configure the threshold.
`audio_alert_cooldown_seconds` accepts 30–3600 seconds. Invalid inputs use defaults.
No project-wide historical call query is run: a bounded in-memory correlator
tracks at most 1,024 groups and 256 call IDs per group. Alert call counts are capped
at that limit, and payloads sample at most ten call IDs. Cooldown/recovery prune
expired observations; a scheduled five-second worker emits recovery even after
all affected browser sockets end. Correlation state resets on process restart.

Published project-scoped SDK events:

- `telephony.audio.degraded` / `telephony.audio.recovered`: per-call stage changes.
- `telephony.audio.alert` / `telephony.audio.alert_recovered`: correlated incidents.

Events and structured logs are the integration points for host alerts. They use
the existing SDK event delivery contract; this does not configure an external
email/Slack notification or a durable incident queue. Per-call health and socket
histories are persisted alongside the existing call diagnostics.
