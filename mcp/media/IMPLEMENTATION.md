# Media 0.14.4 — audit fixes and rendering validation

Based on `media/v0.14.3`, commit `cf28f77344fbeae05869099effb03f84a40b91d3`.

## Audit findings addressed

The numbered findings below describe the audit scope and resulting changes.

| Finding | Implemented change |
|---|---|
| 1 — shared render output deletion | Completion conflicts/cancellations retain potentially shared Storage outputs and record them for reconciliation; cleanup no longer assumes deduplication created a new file. |
| 2 — AI overwrites operator edits | Atomic, source/revision-guarded updates protect prose and ratings independently; late AI results cannot recreate a deleted row. |
| 3 — one-keyframe panic | Explicit single-keyframe handling locally and remotely; short-clip positions remain inside the clip. |
| 4 — ignored batch limit | All candidate append paths respect the batch limit. |
| 5 — vertical Smart Crop | Preserve calculated Y positions for still crops and the reel's static vertical window; retain existing horizontal tracking. |
| 6 — metadata scalar types | JSON type-aware matching distinguishes booleans, numbers, strings, null and missing values. |
| 7 — description starvation | Transcript readiness and manual eligibility are checked before SQL LIMIT; oldest eligible work goes first. |
| 8 — completion stuck on terminal stages | Failed/skipped transcripts, imports, and manual descriptions call the completion coordinator. Completion updates and outbox entries commit in one transaction. |
| 9 — malformed/refusal prose | Parse structured success separately; reject refusals/malformed JSON, accept legitimate rating-only responses, and correctly handle braces inside JSON strings. |
| 10 — rating reset | Explicit unrated resets queue reclassification while retaining prose. |
| 11 — regeneration after clearing prose | Clear-and-force resets human provenance and queues a durable manual request. |
| 12 — ignored Storage errors | Detect MCP and JSON-RPC errors inside HTTP 200 responses. Retain failed derivative cleanup in a retry queue. |
| 13 — cover art classified as video | Exclude attached artwork; use container/frame semantics for still images, preserving actual JPEG and motion-video behavior. |
| 14 — short thumbnail seeks | Clamp thumbnail/keyframe seeks; fall back to the first frame for very short sources. |
| 15 — folder wildcards | Use literal folder-prefix matching across search, facets and folder navigation. |
| 16 — analysis cap | Cap explicit standard-depth ranges at 60 seconds; share admission limits with other media processing. |
| 17 — 24-hour counts | Compare normalized SQL timestamps using julianday. |
| 18 — manual-only workers | Keep workers active for explicit requests; auto settings gate discovery rather than execution. |
| 19 — derivative repair | Schedule missing thumbnails/waveforms/keyframes, retry failures, commit staged replacement generations, retain working pointers on failed replacements, and durably retry obsolete-file cleanup. |
| 20 — stale names/folders | Handle rename events and reconcile inventory metadata independently of probing; avoid per-row reads/writes for unchanged catalog entries. |
| 21 — stale transcription attempts | Preserve running attempts on force requests, identify claims with nanosecond tokens, validate source identity on success, and check affected rows before reporting success. |
| 22 — panel refresh | Preserve loaded pages and refresh the selected detail, abort superseded requests, debounce events, handle MCP rating errors. |
| 23 — stale cards | Current callback refs and request cancellation; explicit missing transcript state; failed/skipped refresh and polling; accurate unavailable-media wording. |
| 24 — install routing | Carry the selected install through render submission and Smart Crop preview. |
| 25 — remote collisions/shutdown | Unique random attempt directories, exact-directory progress/cancel calls, and process-group termination for remote renders. |
| 26 — remote upload encoding | Serialize JSON in Go, parse response paths with a portable JSON reader, quote multipart filenames, use literal form fields, and prefix FFmpeg output paths. Reject newline-containing output filenames explicitly. Remote chunk uploads now also carry authentication. |
| 27 — Cloudinary output contract | Derive transformation format from the resolved filename/source type, use the image endpoint for images, align upscaling semantics, check returned content type, and bound/cancel provider calls. |
| 28 — analysis coverage | Distinguish requested duration; do not report unknown or failed decoding as complete verified coverage. Interrupted coverage is conservatively zero rather than an invented decoded duration. |
| 29 — executor policy/lifecycle | Configured remote failures stay remote failures. Track background workers, cancel their work and join them on unmount; apply lifecycle/admission limits to analysis and previews too. |

The original 17 reproductions now pass. Additional tests cover manual-only fairness, stale attempts, manual rating protection, failed derivative replacement, missing-keyframe repair, resource cancellation, event delivery retry, JSON/multipart encoding, chunk concurrency/authentication/abort, and repeat-render reuse.

## Rendering and UI performance changes

- Verified local source cache, immutable active-job links, two concurrent source downloads, and reuse of render inputs for crop sampling. Remote sampling prefers the configured host and shares its source cache. Hash verification is done on materialization and repeated periodically or when identity changes, instead of rereading every cached source on every request.
- Cache Smart Crop analysis by source/evidence, range, geometry, settings and algorithm version. Preview sends the actual end time; late preview responses are aborted when inputs change.
- Reuse completed renders before downloading/encoding. Every hit rechecks source access, output identity and destination. Keys include project, source hashes, operation/parameters, crop evidence, encoder identity and output destination. Local keys include binary file identity/thread settings; remote/provider reuse is additionally limited to a process lifetime and host/connection identity. A source/config change during execution prevents storing under the old key.
- Wake queued workers immediately, retaining the recovery poll. Weighted admission limits heavy jobs to two concurrent encodes at the default capacity; encoder threads default to two, filter threads to one. The budget is shared within a Media process for each selected host, not distributed across independent sidecars.
- General **Video quality** control: **Legacy (default), Low, Medium, High**. Every new render explicitly starts on Legacy. Low uses x264 veryfast/CRF 28; Medium medium/23; High slow/18. This controls exported video quality and does not change resolution/frame rate. The earlier development names `preview`, `balanced`, and `quality` remain accepted for queued-job compatibility but are no longer advertised. The default and explicit Legacy plans exactly match the original planners. Stream-copy trim/concat and image/audio operations retain their relevant paths; the video quality selector is hidden for image/audio sources. Cloudinary rejects nonlegacy FFmpeg profiles rather than silently ignoring them.
- Bounded concurrent upload chunks locally and remotely, respecting Storage's advertised concurrency, preserving checksums/finalization, and aborting failed uploads. Remote presigned direct upload remains the first choice.
- Persist queue/admission times, local download/analysis/encode/hash/transfer/upload/finalization timings, cache hits and output size. Remote and provider execution have an aggregate stage; remote substage timings are not yet exposed separately. UI elapsed time uses server timestamps.
- Debounced catalog updates, lazy image decoding, and CSS content visibility reduce work for offscreen tiles. This retains the existing pagination API; it is not a full virtual-list rewrite.

## Validation and measured results

Release validation used Go 1.26.6, SDK v0.74.1 (verified as a descendant of v0.73.0), Bun 1.3.13, local FFmpeg 7.1.1 and Google Chrome. The historical encoder benchmark used the same encoder settings before the SDK update.

- Final combined unit + integration run: **638 passing test cases (including subtests), zero failures, 57.397 seconds**. It skipped 22 conditional cases: the opt-in benchmark (run successfully separately) and 21 private-media fixture cases whose environment paths were not configured. Those private regressions are not claimed as validated. Full race suite: pass; targeted race checks also cover the final cache/completion/indexer/executor changes.
- `go vet ./...`, native build, and all four shipped UI entry builds: pass.
- Real Media + Storage sidecar tests: pass, including upload, trim, frame extraction, failed source handling and manual descriptions. The repeated real trim returns the same output with `result_cache_hit=true`.
- Real FFmpeg validation covers still frames, vertical reels, transcodes, concatenation, audio preservation, 4K source geometry, and concurrent jobs at 1/2/4.
- Chrome tests use the actual React components/dashboard CSS and a real generated video. Video preview playback, 150-item pagination/live refresh, selected detail updates, Smart Crop/render range consistency, quality selection and explicit Legacy defaults, install routing, and transcript states pass without uncaught JavaScript errors. Media/Storage responses are mocked in this browser harness.
- Go vulnerability scan: zero reachable vulnerabilities under the patched toolchain. The scanner still reports eight advisories in required modules without affected call paths; this is not a claim that all dependencies or deployed binaries are vulnerability-free.

The historical benchmark labels Preview/Balanced/Quality correspond to the current Low/Medium/High choices, with identical encoding settings. The synthetic 1080p, four-second transcode comparison below uses the updated implementation with a fixed two-thread encoder budget. It compares profiles, not an old-release versus new-release production benchmark.

| Profile | User CPU | Output bytes | SSIM against source |
|---|---:|---:|---:|
| Legacy | 5.025 s | 2,676,023 | 0.995411 |
| Preview | 2.366 s | 1,118,495 | 0.986992 |
| Balanced | 4.945 s | 2,676,023 | 0.995411 |
| Quality | 9.307 s | 4,349,067 | 0.998454 |

Preview used about **53% less user CPU and 58% fewer output bytes** than legacy on this sample, with a quality tradeoff. The machine was running other work, so recorded wall times and concurrency results are noisy; they are not production latency or capacity predictions. Repeat-render tests establish avoided download/encoding work independently of wall-clock noise.

## Review and operational limits

Migrations 016–020 add revision/request state, retained output records, derivative retry/cleanup state, render metrics/caches and the completion outbox. Completion delivery is at least once; consumers should deduplicate `event_id` after a lost acknowledgement. Ambiguous shared outputs are retained for reconciliation, never automatically deleted. The local cache size is a soft eviction target: active/recent files and render scratch can exceed it.

No live remote host or paid Cloudinary execution was used. Remote shell pieces are executed in local contract tests; actual host performance, process-group behavior over the real Instances transport and provider output parity still need staging verification. The legacy multipart fallback requires curl with `--form-escape` support. Go scanning does not validate the shipped Linux FFmpeg binary (manifest 7.0.2), the rolling remote FFmpeg download, or production's installed binaries.

Measurement-dependent proposals remain separate from these verified fixes: hardware encoding, adaptive memory/resolution-aware admission across sidecars, production p50/p95/throughput benchmarking, batched storyboard decoding versus seeking, range-request thresholds, incremental full-catalog reconciliation, SQL projections/cursor redesign, and a broader held-out Smart Crop quality dataset. They have not been represented as completed or enabled without validation.

## Reproduce

From this worktree's `mcp/media` directory:

```sh
GOWORK=off GOMAXPROCS=2 go test -short -count=1 ./...
GOWORK=off GOMAXPROCS=2 go test -race -short -count=1 ./...
GOWORK=off go vet ./...
GOWORK=off go build .
GOWORK=off GOMAXPROCS=2 go test -tags integration -run '^TestSidecar|^TestRenderPipeline|^TestEnvironment|^TestRender4KGeometry' -count=1 -v
RUN_MEDIA_BENCHMARK=1 GOWORK=off GOMAXPROCS=2 go test -tags integration -run '^TestRenderBenchmark$' -count=1 -timeout=10m -v
bun ui/build.ts
bun testdata/browser/run.ts
```

See `testdata/browser/README.md` for browser/vendor runtime overrides. Validation logs, benchmark JSON and screenshots are retained separately from the source release.

## Release validation: 0.14.4

With SDK v0.74.1 and minimum Go 1.26.6, the combined unit/integration suite passed 638 cases (including subtests), with zero failures in 57.397 seconds. The same 22 conditional cases were skipped as described above. All four shipped UI bundles and the browser video/quality/default flow passed. Full race detection, vet, Linux amd64/arm64 source builds, and govulncheck passed; the scan reports zero reachable vulnerabilities and eight advisories in required modules without identified affected calls.
