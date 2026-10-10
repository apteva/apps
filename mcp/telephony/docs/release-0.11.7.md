# Telephony 0.11.7

Cumulative release preserving Telephony 0.11.6 and all seven subsequent local
Telephony commits. Publication does not install, restart or configure any
staging or production instance.

## Audio corrections

- WebRTC PCM framing tracks expiration by retained sample segment. Fresh
  partials and output frames no longer inherit consumed samples' deadlines.
  Truly expired audio is still discarded; queue and freshness limits remain.
- Telnyx JSON and legacy Twilio pacers fill their existing send-ahead allowance
  before trimming a small fresh startup batch that fits the combined bound.
  Larger bursts still trim, and residence-time expiration remains authoritative.
- Expected close-frame errors during normal local shutdown retain diagnostic
  events and separate cleanup counters without presenting a live media failure.

## Optional WebRTC voice recovery

- Per-softphone `audio.webrtcFec` preference and matching audio settings,
  retained through automatic recovery. Opting out selects the portable Go codec
  and requests no browser Opus redundancy. PCM/WebSocket remains the default.
- Optional system libopus voice codec with verified controls, bounded bitrate
  adaptation and short-loss concealment/FEC recovery. Missing libraries safely
  fall back to the existing portable codec. Runtime libopus is required for
  actual server FEC; negotiated SDP alone does not prove FEC is available.
- Bounded native browser jitter-buffer hints and timestamped concealment
  observations. WebRTC replacement audio is shown separately from PCM underruns;
  unsupported native measurements remain absent rather than fabricated zeros.
- Actual RTP send timing, sequence/error history, selected media path and native
  loss observations, collected without database writes in packet callbacks.

## Disconnect and recovery correlation

- Deduplicated structured disconnects preserve sanitized EOF/reset/timeout/
  protocol/local-close causes, connection IDs, UTC, close code, shutdown intent,
  last socket/audio activity and browser/WebRTC state before cleanup.
- Recovery chains and attempt IDs link authorization, reconnect outcomes,
  session generations, replacements and stale-attachment rejections. Headless
  APIs accept initiating actions such as manual reconnect or device change.
- Migration 048 adds optional diagnostic metadata to existing media sessions.
  Metadata is observational and does not participate in authorization. Malformed
  stored diagnostics cannot prevent credential refresh; lease watchdog queries
  remain compact. Request metadata is read before taking the media claim lock.
- Sparse browser observations use bounded paced queues; persistence uses the
  existing nonblocking background collector. Tokens, authorization headers,
  credential-bearing URLs, SDP and arbitrary error details are excluded.

## Verification

Before release preparation, the complete Go suite passed **1,025 tests/subtests**
with five opt-in skips. Frontend/audio suites passed **241 tests**. Focused race
checks, Go vet/build, TypeScript checks, headless/panel builds and whitespace
checks passed. The final corruption guard also passed focused race coverage.

The final 0.11.7 release gate, after merging current upstream changes and
bumping both manifests and frontend metadata, again passed **1,025 Go
tests/subtests (five opt-in skips)** and **241 frontend/audio tests**, with zero
failures. Go vet, TypeScript client/benchmark checks, deterministic Telephony
panel/headless rebuilds, native build and Linux amd64/arm64 builds with
`CGO_ENABLED=0` passed. All builds/tests use the published SDK with `GOWORK=off`.
Release evidence: `/private/tmp/telephony-0117-release-go.jsonl` and
`/private/tmp/telephony-0117-release-frontend.log`.

One existing Twilio cadence assertion failed once (47.9 ms against a 45 ms
threshold), then passed ten focused repetitions and the final complete suite.
Its in-memory writer does not exercise the new WebSocket observation path.
The failure is retained; these tests do not establish universal timing under
host scheduling pressure.

Socket activity tracking measured **48–55 ns/op, zero allocations** locally.
The codec audit measured approximately **1.0–1.4 CPU cores for fifty modeled
codec workloads**, excluding network/database work. Neither measurement proves
production capacity. See `webrtc-fec-cpu.md` for method and limitations.

Regressions cover retained-sample deadlines, bounded startup bursts, stale audio,
codec fallback/FEC opt-out, RTP timing, concealment accounting, cancellation,
replacement, slow diagnostic bodies, malformed metadata, failed authorization
refresh, recovery deduplication and credential redaction. Existing local
WebRTC/SRTP/network tests use carrier substitutes. Speech demonstrations and
constrained Chromium profiles are documented in `webrtc-voice-quality.md`.

## Installation limits

No live carrier call or staging/production operation was performed. Telephony
cannot restore speech that arrives beyond its latency budget; FEC does not
recover an entire long outage. Installation replaces the sidecar process and
requires a separately authorized rollout after active calls have drained.
