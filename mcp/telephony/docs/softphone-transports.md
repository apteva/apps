# Optional WebRTC softphone transport

The shared Telephony browser/headless client accepts `mediaTransport`:

```ts
const phone = telephony.createSoftphone({
  mediaTransport: "webrtc", // "websocket" (default), "webrtc", or "auto"
  onDiagnostics: diagnostics => renderAudioHealth(diagnostics),
});
```

The same option is available in `audio.mediaTransport` and the Calls panel's
local audio settings. The top-level option takes precedence. It applies when a
new audio session starts; it does not silently migrate an existing session.
A custom `AudioRuntime` remains responsible for its own transport implementation.

## Selection and compatibility

- `websocket` retains the existing PCM16, 24 kHz, mono media path. Existing hosts
  need no option or configuration change.
- `webrtc` uses native browser Opus/RTP, encrypted with DTLS/SRTP. An unavailable
  server/browser or failed negotiation returns an audio setup error.
- `auto` tries WebRTC and releases its microphone, peer connection, audio context
  and signaling socket before falling back to WebSocket during initial setup.
  Revoked/expired authorization or denied microphone access does not cause a
  fallback. An established session never silently switches transport.

The carrier boundary remains the existing Telephony media hub. Telnyx, Twilio,
DIDWW/direct SIP and other supported adapters continue to use their existing
codec/control paths. Routing, answer permission, occupancy, hold, DTMF and
recording use the same authorities. This does not give a provider new carrier
capabilities or change AI-to-carrier media.

Only the browser-to-Telephony boundary changes. There is one microphone path and
one caller path. No second carrier media stream is created. Listening retains
its existing authenticated media path. Private coaching retains its bounded
control-socket channel, pinned-adviser permission check, epoch validation and
playback processor; it cannot enter the caller or recording path.

## Server configuration

WebRTC is disabled by default. These Telephony project settings enable it:

| Setting | Meaning |
| --- | --- |
| `softphone_webrtc_enabled` | `true` to accept authorized WebRTC attachments |
| `softphone_webrtc_ice_servers` | Optional JSON ICE server array with `urls`, `username`, `credential` |
| `softphone_webrtc_public_ips` | Optional comma-separated public addresses advertised for host candidates behind NAT |
| `softphone_webrtc_udp_port_min` / `softphone_webrtc_udp_port_max` | Optional inclusive UDP range, 1024–65535; configure both bounds |

Example ICE configuration (placeholders only):

```json
[{"urls":["stun:stun.example.com:3478"]},
 {"urls":["turn:turn.example.com:3478?transport=udp","turns:turn.example.com:5349?transport=tcp"],
  "username":"restricted-user","credential":"restricted-credential"}]
```

The sidecar must have a reachable UDP candidate path, or a functioning TURN relay.
The existing HTTP reverse proxy alone does not forward RTP. For container/NAT
hosting, forward the configured UDP range to the sidecar and advertise the
appropriate public address. ICE may select TURN over TCP/TLS on restrictive
networks; choosing WebRTC does not guarantee UDP on every connection.

ICE/TURN settings are sent only after media authorization. Restrict TURN credentials
and relay access, rotate them operationally, and do not expose unrestricted relay
accounts. No provider API key is required for this transport. This implementation
does not modify firewall, hosting, integrations or any live installation.

## Audio, bounds and recovery

The shared capture worklet keeps the existing input gain, high-pass filter and
limiter. Its native output is tested sample-for-sample against the PCM capture
path. The browser encodes processed microphone audio as Opus; the server decodes
it into the existing 24 kHz mono hub. Caller PCM is paced into 20 ms Opus frames.
RTP timestamps follow the monotonic live clock: missed encoder/scheduler ticks
advance media time instead of accumulating playout delay. Pacing gaps are
recorded separately from PCM discards and do not invent network packet loss.
The Opus target is 32 kbit/s per direction, before RTP/SRTP/IP overhead. Native
browser bitrate controls and jitter targets are hints, not universal guarantees.

Server ingress has a 16-packet reorder bound, a 60 ms initial cushion and a
320 ms age limit. Egress holds at most six 20 ms frames and rejects received
source audio beyond the existing 320 ms budget. All queues remain bounded.
The browser's native RTP jitter buffer is controlled by the browser; a requested
60 ms target cannot enforce a hard 320 ms playout cap. Its measured delay and
packet discard/concealment counters are reported separately.

A media/signaling failure reattaches to the existing carrier call using a fresh
`/softphone/attach` authorization, with a bounded 30-second recovery budget.
Mute persists. Revocation/expiry or permanent microphone failure stops recovery.
Caller termination and browser replacement stop the old session. Failed initial
negotiation cannot replace an existing browser or answer the carrier.

## Diagnostics

Reports explicitly identify `media_transport` and `codec`. Native RTP reports
include selected protocol/candidate type, payload bitrate, RTT, jitter, packet
loss, packets discarded, concealment and mean jitter-buffer delay. Candidate
addresses, media tokens and SDP are not recorded. Concealed samples are not
reported as discarded PCM, and an intentional mute is not a transport failure.
RTC queue/decode drops have bounded timestamped server events and are included
in audio-health classification/dashboard summaries.

The recorded browser network address is the authenticated signaling socket's
trusted peer metadata. A TURN/ICE media path can use a different route; this
address alone cannot prove the route used by RTP or identify a VPN fault.
Virtual hub writer timings describe the local PCM bridge. Native RTP metrics
must be consulted for the browser network boundary.

## Verification scope

Local tests cover disabled/invalid configuration, unauthorized attachment,
caller cancellation during negotiation, failed setup preservation, bidirectional
Opus media, hold, replacement by an existing WebSocket client, private coaching,
DSP parity, explicit selection, cleanup before fallback, and telemetry persistence.

Real Chromium benchmarks exercise the complete sidecar/hub/codec/browser path,
including intentional mute and a fresh audio context after reconnect. Separate
Pion virtual-network profiles shape actual encrypted UDP media at 256, 64 and
24 kbit/s, including jitter and loss. The TCP proxy in the Chromium benchmark
shapes WebRTC signaling, **not RTP**; those browser profiles do not certify
WebRTC performance at the proxy's nominal bandwidth. See the benchmark README.

These checks are software pipeline/regression evidence. They do not certify
physical microphone/speaker quality, perceptual MOS, every carrier, a production
TURN deployment or every VPN/network. Controlled carrier calls and representative
shared-network capacity checks remain necessary before enabling a live deployment.
