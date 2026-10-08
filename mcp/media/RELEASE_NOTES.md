## 0.14.21 — stable seated reel composition

- Extend tighter portrait composition to videos. A concentrated foreground and dense source evidence support one fixed crop across the requested interval. Preserve the combined supported head, upper pose, hands and movement envelope with margins; retain the source bottom edge and cap zoom at 1.5×. Cuts, wide actions and ambiguous or sparse evidence retain existing tracking. Closed eyes and lowered heads remain acceptable.
- Reuse existing video samples and up to 12 background frames. When dense tracking is absent, use the existing bounded adaptive source-sampling budget. Keep these extra samples separate from the original track so an unsuccessful composition pass cannot change its horizontal decisions. Record the effective rectangle, method, evidence and fallback reason under `media-smartcrop-stable-video-composition-7`.
- In verified video composition only, reconnect two overlapping foreground envelopes where room-matching clothing separates head and body. Adjacent subjects and disconnected background components remain ambiguous. Image composition and all trim/normalization/provider behavior stay unchanged.
- Add hash-pinned full-planner regressions for source 90959 and reported reels 94308 (324575–355155 ms), 94349 (391245–427660 ms) and 94398 (436025–461160 ms). Their captured crops tighten from 606×1080 to 506×900 at (838,180), 560×996 at (784,84), and 532×948 at (776,132). Framing remains deliberately conservative; sampled coverage does not certify every unsampled gesture or grant visual approval. Existing files are unchanged.

SDK remains v0.96.0, reverified as the latest tag by commit topology. No schema or configuration changes.

Validation: 622 standard tests passed (27 opt-in/long tests skipped); the standard race suite passed, and new composition/foreground safeguards are covered under race detection. Six real Media–Storage render integrations passed, including trim/normalization. All three proposed crop filters also rendered complete 540×960 reels against the cached production source, retained audio and decoded successfully (917, 1092 and 754 frames). The December source corpus has the same historical hypnoteased crop failure at 446000 ms (X26) on 0.14.20 and this release; no new failures. Retained analyzer/temporal/stationary microbenchmarks have unchanged allocation counts. Timing comparisons were affected by concurrent race-test load; no general speed improvement is claimed. The new four-sample composition benchmark runs at approximately 20 ms on the local host. Vet and Darwin arm64/Linux amd64/Linux arm64 builds passed. All three seated Alexa reels, all five Alexa native portraits, the Holly opening and five portraits, and all five captured August cases passed under race detection. The combined capture run reached Go’s default ten-minute limit; the remaining cases completed in separate runs.

## 0.14.20 — stream transport retries and native portrait headroom

- Recognize the reported flattened HTTP/2 failure `read Codex response: stream error: stream ID 1; INTERNAL_ERROR; received from peer`, including refused streams, and structured nested/JSON-encoded transient provider errors. Media previously treated this transport error as permanent after one attempt. Preserve the existing three-attempt limit, shared timeout, reset-aware waits, no-overlap protection, low reasoning and identical evidence. Authentication, quota/billing, invalid-input and caller cancellation still stop without retry. The shared classifier also improves automatic description recovery.
- Use verified upright native-screenshot foreground extent for still composition against patterned/warm backgrounds, where the standalone bright-background gate cannot authorize tightening. Preserve supported horizontal pose and padded head geometry, cap zoom at 1.5×, and retain the source bottom edge because dark feet may be absent from the skin mask. Wide/ambiguous poses retain scale. P01 (source screenshot 94319; saved output 94382) reduces source headroom from 402/1080 to 200/878, with a 494×878 crop at (850,202). P02 also tightens. Existing files are unchanged.
- Revise crop caches/provenance to `media-smartcrop-native-composition-6`. Video tracking stays unchanged. R03 continues to report action wider than 9:16; strict preflight rejects it and explicit contain preserves the full frame. An identical filled rerender cannot preserve that wide elbow gesture.

Validation: 618 top-level standard tests passed under race detection (26 opt-in skips), with final transport/observation/description race tests after error-event coverage was added. All five captured Alexa portraits, Holly opening/portraits, six Media–Storage rendering integrations, vet and Darwin arm64/Linux amd64/Linux arm64 builds passed. Retained foreground/composition/temporal/stationary benchmark allocation counts were unchanged; the analyzer showed a one-allocation variation among roughly 115,274 per operation. No speed improvement is claimed.

SDK remains v0.96.0, freshly reverified by commit topology. No migration or provider configuration change.

## 0.14.19 — native screenshot subject recovery and explicit crop readiness

- Reproduce Alexa portraits from production 0.14.18. Warm-colour saliency ranked wall/furniture above the subject; thumbnail-to-source conversion and clamping were correct. Native, untransformed screenshots now borrow compatible, distributed parent storyboard evidence to isolate foreground, retain supported upright head geometry without requiring open eyes, and protect connected upper-pose/subject extent. Standalone-photo and video paths retain their existing behavior. Scene identity participates in crop caches; algorithm is `media-smartcrop-native-scene-5`.
- Add `media_preview_crop` for sampled composition preflight without queueing or creating outputs. `require_action_preservation=true` rejects unverified/mispositioned or too-wide geometry before queueing. `crop_fallback=contain` explicitly requests full-frame fallback. Preserve policy and diagnostics atomically with the job across local, remote and Cloudinary backends. Render status remains technical; separate composition status always requires visual review. Sampled evidence cannot certify unsampled poses.
- Reject out-of-range frame extraction before queueing with `timestamp_out_of_range`, requested milliseconds and exclusive valid range. Immediately uploaded videos can obtain an authenticated metadata-only duration probe without waiting for indexing; workers also validate older queued jobs.
- Supply actual existing images to `media_ask` by default; support explicit thumbnail review. Report supplied/source dimensions, evidence representation, render transformation identity and provider-resolution limitations. Existing cached video frames remain reduced evidence. Codex reasoning stays low.
- Persist at most three automatic description attempts per source/metadata revision. Honor upstream reset times and permanent errors; expose retry/running/exhausted state and metadata readiness separately from decodability. Manual requests can restart failed recovery. Migration 025 adds recovery state and deletion cleanup.

Validation: the full standard race suite passed (614 top-level tests, 26 opt-in skips); captured Holly opening, Holly portraits and all five Alexa full-planner regressions passed under race detection. All five earlier August production regressions passed during this patch's validation. Six real Media–Storage render integrations, vet and Darwin arm64/Linux amd64/Linux arm64 builds passed. The private video benchmark retained the same two December baseline failures at identical coordinates; no new failures were introduced in that corpus. Some foreground estimates conservatively warn that action exceeds the portrait width. Neither these tests nor a successful render confer approval on existing outputs.

SDK remains v0.96.0, freshly verified as latest by commit topology. No provider/model configuration change. Existing stored outputs remain intact.

## 0.14.11 — reclining subject protection and crop provenance

- Reproduce the August reports on installed production 0.14.10 before changing the algorithm. The replay still clipped the reclining head in portrait 78221/reel 78178 and the available right arm in portrait 78327.
- Use compatible, distributed background references to isolate supported foreground geometry. Protect upper head geometry in reclining poses without promoting it to a detector face. Reject lower limbs, static furniture, diffuse camera/exposure changes, and similarly substantial competing components. Resolve the exact requested still timestamp when cached reclining geometry may precede a pose change. Preserve supported subject extent with a small margin when it fits.
- Protect inferred reclining heads after competing evidence passes and path smoothing. Measure upright reel extents for diagnostics while preserving the two dance paths. Expose supported sampled geometry wider than the crop as `crop_diagnostics.action_coverage=exceeds_crop_width` and recommend the existing explicit `fit_mode=contain`. Missing or ambiguous evidence remains unknown; sampled fits do not certify an unsampled action.
- Persist app/algorithm version, source hash/dimensions/rotation, requested timeline, evidence timestamps, method, supported extent/head geometry, effective rectangle/path, and stable fallback codes. Align still/reel previews with the render planner. Preserve original resolved parameters on request-cache hits; migration 024 adds a nullable cache provenance column. Cropping caches include app/algorithm revisions.
- Add hash-pinned full-planner regressions for sources 77371 at 528750 ms, 77480 at 427975–468210 ms, 77155 at 525000 ms, and both dance ranges. Customer pixels and signed URLs remain external. Existing saved outputs are preserved. Output-format validation and description backoff from 0.14.9 remain covered.

SDK remains v0.95.0, reverified as the latest tag by commit topology. No dependency or configuration changes.

Validation: 557 top-level standard tests passed under race detection (22 opt-in/long tests skipped); all five captured August cases passed, including the exact requested-frame path under race detection. The Holly opening and five portrait regressions passed under race detection. Six real Media–Storage rendering integrations passed, including saved crop provenance and request-cache reuse. Vet and macOS/Linux builds passed. The existing private benchmark retains the same two December failures at identical coordinates; no new failures were introduced. No rendering-speed improvement is claimed.

## 0.14.10 — still portrait composition

- Refine standalone-image Smart Crop using the existing thumbnail when a single upright foreground has supporting head/torso evidence below a large plain bright background. Recover profile/closed-eye poses missed by the face cascade, protect independently supported faces, and bound tighter framing by the connected subject extent and a 1.5× zoom limit.
- Keep wide gestures at full scale and protect the head/body. A filled 9:16 crop cannot preserve gestures wider than its available window; use the existing explicit `fit_mode=contain` to preserve every source edge with padding.
- Preserve video still/reel tracking, center crops and explicit containment. Revise decision/request caches so new requests cannot reuse earlier portrait results; existing files remain unchanged. No new downloads, detector calls, dependencies, schema or SDK changes.
- Add captured production portrait regressions for outputs 89777/89779/89781/89783/89785, including cached replay, unchanged modes/video decisions, and rendered 540×960 PNG previews. Private pixels stay outside the repository. Add synthetic ambiguity/furniture/headroom cases and photographic seated/reclining checks.

Validation: full race suite passed 551 top-level tests (19 skipped), with the captured portrait and earlier Holly opening fixtures enabled. Final portrait/photo/face checks and updated captured-fixture assertions also passed under race detection. Five real Media–Storage render pipeline tests, vet and macOS/Linux builds passed. Existing private video footage retained only the same two December baseline failures, with identical coordinates. Added composition pass measured approximately 0.65 ms and 7 allocations on the synthetic 320px benchmark; existing analyzer/temporal/stationary allocation counts were unchanged. No rendering-speed improvement is claimed.

## 0.14.9 — output format validation and description retries

- Normalize output filenames before queuing through both MCP and HTTP. Extensionless names receive the operation/source extension; PNG crops receive `.png`. Preserve supported explicit names and reject unsupported extensions and format conflicts with `invalid_output_format`.
- Keep the queued/effective filename, image/video encoder flags, content type and Storage upload consistent across local FFmpeg, remote FFmpeg and Cloudinary. Submission responses expose the effective name and content type; execution persists effective names for older queued jobs.
- Separate normal remote source-cache hit/miss diagnostics into metrics so FFmpeg failures remain the primary error. Render rows and failure events expose an additive `error_code` for output-format failures.
- Persist description rate-limit backoff by connection, tool and model. Retain forwarded Retry-After/reset headers and upstream reset metadata, defer later files in the batch, double the fallback cooldown up to one hour, honor longer upstream windows, and clear the backoff after success. Changing models does not inherit the previous model's cooldown.
- Migration `023_description_backoff.sql` adds only a provider retry-state table. Existing media, renders, descriptions and configuration are preserved. No immediate provider retries or worker sleeps are added.

Validation: 549 top-level tests passed under race detection, including the captured Holly regression; five real Media–Storage integration tests passed, including PNG filename/upload consistency. The complete remote Bash rendering/upload script passed with real FFmpeg in an isolated test. Targeted description retry race checks, macOS/Linux builds and vet passed. SDK remains v0.95.0.

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
