# Telephony 0.31.0

Push-to-talk coaching over WebRTC no longer sets up a connection on every
press, and audio handover no longer drops healthy calls in one race. See
[private-coaching.md](private-coaching.md).

- **One coaching connection per session.** When coaching opens, the browser
  prepares one send-only WebRTC uplink (`coach.webrtc.prepare`) and keeps it for
  every press until coaching ends or its lease runs out.
  - A press only asks the server to start (`coach.start`). There is no ICE
    gathering, offer or answer during a press.
  - Between presses the uplink sends near-silence (about 25 kbit/s), so it stays
    connected and its timing stays aligned.
  - A failed or refused uplink is prepared again on the next press.
  - Prepares are limited to one per second, with increasing connection ids and
    a single send-only audio section.
- **Faster presses.** After the first press, which may show the microphone
  permission prompt, the microphone opens at the same time as `coach.start`.
- **Release cuts the microphone at once.** Its tracks stop as soon as the
  button is released. The uplink stays up for the next press.
- **Strict per-press audio.** The server plays a packet only if its first
  sample falls inside the current press, under 200 ms old. Leftover or late
  audio from an earlier press never plays in the next one, and coaching never
  reaches the caller or other listeners.
- **Older tabs keep working.** Tabs loaded before 0.31.0 still negotiate one
  connection per press (`coach.webrtc.offer`).
- **Audio handover fix.** With `softphone_webrtc_handover` on, a dropped
  signaling socket is held while microphone audio still arrives. If the server
  wrote to the socket before it read the drop, the failed write closed it and
  the call's audio was torn down instead. That case is now held too;
  revocation, takeover, lease expiry and hangup still stop audio at once.
- **Measured locally.** Over 30 presses on one uplink, the adviser heard the
  coach within about 100 ms of the microphone being ready (p95), with no
  network delay added.
