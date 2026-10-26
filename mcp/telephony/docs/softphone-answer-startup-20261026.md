# Browser answer setup, 26 October 2026

Reported local calls entered `answering` after the panel click but produced no media socket or audio telemetry before termination. This does not identify whether the original browser stalled in device/processor setup or its signaling connection failed before reaching the server. A public-gateway probe established a direct WebRTC data channel through zrok signaling in 582 ms; 300/300 packets were received over six seconds. That is a connectivity sample, not proof against intermittent tunnel trouble or a listening-quality result.

## Changes

- Open and resume the playback AudioContext during the user action, alongside microphone permission. Transfer those exact live resources from preparation into the selected WebRTC or PCM transport instead of closing and reopening the microphone after claiming the call.
- Bound preparation to 10 seconds and each transport setup attempt to 18 seconds, including device/worklet work preceding the socket. Automatic mode retains its separate PCM fallback attempt. Cancellation stops pending setup and late devices cannot revive an old call.
- On setup failure, retain the existing guarded release of unanswered claims; do not undo an already activated carrier leg or a replacement session.
- Display setup progress and count `answering` calls as active. Initial connection is no longer mislabeled as reconnection.

## Repeatable checks

Run `bun run test:frontend`, `bun run typecheck`, and `GOWORK=off go test ./...` from this app.

`frontend/tests/audio-preparation.test.ts` covers synchronous playback activation, single-use device handoff, hung permission/playback, permission denial, late microphone cleanup, disconnection during answer, failure cleanup and retry. Both real transport implementations are exercised through device adoption and processor loading.

`frontend/tests/client.test.ts` verifies that stalled post-claim setup releases the claim, restores controls and permits another answer; a late completion cannot stop the replacement. `frontend/tests/webrtc-audio.test.ts` verifies that a stalled initial WebRTC attempt still permits automatic PCM fallback.

`GOWORK=off go test -tags integration -run '^TestTier2HeadlessBrowser$/(headless|headless-webrtc-turn-recovery|panel)$' -count=1 -timeout 8m -v .` runs real Chromium and a real Telephony sidecar with fixture carriers. It verifies PCM and WebRTC/TURN audio, one microphone acquisition per initial answer, recovery after a 40-second HTTP outage and panel navigation without losing the call. No carrier test calls or production configuration changes are required.
