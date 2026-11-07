# Calls

Calls is Apteva's small-room WebRTC application. Media travels directly
between participants in a browser mesh; the sidecar provides authenticated
room membership, signaling, presence, chat, transcripts, and lifecycle events.

## Embed Calls in your UI

Calls 0.3.0 serves a headless browser client through the Apteva Web SDK
(`@apteva/web-sdk@0.7.0` or newer). The dashboard's **Join as host** control
and the hosted invite page use the same session controller.

Give the browser a room invite token issued through `calls_create_join_token`.
Use a guest token for guests and keep any platform API key on your backend.
Pass the project's Calls installation ID and project ID to the SDK. For an
external browser origin, add that exact origin to both the Apteva gateway
CORS allowlist and the Calls installation's `allowed_embed_origins` setting.
A same-origin reverse proxy for the app API can instead be passed as
`clientOptions: { apiBaseURL: "/calls-proxy" }`. Serve the embedding page over
HTTPS (or localhost) and permit the Web SDK's blob module imports in its CSP.

```js
import { AptevaClient } from "@apteva/web-sdk";

const sdk = new AptevaClient({ baseURL, accessToken });
const loaded = await sdk.apps.load("calls", { projectId, installId });
const room = loaded.client.createSession();

function render(snapshot) {
  // The host owns its controls and video elements.
  localVideo.srcObject = snapshot.screenStream ?? snapshot.localStream ?? null;
  renderRemoteVideos(snapshot.remoteStreams);
  renderRoomState(snapshot);
}

const unsubscribe = room.subscribe(render);
render(room.getSnapshot());
await room.join(inviteToken, displayName);

// Call these from UI controls so the browser can prompt for device permission.
micButton.onclick = () => room.setMicrophone(!room.getSnapshot().microphone).catch(showError);
cameraButton.onclick = () => room.setCamera(!room.getSnapshot().camera).catch(showError);
shareButton.onclick = () => room.setScreenShare(!room.getSnapshot().screenStream).catch(showError);

async function unmount() {
  unsubscribe();
  await room.leave();
  room.dispose();
  loaded.dispose();
}
```

Use a muted, inline local video element and give each remote stream an
autoplay, inline video element. The snapshot also exposes participants, chat
messages, capabilities and terminal session state. Use `sendMessage(body)` for
chat and `end()` for participants with the room control capability. Calling
`dispose()` directly stops local devices and peers immediately and attempts to
leave the room. TypeScript contracts live in [frontend/index.ts](frontend/index.ts).

## Security model

- Join links are expiring, revocable, usage-limited bearer credentials. New
  credentials are stored as SHA-256 hashes; the raw token is returned once.
- A successful join returns a separate participant bearer token. Every room
  API except `/api/join` requires it. Participant tokens are also stored only
  as hashes and are scoped to one project, room, and active participant.
- Capabilities gate chat, transcript access, and media sections. Public message
  reads filter `private` and `internal` visibility.
- Public JSON bodies and signaling payloads are bounded. The join page uses a
  nonce-based CSP, no-referrer policy, and no-store caching.

## WebRTC topology

The browser implements the WebRTC perfect-negotiation pattern and creates one
peer connection per other active participant. Configure `ice_servers_json`
with production TURN credentials for reliable NAT traversal. Mesh bandwidth is
quadratic, so keep `max_participants_per_room` conservative; use an SFU-backed
app for large meetings or server-side recording.

## Lifecycle

Browsers heartbeat every 15 seconds. Three missed heartbeats close the
participant, sessions, tracks, and pending signals atomically. Empty rooms are
ended after `idle_room_timeout_seconds`. Ended rooms cascade-delete after
`retention_days`.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
cd ../..
bun install --frozen-lockfile
bun run scripts/build-panels.ts --app calls
bunx playwright install chromium
CALLS_BROWSER_TEST=1 GOWORK=off go -C mcp/calls test -run TestSharedCallsBrowser -v
```
