# Streaming (v0.3)

Live ingest + HLS packaging for sibling Apteva apps.

## What's in the box

- **RTMP ingest** via per-stream `ffmpeg -listen 1`. Each `streams_create`
  allocates a port from `rtmp_port_range` (default `1935-1965`) and
  verifies it's actually bindable before handing out an ingest URL.
- **HLS packaging** with `-c copy` — no transcoding. Whatever the host
  pushes (typically H.264/AAC from OBS) goes straight to HLS.
- **Bounded live playlist + VOD replay.** The live manifest holds a
  rolling window (`hls_window_segments`, default 10). Segments are
  never deleted by ffmpeg; at finalize the app writes a complete
  `replay.m3u8` (`#EXT-X-PLAYLIST-TYPE:VOD`) covering the whole stream,
  and replay is served from that. Each entry carries the segment's
  **real** duration, captured from the live playlist as it rolled by,
  so `EXT-X-TARGETDURATION` is never under-declared and the seek bar
  matches the media.
- **Recording to mp4** as a second tee output. Survives publisher
  disconnect → mp4 is finalized with a faststart moov atom. A
  recording only counts as finalized when the moov atom is actually
  present, so a truncated file is never advertised as a replay.
- **Token-gated playback** — segments served directly from local disk
  via the sidecar's NoAuth HTTP routes; `?t=<playback_token>` is the
  gate for `visibility=signed`.
- **Signed, expiring URLs** — optional per stream. See below.
- **Heartbeat-based viewer counting**, cookie- or `?v=`-identified,
  with per-(IP, stream) throttling so the counters can't be trivially
  inflated — sized for shared egress, not one viewer per address.
- **Watchdog**. Detects ffmpeg children that exited; flips status to
  `ended` (graceful) or `errored` (crash, naming the signal); frees the
  port; finalizes the recording on both paths.
- **One finalize path.** `streams_stop`, publisher disconnect, clean
  shutdown and restart-after-crash all run the same finalization, so a
  recording is picked up wherever the stream happened to end.
- **Retention**. `retention_days` is enforced hourly: media + audit
  rows for terminal streams past their window are reclaimed.
- **Built-in load generator** (`streams_load_test`). Spawns N
  goroutine viewers against the *local* playback route and reports
  p50/p95/p99 TTFB, served Mbps, failures + status breakdown.

## Signed playback URLs

`playback_token` alone is a bearer credential with no lifetime. For
anything that needs a link to actually stop working — a consumer app's
"replay expires in 7 days" — use signed URLs:

```
streams_signed_url(id=42, expires_in_seconds=604800, kind="hls")
  → { url: ".../streams/42/replay.m3u8?exp=…&sig=…&t=…", expires_at: … }
```

The signature is
`hex(HMAC-SHA256(url_signing_secret, "<stream_id>:<exp>:<scope>"))`,
verified with a constant-time compare. **`scope` is the `kind` the URL
was minted for** — `hls`, `mp4` or `heartbeat` — so a long-lived
heartbeat link can't be turned into a recording download, and an HLS
link can't be turned into an mp4 one. The manifest and its segments
deliberately share the `hls` scope, because the player inherits the
manifest's query on every segment fetch.

> **Upgrading from v0.2:** signatures issued before v0.3 covered only
> `<stream_id>:<exp>` and no longer validate. Streams at the default
> `require_signed_urls=0` are unaffected — a plain `?t=` URL still
> works. For the rest, re-issue with `streams_signed_url` or read
> `playback_url` again.

By default a signature is *optional*: a plain `?t=` URL still works, so
nothing that worked in v0.1 breaks. To make it mandatory:

```
streams_set_url_policy(id=42, require_signed_urls=true)
```

after which any playback or heartbeat request without a valid
`exp`+`sig` gets a 404. To revoke everything outstanding for a stream
(leaked link, wrong audience):

```
streams_rotate_key(id=42, rotate_playback_token=true)
```

which mints a new `playback_token` *and* a new signing secret. That
form works on `ended` streams too; rotating the *ingest* key does not,
because it would resurrect a finished session.

### Lifetimes

A signed URL for a **live** stream has to outlast the broadcast: every
segment inherits the manifest's signature, so when it expires playback
stops for every viewer at once. Live URLs therefore default to
`live_url_ttl_seconds` (12h) and replay URLs to `replay_url_ttl_seconds`
(1h). `streams_get` reports `playback_url_expires_at` alongside the URL
so a consumer can refresh ahead of it rather than discovering the
expiry as a 404 mid-session. Callers that want an exact lifetime use
`streams_signed_url`.

### Rotating a key mid-stream

`streams_rotate_key` on a *live* stream kills the session and starts a
new ffmpeg against the same storage. The new session continues the HLS
segment numbering rather than restarting it, so `replay.m3u8` covers
every session end to end; the previous recording is rolled aside to
`record-<n>.mp4` rather than truncated. The mp4 the API serves is the
**last** session's — one file can't be the concatenation of several
without a remux, so HLS replay is the complete record across
rotations.

## Scope note

The app supports `scope: global`. On a global install the sidecar has
no `APTEVA_PROJECT_ID`, so every viewer request must name its project —
all generated URLs (playback, replay, heartbeat, signed) carry
`project_id` automatically. Don't strip it.

## What's deliberately deferred

- Multi-bitrate ABR (one ffmpeg per rung) — v0.4.
- LL-HLS (sub-5s latency) — v0.4 (packager flag tuning).
- WebRTC ingest ("Go Live" from browser, no OBS) — v0.4 (needs SFU).
- Storage app integration for replay persistence — v0.4.
- Media app integration for low-bitrate replay rungs + thumbnails — v0.4.
- mediamtx-as-multiplexer to replace ffmpeg-per-stream — v0.4.
- Admin UI panel — v0.4 (REST surface is in place).
- Concatenating rotated sessions into a single mp4 (needs a remux pass).

## Local development

```bash
cd apps/mcp/streaming
go build .
APTEVA_PROJECT_ID=test \
APTEVA_DATA_DIR=/tmp/streaming-data \
./streaming
curl http://localhost:8080/health
```

The sidecar binds:
- HTTP on its assigned listen port (default 8080)
- One RTMP listener per active stream, on a port from `rtmp_port_range`

## Tools

| Tool | Purpose |
|---|---|
| `streams_create` | Allocate a stream — returns ingest_url, playback_url, heartbeat_url, stream_key, playback_token |
| `streams_get` | Full state snapshot |
| `streams_list` | Filter by status, owner_app, owner_tag |
| `streams_stop` | Graceful stop — finalize recording + VOD playlist |
| `streams_delete` | Tear down + remove segments + recording |
| `streams_rotate_key` | Rotate stream_key; optionally playback_token + signing secret |
| `streams_get_metrics` | bitrate / fps / viewer_count / uptime |
| `streams_replay_url` | Replay URLs once status=ended |
| `streams_signed_url` | Expiring signed playback/replay URL, scoped to its `kind` |
| `streams_set_url_policy` | Require signed URLs for a stream |
| `streams_load_test` | Synthetic N-viewer load test against the local playback route |

## REST surface

| Method | Path | Auth |
|---|---|---|
| GET/HEAD | `/streams/<id>/index.m3u8?t=<token>` | NoAuth + token |
| GET/HEAD | `/streams/<id>/replay.m3u8?t=<token>` | NoAuth + token |
| GET/HEAD | `/streams/<id>/seg-*.ts?t=<token>` | NoAuth + token |
| GET/HEAD | `/streams/<id>/record.mp4?t=<token>` | NoAuth + token, `status=ended` only |
| POST/GET | `/heartbeat/<id>?t=<token>[&v=<viewer_id>]` | NoAuth |
| GET/POST | `/admin/streams[, /<id>, /<id>/{metrics,stop,rotate-key,replay,load-test}]` | session |

Every one of these also accepts `&project_id=` (required on a global
install) and the optional `&exp=&sig=` signature pair.

### Viewer counting

The heartbeat response returns `viewer_id`; a player should persist it
and send it back as `?v=<viewer_id>`. That's the only reliable identity
for a cross-origin player, since playback sets
`Access-Control-Allow-Origin: *` and the `SameSite=Lax` cookie isn't
sent on those requests.

Anti-inflation is a two-tier budget per **(source IP, stream)**, in a
60s window: at most `max_viewers_per_ip` (default 64) distinct
identities are counted — extras are still served, they just collapse
into one synthetic viewer — and requests past `max_viewers_per_ip × 12`
get a 429. Bucketing per stream means one stream's audience can't
exhaust another's, and the default is sized for a large shared egress:
a corporate NAT or a university carries far more than one viewer, and
v0.2's budget of 8 identities / 120 beats undercounted them and then
429'd them outright past ~20 concurrent viewers.

Behind apteva-server's proxy the source IP comes from
`X-Forwarded-For`, trusted only when the immediate peer is loopback.
The header is read **right to left**, skipping private/loopback hops:
`httputil.ReverseProxy` appends the peer it saw rather than replacing
the header, so the leftmost entry is whatever the client sent and the
rightmost public one is the nearest hop we didn't add ourselves. If a
request arrives from loopback with no usable forwarded address, viewers
can't be told apart and the throttle is skipped rather than treating
the whole audience as one client.

## Config

| Key | Default | Notes |
|---|---|---|
| `rtmp_port_range` | `1935-1965` | One port per active stream; probed for bindability at allocation |
| `hls_segment_seconds` | 4 | Lower = lower latency, higher request rate |
| `hls_window_segments` | 10 | Entries in the *live* playlist. 0 = unbounded (v0.1 behavior) |
| `viewer_idle_seconds` | 30 | Heartbeat timeout |
| `max_concurrent_streams` | 4 | Hard cap on simultaneous publishers; must be ≤ port-range size |
| `finalize_grace_seconds` | 60 | Wait for ffmpeg to close outputs before SIGKILL. Recording streams only. Applied concurrently across runners at shutdown, so it bounds total shutdown rather than multiplying by stream count |
| `ffmpeg_path` | `ffmpeg` | Resolved via `$PATH` when unset |
| `max_viewers_per_ip` | 64 | Counted identities per (source IP, stream) per minute; the request ceiling is 12× this |
| `live_url_ttl_seconds` | 43200 | Signed URL lifetime for a stream in progress — must outlast the broadcast |
| `replay_url_ttl_seconds` | 3600 | Signed URL lifetime for a finished stream |

## Retention

`retention_days` (per stream, default 30, `0` = keep forever) is
enforced by the `retention-sweeper` worker every hour. Once a terminal
stream is past its window, its segment directory, recording and
`stream_events` rows are deleted and the row is stamped `pruned_at`.
The stream row itself survives — it carries the session's aggregate
stats — and `streams_replay_url` then reports
`available: false, reason: "media pruned by retention policy"` rather
than a success with no URLs in it.

## Capacity check

A `-c copy` stream's bytes-per-viewer is exactly the publisher bitrate.
**viewer capacity ≈ upload_bandwidth ÷ stream_bitrate**:

| Upload | 720p (2 Mbps) | 1080p (4 Mbps) |
|---|---|---|
| 50 Mbps home | ~20 | ~10 |
| 1 Gbps fiber | ~450 | ~220 |
| 10 Gbps NIC | ~4500 | ~2200 |

CPU is not the bottleneck (`-c copy` uses ~1-2% per stream). To find
your knee:

```
streams_load_test(id=42, viewers=100, duration_seconds=60)
streams_load_test(id=42, viewers=500, duration_seconds=60)
streams_load_test(id=42, viewers=250, duration_seconds=60)
# Bisect until failures > 0 or p99_ttfb_ms degrades.
```

The load generator always targets `127.0.0.1`, on the port the SDK
actually bound (`APTEVA_APP_PORT`) — it measures this sidecar, never
the public host through apteva-server's proxy. It signs its own
requests when the stream is under `require_signed_urls`, so the
configuration you run in production is the one that gets measured. Any
non-2xx counts as a failure and shows up in `status_breakdown`. For
numbers larger than ~2000 viewers, run the test from a separate machine
with `wrk` or `vegeta` so the loadgen and server don't share CPU.
