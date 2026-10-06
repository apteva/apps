# Private coaching

Telephony 0.10.0 extends the existing listener session with explicit, private
push-to-talk coaching. The supervisor hears both call directions; their
microphone is routed only to the current human adviser's browser.

## Media and provider coverage

Coaching reuses Telephony's server media hub and the adviser's existing
WebSocket. No additional carrier stream, conference, PSTN leg or provider API
is created. This works with any supported carrier feeding an active human
browser softphone through Telephony, including direct SIP carrier bridges.
AI peers, external telephone advisers, provider-side transfers that bypass the
hub, and older adviser clients without coaching support cannot be coached.
Call responses expose `coach_supported`, `coachable` and
`coach_unavailable_reason`; the authenticated join/start checks are authoritative.

The supervisor connects through a separate listening/coaching socket. Capture
uses PCM16 at 24 kHz. Adviser-only playback uses G.711 μ-law at 8 kHz, about
70 kbit/s including framing, to limit additional download bandwidth. The caller
and adviser microphone streams keep their existing encoding and routing.
Coaching is mixed at half gain with available clipping headroom. It does not
change the caller's jitter adaptation or discard caller samples to make room.
A three-frame server queue and six-frame browser queue bound coaching backlog;
received stale coaching is discarded after 200 ms of excess age/residence.
These are software bounds, not a guarantee of latency across an arbitrary
network. Unknown initial network delay cannot be inferred from relative clocks.

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
const listener = telephony.createCallListener({ inputDeviceId, outputDeviceId });
const unsubscribe = listener.subscribe(renderListenerState);
await listener.coach(call.id); // join from a user gesture; no microphone yet
await listener.startTalking(); // pointer/key down: requests microphone and activation
listener.stopTalking();        // pointer/key up, cancel, lost capture or blur
await listener.stop();         // releases only this listener
unsubscribe();
await listener.dispose();
```

`listen(call.id)` remains passive and never requests microphone permission.
`getSnapshot()` adds `coaching` and `talking`. The Calls panel provides Private
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
These are different stages and must not be summed as unique lost speech.

See [verification](private-coaching-verification-2026-10-02.md).
