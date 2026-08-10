# DLNA Server

Project-scoped UPnP/DLNA MediaServer for an Apteva host on a private LAN.
Smart TVs, consoles, VLC, Kodi, and other players discover it through SSDP,
browse a virtual library backed by `storage`, and optionally receive duration
and resolution metadata from `media`.

## Access model

DLNA clients cannot carry an Apteva token. The protocol endpoints therefore
remain unauthenticated and reachable on the LAN:

| Endpoint | Purpose |
|---|---|
| `UDP 239.255.255.250:1900` | SSDP discovery and announcements |
| `/device.xml` | UPnP device description |
| `/ContentDirectory/control` | Browse and Search SOAP actions |
| `/ConnectionManager/control` | Playback protocol information |
| `/ContentDirectory/event` | GENA subscription handshake |
| `/media?id={file_id}` | Range-capable media stream |

The security boundary is the published-folder allowlist. A media URL is open
to LAN clients, but its file ID is looked up and checked against the allowlist
before DLNA asks `storage` for a signed URL. Guessing an unpublished storage ID
returns 404 and does not mint a URL. The default is to publish nothing.

Do not expose port 8200 to the public internet and do not publish folders that
contain private documents. `publish_root_by_default=true` deliberately shares
all storage and should only be used on a trusted LAN.

## Library shown to clients

```text
{Friendly Name}
├── Audio       all published audio/* files
├── Video       all published video/* files
├── Photos      all published image/* files
├── Recent      published files, newest first
└── Folders
    ├── {published folder 1}
    └── {published folder 2}
```

The catalog is cached for 30 seconds by default and invalidated immediately by
storage file events and publishing changes. It walks each folder separately,
so libraries larger than 500 items work with storage's existing API. A single
physical folder containing 500 or more direct files can still be incomplete;
`dlna_status.catalog_truncated` and the panel surface that condition.

Metadata enrichment is cached, limited to four concurrent `media_get` calls,
and temporarily backs off if `media` is unavailable. Disable it with
`media_metadata=false` when the optional media app is not installed.

## Setup

1. Install `storage` and optionally `media` in the same project.
2. Ensure the runtime supports host/LAN networking and IPv4 multicast. A
   normal Docker bridge commonly blocks SSDP; use host networking or an
   equivalent multicast-capable CNI configuration.
3. Install DLNA for that project. One install maps to one storage project,
   because incoming TV requests do not contain project context.
4. Add folders in the DLNA panel or with:

   ```text
   dlna_publish_folder(folder="/movies/kids", label="Kids’ movies")
   ```

5. Open the TV's network/media-server picker. Use **Announce now** in the panel
   after powering on a client if it does not appear immediately.

The device UUID and friendly name are persistent, so clients do not see a new
server identity on every restart.

## MCP tools

| Tool | Purpose |
|---|---|
| `dlna_status` | Discovery, dependency, catalog, and client status |
| `dlna_set_friendly_name` | Persistently rename and announce the server |
| `dlna_publish_folder` | Add a storage path to the LAN allowlist |
| `dlna_unpublish_folder` | Remove a path from the allowlist |
| `dlna_clients_recent` | List recently active LAN clients |
| `dlna_announce` | Send an immediate SSDP alive burst |

## Streaming and compatibility

The LAN endpoint proxies the storage response so the TV receives content type,
length, ETag, `Accept-Ranges`, and `Content-Range` headers and can seek without
depending on a public tunnel. Long media transfers have no fixed total HTTP
timeout; disconnecting the TV cancels the request.

There is no transcoding. A file can be listed but fail to play when the client
does not support its source codec/container. IPv4 SSDP is supported; IPv6 SSDP
multicast is not currently advertised.
