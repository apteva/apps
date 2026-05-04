# dlna

Local-LAN UPnP/DLNA MediaServer for Apteva. Pure protocol-translation
sidecar — `storage` is the byte source and folder source, `media` is
the metadata enricher. This app holds two small tables (allowlist +
client log) and computes everything else live.

## What clients see

After install + at least one `dlna_publish_folder` call, any DLNA
client on the same LAN (smart TV, PS5, Xbox, Sonos, VLC, Kodi,
nPlayer, etc.) sees a server with this tree:

```
{Friendly Name}
├── Audio       — every audio/* file in storage, flat list
├── Video       — every video/* file
├── Photos      — every image/* file
├── Recent      — newest-first across all types (last 200)
└── Folders
    ├── {published folder 1, label or path}
    ├── {published folder 2}
    └── …
```

The Audio/Video/Photos top-level containers respect the
`publish_root_by_default` config: if `false` (default), they only
include items inside published folders. If `true`, they include the
entire storage app.

## Setup

1. Install `storage` (required). `media` (optional) is recommended
   for video/audio so TVs get duration + codec hints.

2. Confirm the host can do multicast. SSDP listens on UDP/1900
   multicast `239.255.255.250` and the orchestrator must place this
   container with `network_mode: host`. Without that, no client will
   ever discover the server. See *Open questions* below.

3. Install the `dlna` app and publish at least one folder:

   ```
   dlna_publish_folder(folder="/movies/kids", label="Kids' movies")
   ```

4. On a TV: open the input picker, the friendly name (default
   `"Apteva ({hostname})"`) appears under "Network" / "DLNA" /
   "Media servers". Select it and browse.

## MCP tools

| Tool | Purpose |
|---|---|
| `dlna_status` | Broadcasting state, friendly name, UUID, counts |
| `dlna_set_friendly_name` | Rename — propagates on next SSDP NOTIFY (≤30s) |
| `dlna_publish_folder` | Add a storage folder to the public tree |
| `dlna_unpublish_folder` | Remove one |
| `dlna_clients_recent` | Who's been browsing in the last 24h |

That's the entire surface — DLNA is not an agent-conversational app.

## Composition

```
TV ─SSDP/SOAP─→ dlna ─CallApp─→ storage   (folders, files, signed bytes URL)
                       └──────→ media     (duration, codec, resolution)
```

Bytes never pass through this sidecar. `GET /media/{file_id}` 302s
to a `storage.files_get_url`-signed URL with a short TTL (default
60s). TVs re-request on every seek; that's fine because storage's
URL minting is cheap.

When `media` isn't installed (or `media_metadata=false`), DLNA items
just omit the duration/codec fields. The spec tolerates that and
most clients fall back to "play and see what happens."

## SOAP surface

DLNA clients hit:

| Path | Method | Purpose |
|---|---|---|
| `/device.xml` | GET | Device descriptor — found via SSDP `LOCATION` |
| `/ContentDirectory.xml` | GET | SCPD for the ContentDirectory service |
| `/ConnectionManager.xml` | GET | SCPD for the ConnectionManager service |
| `/ContentDirectory/control` | POST | SOAP `Browse` / `Search` |
| `/ConnectionManager/control` | POST | SOAP `GetProtocolInfo` (stubbed) |
| `/media/{file_id}` | GET | 302 redirect to a storage signed URL |

`/health` and `/api/apps/dlna/*` (panel reads) round out the HTTP
surface.

## Open questions

1. **Multicast in containers.** This is the single hardest constraint.
   SSDP requires sending and receiving on `239.255.255.250:1900`.
   Default Docker bridge networking does not forward multicast — the
   container needs `network_mode: host`, or an avahi/mdns-reflector
   on the host. The manifest declares `network_mode: host` and
   `network.multicast` permission; the orchestrator must honour both.
   On Kubernetes you'll need either `hostNetwork: true` or a
   `MulticastService` CNI plugin. Confirm the deploy target supports
   one of these *before* expecting `dlna_status` to return
   `broadcasting: true`.

2. **Transcoding.** Older Samsung / LG TVs only play H.264-baseline +
   AAC in MP4. Anything else (HEVC, AV1, MKV, FLAC) is listed but
   silently skipped on playback. v0.1 ships nothing here — adding an
   on-demand transcoder means bundling ffmpeg into the image and
   running a streamed-pipe handler off `/media/{id}?transcode=1`.
   Out of scope for now; documented so users know why their 4K HEVC
   doesn't play on the kitchen LG.

3. **DLNA has no auth.** Anything on the LAN can browse. So the
   `published_folders` allowlist is the only access boundary. Default
   `publish_root_by_default=false` is deliberate — empty is the safe
   starting state. Keep it.

4. **IPv6.** SSDP also runs on IPv6 multicast (`ff02::c`), but most
   home-network DLNA traffic stays v4. v0.1 is v4-only; bind to
   v6-multicast as a v0.2 follow-up if anyone asks.

## Why this is an app, not an integration

DLNA is a wire protocol exposed *outward* on the LAN, not a SaaS
connector. There's no OAuth, no remote API, no credentials to store.
The same reasoning as the `tapo` app — see `docs/apps-vs-integrations.md`.
