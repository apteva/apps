# Telephony 0.17.2

Delay from late source audio now clears at the next pause on lines with
background noise, not only on quiet lines.

## Outgoing WebRTC audio (softphone and listeners)

- Audio that arrives late from the source has already lost its slots to silence,
  so it queues up as delay. That delay is removed by dropping frames in a pause.
- A pause used to count only if its frames peaked at 64 or less (about -54 dBFS).
  With background noise at about -60 dBFS or louder no pause qualified, and 40 to
  60 ms of extra delay stayed for the rest of the stream. The 0.17.1 note said it
  lasted until the next pause, which was true only on quiet lines.
- Each stream now tracks its quietest recent frame, over 1.5 to 3 s. Frames are in
  a pause after 60 ms within 6 dB of that level and below about -34 dBFS. Silent
  frames do not move the level, so a dropout cannot switch this off. The rule
  applies once 1.5 s of audio has been heard.
- A dropped noise frame crossfades into the next over 5 ms. Silent frames are
  dropped as before. Drops are reported under the existing
  `webrtc_playback_backlog_silence` event.
- As before, nothing is dropped unless the queue has stayed above its target for
  200 ms, so a call without lateness is not touched.

## Evidence

Real speech through the real framer, queue and tick, with a source 60 ms late.
Delay is the age of the oldest sample in each frame.

- Noise from -60 to -35 dBFS: 0.17.1 stays at 65 ms to the end of the stream. Now
  back to 25 ms at the first pause, about 3 s later in this speech, with 40 ms
  removed from that pause.
- Quiet line: same as 0.17.1.
- A real call stored locally, with -50 dBFS noise added: 0.17.1 stays at 65 ms for
  the remaining 26 s. Now back to 25 ms within about 1 s. As recorded, same as
  0.17.1.
- Removed frames, lateness repeating every 2 s: at -60 dBFS noise the loudest
  speech in a removed frame was 34 dB below normal speech level. Nothing removed
  was louder than about the noise around it.
- No lateness at any noise level: no frames dropped, no audio changed.
- Tones, music and noise at -30 dBFS: no frames dropped.
- The real sender on a real WebRTC transport, three runs at -50 dBFS noise: 40 ms
  removed from the pause, no timeline holes, no speech removed.

Time-compressing speech to clear the delay faster was tried and rejected: it
changed audio on a steady call and failed the checks that audio is not lost or
reordered.

## Limits

- During long unbroken speech the delay stays until the next pause.
- Through tones, music and noise louder than about -34 dBFS nothing is dropped, so
  the delay lasts as long as they play, as in 0.17.1.
- A source 80 ms or more late can still lose speech through the 80 ms budget and
  the 120 ms queue bound, as in 0.17.1.
- On a very noisy line with a quiet speaker, a removed frame can hold weak speech
  at about the level of the noise around it.
- Not measured on a production or staging call. The other limits in the 0.17.1
  note still apply.

No migrations, settings or tools changed. Full suite and race suite pass (1,374
tests, none retried). Timing-sensitive tests such as
`TestAgentAudioSIPWireChunkContinuity` can still fail on an overloaded host.
