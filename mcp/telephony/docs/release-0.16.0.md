# Telephony 0.16.0

Final direct-SIP AI playout fix: pauses absorb provider drift and stalls, so
speech plays without holes or zero padding.

OpenAI `gpt-live-1` streams at about real time, runs 0.5–3.5% slow, stalls
150–360 ms and never catches up; 49–93% of a reply is explicit silence. 0.15.x
stretched speech first and reset the stream at every pause, which disabled
concealment after pauses and split packets with zero padding.

- Explicit silence absorbs lost time first, in whole 20 ms packets. Pauses are
  rounded to whole packets and their first zeros complete a pending speech
  packet, so speech never ends or resumes in a padded packet.
- Adaptive lead: playback still starts after 60 ms. The first near-dry trough
  arms `drift_lead_ms` (160 ms); each later one adds 40 ms, capped at 180 ms.
  After 10 s without one it shrinks 20 ms every 5 s by trimming silence only.
- Silence keeps the input history and stream: concealment and stretching
  continue across pauses.
- Speech is stretched only as a fallback (queue ≤ 40 ms, or over 1 s of speech
  without a pause).
- New playout stats `silence_added_ms`, `silence_trimmed_ms`, `drift_lead_ms`.
- Replay of ten real reply streams with their silence markers
  (`testdata/agent-audio/openai-live-frames-20261029.json`, 54 s of speech):

  | | drift off (0.14.1) | 0.15.1 | 0.16.0 |
  |---|---|---|---|
  | holes inside speech | 5 (280 ms) | 2 (82 ms) | 0 |
  | all underruns | 54 (4.1 s) | 55 (3.1 s) | 5 (0.4 s), pauses only |
  | zero padding | 6 ms | 446 ms | 0 ms |
  | max delay before a delivery | — | 280 ms | 200 ms |

  No click edges or loud restarts; start delay 60 ms; healthy delivery
  unchanged.
- Carrier media streams and `drift: "off"` behave as before.
