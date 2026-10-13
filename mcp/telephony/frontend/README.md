# Headless Telephony

Telephony's app-served client provides human call control and browser audio with
no React dependency. The host supplies its own UI. The Calls panel uses this same
controller through `ui/use-panel-softphone.ts`, a React lifecycle/subscription
adapter. Dial, answer, failure cleanup, reconnect, mute, incoming-call selection,
and durable status handling are shared, as are the audio engine, worker and
worklet. Carrier/SIP handling and routing remain in the Telephony sidecar.
Available starting with Telephony 0.3.10.

## Try the example page

From `mcp/telephony`, run `bun run example`, then open
`http://127.0.0.1:5397`. The Northstar example is plain HTML and TypeScript and
imports only the shared Web SDK at runtime. It loads Telephony's built client
using `apps.load()`. Use **Call test line** or **Simulate incoming call** to try
the controls. The local simulator never dials a real number or contacts a carrier;
it discards microphone frames and supplies a quiet generated tone for playback.
The page shows measured client loading time and an integration snippet.

`bun run test:example` verifies outgoing/incoming calls, mute, reconnect, keypad
acknowledgements and mobile layout. The compiled-sidecar integration below tests
both the headless browser host and the Calls panel, including React StrictMode
and retaining an active call when navigating panel tabs.

## Load from an installed app

The host needs `@apteva/web-sdk@0.7.0`. No Telephony npm package, import map, or
Web SDK changes are needed. Build/install this Telephony revision first.

```js
import { AptevaClient } from "@apteva/web-sdk";

const sdk = new AptevaClient({ baseURL, accessToken });
const loaded = await sdk.apps.load("telephony", { projectId, installId });
const telephony = loaded.client; // TypeScript: use apps.load<TelephonyClient>(...).
const phone = telephony.createSoftphone({
  onLevels: (microphone, speaker) => updateMeters(microphone, speaker),
  onDiagnostics: diagnostics => updateDiagnostics(diagnostics),
  ringback: { country: "FR" }, // optional local ringback while an outbound call rings
});
renderCallState(phone.getSnapshot());
const unsubscribe = phone.subscribe(renderCallState);
const incoming = telephony.watchCalls(calls => {
  renderIncomingCalls(telephony.incomingCalls(calls));
});

// Invoke from user actions, which permit browser microphone/audio setup.
await phone.dial({ to: "+12025550100", from: "+12025550101" });
// Or: await phone.answer(callId, { destination_id: browserOfferDestination });
phone.setMuted(true);
phone.sendDTMF("12#");
phone.setOutputVolume(0.8);
await phone.reconnect({ inputDeviceId: selectedMicrophone });
await phone.hangup();

// On host teardown/logout/scope changes:
incoming.close();
unsubscribe();
phone.dispose();
loaded.dispose();
```

The snippet illustrates independent controls, not a recommended automatic calling
sequence. `src/index.ts` exports the TypeScript contracts and local
`telephonyExtension` for hosts building against this source. The default browser
runtime requires HTTPS or localhost, microphone permission, and a user gesture
for audio. Same-origin hosts (including the dashboard) load Telephony's audio
worklet and worker from content-addressed installation URLs compatible with
`script-src 'self'` and `worker-src 'self'`. This selection is automatic for both
call audio and `createMicrophonePreview()`; no host-specific options are needed.
External origins retain embedded blob workers/worklets and must permit those
in their CSP, as well as the gateway's HTTPS/WSS connections. The Web SDK's
`apps.load()` loader also requires permission for its blob module imports;
this Telephony change does not alter the SDK's own module-loading policy.

## Lifecycle and recovery

- Keep one controller for an operator across page navigation; do not recreate it
  on each render. Snapshots exclude media URLs and session credentials.
- Dial preflights the microphone and retains an idempotency key across retries
  of the same request on that controller. For recovery across reloads, supply and
  persist your own `idempotency_key` and exact request. Writes are not retried
  automatically. Failed audio setup attempts to hang up an accepted outbound leg.
- Answer claims a browser offer before attaching audio. Failed setup releases
  that claim. If cleanup fails, `getSnapshot().callId` remains available for
  explicit recovery. `join(callId)` explicitly rejoins a known call after reload.
- `hangup()` also cancels device setup, even while a permission prompt is open.
  If placement is already in flight, the controller stays busy until its outcome
  is known and attempts to hang up any accepted leg. Local audio stops immediately
  when hanging up an identified call; failed hangup retains controls for retry.
- Reconnecting your own call uses `join(callId)` or `answer(callId,
  { rejoin: true })`; taking another user’s call requires `takeover(callId)` and
  explicit supervisor permission.
- Media failure preserves carrier call identity. `reconnect()` recreates audio
  and preserves mute; durable terminal status stops devices and monitoring.
- `dispose()` releases local audio and polling; it does **not** hang up an
  established carrier call. Await `hangup()` first when that is the intent.
  In-flight placement/answer operations continue best-effort cleanup when their
  responses arrive; `dispose()` itself is synchronous.
- Transport health is checked in the worker with heartbeats. A ringing call can
  remain connected without a carrier audio peer. Stalled sockets/handshakes retry
  with a bounded budget; mute survives reconnect and buffers discard stale audio.
- Snapshots expose `phase` (`idle`, `placing`, `ringing`, `connected`, `ended`)
  next to the raw `carrierStatus`. After a call ends, `termination`
  (`reason`, `cause`, `code`, `initiator`), `answeredBy`, and `endedAt` stay on
  the snapshot until the next dial or answer. `detail` is cleared at call end as
  before.
- `ringback` plays a locally synthesized tone through the SDK's own audio
  context from ringing until answer, so it follows the selected output device.
  It is off by default; pass `true` or `{ country }` (France otherwise). The
  tone follows the call's direction, so a call placed by a backend and joined
  with `attach()` or `takeover()` rings like one placed with `dial()`.
- The media socket pushes `call.status` frames (status, answered and ended
  times, `termination`, `answered_by`), so phases update without polling.
- Active-call reconciliation polls every two seconds without overlapping reads.
  Pass `pollIntervalMs: 0` and feed `observeCall()` if the host already watches
  calls. `watchCalls()` is an optional cancellable watcher for incoming calls.

Worker and worklet source are embedded in the hashed client bundle. Same-origin
audio assets are fetched through the original authenticated app handle and
checked against those embedded bytes before use. Native module requests use
the public `/ui/frontend/` routes with explicit project/installation scope and
no credential in the URL. Deploy the hashed audio files together with the
client and panel; retain old hashed audio files for already-loaded clients.
For external origins, blob URLs are created for each audio session and revoked
on cleanup. They contain no credentials. Media URLs resolve against the SDK's gateway and must match the
selected installation and call; they are never pinned to the external host's
origin. Media session URLs/tokens are credentials: keep them in memory.

## Script-only control

`TelephonyClient` never accesses devices until `createSoftphone()` is used to
start audio. A trusted Bun script can import the local extension:

```ts
import { AptevaClient } from "@apteva/web-sdk";
import { telephonyExtension } from "./frontend/src/index";

const sdk = new AptevaClient({ baseURL, apiKey }); // trusted backend only
const telephony = sdk.use(telephonyExtension, { projectId, installId });
const calls = await telephony.listCalls();
await telephony.hangup(callId);
```

Low-level `place({ ..., idempotency_key })`, `answer()`, and `release()` expose
human call sessions for custom media adapters. They do not attach audio or
perform browser cleanup; browser hosts should use the controller. These methods
are separate from Telephony's AI-call MCP tools. The browser `apps.load()` blob
loader is not a portable Node module distribution mechanism. The extension can
also be bundled locally for script hosts. No standalone npm package is published.

## Access and scope

Telephony 0.4.0 supports application-user softphones with verified online Auth
sessions or scoped platform delegated identities. Configure installation grants
and use `clientOptions: { authProvider: "customer-login" }` when loading the app
for an online Auth session. The same controller enforces user ownership and
renews short-lived media leases automatically. Operator access remains supported.

See [Application-user setup, routing and backend attachment](../docs/application-users.md)
for the complete policy, authentication, ringing, revocation and supervisor model.
`phone.attach(callId)` connects a backend-assigned human call without redialing;
`phone.takeover(callId)` is a separate explicit supervisor action.

## Build and verify

From the apps repository:

```sh
bun install --frozen-lockfile
bun run scripts/build-panels.ts --app telephony
cd mcp/telephony
bun run test:frontend
env GOWORK=off go test -short ./...
env GOWORK=off go test -tags=integration -run '^TestTier2HeadlessBrowser$' -count=1 -timeout=3m .
```

The integration test requires Playwright Chromium. It starts a compiled Telephony
sidecar, a controlled carrier/platform gateway and a separate browser host. That
host imports only the shared SDK, loads the app's actual `/ui/frontend.json`, and
uses the real browser audio engine. It verifies incoming answer, generated audio
in both directions, mute/reconnect, acknowledged DTMF and hangup. No live carrier,
paid phone call, or physical headset is exercised. Fixture operator authentication
does not establish external application-user authorization.

The new controller and existing worker/worklet tests run in separate Bun
processes: the existing UI tests evaluate worker scripts as code, while the
headless bundle imports the same files as text assets.

### Microphone selector and local preview (Telephony 0.4.4+)

The served client includes `listMicrophones()` and
`createMicrophonePreview(onLevel)`. Enumeration returns `{deviceId, label}[]`
and does not request permission. Start the preview from a user gesture, then
refresh the device list if the browser initially hid labels:

```ts
const inputs = await client.listMicrophones();
const preview = client.createMicrophonePreview(level => updateMeter(level));
await preview.start({ inputDeviceId: inputs[0]?.deviceId });
// Call when stopping the test, closing the selector, or starting a call:
await preview.stop();
```

A preview is single-use; create a new one for each test. It is local, uses the
shared capture engine, and opens no carrier or audio WebSocket. Microphone tracks,
AudioContext and worklet URLs are released on stop or startup failure. The meter
returns to zero on stop. `phone.configureAudio({inputDeviceId})` selects the device
for future calls; `phone.reconnect({inputDeviceId})` applies it to an active call.
The 0.4.3 gain/playback options remain available.

## Incoming call notifications

From Telephony 0.5.1, `watchCalls()` uses authenticated fetch/SSE hints by default
and retains polling for recovery. The native Calls panel uses this same watcher.
Existing integrations benefit after reloading the updated installed client.

```ts
const watcher = telephony.watchCalls(renderCalls, {
  push: true,          // default; false uses polling only
  intervalMs: 2000,    // recovery; use 500 for faster polling when push is unavailable
  onTiming: ({trigger, fetchMs}) => recordCallListTiming(trigger, fetchMs),
  onError: reportCallListError,
});
// On logout or a change of project, installation, or authenticated user:
watcher.close();
```

One list request runs at a time; bursts coalesce into one follow-up refresh.
Reconnects use the current SDK credentials and refetch authorized state. No token
is placed in a stream URL. Missing streaming support in older servers or host SDK
handles leaves polling active. Hints contain no call data and reveal changes only
to visible calls/offers. `fetchMs` measures the list request locally, not total
caller-to-ringtone latency. Routing traces expose separate `dispatch_delay_ms`.

Online sessions and Telephony grants are rechecked on each hint/heartbeat;
gateway-delegated issuer sessions are revalidated on the twenty-second stream
lease reconnect. A revocation closes push and triggers an authorized refresh;
recreate the watcher after renewed login to restore push. `close()` or abort stops
subscriptions, timers and late callback delivery.


## Media session recovery

Adviser reconnect obtains a fresh authorized `/softphone/attach` session for the
same call. It does not dial or answer another carrier leg. Temporary renewal
failures retry within the current lease; confirmed revocation or expiry stops
local media while durable call status remains authoritative. Listening and
coaching share lease renewal; coaching never automatically retargets an adviser.

`createSoftphone({ onSessionEvent })` and
`createCallListener({ onSessionEvent })` expose structured renewal and recovery
events. Adviser `onDiagnostics` includes bounded session events and directional
frame-drop samples. See [media session resilience](../docs/media-session-resilience.md)
for clock handling, authorization boundaries and diagnostic fields.

## Connection and recovery correlation

Both browser transports and the headless backbone retain bounded structured
session events. `recovery_id` identifies one recovery chain; `attempt_id`
identifies an individual authorization/connection attempt. A coalesced refresh
also reports `shared_attempt_id`. Outcomes include started, failed, connected,
timed out, revoked and cancelled. These IDs confer no access permissions.

`/softphone/attach` and `/softphone/takeover` accept optional `media_diagnostics`
with those IDs and an `initiating_action`. The response includes a nonsecret
`session_generation`. Server events link previous/new generations, issuance
UTC time, issuer identity and the affected browser connection. The generation
can appear in an attachment query for correlating a rejected stale credential;
it never replaces token validation. Existing clients remain compatible.

```ts
await phone.reconnect(undefined, "manual_reconnect");
await phone.reconnect({ inputDeviceId: selectedMicrophone }, "audio_device_change");
await phone.attach(callId, "component_recreation");
```

Server disconnect events include canonical error class/detail (EOF, reset,
timeout, local closure or protocol error), close code, shutdown intent, last
socket/audio read and write, ping/pong UTC times, recent browser measurements
and pre-cleanup WebRTC states. `last_browser_sample_at` makes snapshot age
explicit. A WebRTC signaling failure is preserved before its internal media
pipe is closed. Session replacement/rejection and setup failure are separate
events. Selected media endpoints remain linked by `connection_id`.

Collection uses the existing 1,024-event nonblocking background queue. Socket
history is capped at 64 events; session history at 50. Persistence is batched
and has a two-second budget. Queue pressure increments the skipped counter;
it cannot reject a call or delay audio for a database write. Frame activity
uses only atomic timestamps. No new media timers or carrier operations are
introduced. Tokens, token hashes, headers, SDP, request URLs and arbitrary
underlying error strings are excluded; canonical error details are retained.
This is observational evidence, not proof of which network hop failed.

## Optional browser transport

`createSoftphone({ mediaTransport: "websocket" | "webrtc" | "auto" })` selects
media for the shared browser runtime. WebSocket remains the default. WebRTC
requires an enabled Telephony server and reachable ICE/UDP or TURN; explicit
WebRTC reports setup failure, while `auto` can fall back during initial setup.
See [softphone transports](../docs/softphone-transports.md) for configuration,
recovery, provider independence, diagnostics and verification limits.

### Optional WebRTC packet recovery

Opus FEC is enabled by default when the server has a compatible native codec.
It can be disabled for one softphone through the existing audio options:

```ts
const phone = telephony.createSoftphone({
  mediaTransport: "webrtc",
  audio: { webrtcFec: false },
});

// Change the next call's preference without altering active audio.
phone.configureAudio({ webrtcFec: true });

// Or explicitly reconnect the browser audio on the existing carrier call.
await phone.reconnect({ webrtcFec: false });
```

The preference is retained through automatic audio recovery and is scoped to
each media attachment. Opting out disables redundancy while retaining native
Opus encoding/decoding when available, disables SDP requests for redundant audio in both directions,
and retains bounded buffering, ordinary packet-loss concealment, bitrate
adaptation and diagnostics. It does not change PCM/WebSocket audio or another
softphone's codec. The Telephony audio settings also expose this preference
when WebRTC or automatic transport is selected. Updating the saved preference
does not restart the app or modify an already established audio session.

## Automatic outbound SIP setup

Operators can select **Direct SIP / RTP → Automatic setup** under a carrier
account or one owned number in Telephony. The same operation is available
through the headless client and `telephony_outbound_transport`:

```ts
const options = await telephony.outboundTransport({
  carrier_connection_id: 10,
  setup_options: true,
});

await telephony.outboundTransport({
  carrier_connection_id: 10,
  // Optional: omit to set the account default.
  phone_number: "+33123456789",
  transport: "sip_direct",
  provision: true,
  // Telnyx only; omit when the account has exactly one enabled profile.
  outbound_profile_id: "existing-enabled-profile-id",
});
```

Telnyx setup creates a dedicated TLS credential connection, attaches the chosen
Outbound Voice Profile, and translates codecs to the provisioning API's names.
Twilio setup creates a secure termination trunk, a SIP credential list and its
credential, then attaches that list to the trunk. Both generate separate SIP
credentials and persist them encrypted; passwords are never returned. Carrier
API credentials remain in the authorized integration account.

When creating a Telnyx profile, supply `profile_policy` instead of
`outbound_profile_id`:

```ts
await telephony.outboundTransport({
  carrier_connection_id: 10,
  transport: "sip_direct",
  provision: true,
  profile_policy: { destinations: ["FR", "ES"], daily_spend_limit: "20.00" },
});
```

The spending limit is in USD. Telephony never chooses between multiple enabled
profiles or invents a destination/spending policy. A missing or disabled profile
fails setup. Country codes are normalized and validated locally; the carrier
also validates supported countries.

The SIP gateway needs TLS configuration, a usable hostname certificate, SRTP
support and reachable signaling/media ports. Setup validates local configuration
and the certificate before provisioning; it does not test external reachability
or bind/restart the listener. A carrier signaling and two-way audio test is still
needed to validate a deployment.

Configuration requests share a cancellable 30-second setup budget. Setup is
serialized per project/account independently of media handling. A persisted
journal records generated credentials, intended calling policy and intermediate
resource IDs. Retry resumes completed steps. If a create response is lost, setup
reconciles the exact managed resource before continuing; if it is not yet visible,
setup waits for a later retry instead of issuing another create. Restore the
persisted `outbound-sip.key` if it is lost; setup does not rotate credentials to
hide a key failure.

Only successful verification changes outbound admission settings. Existing
calls retain their transport. Setup neither assigns inbound numbers nor changes
inbound routes, provider recording or shared third-party authentication settings.
Number ownership, permissions and provider readiness remain checked on new
calls. Resetting to Voice API retains the managed SIP resources for later reuse;
it does not delete resources potentially used by active calls.

The advanced existing-resource form remains supported: use `config`, plus
write-only `username`/`password` for digest authentication, without `provision`.
Omitted credentials retain the existing exact scope's encrypted credentials.
Automatic setup can reuse a saved verified selection, including an inherited
account selection, without creating or rotating credentials. To change the
profile on an already managed connection, configure that resource explicitly
through advanced settings; automatic setup does not rewrite existing policy.


### Listening and private coaching transport

The Calls panel defaults these sessions to the softphone's audio settings, with
separate WebSocket/WebRTC/automatic, FEC and stereo overrides. Headless hosts can
share a profile across instances or derive a listener from the current phone:

```ts
const client = new TelephonyClient(app, {
  audio: { mediaTransport: "webrtc", webrtcFec: true, outputVolume: 0.8 },
});
const phone = client.createSoftphone();
const listener = phone.createCallListener({ stereo: true });
// Optional override: phone.createCallListener({ mediaTransport: "websocket" })
await listener.coach(callId); // receive-only; no microphone permission
await listener.startTalking(); // explicit push-to-talk
listener.stopTalking();
console.log(listener.getSnapshot().mediaTransport); // actual chosen transport
listener.setAudioOptions({ mediaTransport: "auto" }); // next attachment only
```

`client.createCallListener()` inherits client-level `audio` defaults. Per-phone
`audio`/`mediaTransport` overrides are inherited by `phone.createCallListener()`.
`createCallListener({ audio: {...} })` also accepts the same nested profile as
`createSoftphone`; explicit top-level listener options take precedence.
Explicit WebRTC fails when unavailable; automatic mode may fall back only before
ready. Listening never opens a microphone. WebRTC coaching uses a new uplink PC
per press, retaining the existing adviser pin, authorization, short PTT lease,
and no-auto-retarget behavior. The adviser's selected media transport determines
private delivery, without adding a carrier stream or including coaching in
recordings/listener taps. See [listening](../docs/live-listening.md) and
[coaching](../docs/private-coaching.md) for limits and diagnostic differences.

### Media readiness (0.12.13)

`call.status: answered` is a signaling fact. `media.ready` means Telephony has an
answered call, the current browser session and an attached carrier bridge, with
hold inactive. `media.not_ready` revokes that state during a bridge interruption,
hold or call ending. Both arrive on the existing media control socket and are
retained in `browser_audio_diagnostics.server.startup.readiness_events`.

```ts
const phone = telephony.createSoftphone({
  onMediaReadiness(event) {
    // type, call_id, connection_id, revision, bridge_generation, occurred_at, reason
    console.log(event.type, event.reason);
  },
});
phone.subscribe(snapshot => {
  // Display connected only here: carrier answer AND browser media readiness.
  const connected = snapshot.phase === "connected";
});
```

The revision is ordered within the active socket; discard older revisions and
reset on a new socket. The headless client handles this automatically. Local
`audioState: live` also checks the browser transport/audio readiness. Neither
event proves that the remote person heard speech. `carrierStatus` retains the
actual signaling status while `phase` remains `placing` until audio is ready.

## Optional managed execution transport (0.12.14)

`TelephonyClientOptions.runtime` accepts `"auto"` (default), `"proxy"` or
`"direct"`. The app-owned client discovers deployment endpoints and obtains a
scoped browser credential through the management proxy. Eligible platform users
then use the remote execution for call HTTP, SSE, media/control sockets and
listen/coach sessions. No host credentials are sent to the remote execution.
Legacy/local installations continue using the existing proxy.

Delegated `authProvider` clients retain the proxy because the current Server
credential contract supports platform users at its own managed origin only.
An explicitly bundled client needs rebuilding to use the new transport; dynamic
`apps.load()` clients receive it from the installed app. The browser must allow
the advertised origins in its CSP. Raw SIP/RTP and WebRTC UDP networking remain
backend configuration responsibilities. See
[the runtime guide](../docs/managed-runtime.md) before opting into placement.
