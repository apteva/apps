# Telephony 0.12.24

## SIP recovery with ordinary arrival variation

Corrects the optional SIP playout resynchronization added in 0.12.23. Its
per-packet 3 ms variation gate could indefinitely reject a stable shifted stream
with alternating 12 ms jitter. The new gate permits up to 20 ms per-packet
variation while requiring source RTP time and physical arrival time to agree
within 3 ms across the complete observation window of at least one second.
The age variation, contiguous sequence, processing-residence and ten-second
cooldown safeguards remain. Fast and slowly draining backlogs cannot use this
path to renew stale speech. No I/O or allocations are added to admission.

In the deterministic stable 240 ms shift with alternating 12 ms jitter,
the exact released 0.12.23 source rejected 35 seconds of audio without recovery.
The corrected policy rejected 1.22 seconds, recovered once and resumed a clean
tail. This is a synthetic comparison, not an exact replay of a production call.

Resynchronization **still defaults off**, remains project/route scoped and uses
the same shared inbound/outbound SIP engine for every supported carrier.
Capacity, expiry, audio quality settings and active call policies are unchanged.
Trial it in staging before enabling it for production. Recovering a relative
RTP epoch does not prove upstream latency disappeared.

## Speech acceptance, not only queue/recovery safety

Adds 84 default-running human-speech scenarios using two attributed LibriSpeech
utterances, PCMU/PCMA/G.722 and resynchronization on/off. The checks verify the
complete healthy decoded utterance, including its final word; opening speech;
active waveform coverage; longest damaged-speech/playback intervals; and maximum
and p95 added delay. Small bounded clock adjustments are explicitly accounted
for. Natural pauses are excluded. Six intentionally broken controls must be
detected, including clipped opening speech, muted/missing/repeated output,
excessive delay and excessive sample-clock correction.

Normal speech is byte-identical to an independent clean codec decode, with no
interruption or added delay beyond the healthy scheduling baseline. Ordinary
jitter preserves all active speech with no measured interruption and up to
26.4 ms additional delay in these fixtures. An 80 ms delivery batch can still
produce a 40 ms playback interruption despite retaining every word. Severe
420 ms bursts, 440 ms startup stalls and stable 240 ms shifts are reported as
degraded even when queue safety and recovery pass. This release does not claim
cut-free speech under those conditions. Metrics are objective continuity
proxies, not MOS/PESQ or human intelligibility scores.

CI retains JSON metrics and WAV listening comparisons. Collection is test-only,
adds no processing to live calls and requires no Docker or speech downloads.

## Validation and packaging

Includes production-boundary scenario replays, actual 32/64-stream mixed
UDP/SRTP/codec/recording/database load gates, guarded real Chromium recovery
after 90/120-second HTTP outages, native Opus on Linux/macOS amd64/arm64 and
Linux musl, race checks and the 30/60/120-minute recording matrix.

Both manifests, source refs and generated browser assets identify 0.12.24.
Installer source is `telephony/v0.12.24`; SDK remains pinned to v0.102.0.
Publishing does not install, restart or change staging or production settings.
