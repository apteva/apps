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

### Timestamped transport history

The shared PCM session and the headless WebRTC connection expose
`SoftphoneDiagnostics.transportSamples` in `onDiagnostics`. The same data is
available to operators in Telephony’s audio-health call detail under
`transport_samples`, alongside `network_events`, browser and carrier diagnostics.
No client app needs to parse Chrome’s internal debugging page.

WebRTC reads an allowlist from `RTCPeerConnection.getStats()` once per second.
Only one read may be outstanding per peer connection. Failed or late reads never
close, claim, answer or reconnect a call. Samples include:

- Directional RTP bytes/packets, loss, retransmissions/NACK/FEC counters when
  supported, concealment, silent concealment and sample adjustments.
- Interval jitter-buffer delay/target/minimum and processing delay, calculated
  from counter deltas for the same RTP stats identity. The existing summary’s
  jitter-buffer delay remains a lifetime average for compatibility.
- Remote receiver reports for **browser → Telephony**, including loss, fraction
  lost, jitter and RTCP RTT. These do not measure the carrier/PSTN leg.
- Selected ICE pair bandwidth estimates, request/response counters, RTT,
  candidate types, relay protocol, ICE/DTLS states, a numeric path revision and
  safe codec fields. Raw candidate IDs are used only for private delta tracking.

Missing native fields stay absent. Counter decreases, new RTP identities and
reconnects establish new baselines rather than producing large artificial rates.
Native `jitter`, `roundTripTime` and pair RTT values retain WebRTC’s **seconds**;
keys ending `_ms`, the common `rtt_ms` and legacy flat summaries use milliseconds.
Available bitrate is a browser estimate, not proof of spare office/VPN bandwidth.

PCM adds interval wire-byte rates, receipt gaps, capture age, transit/delivery
excess and server queue maxima on the existing Worker statistics timer. Delay is
observed before stale frames are discarded. Unknown delay/clock measurements
stay absent. Wire rates include the Telephony binary frame header; native RTP
rates use RTP payload bytes, so these rates are not directly interchangeable.
Receipt gaps are observations and do not independently classify silence as a
fault. Cumulative mute, loss, underrun, reconnect and scheduling counters remain
separate, with existing timestamped drop and underrun events unchanged.

Both transports emit at most two additional samples per five-second observation
period, with no catch-up loop after timer delays. A new incident preserves its
first observation and the preceding observation until the next emission.
The client keeps 24 emitted samples and at most eight pending samples.
The additional history is split into parts of at most 768 ASCII JSON bytes,
sent at most once per 250 ms. Including the control envelope, each history
message stays below 1 KiB. The sender has at most 32 pending parts, and PCM
skips observational pieces when more than 40 ms of PCM is already queued.
Parts keep one sample identity and the server idempotently merges their
allowlisted maps. `complete` and `parts` distinguish a fully collected sample
from a sample missing diagnostic pieces; missing monitoring never counts as lost
audio. WebRTC summary reports share this pacing slot so they cannot collide with a
history piece. Root call/audio controls retain their existing protocol. The server
accepts at most four samples per incoming control. These are
bounded diagnostic windows, **not a complete all-call Chrome internals dump**.

A nonblocking, 1,024-entry collector stores history in a separate indexed table,
with batches of 100, at most 96 periodic and 32 incident samples per call, and
the existing network retention setting (seven days by default). Database failure
retains a bounded pending batch; contention/overflow drops monitoring data and
logs a warning. Idle expiry cleanup runs at most once per minute. Audio frames
never wait for collection or storage. Call-list and SSE summaries do not read
history. Server-validated call/project and receiving-connection IDs override
browser input; source-time connection attribution may remain unknown if clocks
cannot be matched to an observed interval.

No raw SDP, ICE credentials, candidate addresses, media tokens or token-bearing
URLs are accepted in samples. The existing separately protected network-event
store remains the place for validated browser IPs. An RTC signaling socket’s
address does not establish the route used by RTP through ICE/TURN.

## Verification scope

Local tests cover disabled/invalid configuration, unauthorized attachment,
caller cancellation during negotiation, failed setup preservation, bidirectional
Opus media, hold, replacement by an existing WebSocket client, private coaching,
DSP parity, explicit selection, cleanup before fallback, and telemetry persistence.

Real Chromium benchmarks exercise the complete sidecar/hub/codec/browser path,
including intentional mute and a fresh audio context after reconnect. Separate
Pion virtual-network profiles shape actual encrypted UDP media at 256, 64 and
24 kbit/s, including jitter and loss. The `webrtc-udp-*` Chromium profiles
add a forced local TURN/UDP relay: actual encrypted browser audio and signaling
share the configured per-direction bandwidth, with explicit overhead/drop counters
and selected-relay verification. Unshaped WebRTC baseline/mute/reconnect profiles
continue to constrain signaling only. See the benchmark README for accounting,
measurement gates, network-model limits and reproducible commands.

These checks are software pipeline/regression evidence. They do not certify
physical microphone/speaker quality, perceptual MOS, every carrier, a production
TURN deployment or every VPN/network. Controlled carrier calls and representative
shared-network capacity checks remain necessary before enabling a live deployment.
