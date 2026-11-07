# Telephony 0.12.28

## Preserve fresh trailing SIP audio

When SIP playback has stopped and its packet queue is empty, a decoded
remainder shorter than 20 ms is now emitted on the ordinary playback tick.
Its real samples are copied unchanged; only the unused frame suffix is padded
with silence. Previously this remainder could wait until its 200 ms age limit
and be discarded as `decoded_partial_age`.

The change uses the shared inbound/outbound SIP receive framer, independently
of carrier. It does not bypass priming when actual packets are queued, change
RTP sequence/timestamp tracking, replay expired audio, or release catch-up
bursts. Hold and bridge-generation checks still apply. Existing primed
playback concealment is unchanged.

## Diagnostics

`/audio-health` exposes `tail_padding_ms` and `tail_padded_frames` in the SIP
jitter snapshot, plus `sip_tail_padding_ms` and `sip_tail_padded_frames` in
dashboard metrics. The bounded `decoded_partial_padded` event reports the
padding duration and `preserved_audio_ms`. Fractional milliseconds are retained,
including decoded G.722 half-RTP-sample durations. Padding is separate from
discards and missing-packet concealment and does not trigger a degraded state.

No background worker, timer, database write or synchronous log was added to
the media path. A local Apple M1 Pro microbenchmark measured approximately
227–238 ns for a G.722 trailing flush, with one 640-byte output frame allocation;
empty/unprimed ticks measured 76 ns and zero allocations. These are framer
measurements, not a call-capacity claim.

## Verification

Regression tests first reproduced the defect, then verified the fix for PCMU,
PCMA and G.722, including 15.75/15.875 ms tails, exact sample preservation,
zero-only padding, real packet bursts, speech resumption, no replay, expiry,
queued-packet priming, hold and generation changes. Tests also verify fractional
telemetry/coalescing, dashboard classification and unchanged continuous PCM.
The new tests are included in the existing media race CI gate.

Both manifests, installer source refs and generated frontend metadata use
0.12.28. SDK remains v0.102.0, reverified by published tag ancestry.

This fixes a small trailing-frame loss mechanism. It does not resolve or
requalify the separate long-run interruption/load limits recorded in 0.12.27.
Publishing does not install or restart production/staging or enable TURN.
