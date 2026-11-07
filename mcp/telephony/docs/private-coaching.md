# Private coaching

Telephony 0.10.0 extends the existing listener session with explicit, private
push-to-talk coaching. The supervisor hears both call directions; their
microphone is routed only to the current human adviser's browser.

## Media and provider coverage

Coaching reuses Telephony's server media hub and the adviser's existing
browser media connection. No additional carrier stream, conference, PSTN leg or provider API
is created. This works with any supported carrier feeding an active human
browser softphone through Telephony, including direct SIP carrier bridges.
AI peers, external telephone advisers, provider-side transfers that bypass the
hub, and older adviser clients without coaching support cannot be coached.
Call responses expose `coach_supported`, `coachable` and
`coach_unavailable_reason`; the authenticated join/start checks are authoritative.

The supervisor connects through a separate listening/coaching control socket.
With PCM/WebSocket selected, capture
uses PCM16 at 24 kHz. Adviser-only playback uses G.711 μ-law at 8 kHz, about
70 kbit/s including framing, to limit additional download bandwidth. The caller
and adviser microphone streams keep their existing encoding and routing.
Coaching is mixed at half gain with available clipping headroom. It does not
change the caller's jitter adaptation or discard caller samples to make room.
A three-frame server queue and six-frame browser queue bound coaching backlog;
received stale coaching is discarded after 200 ms of excess age/residence.
These are software bounds, not a guarantee of latency across an arbitrary
network. Unknown initial network delay cannot be inferred from relative clocks.

With WebRTC selected, listening uses two receive-only Opus tracks. Every explicit
push-to-talk press creates a fresh, send-only Opus peer connection after the
existing generation has been authorized. The signaling socket carries the SDP,
PTT start/stop and keepalive controls. Uplink RTP is converted into the existing
generation-tagged PCM protocol and passes the same ownership, lease and age
checks. Release, expiry or target replacement closes the uplink; old peer media
cannot acquire a new talk generation. Starting a talk has an additional bounded
ICE negotiation step; releasing during that step still stops the microphone.

The pinned adviser's transport independently determines private delivery:
WebSocket retains its APW1 overlay; WebRTC mixes the overlay at half gain with
clipping headroom into only that browser's outgoing Opus frame, after all
carrier/recording/listener taps. A bounded six-frame private queue and validation
at dequeue/encoding retain the 200 ms protection. Mixing/resampling runs in the
browser encoder goroutine, never on the carrier receive loop. No extra adviser
RTP track, carrier leg or carrier stream is needed.

Listening and coaching inherit the softphone's profile in the Calls panel, with
independent transport/FEC and stereo overrides. For headless hosts, use shared
client `audio` defaults or `phone.createCallListener(overrides)`. Devices,
processing, volume and FEC are configurable for both transports. Options change
only future attachments; `setOutputVolume()` remains the immediate local control.

There is no digital route from coaching to the carrier, recording tap or other
listeners. Advisers and supervisors should use headsets: loudspeaker audio can
be picked up acoustically by a microphone despite echo cancellation. Native
carrier recordings therefore exclude the private digital coaching overlay.

## Authorization

Existing supervisors are not automatically granted coaching. Application users
require all of:

- Supervisor role and destination/outbound-number access for the selected call.
- Explicit `listen: true` and `coach: true` on the user or access group.
- Both `call.listen` and `call.coach` in delegated gateway scopes or approved
  online authentication provider actions. Discovery also needs `call.read`.

Retain the policy's other fields and current revision when editing access:

```json
{
  "role": "supervisor",
  "destinations": ["sales-desk"],
  "outbound_numbers": ["+33123456789"],
  "listen": true,
  "coach": true
}
```

Trusted authenticated project administrators retain the administrative API's
existing broader project access. Coaching credentials cannot operate calls or
be used as adviser media credentials. Passive tokens cannot be upgraded or
renew coaching sessions. Both modes share `max_call_listeners` (default 4,
maximum 16). One supervisor may talk at a time; other authorized supervisors
can continue listening and retry after that talk ends.

## Frontend SDK and controls

```ts
const listener = phone.createCallListener({ /* optional mediaTransport, webrtcFec, stereo */ });
const unsubscribe = listener.subscribe(renderListenerState);
await listener.coach(call.id); // join from a user gesture; no microphone yet
await listener.startTalking(); // pointer/key down: requests microphone and activation
listener.stopTalking();        // pointer/key up, cancel, lost capture or blur
await listener.stop();         // releases only this listener
unsubscribe();
await listener.dispose();
```

`listen(call.id)` remains passive and never requests microphone permission.
`getSnapshot()` adds `coaching`, `talking` and the actual `mediaTransport`. The Calls panel provides Private
coaching and Hold to talk to adviser controls, with keyboard support.
Hold-to-talk is deliberate: release stops tracks immediately, including when a
permission prompt or server activation is pending. The runtime also stops on
blur, backgrounding, device termination and socket/audio failure.

Coaching is pinned to the adviser owner, peer credential and browser generation
at join time. Takeover, credential replacement and adviser reconnection require
joining again. It never automatically reconnects to a new adviser. Holding the
call, carrier disconnection and terminal status stop active talk; a resumed
eligible session needs a fresh press. Keepalive runs every second; missing
keepalive expires talk after two seconds, with the periodic watcher notifying
the supervisor and closing the talk audit. Expired queued frames cannot revive
coaching. Permission revocation disconnects the coaching session while retaining
the main call.

Low-level APIs: `POST /softphone/coach/:id`, `coach-renew/:id`, `coach-stop/:id`;
application-user and online-provider prefixes follow the existing softphone
contract. The lease is 60 seconds and the client renews every 20 seconds.

## Diagnostics

`listenerAudit(callId)` / `GET /softphone/listen-audit/:id` returns listener
sessions with their `mode`, plus coaching talk spurts with start/end/reason.
It retains no supervisor microphone recording. Listener diagnostics count
capture received/discarded frames. Server `to_browser` diagnostics separately
count coaching frames sent/discarded by the writer; browser playback timing
separately counts coaching played/discarded duration and maximum queue depth.
For RTC, listener audit diagnostics retain caller/adviser RTP snapshots and
coaching RTP drops/concealment. The adviser RTP snapshot reports
`coaching_mixed_ms`; it measures server mixing, not separately observed browser
playback. Native RTP/browser stats cannot independently separate the encoded
private overlay from the main audio.
These are different stages and must not be summed as unique lost speech.

See [verification](private-coaching-verification-2026-10-02.md).


## Local transport verification (11 October 2026)

- Full Go suite and focused listener/coaching/hub race tests passed.
- 251 frontend/worklet tests passed; type checking, Go vet and panel builds passed.
- Seven real Chromium/sidecar scenarios passed: existing headless, WebRTC
  listening/coaching, automatic fallback with WebRTC disabled, 512 kbit/s and
  jittered coaching links, Calls panel and authenticated application-user calls.
- Passive listening denied microphone access in the browser test. Explicit PTT,
  release during delayed permission, repeated talk and blur stopped tracks.
- Pion tests decoded both listening directions and the adviser-only Opus overlay;
  carrier audio did not receive coaching. Expired/stopped generations, bounded
  private queues, FEC opt-out and call-ending closure were covered.
- The isolated private mix/resampler benchmark measured 34.8–35.2 µs per 20 ms
  frame on an Apple M1 Pro, about 1.75 ms CPU/second per active coach. This is
  mixing overhead only, excluding encoding, ICE and network work, and is not a
  production capacity measurement. The carrier path does not run this mixer.

Reproduce with `GOWORK=off go test ./...`, `bun run test:frontend`,
`GOWORK=off go test -race -run 'TestListener|TestCoach|TestRTCPrivate|TestRTCWhisper' .`,
`GOWORK=off go test -tags integration -run TestTier2HeadlessBrowser .`, and
`GOWORK=off go test -run '^$' -bench BenchmarkRTCPrivateCoachingMix -benchmem .`.
All checks used local fixtures; no real carrier calls or deployed settings changed.
