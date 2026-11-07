# Telephony 0.17.1

Outgoing browser audio no longer loses speech when the sender falls behind, and
recording conversion no longer competes with live calls for CPU.

## Outgoing WebRTC audio (softphone and listeners)

- RTP timestamps now number the audio frames instead of following the wall
  clock. A tick that wakes late is repaid: up to 3 frames per tick until the
  sender is back on time, for delays up to 120 ms. The browser sees jitter, not
  a hole, and no queue is left behind.
- A longer pause still jumps the timeline and counts in `pacing_skipped_ms`, but
  whatever is queued now drains at once instead of staying as latency.
- While behind, silence takes a slot only after a whole tick in which the delayed
  audio still did not arrive.
- Speech is shortened only when it has stayed older than 80 ms for 100 ms, then
  at most every 100 ms. It used to be shortened whenever a backlog lasted 400 ms,
  however young the audio was. Silence trimming and the queue and expiry limits
  are unchanged.
- Trade-off: audio that arrives late from the source can now add up to about
  80 ms until the next pause, where it used to be trimmed sooner. Correction: on
  lines with background noise at about -60 dBFS or louder no pause qualified, so
  the delay stayed for the rest of the stream. Fixed in 0.17.2.
- New counter `pacing_catchup_frames`, also on the audio dashboard as
  `server_webrtc_pacing_catchup_frames`. Catching up is not reported as degraded
  audio.

## Recording conversion

- WAV to MP3 now runs in a separate process: the same executable, at nice 10 on
  all its threads, with `GOMAXPROCS=1` and none of Telephony's environment.
- The recording job is unchanged: claims, retained originals, retries, size
  limits and upload verification stay in Telephony. MP3 output is byte-identical.
- The worker exits if Telephony goes away. A worker that started and failed is an
  ordinary failed conversion, retried by the job.
- If the process cannot start, conversion runs inside Telephony and a warning is
  logged.
- LAME now takes about one MP3 frame per call and yields between calls, so the
  fallback path also leaves room for other work. Short calls alone did not help:
  the yield is what mattered in the measurements.

## Evidence and limits

- Stall simulation over the real framer, queue and tick (5 to 100 ms stalls, both
  orderings at the end of a stall): no timestamp holes, no speech removed, and
  latency back to normal. The frozen 0.17.0 sender shows holes and removed speech
  in the same scenarios from 40 ms up.
- The real sender loop on a real WebRTC transport, stalled for 80 ms twice: every
  packet continuous and voiced.
- Linux VM, Telephony at nice -5 as an unprivileged user: all worker threads at
  nice 10, all Telephony threads still at -5.
- With a single Go scheduler slot, 20 ms timers more than 10 ms late during
  conversion: 63% with 16 KiB batches, 70% with one-frame batches and no yield,
  about 0 to 2.5% with the yield, and about 0 to 1.7% with the worker. The baseline
  during those last runs was 1.5 to 2.8%, on a heavily loaded host. Only the
  single-slot comparison is conclusive: with two slots the host noise hid the
  difference.
- Not measured on a production or staging call. Whether conversion was the cause
  of the reported gaps is not proven, and gaps in browser microphone uploads are
  a separate issue.

No migrations, settings or tools changed. Full suite passes; race suite passes
at normal load (`TestAgentAudioSIPWireChunkContinuity` and others are
timing-sensitive on an overloaded host, on 0.17.0 as well).
