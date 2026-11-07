# Local internal audio verification — 11 October 2026

**Outcome:** all four healthy transport profiles passed with 24 simultaneous
bidirectional streams each: **57,600 frames, zero missing/corrupt/duplicate/
out-of-order frames**. Fault injection confirmed that downstream buffering can
still deliver seconds-old audio. The complete run had two isolation failures
under host saturation; both targeted reruns passed. These failures are retained.

## Healthy profiles

Each profile transmitted 300 frames per stream in each direction over six seconds.
The table reports the worst individual stream/direction p95, not a pooled p95.

| Actual components | Worst stream p95 | Maximum receipt age | Missing frames |
| --- | ---: | ---: | ---: |
| Telephony human loopback | 2.90 ms | 15.44 ms | 0 |
| Human loopback through Apteva app proxy | 4.60 ms | 23.51 ms | 0 |
| Telephony writer → Core handler, direct | 3.31 ms | 23.61 ms | 0 |
| Telephony writer → Apteva realtime proxy → Core handler | 2.60 ms | 4.61 ms | 0 |

These sequential profiles ran under changing host conditions. Their timing
variation is not evidence that the proxy improves performance.

## Confirmed adverse paths

1. **Simulated browser receiver pause:** a three-second read pause delivered
   frames up to **2,980 ms old** when reads resumed, despite Telephony's own
   bounded queues. This measures socket receipt before normal browser playback
   rejection. It does not mean the browser would play all that old audio.
2. **Core input consumer pause:** a three-second pause left 64 input frames over
   250 ms old and a maximum **2,980 ms** receipt age. Core disconnected after
   its input channel filled. Test-driven token renewal/reattachment restored
   delivery. In the targeted run, 111 input frames were missing during the
   outage; 41 output frames were rejected by the synthetic provider's full
   output channel. The healthy companion had no loss.
3. **Core output socket pause:** a three-second pause delivered frames up to
   **3,000 ms old**, with 65 frames over 250 ms and 86 synthetic-provider
   output admission drops in the full run. The healthy companion remained
   loss-free. A 400 ms pause in the targeted run delivered eight late frames,
   peaking at **401 ms**, without frame loss.
4. **Abrupt Core socket closure:** the fresh attachment resumed final-second
   delivery in both directions. This tests the transport and explicit fixture
   renewal, not Telephony's automatic AI recovery policy.

Production source inspection confirms 15-second write deadlines in the Server
realtime proxy and Core audio handler, and 64-frame Core input/output channels.
Telephony's local queue limits therefore do **not** enforce an end-to-end age
budget. The test proves this weakness under injected stalls; it cannot attribute
any historical production incident to it.

## Failed runs and host conditions

The first run used a ticker-driven synthetic output producer. Host scheduling
pauses dropped ticker events and stretched emission past the fixed measurement
window, invalidating its final-second recovery check. The fixture now uses
absolute schedules and reports scheduling delays explicitly. The original
failure report remains available.

The corrected complete run passed **9 of 11 profiles**. Two healthy companions
lost frames through Telephony's bounded writer:

- `core_input_pause_3000ms`: five missing 20 ms frames, exactly matching
  4,800 overflow bytes. Host CPU sampled **99.4% busy** around the loss;
  sender scheduling delay reached **122 ms**.
- `core_write_pause_400ms`: 56 missing 20 ms frames, exactly matching
  53,760 overflow bytes. Host CPU sampled **99.6–100% busy** around the
  loss; sender scheduling delay reached **1,230 ms**. Both streams showed
  source scheduling disruption, beyond the injected write pause.

These observations support host scheduling pressure as a contributor to those
local failures. They do not identify which process caused the pressure. Absolute
schedule catch-up generates a burst after a late producer wakeup, which can
legitimately trigger queue trimming. The healthy gate still fails in that case.

A targeted repeat of those two profiles passed both isolation/recovery gates
without widening limits: **zero companion loss**, with maximum companion delays
of 5.17 ms and 49.49 ms. Expected fault losses/delays remained visible. The full
run is not relabelled as passing.

## Evidence

- Compact saved summary: [verification.json](verification.json).
- Complete corrected run: `/private/tmp/telephony-internal-audio-20261011-final/`.
- Targeted rerun: `/private/tmp/telephony-internal-audio-20261011-targeted/`.
- Original failed run: `/private/tmp/telephony-internal-audio-20261011/`.

Each report directory includes results, source hashes/revisions, host-load
samples, fixture logs and build logs. No production runtime source was changed.
Server/Core repositories were used read-only through test overlays. No live
instances were installed, restarted or contacted.

## Additional validation

The two marker-integrity/accounting tests and existing concurrent bidirectional
PCM, blocked-peer isolation, and unsent-timeout recovery tests passed under
`go test -race` (five selected tests). The TypeScript runner typecheck and
`git diff --check` passed. This was targeted validation of test tooling and the
writer; the entire unrelated app suite was not rerun for this test-only change.

## Recommended follow-up

Give realtime frames a consistent source age/deadline across Core and Server;
discard expired audio before writing and use bounded recovery on stalled sockets.
Preserve separate counters for provider admission, internal queue discard,
transport failure and browser rejection. Reuse this benchmark to check those
changes. That work spans Server/Core and is not implemented by this test-only
Telephony change.
