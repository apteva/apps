# Telephony 0.11.2

Cumulative Telephony release retaining all 0.11.1 functionality.

## Changes

- Require a real minimum reserve for continuous PCM playback at startup and
  rebuffering. Prefer the adaptive target; after a bounded 120–300 ms wait,
  permit a minimum-reserve start. An isolated short segment is drained as an
  explicit short-tail event without inventing an initial conversation underrun.
  Rebuffering after a real live underrun continues to report missing duration.
  After waiting for the target, allow at most one render block of rounding
  tolerance (and never below 40 ms) so resampler startup residue does not require
  another whole carrier packet. Record this separately as `render_quantum_ready`.
- Record timestamped startup/rebuffer, target changes and sampled successful
  reserve adjustments, including queue, target, wait, phase and adjustment size.
  Preserve the waveform-matching checks and 320 ms source-age/backlog cap.
- Record timestamped outgoing queue buildup/drain and sampled backpressure
  observations, with queue bytes, separate frame-age estimates, dispatch-delay
  lower bounds and clock uncertainty. Keep the 120 ms outgoing backpressure
  threshold and 250 ms capture-age protection. No additional microphone queue,
  bandwidth probe or carrier operation is introduced.
- Correlate unambiguous one-frame browser losses with server sequence gaps by
  connection ID and sequence, retaining both observations without adding their
  durations. Ambiguous multi-frame gaps remain unattributed.
- Send intentional browser shutdown with code 1000 and bounded close handshake,
  record intent per authenticated socket, and prevent reconnect after shutdown.
  Expected legacy 1005 at recorded call completion is not a browser error;
  earlier/live unexpected 1005, failed closes and permission errors remain visible.
- Preserve bounded observational histories through reconnect and persistence;
  send observation deltas instead of repeatedly transmitting entire histories.
  Saved call details show recent buffer observations. Shared/headless clients and
  built-in panels use the same implementation; carrier routing is unchanged.

## Verification

Release verification includes the complete Go and frontend suites, focused race
checks, vet, typechecks, Linux release build, real local Chromium headless/panel
checks, all existing network benchmark profiles and startup/adaptive worklet
replays. Final counts and network results are recorded in the accompanying
verification report.

Final isolated verification: 749 Go checks/subtests and 212 frontend/audio tests
pass; two opt-in live-carrier tests skip. All five local Chromium calling
surfaces pass. The final 28-profile network matrix passes 25 gates; isolated
repeats resolve broadband and degraded reconnect, while baseline and seven-second
browser recovery still fail timing gates. Earlier failures and unchanged 0.11.1
controls are retained. Independent source-clock lag and busy-host snapshots make
local scheduling interference plausible, but do not prove it or certify a lack
of production audio issues. **Network performance is not consistently all green.**
See [full verification](audio-startup-verification-0.11.2.md) for directional
measurements, processing cost, remaining failures and source fingerprints.

An old short-clip harness froze AudioContext time; it now advances render time
like the browser. Early concurrent test runs hit the default simulation runtime
timeout; isolated checks retain the existing latency, loss and waveform gates.

These tests cannot reconstruct Long's unavailable packet history or guarantee
uninterrupted calls on every network. Queue bytes include framing/control
overhead; frame age is estimated from the audio-clock mapping, and dispatch delay
is a lower bound rather than proof of a Worker scheduling fault. Changes preserve
sample format, microphone DSP, waveform matching, WebRTC native media, routing,
permissions and carrier call control. Publishing does not upgrade staging or
production, change bindings or place real carrier calls.
