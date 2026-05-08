# Streaming (v0.1)

Live ingest + HLS packaging for sibling Apteva apps.

## What's in v0.1

- **RTMP ingest** via per-stream `ffmpeg -listen 1`. Each `streams_create`
  allocates a port from `rtmp_port_range` (default `1935-1965`).
- **HLS packaging** with `-c copy` — no transcoding. Whatever the host
  pushes (typically H.264/AAC from OBS) goes straight to HLS.
- **Recording to mp4** as a second tee output. Survives publisher
  disconnect → mp4 is finalized with a faststart moov atom.
- **Token-gated playback** — segments served directly from local disk
  via the sidecar's NoAuth HTTP routes; `?t=<playback_token>` is the
  gate for `visibility=signed`.
- **Heartbeat-based viewer counting**. Active viewers, peak, total
  watch-seconds, all updated by the `viewer-counter` worker every 10s.
- **Watchdog**. Detects ffmpeg children that exited; flips status to
  `ended` (graceful) or `errored` (crash); frees the port.
- **Built-in load generator** (`streams_load_test`). Spawns N
  goroutine viewers against the playback URL and reports p50/p95/p99
  TTFB, served Mbps, refusals, http_5xx, segments_late.

## What's deliberately deferred

- Multi-bitrate ABR (one ffmpeg per rung) — v0.2.
- LL-HLS (sub-5s latency) — v0.2 (packager flag tuning).
- WebRTC ingest ("Go Live" from browser, no OBS) — v0.3 (needs SFU).
- Storage app integration for replay persistence — v0.2.
- Media app integration for low-bitrate replay rungs + thumbnails — v0.2.
- mediamtx-as-multiplexer to replace ffmpeg-per-stream — v0.2.
- Admin UI panel — v0.2 (REST surface is in place).

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
| `streams_create` | Allocate a stream — returns ingest_url, playback_url, stream_key, playback_token |
| `streams_get` | Full state snapshot |
| `streams_list` | Filter by status, owner_app, owner_tag |
| `streams_stop` | Graceful stop — finalize recording |
| `streams_delete` | Tear down + remove segments + recording |
| `streams_rotate_key` | Rotate stream_key (kills active session) |
| `streams_get_metrics` | bitrate / fps / viewer_count / uptime |
| `streams_replay_url` | Replay URLs once status=ended |
| `streams_load_test` | Synthetic N-viewer load test against the playback URL |

## REST surface

| Method | Path | Auth |
|---|---|---|
| GET/HEAD | `/streams/<id>/index.m3u8?t=<token>` | NoAuth + token |
| GET/HEAD | `/streams/<id>/seg-*.ts?t=<token>` | NoAuth + token |
| GET/HEAD | `/streams/<id>/record.mp4?t=<token>` | NoAuth + token |
| POST/GET | `/heartbeat/<id>?t=<token>[&v=<viewer_id>]` | NoAuth |
| GET/POST | `/admin/streams[, /<id>, /<id>/{metrics,stop,rotate-key,replay,load-test}]` | session |

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
# Bisect until http_5xx > 0 or p99_ttfb_ms degrades.
```

For numbers larger than ~2000 viewers, run the load test from a
separate machine with `wrk` or `vegeta` so the loadgen and server
don't share CPU.
