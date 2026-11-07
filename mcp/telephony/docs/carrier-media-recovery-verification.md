# Local carrier recovery verification — 9 October 2026

Scope: Telephony only, based on commit `902c99d8` (0.11.4). Published SDK
`v0.99.0`; `GOWORK=off`. No staging/production installations, configuration,
carrier accounts or real calls were changed. This is an unreleased local change.

## Passing checks

- Full Go suite: **839 passing tests/subtests**, zero failures. Three existing
  opt-in skips: query-performance measurement and two live Twilio tests.
- Additional connecting-budget rehydration and terminal-runtime cleanup tests,
  added after the full run: pass with the recovery suite under the race detector.
- Focused race suite covering recovery, actual Telnyx/JSON/Twilio bridges and
  browser/peer reconnection: passes. Subsequent recovery-only race rerun passes.
- Canonical frontend suite: **223 passed**, zero failures.
- TypeScript typecheck, Go vet and Go build: pass. Rebuilt the headless client
  and verified asset checks. PCM worker/worklet and their hashes are unchanged.
- Existing carrier continuity checks preserve sample amplitude, waveform
  continuity, duplex delivery, playback cadence, interruption and listening.
- Recovery established-frame check: **2.26–2.76 ns/frame, zero allocations**
  across three microbenchmark runs on Apple M1 Pro. This measures only the new
  ownership-confirmation fast path, not total transcoding or browser CPU.
- A held failure-reporting mutex does not block 10,000 established frame checks.

The full suite also exposed an existing embedded/disk manifest mismatch
(`0.11.3` versus `0.11.4`). The embedded manifest now matches the current disk
version. This task does not publish a new release.

## Audio performance is not fully green

The actual Chromium/compiled sidecar/local Telnyx substitute benchmark ran 12
profiles, including limited bandwidth, jitter, carrier catch-up/missing frames,
mute, outages and browser/WebRTC reconnection. Eight profiles passed their gates;
PCM baseline, broadband, carrier catch-up and WebRTC baseline failed.

A fresh isolated rerun after compilation/race work retained three failures.
Broadband passed. A control snapshot of the previous release (`902c99d8`, without
these changes) then failed all four comparison profiles:

| Profile | Changed upstream/downstream p95 | Prior release upstream/downstream p95 |
|---|---:|---:|
| PCM baseline | 446 / 241 ms | 463 / 298 ms |
| Broadband | 279 / 301 ms | 423 / 258 ms |
| Ten-second carrier catch-up | 463 / 440 ms | 481 / 451 ms |
| WebRTC baseline | 539 / 490 ms | 312 / 376 ms |

Both versions experienced measured render-clock lag. Host samples across the
three runs averaged approximately 64%, 49% and 53% CPU busy, with peaks of 100%
and one-minute load peaks of 82, 56 and 80 on ten logical CPUs. These observations
support host/render scheduling as a contributor but do not establish the full
cause or prove absence of every regression. The benchmark failures remain
failures; thresholds, latency limits and stale-frame rejection were not relaxed.

Retained reports:

- [Initial 12-profile run](verification/carrier-recovery-network-initial.md)
- [Fresh four-profile run](verification/carrier-recovery-network-fresh.md)
- [Prior-release control](verification/carrier-recovery-network-control.md)

Raw browser clocks, timings, diagnostics and host samples remain in the local
`/private/tmp/telephony-carrier-recovery-benchmark*` output directories. Missing
markers are not a measured percentage of lost speech or a MOS score. Expected
loss during injected outages/mute is distinct from unexplained baseline loss.

## What is demonstrated

Tests demonstrate prompt failed-socket closure, one active generation, retained
call/adviser/browser identity, valid-media recovery confirmation, bounded
same-leg Telnyx restart commands, stale callback/cleanup isolation and immediate
caller-cancellation handling. They do not demonstrate the operational behavior
of a live Telnyx edge, identify the source of the production disconnections, or
prove uniformly acceptable audio performance on this loaded host.

A controlled real carrier recovery check and renewed network-quality comparison
on a host with stable audio rendering remain verification work. No live test or
deployment was performed.
