# Telephony 0.13.0

Reliability, audio-continuity and security release from a full review of the
app. Telephony stays compatible with the current Core and Server; the companion
Core/Server changes (playback credit, bridge replacement, interrupt ordering,
provider read timing) are released separately and improve the same paths further.

## AI voice to the caller

- Underruns no longer click. The last speech before real silence fades out over
  3 ms and the next speech fades in, on SIP and on every carrier stream. A SIP
  partial frame is no longer cut to zero padding mid-word.
- Fades apply only to real gaps. Carrier streams treat lateness under one packet
  (20 ms) as continuous, which their jitter buffers absorb; 0.12.61 had no such
  path, but an intermediate build that faded every late packet clicked once per
  provider chunk. An end-to-end tone test now guards this on all 12 carrier legs.
- SIP mid-reply recovery rebuilds a bounded cushion (up to 60 ms) instead of
  resuming with none, so one upstream stall no longer cascades.
- A late SIP ticker no longer leaves 20 ms holes: queued audio for missed ticks
  goes out with contiguous RTP timestamps (at most 3 slots). Empty slots stay gaps.
- Core progress receipts leave the 20 ms RTP tick on their own coalescing writer;
  a slow Core socket cannot skip ticks or stop outgoing audio.
- Overflowed replies are removed from the SIP queue instead of playing as dead air.
- Hold, remote hold and hold music return Core's playback credit with an honest
  `playback.cleared`, so the AI cannot go silent for the rest of the call.
- JSON carriers (Telnyx, Plivo, SignalWire, Bandwidth) flush a reply's final
  partial frame after one idle packet instead of holding it until the next reply.
- The output resampler resets at real reply boundaries and Vonage interrupts.
- Relay and control-queue backpressure no longer tears down the AI leg: progress
  receipts coalesce and the renderer write budget covers pressure plus overflow.
- Vonage non-continuous playback is paced so interrupts clear queued audio.
  Plivo `clearAudio` addresses its stream. Estimated acks wait 250 ms for carrier
  playout before reporting audio as heard.
- Timing traces keep the first events plus a ring of the most recent, and record
  optional Core provider read timing (`provider_read_started_at`,
  `provider_read_wait_ms`, `provider_delta_duration_ms`).

## Caller audio and human softphone

- SIP receive re-anchors a sustained late offset instead of muting the call,
  treats consistent same-SSRC sequence/timestamp jumps as a stream reset, tracks
  sender clock skew up to ±1%, and its EWMA target decays under ordinary jitter.
  The SIP default estimator stays `ewma` (see receive-timing-recovery.md).
- WebRTC receive defaults to `distribution`, verified better on the same inputs.
  WebRTC outgoing backlog trims back to target after stalls; transient RTP write
  errors count as loss; coach peers close outside the listener lock; upload
  bitrate no longer oscillates.
- Softphone clock baselines are windowed, so long calls with 50–100 ppm clock
  skew no longer drop one direction after 40–80 minutes. Reconnects cannot attach
  to a just-removed hub; slow audio-clock probes degrade instead of muting.
- Voicemail recording reorders, de-duplicates and fills losses.
- Local recording keeps caller and agent tracks aligned after stalls.

## Security and call state

- Inbound SIP requires the source carrier to match the route's carrier and
  supports a per-route secret, provisioned automatically for Twilio and DIDWW
  and enforced after its first verified call (see direct-sip.md).
- Twilio media signatures validate against the split `carrier_media` ingress.
- Routing tools enforce agent route ownership; identity-provider URLs reject
  private and loopback destinations; softphone media tokens are redacted in logs.
- Outbound DIDWW acknowledges and hangs up a 200 OK that crosses a CANCEL;
  re-INVITEs keep their SDP session id; ringing hangups always send 487.
- Ring-group overflow loops are rejected at publish and fail through at runtime;
  answer-claim release is atomic; purchase intents no longer poison a number.
- Durable notifications, the incoming-call outbox (leased) and lifecycle events
  (rejected events parked) no longer drop, duplicate or block.

## Migrations

`069_inbound_outbox_lease.sql` and `070_call_event_publish_error.sql` only add
text columns with an empty default, which older builds ignore. `database_upgrade`
remains `backward_compatible`.

## Tests

`bun run test:race` runs the whole package under `-race`, sharded across
processes. Long single-goroutine replays run a representative case in race
builds and full length in normal builds.
