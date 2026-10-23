# Telephony 0.12.32

## Continuous SIP playback across short source-time gaps

The adaptive SIP receiver preserves short, contiguous-sequence RTP timestamp
gaps as separately tracked silence. Empty-queue replacement audio advances the
playback timestamp without inventing a received/lost packet sequence. Fresh
in-order speech can resume without unnecessary reserve re-priming.

G.722 decoder state is preserved. Real packet loss still uses concealment;
large timestamp discontinuities and genuinely stale packets retain bounded
recovery. The 160 ms default capacity and 200 ms age limit are unchanged.
The reference receiver remains available through the existing audio profiles.

`/audio-health` now separates declared, filled and skipped source timestamp
gaps from packet-loss concealment and discarded speech. A timestamp gap alone
does not prove intentional silence suppression.

## Verification

The new repeatable natural-speech bench covers G.722 and PCMA, 10/20/40 ms
timestamp jumps, repeated short transitions, modest jitter, a 60 ms arrival gap,
sequence/timestamp rollover, real loss, a 500 ms stall and a 2-second source-time
discontinuity. Healthy decoded PCM remains identical. Existing latency/age
ceilings and first-word/continuity checks remain enforced. CI runs the bench,
archives its measurements and includes the regressions in race checks.

The constructed report-shaped comparison changes the longest PCM output interval
from 60 ms to 20 ms and removes the second playback start. It preserves all
measured speech and fills the declared 60 ms source gap separately from loss.
This reproduces the reported pattern, not an exact packet replay: the original
call did not retain per-packet timing. Earlier browser startup concealment is
separate and is not addressed by this SIP correction.

Full verification and limits: [local results](sip-short-source-gaps-verification.md).

Both manifests, installer refs and frontend metadata use 0.12.32. SDK remains
v0.104.0, verified against published tag ancestry. Publishing this version does
not install, restart, configure or access staging or production.
