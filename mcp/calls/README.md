# Calls

Calls is Apteva's small-room WebRTC application. Media travels directly
between participants in a browser mesh; the sidecar provides authenticated
room membership, signaling, presence, chat, transcripts, and lifecycle events.

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
bun run scripts/build-panels.ts --app calls
```
