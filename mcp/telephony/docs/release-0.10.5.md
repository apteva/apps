# Telephony 0.10.5

Cumulative release retaining all Telephony 0.10.4 functionality and earlier
routing, providers/compliance, inbound protection, multi-carrier outbound,
bounded AI handoff, call duration, listening, private coaching, browser media
recovery and the filterable SSE Audio health widget.

- Add generic browser connection/network observations: authenticated adviser,
  connection IDs, attachment/replacement/reconnection/disconnection events,
  trusted client IP and configurable known VPN exit IPs/CIDRs. Classification
  is `known_vpn_exit` or `unknown`; unknown does not establish absence of a VPN.
  Collection is bounded and does not wait for persistence in the audio path.
  Raw IP readback is operator-only and scoped to the project and call.
- Preserve timestamped Worker playback rejection events when Worklet snapshots
  arrive. Merge their bounded histories without duplicate snapshots; neither
  source may erase the other's evidence. Incoming stale-audio limits remain
  unchanged.
- Record microphone mute/unmute transitions and exact, coalesced intentionally
  omitted capture sequence ranges. Exclude only those ranges from unexpected
  microphone-loss counters; retain genuine losses before/after/between them.
  Informational muted metrics do not become dropped-audio issues. The shared
  headless client and Calls panel use the same implementation.
- Extend local network benchmarks with seven-second delayed delivery, main
  thread pauses, 48 kHz capture and exact Worker processing measurements.
- Pin the app SDK to v0.96.0, the latest release by commit ancestry at release.

## Verification and limits

687 Go tests/subtests and 162 frontend/audio tests passed; two opt-in live-carrier
tests were skipped. Focused race checks, benchmark gate tests, Go vet,
frontend/benchmark typechecks and Telephony-only panel/headless builds passed.
Local Chromium baseline, Wi-Fi jitter, mute and reconnect profiles passed.
The SDK v0.96.0 mute rerun recorded about two seconds of intentional omissions,
zero false capture gaps, zero Worker capture drops and no incoming playback loss.

Controlled delayed browser delivery reproduces the reported multi-second audio
discard mechanism and recovers without accumulating stale speech. These tests
do not establish the production cause or measure available network bandwidth.
Individual playback underruns and browser sequence gaps remain primarily
cumulative counters; richer per-burst clock evidence is not part of this release.
See [investigation and benchmarks](audio-delay-investigation-2026-10-07.md) and
[telemetry behavior](audio-health-telemetry.md).

The mute fix requires the new shared client/Worker as well as the server;
historical counters are not rewritten. Authorization, DSP, media gates,
carrier commands, reconnection behavior and latency caps are unchanged.

Publishing does not install or activate staging or production. No instance
configuration, carrier settings, routes, numbers or live calls are changed.
