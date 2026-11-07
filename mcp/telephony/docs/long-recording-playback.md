# Long-recording import and playback

Local implementation and verification evidence for Telephony 0.12.10.
No carrier calls, staging/production changes or package installs were performed. The earlier bundled Opus work is
retained in this checkout.

## Limits and playback

The project recording settings accept `max_file_bytes`, default **1 GiB**,
validated from **1 MiB to 1 GiB**. It applies to import/playback downloads and
generated playback files, not the recorder's capture/duration policy. Configure
it with `POST /recording-settings` or `telephony_recording_settings_set`:

```json
{"max_file_bytes": 1073741824}
```

Storage and provider downloads stream to private files. Storage upload parts
also stream from disk, including large negotiated parts; no part-sized or
recording-sized audio buffer is allocated. Small control responses remain bounded.

Reusable playback cache remains 256 MiB, with at most 128 entries and a ten-minute
TTL. Open readers pin entries, so eviction cannot hide unaccounted open files.
Files that do not fit use temporary entries; concurrent readers share the file,
and the last close removes it and releases its disk-work permit. Later range
requests can regenerate a large variant. HTTP ServeContent retains Range/HEAD
support and private/no-store response headers.

Two shared permits cover playback generation and imports. Each admitted job
can retain at most a bounded source and output: at most 4 GiB of playback/import
workspace plus the 256 MiB cache per process. Durable capture files, recorder
finalization and Storage's retained files have separate lifetimes and are not
included in that workspace budget. Waiting for admission is cancellable and
does not run in a live-media handler.

Transient files live in the installation's `recording-playback-tmp` directory.
Mount clears leftovers after the exclusive lifecycle has stopped the previous
process. Durable `recordings/` originals are untouched. Standalone tests use a
private process-specific temporary directory. Conversion/download errors and
cancelled requests remove incomplete files; cache timers remove idle entries.

Default/balanced conversion failure redirects to the authorized original URL,
with `X-Telephony-Playback-Variant: original` and
`X-Telephony-Playback-Fallback: mix_unavailable`. Explicit caller-only/agent-only
failure returns 422 and never substitutes both sides. The browser player also
recovers balanced media errors to Original with a visible notice; explicit
channel errors require the user to select another track.

## Import failures

HTTP 413 and Telephony's file-limit failures remain `storage_status: failed`,
with a reason and `retry_blocked: true`. Automatic claims skip them until the
project file-limit value changes or an operator explicitly retries. Transient
failures retain their bounded backoff. Duplicate provider callbacks and stale
worker completion do not revive a blocked/retired import.

The UI/public metadata distinguish **Recorded locally**, **Stored successfully**
and **Storage import failed**. A local original remains available after an
import failure. Existing size and SHA-256 verification remains mandatory before
marking a recording stored or removing the original in move mode.

## Local verification — 12 October 2026

The accelerated matrix uses the actual local SIP recorder, 24 kHz dual PCM,
markers at start/middle/end and production import/playback code. It substitutes
Storage with an authenticated HTTP server that streams parts to disk and verifies
SHA-256. It does not simulate SIP networking or wait for real two-hour calls.

| Duration | Original bytes | Inbound | Outbound |
| --- | ---: | --- | --- |
| 30 minutes | 172,800,044 | Passed | Passed |
| 60 minutes | 345,600,044 | Passed | Passed |
| 120 minutes | 691,200,044 | Passed | Passed |

Every case covered verified Storage import, original/mix/caller/agent playback,
start/middle/end range seeks, project/agent/application-user access checks and
temporary-file/permit cleanup. The 120-minute mono variants are 345,600,044 bytes,
above the reusable-cache capacity. Playback allocated **under 1 MiB per case**
across four variants; this is Go TotalAlloc, not total process RSS or kernel cache.
The complete matrix passed in 50.2 seconds on the local M1 Pro.

Failure tests cover size/hash mismatch, real HTTP 413, manual/limit-change retry,
duplicate callbacks, cancellation, large-reader sharing, expired cache readers,
restart cleanup and preservation of durable originals. Chromium tests verify
balanced fallback and no automatic dual-channel fallback for Caller/Agent.

Full Go suite: **1,239 tests/subtests passed**, eight optional skips. The long
matrix was run separately with its opt-in flag. Recording-focused race tests,
251 frontend tests, typecheck, panel/headless builds and three recording-player
browser tests passed. The SDK remains pinned to released v0.100.0.

```sh
GOWORK=off CGO_ENABLED=0 GOMAXPROCS=2 \
TELEPHONY_LONG_RECORDING_TEST=1 \
TELEPHONY_LONG_RECORDING_OUTPUT=/absolute/path/results.json \
go test -run '^TestLongSIPRecordingImportAndPlaybackMatrix$' -count=1 -timeout 10m -v .

GOWORK=off go test ./...
bun run test:frontend
bunx --no-install playwright test -c frontend/recording-playback.playwright.config.ts
```

Raw local evidence: `/Users/marcoschwartz/.codex/artifacts/telephony-long-recordings-20261012/`.
CI now includes this matrix and the browser recording checks; the new workflow
had not been run remotely at the time of this local verification.
