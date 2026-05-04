# torrent

Local BitTorrent client + indexer-search frontend for Apteva.
Powered by [anacrolix/torrent](https://github.com/anacrolix/torrent).
Search comes from user-supplied Jackett / Prowlarr / RSS sources;
finished downloads land in the `storage` app and get probed by
`media`.

## What this app does and doesn't ship

- ✅ A BitTorrent engine that runs as a long-lived sidecar.
- ✅ An aggregator over Jackett-compatible search APIs.
- ✅ Cross-app handoff to `storage` and `media` on completion.
- ❌ No indexers preconfigured. The user supplies them.
- ❌ No recommended sources, lists, or curation. None.

This is a generic BitTorrent client. What you search for and what
you download is entirely up to you, and entirely your responsibility.

## Setup

1. Install `storage` (required). Optionally install `media` so
   completed video/audio files get duration + codec metadata.

2. Configure at least one indexer. The most common path is to run
   [Jackett](https://github.com/Jackett/Jackett) on the same LAN and
   point this app at `https://<host>:9117/api/v2.0/indexers/all`
   with the Jackett API key. Each indexer is a row in the `indexers`
   table; add via the panel's *Indexers* tab or `indexers_add` route.

3. Set `default_target_folder` (default `/downloads`) — that's where
   completed torrents go in `storage`.

4. Open the *Downloads* tab, click `+ add torrent`, paste a magnet,
   or use *Searches* to run a query and pick a result.

## MCP tools (13)

| Tool | Purpose |
|---|---|
| `torrent_search` | Fan out across indexers, dedupe by infohash, rank by seeders |
| `torrent_search_save` | Save a search to run on a schedule |
| `torrent_search_save_list` / `..._delete` | Manage saved searches |
| `torrent_add` | Start a download (magnet / infohash / .torrent URL) |
| `torrent_list` | List downloads filtered by state |
| `torrent_get` | Detail one download incl. per-file progress |
| `torrent_pause` / `torrent_resume` / `torrent_remove` | Lifecycle |
| `torrent_set_priority` | Selective downloading per-file (skip / low / normal / high) |
| `torrent_stats` | Global rates, active count, disk, queue |
| `torrent_indexers_test` | Health-check each indexer |

## Composition

```
agent ─MCP─→ torrent ─search→ {indexers}                  (Jackett aggregation)
                     │
                     ├─bittorrent→ peers/trackers           (active downloads)
                     │
                     └─CallApp───→ storage.files_upload    (final files)
                              └──→ media.probe_file        (best-effort)
```

The `dlna` app picks up new arrivals indirectly: storage emits
`storage.file.created` on each upload; the dlna tree is computed
live, so the new file appears on the TV within one Browse cycle.
Three apps composing without any direct knowledge of each other.

## Platform events emitted

- `torrent.added` — `{id, infohash, name, magnet}`
- `torrent.completed` — `{id, infohash, name, file_ids: [...]}`
- `torrent.error` — `{id, error}`
- `torrent.search_match` — `{search_id, query, results: [...]}`

## Open questions / caveats

1. **Legal posture.** This is a tool. The user supplies sources and
   chooses what to download. We don't ship indexers, lists, or
   recommendations.

2. **NAT / port forwarding.** The default 6881 inbound port works
   much better when forwarded on the LAN router. UPnP-IGD port
   mapping is on by default; manual forwarding is more reliable
   long-term.

3. **VPN routing.** Set `bind_interface=tun0` (or whatever your VPN
   interface is named) to pin outbound traffic. Without it, the OS
   default routing applies — fine on a host with a system-wide VPN,
   not fine on a dual-homed host.

4. **Disk pressure.** Pre-flight check: a torrent larger than
   `free_disk × (1 − free_disk_safety_pct/100)` is rejected at add
   time. Keep `working_dir` on a partition with breathing room. If
   `keep_working_copy=false` (default) the working copy is deleted
   after upload, freeing the space again.

5. **Streaming-while-downloading.** Tempting but punted to v0.2 —
   would require `storage` to learn about partial / growing files.

6. **Resume across restarts.** On boot, the engine reloads its state
   from `working_dir/.engine/`, the `torrents` table is reconciled
   against actual completion, and any torrent with
   `state=completed && storage_file_ids_json IS NULL` is re-handed
   to the completion-mover. Restart-safe by design.

## Why this is an app, not an integration

There's no SaaS to OAuth into. The torrent engine is a long-running
local workload, not a credentialed connector. See
`docs/apps-vs-integrations.md` for the wider rationale and
`apps/mcp/dlna/README.md` for a sibling case.
