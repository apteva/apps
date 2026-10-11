# Internal audio transport benchmark

Run locally from Telephony:

```sh
bun run benchmark:internal-audio \
  --server /path/to/server --core /path/to/core \
  --calls 24 --seconds 6 --output /new/local/report-directory
```

The output directory must not exist. Previous results are never overwritten.
Use `--profile core_input_pause_3000ms,core_write_pause_400ms` to repeat selected
cases without rerunning unrelated profiles.

Default output is a new directory in the system temporary directory. Requires
Bun, Go, rg, and local Server/Core source repositories with their dependencies.
Allow several minutes on the first run to download toolchains and build fixtures.

## What is exercised

- Human baseline: production Telephony `/peer` and `/softphone/media` handlers.
- Human proxy comparison: those handlers plus Apteva's observed generic app
  WebSocket reverse proxy.
- AI baseline: Telephony's production queued WebSocket writer and Core's real
  `realtimeAudioHandler`.
- AI proxy comparison: the same components through Apteva's real
  `handleRealtimeAudioProxy`.

Server and Core fixtures are injected as test files using Go overlays. Their
repositories are not edited. They use temporary state and loopback listeners.
Core uses its production bridge handler and 64-frame channel capacities, with a
synthetic provider replacing an external AI service. Temporary test tokens are
used at runtime; reports do not include them. There are no real carrier calls,
CRM records, paid AI requests, staging changes, or production changes.

This is a transport/component test. It does not exercise the entire Telephony AI
call controller, real carrier, browser audio processing, browser WebRTC, TLS,
production authentication/middleware, external proxies, or actual network paths.
It cannot establish production capacity or identify a past incident's cause.

## Profiles and gates

| Profile | Streams | Injection |
| --- | --- | --- |
| `human_loopback` | `--calls` | None |
| `human_server_proxy` | `--calls` | None |
| `ai_direct_core` | `--calls` | None |
| `ai_server_proxy` | `--calls` | None |
| `human_browser_pause_400ms`, `human_browser_pause_3000ms` | 2 | One simulated browser stops reading |
| `core_input_pause_400ms`, `core_input_pause_3000ms` | 2 | One synthetic Core provider stops consuming input |
| `core_write_pause_400ms`, `core_write_pause_3000ms` | 2 | One Core socket write is paused |
| `core_transport_reset` | 2 | One Core socket is closed, followed by test-driven attachment renewal |

Synthetic PCM is 24 kHz, mono, 16-bit: 960 bytes every 20 ms in both directions.
Markers encode direction, stream identity, sequence, emission time, and a
sample pattern. Corruption, misrouting, duplicates, order faults, missing frames,
latency, late frames, and final-second delivery are checked. Two small integrity
and accounting tests can run independently:

```sh
GOWORK=off go test -tags integration -run '^TestInternalAudioProbe' -count=1 .
```

Healthy gates require zero missing/corrupt/duplicate/out-of-order frames,
p95 receipt age <=150 ms, and maximum <=500 ms. These are local test acceptance
limits, not production service guarantees. Fault profiles require integrity,
observable injected delays, final-second delivery recovery, and a healthy
companion with no missing frames and no delay over 500 ms. Expected fault losses
and latency are reported explicitly. Passing a fault test means it exposed the
fault and checked isolation/recovery, not that stalled audio remained healthy.

The three-second input pause and reset profiles use an explicit fixture token
renewal and attachment. This verifies that the transport can be reattached;
it does **not** test Telephony's automatic AI recovery policy or carrier restart.

## Evidence and accounting

`results.json` contains per-direction summaries and individual receipt timestamps,
Telephony writer counters, human bridge diagnostics, sanitized closure evidence,
Core input/output queue high-water marks, proxy connection events, and helper
process CPU totals. `host-load.json` samples host CPU, load, and free RAM each
second. `source.json` records revisions, dirty state, hashes of Go sources and
modules, fixture hashes, platform and toolchains. Sources are checked again at
completion; concurrent source edits make the run fail.

Core output admission in the fake provider is nonblocking: when its 64-frame
channel fills, `fixture_producer_drops` counts frames the fixture did not admit.
Those are included in missing totals but must be distinguished from frames lost
by production transport. Telephony writer counters separately identify queue
trimming, expiration, failed writes, and timeouts. Counters from multiple stages
may describe the same loss and must not be added blindly.

Producers use absolute 20 ms schedules. After host scheduling stalls they catch
up instead of extending the test unnoticed. `sender_scheduling_max_ms` and
`core_producer_scheduling_max_ms` record the scheduling delay separately. Receipt
age starts at actual emission; it excludes time before the producer ran. A
catch-up burst can legitimately overflow a bounded transport queue, and the
result must remain a failure for healthy profiles.

For the simulated browser pause, receipt age is measured before the browser's
normal stale-frame protection/DSP. Old frames arriving at this probe do not prove
that a user would hear old audio; the browser would normally reject stale frames.

## Interpreting failures

Compare direct and proxy baselines first. Delay already present on the direct
Core path points to Core/provider/host scheduling, while extra proxy-only delay
warrants inspection of the Server/proxy boundary. A browser read pause tests
buffering outside Telephony's own queue. Check receipt times, writer counters,
proxy close events, scheduling maxima and host load together.

A slow or overloaded test host must not be used to claim that the implementation
is loss-free. Retain the failed report and repeat after resolving a concrete
harness defect or host condition. Do not widen healthy gates to hide loss.

See [local verification](VERIFICATION.md) for measured outcomes and limitations.
