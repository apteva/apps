## Unreleased — output format validation and description retries

- Normalize output filenames before queuing through both MCP and HTTP. Extensionless names receive the operation/source extension; PNG crops receive `.png`. Preserve supported explicit names and reject unsupported extensions and format conflicts with `invalid_output_format`.
- Keep the queued/effective filename, image/video encoder flags, content type and Storage upload consistent across local FFmpeg, remote FFmpeg and Cloudinary. Submission responses expose the effective name and content type; execution persists effective names for older queued jobs.
- Separate normal remote source-cache hit/miss diagnostics into metrics so FFmpeg failures remain the primary error. Render rows and failure events expose an additive `error_code` for output-format failures.
- Persist description rate-limit backoff by connection, tool and model. Retain forwarded Retry-After/reset headers and upstream reset metadata, defer later files in the batch, double the fallback cooldown up to one hour, honor longer upstream windows, and clear the backoff after success. Changing models does not inherit the previous model's cooldown.
- Migration `023_description_backoff.sql` adds only a provider retry-state table. Existing media, renders, descriptions and configuration are preserved. No immediate provider retries or worker sleeps are added.

## 0.14.8 — stationary Smart Crop subject preservation

- Prevent stationary tracking runs from replacing the subject with a disconnected static background feature. Reuse the existing subject-containment policy while preserving motion-continuity corrections.
- Version crop-decision and pre-analysis request caches so new cropping requests cannot reuse an earlier incorrectly cropped output. Unrelated render caches remain reusable; existing stored renders are unchanged.
- Add a captured-footage opening regression and synthetic stationary/background/edge-recovery cases, plus a regression for cache upgrades. No additional frame sampling or allocation increase in the existing microbenchmarks.
- Upgrade app-sdk to v0.95.0, the latest tag by commit ancestry. No database migrations or required configuration changes.

Validation: standard Go suite, race checks, real Media–Storage integration, build/vet, existing Smart Crop benchmarks, and a 40-second FFmpeg preview. Two pre-existing December private-fixture failures remain unchanged; no new private-corpus failures were introduced. Private footage and credentials are not included in the release.

## 0.14.7 — explicit metadata batches

- Add read-only `media_get_batch` for up to 100 explicit Storage file IDs, returning current descriptions, provenance/timestamps, probe status, audience rating and duration in one project-scoped query.
- Report missing records separately. No folder enumeration, processing, Storage enrichment or URL signing.
- Upgrade the app SDK to 0.90.0. Preserve the 0.14.6 planning/search and indexing behavior.

# Media 0.14.6

Media 0.14.6 makes safe, planning-oriented catalog discovery the default.

- Adds `compact`, `planning`, and bounded `full` search detail levels. Full
  searches default to five records, reject limits above ten, and keep raw probes
  and large derivation arrays behind explicit expansions.
- Adds field projection and normalized release-readiness fields for Patreon,
  social usage, hosting, audience suitability, required derivatives, and exact
  session/package lineage.
- Makes partial results explicit with exact totals, returned and remaining
  counts, `must_continue`, and stable filter-bound cursors.
- Adds recording/session, creation, hosting, Patreon, and audience planning
  sorts, backed by new catalog indexes.
- Adds `media_inventory` for bounded grouped counts by content type, audience
  rating, Patreon status, model, session, hosting readiness, and recording
  month, without returning media records.

# Media 0.14.5

Media 0.14.5 fixes a general indexing queue defect that could leave valid
Storage files permanently pending.

- Dispatches exact `media_reindex(file_id)` requests immediately instead of
  relying only on the inventory sweep. Explicit requests are allowed to index
  valid media in hidden folders such as `/.composer/`; discovery-only hidden
  folder exclusions remain unchanged.
- Reclaims pending/failed rows whose worker claim is older than 15 minutes, so
  crashes and stale in-flight state cannot wedge indexing indefinitely.
- Records durable attempt counts, claim timestamps, and the last diagnostic;
  `media_get` and `media_index_status` expose this information for operations.
- Keeps transient Storage resolve misses retryable and marks unsupported exact
  files with an actionable reason.

# Media 0.14.4

Media 0.14.4 fixes catalog, processing and rendering defects identified in the
0.14.3 audit, and reduces repeated rendering work.

- Protect manual descriptions and ratings from late AI responses, reject stale
  transcription attempts, and process explicit requests with automatic discovery
  disabled. Deliver completion events through a durable outbox.
- Repair missing derivatives, retain working replacements on failure, retry
  cleanup, and preserve shared Storage outputs when render completion fails.
- Fix single-keyframe crashes, short-video seeks, cover-art classification,
  vertical Smart Crop placement, metadata type comparisons, literal folder
  filters, analysis coverage, and render statistics.
- Reuse verified source files, crop analysis and completed render results. Add
  bounded processing, encoder threads and concurrent uploads, prompt queue wakeup,
  and render stage metrics. Isolate remote attempts and improve cancellation,
  upload encoding and authentication.
- Preserve paginated UI results and selected details during live updates, improve
  transcript states, and consistently route operations to the selected install.

## Video quality

Export quality choices are **Legacy (default), Low, Medium and High**. Every new
render starts on Legacy, preserving the operation's existing encoding settings.
Changing quality is an explicit choice and does not change resolution or frame
rate. Medium is not advertised as a speed upgrade over Legacy. Source/result
caching and scheduling improvements apply independently of quality selection.

The quality choices apply to H.264 video encoding in MP4, MOV and MKV. Stream-copy,
image and audio paths retain their existing behavior. Cloudinary currently uses
Legacy; unsupported quality selections fail explicitly. Earlier development
profile names remain recognized for existing queued jobs.

## Upgrade notes

- Source builds require **Go 1.26.6 or newer** and use **app-sdk v0.74.1**.
- Migrations **016–020** add worker revisions/manual requests, render metrics and
  caches, derivative repair/cleanup state, retained output records and the event
  outbox. Use the normal database backup procedure before upgrading.
- Completion delivery is **at least once**. Consumers can deduplicate the
  `event_id` field after a lost acknowledgement.
- Potentially shared render outputs are retained for reconciliation rather than
  automatically deleted. Source caches default to a 20 GiB eviction target;
  active/recent files and working scratch can exceed that target.
- Remote rendering requires process-group support through `setsid`; the legacy
  multipart upload fallback requires curl's `--form-escape` option.

## Validation

Validation covers the Go suite and race detection, real Media–Storage sidecar
integration, FFmpeg rendering and 4K geometry, UI builds, and browser video
playback, Smart Crop, render submission and Legacy defaults. See
[implementation and validation details](IMPLEMENTATION.md) for the original
audit coverage, synthetic encoder measurements and test limitations.

Conditional private-media fixtures are not included in the release validation.
Production-host and hardware-encoder benchmarks and live Cloudinary execution
remain unverified; synthetic measurements are not production speed guarantees.
