# Full duplex browser voice validation — 2026-10-23

Implementation is local and unreleased. Existing installations and production
processes were not changed. Source, packaged panels, and app frontend assets are
rebuilt together.

## Verified behavior

The app-owned VoiceAudioClient advertises playback readiness, measures partial
consumption from AudioContext time, acknowledges item-scoped clearing, ignores
canceled PCM and callbacks, and isolates connection generations. Legacy
interrupts still clear all current audio. Microphone PCM remains continuous,
including silence while muted. The worklet reports debounced speech onset/end;
playback silence retains its timing without keeping the speaking status on.
Bridge renewal is bounded and late renewals cannot reconnect a departed pane.

## Real Tier 3 runs

Both runs used the native runner's disposable Server, Core, agent, project and
Conversations sidecar, real `gpt-live-1` (Brise), OpenAI-synthesized French caller
audio, and Chromium's actual Web Audio player and capture worklet. The browser
connected to Core through Server's production WebSocket relay. Provider keys
were supplied only at runtime and were absent from the browser bundle and source.

- Initial run: 107.292 seconds, 30 playback progress receipts, 363 microphone
  frames during interruption/recovery, one scoped clear.
- Strengthened run: 107.631 seconds, 74 playback progress receipts, 389 microphone
  frames during interruption/recovery, one scoped clear. Required consumed
  voiced assistant audio still arriving after the initial caller stopped before
  triggering the interruption; silent receipts cannot satisfy this condition.
- Both runs captured automatic caller speech onset, the matching Core interrupt
  and browser clear receipt, subsequent consumed assistant audio, and the correct
  answer mentioning **mardi** in durable Conversations history. No realtime
  errors, overflows, disconnects or missing-caption blocks were observed.
- Native runner separately verified the requested OpenAI text backend and
  `gpt-6.1-sol`. Two main-thread iterations consumed 28,624 and 28,611 tokens.
- Durations include the 65-second workaround for Server telemetry subscription
  ownership refresh. CLI-reported cost excludes this driver's paid Live/TTS
  usage and must not be interpreted as a free run.

## Other checks

- 123 Bun UI/client/audio tests.
- 118 Playwright browser tests; the four voice/audio/transcript cases rerun after
  final connection-lifecycle changes.
- Full Conversations `go test -race -tags=integration ./...` passed.
- Frontend package/declarations, consumer type checks, panel bundling and host
  React import verification passed.
- Server relay tests preserve all additive duplex controls byte-for-byte.
- Targeted CLI tests verify OpenAI project-scoped provider provisioning and
  preserve Codex provisioning. The older unrelated OpenCode env-bootstrap test
  still expects behavior its existing connection-backed implementation no longer
  provides; the full CLI suite is not claimed green.

The CLI required a small bootstrap correction: create a project-scoped
`openai-api` connection using the catalog's `token` credential instead of relying
on Server environment discovery. This is retained locally with a regression
test. Standalone CLI compilation used a temporary modfile to supply the already
imported but undeclared `golang.org/x/term` dependency, leaving its module files
unchanged.

## Limits

A synthesized MediaStream exercises actual browser scheduling and resampling,
not physical microphone/speaker acoustics. These runs do not establish echo
cancellation quality for every device or long-session reliability. No change to
Core's fixes was needed. No public release was created by this task.
