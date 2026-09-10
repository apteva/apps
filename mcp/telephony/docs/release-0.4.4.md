# Telephony 0.4.4

Restore the shared-client microphone selector and local Tester APIs missing from
0.4.3. These methods had existed in the integration working copy but were excluded
from the previous release. Both are now part of the integrity-checked served
client, with exported source types:

- `client.listMicrophones()` returns input device IDs and labels without opening
  the microphone. Browsers may hide labels until microphone access is granted;
  unlabeled inputs receive a fallback name.
- `client.createMicrophonePreview(onLevel)` returns a single-use preview with
  `start({inputDeviceId, ...audioOptions})` and async `stop()`. It uses the same
  capture processing as calls, without a carrier, WebSocket, or retained recording.
  Stop releases microphone tracks, the AudioContext, and the worklet URL. Pending
  permission cancellation, denied permission, repeated stop, and cleanup errors
  are covered.
- `configureAudio()` now validates playback bounds just like construction and
  reconnect, before replacing previously valid options.

This is an additive patch on the complete 0.4.3 release. Generic application-user
permissions, authenticated frontend loading, Twilio socket ownership protection,
0 dB microphone / 60 ms playback defaults, configurable playback and carrier
pacing, and human transport diagnostics are retained. No existing client methods
are removed. Original working copies are preserved.

Release verification checks every shared client/controller method currently used
by the consuming microphone-selector integration against the actual served bundle.
Chromium enumerates a device, starts its preview, receives levels, stops it, verifies
tracks ended and no preview WebSocket opened, then answers a call, exchanges audio,
and exercises mute, DTMF, configurable reconnect and hangup. Unit checks cover
permission failure and cancellation; the normal Go/frontend suites retain earlier
regressions.

## Host usage

```ts
const devices = await client.listMicrophones();
const preview = client.createMicrophonePreview(level => updateMeter(level));
await preview.start({ inputDeviceId: devices[0]?.deviceId });
// Refresh labels after permission if necessary.
const labeledDevices = await client.listMicrophones();
await preview.stop(); // also call on close/unmount or before starting a call

phone.configureAudio({ inputDeviceId: devices[0]?.deviceId });
// If a call is already active, apply the selection through audio reconnect:
await phone.reconnect({ inputDeviceId: devices[0]?.deviceId });
```

No consuming-app API change is required; upgrade the Telephony installation and
reload the extension so it loads the new hashed client. This release does not
itself deploy or verify production calls.
