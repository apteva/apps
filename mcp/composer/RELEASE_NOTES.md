# Composer v0.9.0

Composer now supports procedural clips as an additive source alongside image,
video, audio, text, and AI-generated clips. A procedure is project-scoped,
revisioned source code with named asset inputs and parameters. Composer runs the
trusted local procedure, validates and stores its result, then passes the
materialized `storage:N` asset through the existing timeline renderer.

- Add immutable procedure resources, revisions, materialization history, MCP
  tools, HTTP endpoints, composition bindings, and database migration 006.
- Run Python, Bun/TypeScript, and Go through one language-neutral
  `job.json`/`result.json` contract. Web Canvas remains reserved but is not yet
  locally executable.
- Materialize Storage, Media Studio, and new AI inputs before execution. Reuse
  generated AI assets and cached procedure artifacts across retries and saved
  output variants.
- Apply runtime timeouts, process-tree cancellation, bounded/redacted logs,
  minimal non-secret environment variables, source/input/output size limits,
  path and symlink checks, and FFprobe media/duration validation.
- Preserve existing V1/V2 render behavior. Procedural documents open in the
  JSON editor so bindings remain intact, while render cards expose a dedicated
  procedural-materialization phase.

## Compatibility

Migration 006 only adds procedure tables; existing compositions, renders, and
Storage artifacts are unchanged. Trusted local execution is enabled by default
and can be disabled with `procedural_execution_enabled`. Runtime paths, timeout,
and maximum artifact size are installation settings. This release does not add
Containers, Compute, or another app dependency.

## Validation

The full Composer Go suite and `go vet` pass. New tests cover immutable revision
conflicts, reference validation, cache invalidation/reuse, traversal and output
safety, local Python/Bun/Go execution, process timeout, and artifact probing.
Composer panel bundles and host-import verification also pass.

---

# Composer v0.8.0

One composition now manages Song, Image video, and Full clip exports. Each output keeps independent settings, excerpts, attempts, history, and its last successful preview. Shared audio and completed shots are reused across exports; only dependencies needed by the selected output are generated.

- Add revision-checked HTTP and MCP output APIs, idempotent attempts, durable asset claims, asynchronous generation polling, and explicit artifact adoption.
- Preserve the selected master while exporting shorter excerpts. Track stale outputs when shared inputs change, and distinguish recorded shared/visual costs from unknown estimates.
- Add the Outputs panel and expose the same controls in Persona Studio without duplicating composition records.
- Preserve legacy rendering, project isolation, cancellation, and native/browser scene rendering behavior.

## Compatibility

Migration 005 adds output records and shared asset jobs without deleting compositions, renders, or Storage files. Existing previews remain available; adopting historical artifacts is explicit. Requires Media Studio v0.10.62 and app-sdk v0.77.1. Persona Studio v0.2.0 integrates these outputs.

## Validation

Go suites and Composer race tests cover dependency filtering, shared-generation concurrency, retries, failure isolation, project access, revisions, migration, excerpts, and cancellation. Panel builds, import checks, and Bun editor regressions validate the UI integration. Tests use fake providers and local media; no paid generation or production data migration is performed.

---

# Composer v0.7.5

This release fixes correctness, isolation, reliability, and editor defects found in the published v0.7.4 release.

- Scope composition CRUD, rendering, status, and cached media to the caller’s project. Validate output parameters before remote execution and authenticate remote chunk uploads.
- Keep render snapshots separate from saved compositions. Add revision checks to saves and render submissions. Save & render now persists the current draft before queueing it, and failed saves stop submission.
- Resume asynchronous AI generation by polling its saved job ID, including refresh requests and soundtrack jobs. Bound queue/generation lifetime to two hours.
- Require Storage or local-cache persistence before completion. Retain encoded output on persistence failure. Upload large files in bounded 1 MiB chunks and serve cached media with HTTP Range support.
- Cancel active render jobs, propagate shutdown to the worker, and bound QA and image-fetch operations. Prevent later progress or failure updates from reviving cancelled jobs.
- Prepare silence for remote video inputs without audio. Preserve explicit zero volume through JSON, preview, V1, and V2 rendering.
- Correct source offsets and camera metadata when splitting clips; keep generated IDs unique across tracks. Preserve loaded examples and drafts during background status refreshes. Open V2 documents in JSON, track unsaved edits, and use one mobile drawer at a time.
- Loop media correctly in preview, retain base-video audio by default, scale text to the preview canvas, and preview text entrance effects.
- Hold final V2 keyframe values, retain HTTP image URLs, resolve inline browser images, and wait for images/fonts before capturing frames. Route components to the browser renderer and reject unsupported combinations instead of omitting them.
- Allocate native drawing buffers to element bounds. The 32×32-shape benchmark at 1080p fell from approximately 8.3 MB to 4.2 KB per draw on an Apple M1 Pro.

## Compatibility

Requires app-sdk v0.77.0 and Storage v0.10.13 or newer for chunked uploads. Migration 004 adds a composition revision counter. Existing V1 and supported V2 compositions remain supported. V2 mixed scene/visual-track content, browser video, and unsupported layered FFmpeg V2 content now return explicit validation errors; use V1 for layered video. Components use the browser renderer and require Chrome/Chromium plus the configured JavaScript runtime.

## Validation and remaining limits

Validated with Go tests and race detection, go vet, TypeScript checking, Bun editor regressions, panel import checks, local FFmpeg fixtures, a delayed-image Chrome render, and desktop/mobile browser interactions. Transport tests use local fixtures and stubs; no paid generation or production render was submitted.

Native/browser scene rendering still stages JPEG frames before final encoding; raw-frame streaming and broader editor modularization remain future work. Failed-persistence output is retained for manual recovery; there is no retry-upload button in this release.
