# tapo

Local-LAN control of TP-Link Tapo cameras for Apteva. Talks the
camera's HTTPS JSON-RPC protocol directly — no cloud, no Tapo
account required.

## Setup

1. **Create a Camera Account** in the Tapo mobile app:
   `Settings → Advanced Settings → Camera Account`. This gives you a
   local username + password the camera will accept on its on-device
   API. Your TP-Link cloud login does **not** work for this.

2. **Find the camera's IP** on your LAN. The Tapo app shows it under
   `Camera Settings → Device Info`. A static DHCP lease is wise — the
   camera registry stores the IP, not the hostname.

3. **Register it** from the dashboard's Cameras panel, or via MCP:

   ```
   cameras_add(
     name="porch",
     room="outside",
     ip="192.168.1.42",
     username="apteva",
     password="hunter2",
   )
   ```

   The `cameras_add` call probes the camera, so a wrong password or an
   unreachable IP fails the registration immediately rather than
   leaving a half-row.

## Firmware support

| Auth scheme | Firmware | Status |
|---|---|---|
| MD5 + stok cookie | ≤ 1.3.x | ✅ supported |
| KLAP handshake | ≥ 1.4.x (~mid-2023+) | 🚧 scaffolded, not wired |

If `cameras_add` fails with `error_code=-40401` or 401-loops, your
camera is on KLAP firmware. Two paths:

* Pin the camera to ≤ 1.3.x via the Tapo app (disable auto-update,
  factory-reset, install old firmware bundle).
* Contribute the KLAP path — see `tapo_client.go` ⇒ `klapAvailable()`
  for the integration point.

## Composition with `storage`

`snapshot_capture(save_to_storage=true)` pushes the JPEG into the
`storage` app via `files_upload` and returns the storage file_id, so
the agent can hand out a signed URL through `files_get_url`. The
motion poller does the same automatically when
`default_snapshot_on_motion=true` (the default), and stamps each
`motion_events` row with the resulting `snapshot_file_id`.

If `storage` isn't installed, set `default_snapshot_on_motion=false`
and only use `snapshot_capture` with `save_to_storage=false` — that
returns the bytes inline (capped at 4MB).

## Streaming

`stream_get_url` returns an `rtsp://user:pass@ip:554/stream1` URL
embedding the camera-account credentials. Treat it as a secret — the
URL itself is the bearer token. HLS is intentionally not implemented;
adding it requires bundling ffmpeg into the sidecar image and running
a transcoder, which is out of scope for v0.1.

## Events

Motion events are emitted on the platform bus as `tapo.motion`:

```json
{
  "camera_id": 3,
  "camera_name": "porch",
  "occurred_at": "2026-05-03T12:34:56Z",
  "kind": "person"
}
```

Other apps (`todo`, messaging) subscribe to react. The CamerasPanel
also subscribes and flashes the affected tile.

## Credentials at rest

By default, camera passwords are stored as plaintext in the app's
private SQLite DB at `/data/tapo.db`. The DB file isn't accessible
from outside the install, so this is fine on a trusted single-tenant
host. On shared infra:

```bash
# 32 bytes, base64
APTEVA_SECRET=$(openssl rand -base64 32)
```

…or set `shared_secret` in the app's config schema. With either set,
new `cameras_add` calls AES-GCM-encrypt the password; old plaintext
rows are migrated transparently on next write.

## Why not an `integration`?

Tapo isn't a cloud SaaS — it's a local protocol over HTTPS on the
camera itself. The Apteva integration framework is built around OAuth
+ REST connectors (Composio-style) and doesn't fit. See
`docs/apps-vs-integrations.md` for the wider rationale.
