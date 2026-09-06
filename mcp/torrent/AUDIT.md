# Torrent review — 2026-09-06

Released as Torrent v0.2.4. The release preserves v0.2.3 storage proxy
authentication and v0.2.2 default ApiBay initialization, alongside the audit fixes.
Publishing this release does not itself update a running installation. This review covers search adapters, download admission and
selection, state projection, storage handoff, and build compatibility. It is not
a claim that all possible defects have been eliminated.

## Implemented

- ApiBay: accept root or full `/q.php` URLs, preserve proxy paths/query parameters,
  handle numeric or string counts, skip malformed/sentinel rows without discarding
  subsequent results, recognize books, and explain HTML/challenge responses.
- Native search: a magnet or v1 infohash in the existing search field resolves name
  and size from peers/DHT, with no indexer or file download. Two concurrent isolated
  clients maximum; 25-second deadline; existing interface/encryption/DHT settings
  retained. Unknown availability is shown explicitly. Hex and base32 hashes work.
- Jackett: the URL suggested by the panel no longer produces a duplicated API path.
  Prowlarr also accepts an `/api/v1` base without missing `/search`.
- Torznab: parse magnet links and magnet attributes, sizes from extension/enclosure
  attributes, peer counts correctly, and normalize publication timestamps.
- Bound outgoing indexer requests to six per app, clamp query timeouts, respect
  cancellation, and avoid exposing API keys through transport error URLs.
- Scheduled auto-add now chooses one download source instead of passing magnet,
  hash, and URL together into an API that rejects multiple sources.
- Replace `DownloadAll` with file priorities: explicit piece requests previously
  overrode skipped files. Completion/progress now uses selected files.
- Fix admission when multiple torrents already have metadata, admit queued work
  in arrival order, reserve disk space for active downloads, and recheck on resume.
  Paused/queued priority edits no longer start payload transfers.
- Replace shared priority hints when project references change; prevent stale
  metadata callbacks from changing a newly re-added torrent with the same hash.
- Clear recovered metadata/disk errors while retaining failed storage handoff errors.
  Apply state filtering after combining database and engine state.
- Close the file storage database with its owning client; drain successful upload
  part responses to permit connection reuse. Paused rows do not start handoff retries.
- Pin app-sdk to v0.73.0 (tag at fetched local HEAD), compatible with the manifest;
  update the Pion WebRTC dependency family to fix the workspace DTLS/transport mismatch.
- Explicitly reject unsupported v2-only inputs rather than risk a zero v1 hash.

## ApiBay evidence and native keyword search

A read-only Go adapter query for `ubuntu` against `https://apibay.org/q.php`
returned 100 results from this desktop. Root-host access also returned HTTP 200.
This does not establish why the deployed instance fails. Its saved source URL,
upstream response, DNS and egress access still need inspection on that host.
No arbitrary mirrors or challenge bypass were added. The release retains the
existing default ApiBay source initialization introduced in v0.2.2.

[BEP 5](https://www.bittorrent.org/beps/bep_0005.html) discovers peers for a known
infohash; it does not search titles. Native network-wide keyword search would
require a catalog built using [BEP 51](https://www.bittorrent.org/beps/bep_0051.html)
infohash sampling, peer metadata retrieval, and a local text index, with storage,
bandwidth and retention limits. That crawler is not implemented in this change.

## Remaining findings

1. **High: working files are keyed by torrent name, not infohash.** Two different
   torrents with the same name can share files; cleanup for one can remove the
   other's bytes. `engine.go` uses the default file layout and `mover.go` deletes
   `working_dir/torrentName`. A follow-up needs per-infohash directories and an
   explicit migration of existing working copies before deployment to overlapping
   swarms. Existing on-disk data was not migrated in this review.
2. **Storage retry is per file, not per chunk across failures/restarts.**
   `upload_progress_json` remembers finished file IDs, but `uploadOneFile` starts
   a new session and aborts it on a failed part. Large individual files restart
   from zero. Persist upload IDs and accepted parts, and retry transient failures.
3. **Removal/selection can race an in-flight storage handoff.** A running upload
   holds a previously read row/selection. Removal does not cancel that upload,
   and a completion stamp can make later selection changes skip handoff. A
   per-torrent operation generation/cancellation mechanism is needed.
4. **Restart fidelity for `.torrent` URLs is incomplete.** Only a supplied magnet
   is persisted; a URL-added torrent is restored from its hash, losing private
   tracker metadata. Persist metainfo (including private flag/tracker credentials)
   with appropriate access control, rather than relying on DHT after restart.

## Validation

- Standalone `GOWORK=off go test -race -short ./...`: passed.
- Workspace `go test -short ./...`: passed.
- `GOWORK=off go vet ./...` and standalone binary build: passed.
- Local peer metadata exchange, selected completion, pause/queue priority edits,
  FIFO admission, saved-search auto-add, shared priority replacement, recovered
  errors, and indexer endpoint/parser regressions are covered by `audit_test.go`.
- Opt-in real ApiBay test: passed, 100 results; no downloads were added.
- Release worktree: standalone race tests, vet, binary build, and the Torrent
  panel build/import verification all passed after merging onto current main.
  The original dirty workspace had unrelated Instances/SEO import-check failures.
- No throughput benchmark or production/network-wide DHT availability test was run.
