# Telephony 0.12.9

Listening and private coaching now support WebRTC/Opus as well as the existing
PCM/WebSocket transport, independently of the carrier's SIP or media-stream path.
The Calls panel defaults to the softphone's audio settings. Supervisors can
override transport, optional FEC and stereo listening. Device selection, volume,
microphone processing and initial playback reserve share the softphone profile.
Settings apply to the next listener attachment; established sessions continue.

The shared headless backbone exposes the same controls through client `audio`
defaults, `phone.createCallListener(overrides)`, and listener `setAudioOptions()` /
`getAudioOptions()`. The listener snapshot reports the actual media transport,
including automatic fallback. Existing WebSocket audio defaults are preserved.

WebRTC listening uses two receive-only Opus tracks and requests no microphone
access. An explicit coaching push-to-talk press creates a fresh send-only peer
connection for the authorized talk generation. Release, expiry, call ending or
adviser replacement stops the microphone and invalidates queued old-generation
audio. Coaching does not automatically retarget a replacement adviser.

Private delivery follows the adviser's selected browser transport. WebSocket
retains its existing overlay. WebRTC mixes the overlay only into the adviser's
outgoing Opus audio, after carrier, recording and listener taps. Authorization,
leases, bounded queues and 200 ms coaching freshness checks are retained. No
extra carrier stream or telephone leg is created. Coaching stays out of caller
audio, recordings and other listeners.

Local verification passed:

- Full Go suite, Go vet, TypeScript checks and frontend/panel builds.
- 251 frontend/worklet tests and focused listener/coaching race checks.
- Seven real Chromium/local-sidecar scenarios, including WebRTC listening/PTT,
  automatic fallback, a 512 kbit/s link, jitter, Calls panel and application-user
  softphone operation.
- Pion decoding of both listening directions and adviser-only coaching, carrier
  exclusion, generation expiry, bounded queues and FEC opt-out.

The isolated coaching mix/resampler costs 34.8–35.2 microseconds per 20 ms frame
on an Apple M1 Pro. Encoding/network costs are separate; this is not a production
capacity claim. Mixing runs in the browser encoder goroutine, outside the carrier
receive loop. See `docs/private-coaching.md` for verification and API details.

Retains all previous Telephony changes, SDK v0.100.0, minimum Apteva 0.87.4 and
exclusive activation. Publication does not install anything or change carrier
resources/settings in staging or production. App installation uses the existing
exclusive process cutover; schedule upgrades without active calls. Real carrier
and deployment-network validation remains separate from these local tests.
