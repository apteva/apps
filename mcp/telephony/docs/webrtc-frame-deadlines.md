# WebRTC PCM framing deadlines

Local correction based on public Telephony 0.11.6. No version publication,
staging/production operation or real carrier call is part of this verification.

## Confirmed defect

`sendSoftphoneRTP` retained one minimum expiration for the entire unfinished PCM
buffer. Emitting a 480-sample frame removed samples without removing their
expiration. A nonempty partial remainder could therefore keep the first chunk's
deadline through many subsequent chunks.

The production framing logic was extracted without changing its behavior, then
tested against an explicit clock: 18 ms initially, followed by 20 ms chunks.
The regression failed in both the queued-frame and retained-partial cases.
At 260 ms the frame emitted at 240 ms incorrectly expired at 250 ms, resulting
in a 20 ms `webrtc_playback_queue_age` discard. Its actual earliest constituent
deadline is 470 ms. The remaining fresh 18 ms partial also expired incorrectly.
The same tests pass with the corrected production framer.

This confirms a server WebRTC defect. It does not prove the precise share of
Léa's reported 98 ms startup loss caused by this defect. No production call
history or live carrier path was inspected or reproduced during this change.

## Correction

- Retain a sample count and expiration per pending source segment. Segments
  with the same consecutive expiration may share metadata.
- Assemble each 480-sample frame from its constituent segments and use only
  their minimum deadline. Consumed segment metadata is removed immediately.
- Retain at most 479 unframed samples between chunks. Large input chunks are
  split progressively; the staging sample capacity is 480 and metadata is
  bounded by pending sample count.
- Remove genuinely expired partial segments while retaining newer samples in
  their original order. Queue expiry and post-encoding expiry remain enforced.
- Use the existing six-frame/120 ms queue, oldest-frame overflow policy,
  250 ms local freshness budget, 320 ms source-age cap and loss reasons.

The framer operates on normalized carrier-to-adviser PCM in the server's
WebRTC path, independently of the carrier. The browser PCM/WebSocket playback
path, Partners UI, Opus configuration, resampler and RTP pacing are unchanged.

## Verification

`softphone_webrtc_framer_test.go` exercises the exact production framer and
queue selector using deterministic clocks:

- 18 ms startup offset with recurring 20 ms chunks, including both queue and
  partial expiration at the reported 260 ms boundary.
- Frames containing genuinely expired samples, regardless of whether the
  minimum deadline belongs to the first or last segment.
- Mixed fresh/expired partials with both prefix and suffix expiration.
- A playback scheduling pause that drops only expired queued frames.
- Fifty-frame bursts retaining the latest six frames, the 120 ms queue bound
  and correct 880 ms overflow accounting.
- Bit-identical PCM/sample order for chunk sizes from one sample to one second,
  including the 18 ms offset and exact expiration boundary.
- Individually timestamped one-sample segments with bounded metadata and safe
  compaction.

Ten repeated focused race runs pass. The complete Go suite passes **964 tests/
subtests with zero failures**, with three opt-in skips. Go vet/build and
whitespace checks pass. The existing Go suite includes actual WebRTC/SRTP/Opus
transport, constrained UDP network profiles, duplex hub media, hold,
replacement, cancellation, authorization, diagnostics and carrier regressions.

Local evidence:

- `/private/tmp/telephony-webrtc-framer-before.log`: reproduction failures.
- `/private/tmp/telephony-webrtc-framer-after.log`: corrected cases.
- `/private/tmp/telephony-webrtc-framer-race.log`: ten focused race repetitions.
- `/private/tmp/telephony-webrtc-framer-go.jsonl`: complete Go suite.
- `/private/tmp/telephony-webrtc-framer-build.log`: vet/build.
