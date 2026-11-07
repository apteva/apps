# Startup audio and cleanup diagnostics

Local inspection of Lisa's 10 October report found three independent paths.
No staging or production access, calls, configuration changes or installation
was performed.

## Caller to adviser

The 100 ms queued plus 18 ms partial WebRTC expiration pattern is consistent
with the retained-sample deadline defect fixed in commit `9dd3be6c` on this
branch. That reproduction establishes the code defect, not exact attribution
for Lisa's call. A browser reporting no RTP packet loss does not exclude PCM
being discarded by Telephony before RTP transmission.

## Adviser to caller

The existing `stale_live_audio` pacer event is a **queue-depth trim**, not proof
that samples have exceeded the wall-clock age limit. A local reproduction
queued ten fresh 20 ms packets and discarded 100 ms before any carrier write.
The configured local ceiling is 180 ms, with a 110 ms trim target rounded down
to whole packets. The pacer was checking that ceiling before filling its
existing carrier send-ahead allowance (40 ms by default).

The JSON carrier and legacy Twilio pacers now fill their existing lead first
only for a near-limit batch that fits within the local ceiling plus that lead.
A 200 ms startup batch therefore plays all ten frames. Larger bursts still
trim first; the 250 ms residence-time expiration remains authoritative before
every human media write. No queue ceiling, codec, sample rate, audio resampler,
recording behavior, AI policy or socket write deadline was increased or changed.
A single enqueue fills its lead once, avoiding an additional send-ahead burst.

Queue-depth events retain the existing reason and gain `trigger: queue_depth`
and `queue_residence_ms` (age of the first discarded packet at this pacer).
This is local residence time, not microphone capture age. Counters and bounded
events remain in memory on the media path. This change cannot establish why
Lisa's microphone frames arrived in a batch: browser/RTP jitter buffering,
scheduling, activation and network delivery remain possible.

## Cleanup

An explicitly normal local shutdown (1000 or 1001) whose close-frame write
fails now records `local_close_write_error` or `local_close_write_timeout`.
`cleanup_write_errors` and `cleanup_write_timeouts` are separate counters and
survive diagnostic merges. Ordinary write error/timeout counters describe live
or unexpected writes, and no longer include these expected cleanup failures.

Data writes failing during shutdown and protocol/error close frames retain
error classification. Socket closure, deadlines, forced cleanup, media recovery
and carrier termination behavior are unchanged. Existing historical counters
are not rewritten.

## Regression verification

- Red-before/green-after: 200 ms fresh startup batch on Telnyx JSON and Twilio.
- Startup delivery at all existing 20/40/60/80 ms lead settings; exact Telnyx
  L16 PCM sample order and values preserved.
- Old startup frames expire while fresh audio remains playable.
- Existing 400 ms overflow tests retain bounded trimming for both pacers.
- Normal close errors/timeouts retain events without live-error counters.
- Protocol close errors and data failures during cleanup remain errors.
- Cleanup diagnostic merges preserve counters and bounded events.
- Retained-sample WebRTC deadline regressions remain covered.

Evidence lives in `/private/tmp/telephony-startup-*.log` and
`/private/tmp/telephony-startup-go.jsonl`; these are local artifacts, not carrier
traces or production measurements.

Final local verification: 977 Go tests/subtests passed, zero failures; three
opt-in tests skipped (`TestQueryEfficiencyPerformance`, `TestTwilioLiveAccount`,
`TestTwilioLiveOutboundCall`). Five repeated focused race runs passed, including
both startup pacers, cleanup, stale-audio bounds and the WebRTC framer. Go vet,
Go build and whitespace checks passed. The complete suite includes local
WebRTC/SRTP/Opus transport and constrained UDP network profiles. No real carrier
call or production attribution is claimed.
