# Telephony 0.9.1

Cumulative release retaining all Telephony 0.9.0 functionality and adding bounded
handling of delayed carrier media, directional reception monitoring and diagnostics.

## Changes

- Preserve carrier source timestamps, chunks and stream epochs through the human
  media bridge and negotiated APT3 browser playback. Legacy clients retain APT2
  or plain PCM; AI peers retain their existing plain PCM and buffered policy.
- Discard stale or duplicate received audio with separate counters and a 320 ms
  source excess-age budget. Keep playback queues bounded, including when a slow
  connection prevents clock probes. Missing frames are not counted as discarded PCM.
- Diagnose caller-direction stalls independently of adviser audio for continuous
  Twilio/Telnyx media; report interruption and recovery through the shared client.
  Intentional microphone mute and silent PCM do not create caller-side incidents.
- Queue WebSocket pong replies through the sole writer without blocking the
  media reader. Expiring source audio does not close a healthy media connection.
- Retain source timing, directional queues, discard counters, browser/device
  state and explicit operator interrupt attribution in diagnostics.
- Expand the local browser benchmark to 13 profiles, including ten-second carrier
  catch-up, missing frames, intentional mute and browser reconnection. Include
  regression tests, durable verification evidence and regenerated frontend/panel
  assets. Pin the SDK to v0.91.0, the latest release by commit topology.

## Verification and limits

- 625 Go cases/subtests and 115 frontend/audio tests passed; focused application
  and harness race checks, build, vet and type checks passed.
- Release validation synchronizes the disk, embedded and frontend versions,
  rebuilds the Calls panel, and reruns Go tests against the final SDK dependency.
  The earlier network measurements used SDK v0.90.0; v0.91.0 adds MCP tool
  annotations without changing the audio transport.
- Actual Twilio JSON/G.711 bridge tests cover stale catch-up and healthy adviser
  transmission. Concurrent regression isolates 24 calls.
- Ten-second catch-up discards 9,680 ms of received stale PCM and recovers without
  disrupting adviser transmission. Missing-frame and mute/reconnect checks pass.
- The durable 20-second matrix contains one marginal Wi-Fi gate failure (4 of 38
  adviser markers missing). Two subsequent 60-second Wi-Fi runs on different seeds
  delivered 118/118 markers each way. The short failure remains in the evidence.
- 256 kbit/s links remain degraded: raw PCM requires 384 kbit/s per direction
  before framing. Synthetic markers are not a perceptual speech-quality score.

See [verification](carrier-media-verification-2026-10-01.md) and
[design](carrier-media-delivery.md). This release bounds delayed-media handling;
it does not establish the origin of the reported production Twilio stalls or
restore speech already delivered ten seconds late.

Publishing the source and marketplace entry does not update any installation.
No staging or production instance, carrier configuration, route or number was
changed, and no live carrier call was placed.
