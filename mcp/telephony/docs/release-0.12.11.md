# Telephony 0.12.11

Corrects a confirmed SIP/RTP timing regression shared by inbound and outbound
calls on all supported carriers, including Telnyx and Twilio.

- Arrival jitter no longer repeatedly restarts drift measurement. A bounded
  lower-envelope estimator observes in-order timing before admission, including
  rejected packets, and corrects frequency and phase separately.
- Genuine delivery stalls cannot reset the source-age baseline. Existing
  160 ms default capacity and 200 ms age limit remain; expired audio is rejected.
- Decoded partial reserves are reconciled with concurrent packet admission,
  retaining the shared capacity bound after interrupted frame assembly.
- Operator SIP snapshots and audio-health summaries expose estimator updates,
  rejected fits, observation-window count and signed phase adjustments.

The estimator uses fixed per-call storage and adds no packet allocation,
database operation, network request or logging to the RTP hot path. Healthy
ingress/queue processing measured 0.424 µs before versus 0.443 µs after, with
the same existing payload-copy allocation.

## Verification

- The exact report's 20-minute controlled clock/jitter input rejects 220 seconds
  with released ingress and zero with corrected ingress.
- 64 interleaved twenty-minute calls: worst cumulative discard with drift/jitter
  falls from 456.715 seconds to zero. Shared 80/400 ms processing pauses retain
  bounded actual overflow/expiry, with no continuing drops outside recovery.
- 24 thirty-minute/two-hour replays across PCMU, PCMA and G.722, both clock
  directions and jitter profiles: zero discarded or replacement audio.
- Fresh paced local UDP/SRTP + G.722 + resampling + Opus tests pass at 32 and
  64 streams for portable and native/FEC codecs, with zero discards/replacement
  and all expected incoming packets. At 64 native/FEC streams with four Go
  execution threads: 2.054 CPU cores, playback work p99 0.766 ms. Synthetic voiced
  audio; excludes signaling, database/routing, recording, proxy and browser
  rendering. Not a production capacity guarantee.
- Full local Go suite passed 1,277 tests/subtests, nine optional skips; the
  subsequent additive diagnostic-summary regression also passed, as did the
  final remaining-suite run. All 251 frontend tests, focused race checks, vet,
  typecheck and app/panel/headless builds passed.

Earlier overloaded-host load failures and one frontend wall-clock retry failure
are retained alongside passing reruns. Severe scheduling/network stalls can
still cause legitimate bounded audio loss. Production evidence supports this
fix, but does not prove that every cut or reported noise has the same cause.

Retains bundled libopus, optional FEC, long recording fixes and all previous
Telephony changes. Browser PCM/WebSocket code, codec tuning, SIP negotiation,
routing, recording and termination behavior are unchanged. SDK v0.100.0,
minimum Apteva 0.87.4 and exclusive activation remain.

Publishing does not install or restart an instance, change carrier settings or
make calls. Installation still uses exclusive process cutover and should be
scheduled without active calls. See `docs/sip-clock-recovery.md` for detailed
evidence, simulation scope and reproducible commands.
