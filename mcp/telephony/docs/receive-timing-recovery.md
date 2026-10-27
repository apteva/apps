# Receive timing recovery and optional delay distributions

This change concerns the two server receive paths: carrier RTP into SIP playback,
and browser Opus RTP into Telephony's decoder and microphone forwarding path.
The browser's native WebRTC playback engine remains responsible for its own
adaptive jitter buffering. This is not an import of NetEQ or a claim of equivalent
perceptual quality.

## Configuration

Existing defaults remain SIP `ewma` and WebRTC `fixed`. Guarded WebRTC timeline
recovery applies automatically in either mode. No ICE restart is required.

- SIP audio profile: set `rtp_playback.delay_estimator` to `distribution`.
  `ewma` restores the existing target estimator. Distribution estimation is used
  by the `adaptive` receiver; it does not change reference-receiver behavior.
  Existing project/number/adviser profile resolution and revision pinning apply.
- Telephony settings: set `softphone_webrtc_receive_estimator` to `distribution`.
  `fixed` restores the existing 60 ms reserve. This applies to new browser and
  coaching receive sessions. It is an installation setting, not an adviser
  override.

Both settings are selected for new media sessions. No app redeployment is needed
to change them; existing calls retain their selected policies.

The distribution estimates relative arrival variation, not absolute network
latency. It retains ten seconds of observations in 100 ms buckets, selects a
95th-percentile delay with a 20 ms cushion, increases its target promptly and
reduces it by at most 5 ms per second. Bucketing prevents a large catch-up batch
from receiving an arbitrarily large statistical weight. Duplicates cannot add
independent observations.

SIP uses the profile's existing target bounds (normally 60–100 ms), capacity
(normally 160 ms), local age limit (normally 200 ms), concealment, and bounded PCM
sample correction. WebRTC moves its actual reserve gradually, at most 125 µs per
forward admitted packet, within 60–100 ms. Its 16-packet queue, 320 ms mapped-time
admission limit, and downstream 250 ms source-age limit remain. Growing a target
cannot recover absent speech or justify an unlimited backlog.

No general WSOLA receiver stage is added. The existing optional AI playback
correction is separate. Additional time scaling needs its own listening-quality
and speech-integrity acceptance tests.

## Recovery of a stuck WebRTC timeline

After a timing rejection, a replacement timeline requires six consecutive,
forward-sequence packets over at least 100 ms. They must belong to the same SSRC,
decode as Opus in a separate validation decoder, have plausible advancing RTP
steps and paced arrival intervals, and agree on the timing offset within 40 ms.
A two-second cooldown bounds repeat resets. Isolated reorder, duplicate packets,
malformed payloads and an instantaneous backlog cannot establish a new epoch.

A qualified reset drops and accounts for the old encoded and decoded queues,
clears the old loss-repair cursor, and establishes a 60 ms reserve on the newest
validated packet. It retains active decoder state and uses the existing brief
crossfade on resumed PCM. A reset does not reopen sockets or interrupt the
opposite audio direction. This is a recovery from a broken clock mapping, not a
blanket relaxation of packet age.

## Diagnostics

`receive_timing` contains bounded reason counters and the most recent 32 events.
Events include connection/path identity, SSRC, sequence, RTP timestamp, playback
cursor, timing offset, reserve, queue size and epoch. Reasons distinguish invalid
payload size, foreign SSRC, duplicate timestamp, already-played timestamp, early
or late mapping, capacity, expired playout and replacement of an old epoch.

`resynchronizations` counts timeline changes. `decoded_recoveries` requires a
successful real decode. `bridge_handoffs` means the first recovered frame was
passed to the bridge callback; it does **not** mean the downstream queue accepted
it or that a caller heard it. SIP transmission remains separate evidence. The
existing aggregate rejection/discard counters remain available. Diagnostic
snapshots own their maps and event slices and are safe to read concurrently.

## Local evidence

See [verification results](verification/receive-timing-20261027.json). Replays use
real encoded speech and production decoders, release queues and SIP pacer; the
wire check sends actual PCMA RTP over loopback UDP.

| Controlled case | Released 0.12.59 / existing mode | New behavior |
| --- | --- | --- |
| Five healthy minutes, then RTP timestamp −500 ms | 1,500 subsequent packets rejected | Five rejected during qualification; one reset; first decoded release 160 ms after disturbed arrival |
| Persistent +500 ms delivery offset or timestamp reset | Same sustained rejection | Same bounded recovery |
| Every tenth WebRTC packet delayed 95 ms, last 40 s measured | 200 rejected; max residence 80 ms | Zero rejected; max residence 100 ms |
| SIP ordinary 0–18 ms arrival variation, G.722/PCMA/PCMU | Mean late-run residence 79.02 ms | 59.01 ms, no extra discard or concealment |

Across the full 50 seconds, including adaptation, WebRTC rejections fell from
250 to 13; the final 40 seconds had 200 versus zero.

The WebRTC comparison improved the source-aligned active-speech coverage proxy
from 96.85% to 99.91%, and longest damaged speech from 40 to 20 ms. Healthy WebRTC
PCM is bit-identical to an independent clean decode in both estimator modes.
SIP waveform coverage did not regress in the ordinary-variation comparison.
These waveform measures are regression proxies, not MOS scores or human listening
results. Measurement-window tails can count as undelivered speech.

The 160 ms recovery measurement starts at the first disturbed arrival: it excludes
any preceding network outage. Five qualification packets are lost, and a new
baseline cannot prove their original capture age. This is not lossless recovery.
No packet-timing capture exists to establish that the reported production call
had the same trigger. Severe bursts and actual packet loss can still damage
speech; increasing the buffer does not universally improve them. No production,
staging or real carrier call was changed or tested as part of this patch.

A harsher SIP test delays four packets in every 500 ms interval by another 80 ms.
Both estimators settle at about 100.12 ms mean residence. Distribution caused no
measured waveform/discard regression, but did not remove the severe-burst damage:
G.722's longest damaged speech remained 180 ms; PCMA/PCMU remained 120 ms.
This is a reason to keep the new estimator optional, not claim universal repair.

Validation completed: full Go suite (448.366 s), targeted race tests including
the supplemental burst/cooldown/allocation cases (31.844 s), and all 437 frontend
tests. Delay estimation allocates no heap memory per observation.
