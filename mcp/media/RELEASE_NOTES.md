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
