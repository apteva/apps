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
for audio. The host must permit SDK blob modules, blob workers/worklets and the
Telephony gateway's HTTPS/WSS connections in its CSP.

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
- Active-call reconciliation polls every two seconds without overlapping reads.
  Pass `pollIntervalMs: 0` and feed `observeCall()` if the host already watches
  calls. `watchCalls()` is an optional cancellable watcher for incoming calls.

Worker and worklet source are embedded in the hashed client bundle. Blob URLs
are created for each audio session and revoked on cleanup. They contain no
credentials. Media URLs resolve against the SDK's gateway and must match the
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
