# Telephony 0.12.13

## Startup audio

- Repeated unmute/readiness notifications preserve partial microphone frames
  and DSP history. Actual mute transitions still flush capture immediately.
- Warm authorized browser WebRTC capture before carrier attachment. The server
  still gates ringing, hold, ownership and terminal calls; intentional microphone
  mute remains effective. This removes a browser notification round trip from
  the first microphone audio after answer.
- PCM/WebSocket starts transmitting at the authorized session-ready handshake,
  without waiting for the later carrier-peer notification. Audio before answer
  remains suppressed by the server.
- After answer, keep at most 120 ms / 5,760 bytes of fresh 24 kHz PCM while the
  carrier peer attaches. Flush in order with original receipt times. Expiry,
  overflow, hold, call ending and browser replacement are counted separately.
  Existing stale-audio limits remain. A prolonged failure cannot be lossless.
- The shared headless controller reports connected only when carrier answer and
  local audio readiness agree. Carrier status remains available separately.

## Explicit readiness and diagnostics

- Media control sockets emit `media.ready` and `media.not_ready`, with call ID,
  connection ID, bridge generation, monotonic revision, UTC time and reason.
  The current state is retried if the bounded control queue is full. Collection
  and notification never wait for a network write on the audio path.
- Shared clients expose `onMediaReadiness`; local `onState("live")` additionally
  requires browser readiness. Compatible older servers retain the existing
  ready/peer/status fallback. Readiness is not proof that a remote human heard
  speech. The last 16 server readiness transitions are retained in diagnostics.
- Retain first media-stage observation, first browser RTP packet, first SIP RTP
  receive/send and microphone startup timestamps. Record attachment reserve
  duration, expiry/overflow and first/last pre-answer microphone suppression.
- Persist safe SIP codec negotiation summaries: local preference, remote payload
  mappings/clock rates, chosen codec and negotiation direction. Never store raw
  SDP, media keys, credentials or remote addresses in this summary.

## Validation and rollout

Tests cover immediate opening speech through the shared handoff, native Opus
encode/decode before handoff, inbound/outbound state, queue bounds, expiry,
hold/end/replacement isolation, event ordering and blocked observer queues.
The fixture is synthesized speech, not a customer recording. These local tests
cannot establish what Telnyx's far endpoint heard or eliminate external loss.

SDK v0.100.0, minimum Apteva 0.87.4, native Opus/FEC and the 0.12.11 clock fix
are retained. Publishing does not install or restart any instance. Schedule
installation outside calls; then reload browser clients. Dynamic app clients
need no host UI rebuild. Explicitly bundled clients need their bundle updated.
