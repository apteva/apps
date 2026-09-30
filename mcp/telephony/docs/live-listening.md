# Passive live listening

Telephony 0.9.0 adds independent, receive-only call listeners. Audio is copied
inside the existing server media bridge, never forwarded by the adviser's
browser. Listening does not create a second carrier stream or PSTN leg, answer
a call, change its owner, reserve adviser capacity, or connect a microphone.

## Coverage

The same tap handles human, AI and locally bridged external peers. Supported
media paths are Twilio, SignalWire, Telnyx, Plivo and Bandwidth JSON WebSockets;
Vonage and Sinch binary WebSockets; and direct SIP/RTP, including DIDWW when
configured through Telephony's SIP gateway. Only paths actually carrying media
through Telephony can be listened to. Provider-side transfers that bypass the
bridge cannot be monitored this way. `listen_supported` is true only while a
live server tap exists and the call's media status is connected.

Caller audio is captured after carrier decoding. Audio toward the caller is
copied only after successful transmission, following pacing, stale-audio
removal and codec conversion. This represents what Telephony sent; it is not
proof of what an upstream carrier played or the caller heard. No canceled,
unsent AI speech is copied. Native carrier prompts/hold music generated outside
the bridge are not included.

## Access

Existing supervisors gain no listening access automatically. An administrator
must grant all three:

1. Supervisor role, with the destination/outbound-number resource grants for
   the calls they may inspect. Supervisors may grant any enabled routing
   destination, including AI destinations; normal users remain browser-only.
2. `listen: true` on that user's or access group's Telephony policy grant.
3. `call.listen` in the delegated gateway scope or approved online authentication
   provider's `actions`. `call.read` remains necessary for call discovery.

Example policy fragment (retain the other policy fields and its current revision):

```json
{
  "role": "supervisor",
  "destinations": ["sales-desk", "ai-fallback"],
  "outbound_numbers": ["+33123456789"],
  "listen": true
}
```

Trusted administrative integrations may listen within the authenticated project.
As with existing administrative APIs, their access is broader than application
users' access. The app policy API uses optimistic revision checks.

Listener credentials are independent of operator credentials, stored only as
hashes, and bound to call/project/principal. They expire after 60 seconds and
are renewed through authenticated HTTP every 20 seconds. Policy writes revoke
affected listeners promptly; the media handler also rechecks access and terminal
status once per second. Logout or loss of an online login prevents renewal;
existing media access expires within its remaining lease, as for softphone audio.
A listener token cannot answer a call, attach operator media, or renew another
principal's grant. One simultaneous socket per credential prevents replay.

## Frontend SDK

```ts
const listener = telephony.createCallListener({
  stereo: false, // default: mixed playback; true: caller left, adviser/AI right
  onDiagnostics: diagnostics => console.log(diagnostics),
});
const unsubscribe = listener.subscribe(state => renderListeningState(state));
await listener.listen(call.id); // invoke from a user gesture for browser audio
listener.setOutputVolume(0.7);
await listener.stop(); // releases only this listener, never hangs up the call
unsubscribe();
await listener.dispose();
```

Use `call.listenable` for the Listen button; the request-time check remains
mandatory because the value can become stale. The bundled Calls panel includes
Listen, Stop listening and volume controls. The receiver opens no microphone.
It uses the SDK's installation-scoped gateway and verified audio module loading,
including same-origin hosts with a Content Security Policy that disallows blobs
in `script-src`. External hosts must permit the embedded blob AudioWorklet.

States: `idle`, `connecting`, `listening`, `reconnecting`, `disconnected`,
`access_revoked`, `call_ended`. Transient media/network disconnections request
new credentials with exponential backoff for up to 30 seconds. Revocation and
call termination never trigger automatic reconnection. Set `reconnect: false`
to make reconnect an explicit UI action.

Low-level APIs: `listenSession(callId)`, `listenerMediaURL(session)`,
`renewListening(session)`, `stopListening(session)`, `listenerAudit(callId)`.
Endpoints: POST `/softphone/listen/:call`, `/softphone/listen-renew/:call`,
`/softphone/listen-stop/:call`; GET `/softphone/listen-audit/:call`. Online-user
requests use `/user` and the normal `auth_provider` query parameter. The socket
is `/softphone/listen-media/:call/:credential`; its only authority comes from
the listener credential, not an unauthenticated project query.

## Isolation and diagnostics

`max_call_listeners` defaults to four per call and is bounded to 1–16. Both
issued unexpired credentials and attached sockets obey the cap. Expired grants
are pruned when issuing new ones; disconnected grants are removed.

Each listener has an independent combined queue of twelve 20 ms directional
frames. Frames older than 200 ms are discarded; socket writes have a 200 ms
deadline. A slow listener cannot backpressure the original conversation.
The browser uses a single render clock for both directions, an initial 60 ms
cushion, queues bounded to 160 ms per direction, and rejects stale/far-future
scheduling. It never accumulates delayed speech to repair a poor network.
The original softphone capture, playback and carrier pacing policies are unchanged.

Server diagnostics record sent, overflow, source-trimmed and stale frames by direction plus
maximum write duration. Browser diagnostics expose directional played/dropped
milliseconds, sequence gaps, maximum queue/lateness, network drops and network transit excess
relative to the best observed transit time (not absolute one-way latency).
The authenticated audit API returns the latest 100 listener joins/leaves,
principal, reason and final diagnostics; it never exposes listener credentials.
Whisper/coaching, barge-in, recording permissions and native carrier conferences
are outside this passive listening feature.
