# Streaming (v0.2)

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
  and replay is served from that.
- **Recording to mp4** as a second tee output. Survives publisher
  disconnect → mp4 is finalized with a faststart moov atom. A
  recording only counts as finalized when the moov atom is actually
  present, so a truncated file is never advertised as a replay.
- **Token-gated playback** — segments served directly from local disk
  via the sidecar's NoAuth HTTP routes; `?t=<playback_token>` is the
  gate for `visibility=signed`.
- **Signed, expiring URLs** — optional per stream. See below.
- **Heartbeat-based viewer counting**, cookie- or `?v=`-identified,
  with per-IP throttling so the counters can't be trivially inflated.
- **Watchdog**. Detects ffmpeg children that exited; flips status to
  `ended` (graceful) or `errored` (crash, naming the signal); frees the
  port; finalizes the recording on both paths.
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

The signature is `hex(HMAC-SHA256(url_signing_secret, "<stream_id>:<exp>"))`,
verified with a constant-time compare. By default a signature is
*optional*: a plain `?t=` URL still works, so nothing that worked in
v0.1 breaks. To make it mandatory:

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

## Scope note

The app supports `scope: global`. On a global install the sidecar has
no `APTEVA_PROJECT_ID`, so every viewer request must name its project —
all generated URLs (playback, replay, heartbeat, signed) carry
`project_id` automatically. Don't strip it.

## What's deliberately deferred

- Multi-bitrate ABR (one ffmpeg per rung) — v0.3.
- LL-HLS (sub-5s latency) — v0.3 (packager flag tuning).
- WebRTC ingest ("Go Live" from browser, no OBS) — v0.3 (needs SFU).
- Storage app integration for replay persistence — v0.3.
- Media app integration for low-bitrate replay rungs + thumbnails — v0.3.
- mediamtx-as-multiplexer to replace ffmpeg-per-stream — v0.3.
- Admin UI panel — v0.3 (REST surface is in place).

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
| `streams_signed_url` | Expiring signed playback/replay URL |
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
sent on those requests. Per source IP the app counts at most 8 distinct
identities per minute (extras collapse into one synthetic viewer) and
answers 429 past 120 beats/minute. Behind apteva-server's proxy the
source IP is taken from `X-Forwarded-For`, trusted only when the
immediate peer is loopback; if a request arrives from loopback with no
forwarded address, viewers can't be told apart and the throttle is
skipped rather than treating the whole audience as one client.

## Config

| Key | Default | Notes |
|---|---|---|
| `rtmp_port_range` | `1935-1965` | One port per active stream; probed for bindability at allocation |
| `hls_segment_seconds` | 4 | Lower = lower latency, higher request rate |
| `hls_window_segments` | 10 | Entries in the *live* playlist. 0 = unbounded (v0.1 behavior) |
| `viewer_idle_seconds` | 30 | Heartbeat timeout |
| `max_concurrent_streams` | 4 | Hard cap on simultaneous publishers; must be ≤ port-range size |
| `finalize_grace_seconds` | 60 | Wait for ffmpeg to close outputs before SIGKILL. Recording streams only |
| `ffmpeg_path` | `ffmpeg` | Resolved via `$PATH` when unset |

## Retention

`retention_days` (per stream, default 30, `0` = keep forever) is
enforced by the `retention-sweeper` worker every hour. Once a terminal
stream is past its window, its segment directory, recording and
`stream_events` rows are deleted and the row is stamped `pruned_at`.
The stream row itself survives — it carries the session's aggregate
stats.

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

The load generator always targets `127.0.0.1` — it measures this
sidecar, never the public host through apteva-server's proxy. Any
non-2xx counts as a failure and shows up in `status_breakdown`. For
numbers larger than ~2000 viewers, run the test from a separate machine
with `wrk` or `vegeta` so the loadgen and server don't share CPU.
