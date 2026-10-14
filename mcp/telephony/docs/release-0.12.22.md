# Telephony 0.12.22

## Audio delivery fixes

- Correct SIP clock estimation under sustained arrival variation and clock drift,
  including recovery of accumulated timing offset. Retain the existing queue and
  stale-audio limits; delayed packets still expire.
- Prepare the authenticated human media bridge before answering SIP calls,
  including after IVR, to avoid unnecessarily aging initial microphone speech.
- Pace decoded WebRTC microphone bursts and carry original source timing through
  carrier output. Flush fresh partial speech tails with their correct deadlines.
- Add bounded socket and processing evidence to distinguish kernel/Pion delays
  from carrier timing without database or socket work in the audio observation.

## Efficiency improvements

- Reuse owned encoder/resampler scratch buffers while preserving independent
  ownership of queued PCM and RTP packets. Use a fixed native Opus binding and
  retain the original binding for regression comparisons.
- Share permission-scoped call-list and notification reads. Keep cold notification
  projections light; fingerprints include authenticated control grants.
- Fetch persisted carrier audio history incrementally with scoped cursors,
  retention-gap signaling and bounded browser history merging.
- Offer lightweight dashboard responses and briefly reuse revision-checked totals.
- Reuse parsed access policies while retaining revision and revocation checks.
- Suppress no-op cache invalidations, use keyset historical call pagination and
  coalesce unchanged notification reconnect hints.
- Defer empty background queues using durable work epochs and a bounded fallback;
  active jobs retain one-second enforcement. Adds migration 055.

## Optional native 24 kHz Opus input

The installation setting `softphone_webrtc_encoder_input_rate` defaults to
`48000`. Selecting `24000` removes application-side upsampling for native Opus
encoders on new call, listening and coaching sessions. Portable fallback remains
48 kHz and the WebRTC RTP clock remains 48 kHz at either input rate. Diagnostics
report the actual encoder rate. Existing sessions retain their encoder.

## Validation and measured results

All eight validation jobs passed on runtime commit
`8baf969436c45e592e87bfb82760d895f897df19`: full application tests, two race jobs,
mixed SIP load and native Opus on Linux/macOS amd64/arm64, including Linux musl.
The full CI application suite passed 1,467 tests/subtests. Local validation also
passed 296 frontend tests and eight compiled-sidecar Chromium scenarios. The
30/60/120-minute inbound/outbound recording matrix covers import integrity,
playback variants, seeking, private access and temporary-file cleanup.

The local Linux simulation passed 32 and 64 bidirectional UDP/SRTP streams with
portable Opus, native 48 kHz/FEC and native 24 kHz/FEC, alongside recording,
database and notification work, with zero unexpected discards, replacement audio
or recording loss in all six variants. It excludes live carrier signaling,
external proxy/network, browser rendering and competing production workloads.

Representative local measurements:

| Operation | Before | After |
| --- | ---: | ---: |
| Default conversion + encoding allocation | 14,350 bytes/frame | 49 bytes/frame |
| Unchanged notification fingerprint CPU | 0.0688 ms | 0.00738 ms |
| Unchanged 256-record audio history CPU | 4.062 ms | 0.0538 ms |
| Changing call-list CPU | 9.900 ms | 8.050 ms |

These measure individual operations, not whole-server CPU savings. No-op trigger
guards add approximately 10 microseconds per changing call-state write. Native
binding output matches the original binding at both rates with FEC on/off, but
subjective voice quality across every device is not established by these tests.

Both release manifests and bundled frontend identify 0.12.22, and the installer
source is `telephony/v0.12.22`. Publishing does not install, restart or modify
staging or production. External network loss and shared host contention remain
deployment concerns.
