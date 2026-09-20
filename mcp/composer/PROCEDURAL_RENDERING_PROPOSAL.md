# Procedural rendering in Composer

Status: accepted; local MVP in v0.9.0
Target: Composer v0.9.0
Working name: Procedural Clips

## MVP execution decision

The first release intentionally uses **trusted local execution** inside the
Composer sidecar. It does not require Compute, Containers, runtime images, or a
new app. Composer writes the same language-neutral job directory, invokes a
configured Python, Bun, or Go executable directly, validates `result.json`,
stores the selected artifact, and then renders it as an ordinary clip.

Local execution is enabled by install configuration and must be treated as a
trusted-author feature: timeouts, bounded logs, output limits, a minimal
non-secret environment, path validation, and cancellation reduce accidental
damage but are not a security boundary. `web-canvas-1` remains a declared
future profile and reports unsupported when locally invoked. The structured
contract deliberately remains compatible with a later isolated runner, so
adding Containers/Compute will not change composition or procedure JSON.

## Decision summary

Composer supports code-authored animation as an advanced rendering mode.
It should remain the user-facing owner of the composition, inputs, previews,
render history, cancellation, costs, provenance, and saved outputs. User code
executes locally in the Composer sidecar for the trusted MVP; isolated
execution is a later hardening step.

The implementation should be language-neutral:

1. Composer freezes a composition and an immutable procedure revision.
2. Composer resolves existing assets and materializes requested AI assets.
3. Assets are staged under stable logical names as read-only local files.
4. Composer invokes the configured local runtime with a bounded timeout.
5. The procedure writes artifacts and a result manifest to a bounded output
   directory.
6. Composer validates, stores, and attaches the artifacts to the composition.

No new user-facing creative app is needed. Storage and Media Studio retain
their existing responsibilities. Code and Workspaces may later improve
authoring, while Compute and Containers are optional future hardening rather
than MVP dependencies.

## Motivation

Scene graphs and timeline editors are the best representation for common
composition work. They are inspectable, editable, and portable across renderers.
They become awkward for procedural designs such as:

- data-driven charts, maps, diagrams, and counters;
- particle systems, generative art, and mathematical animation;
- custom typography, masks, line drawing, and per-pixel effects;
- branded templates with complex layout rules;
- programmatic camera paths or transitions;
- simulations and visualizations;
- frame-by-frame compositing against generated images or videos;
- algorithmic music or synchronized sound design.

A complete Python advertisement that draws frames with Pillow and NumPy,
synthesizes audio, and encodes with FFmpeg is a representative example. Similar
work may be easier in TypeScript/Canvas, Go, Rust, WebGL, or another runtime.
Composer should not force one language or require every technique to become a
new `composer/v2` element.

## Goals

- Make coded animation a first-class Composer workflow.
- Support multiple languages without language-specific logic in Composer.
- Let procedures consume AI-generated and existing project assets by name.
- Preserve project-scoped access, idempotency, cancellation, history, and costs.
- Produce deterministic rerenders when all frozen inputs are unchanged.
- Support low-resolution previews and production-quality output.
- Keep procedure source reusable and revisioned.
- Reuse the current Composer output and asset-generation machinery.
- Run trusted procedure code locally now without passing platform credentials,
  while preserving a path to isolated execution later.

## Non-goals

- Turning the Composer sidecar into a general-purpose code executor.
- Evaluating arbitrary code inline inside the trusted native or browser V2
  renderer.
- Replacing V1/V2 timelines with code.
- Letting procedures call arbitrary AI providers or receive provider secrets.
- Promising bit-identical output across different runtime image digests.
- Providing unrestricted package installation or network access.
- Using Functions for long-running media builds.
- Using durable development Workspaces as the production render queue.

## Product model

### Procedure

A procedure is a project-scoped, reusable source resource owned by Composer.
It has a stable identity and immutable revisions. A revision contains:

- runtime profile and exact runtime image digest;
- entrypoint and source bundle;
- dependency manifest and lockfile, if allowed by the profile;
- input declarations;
- parameter schema and defaults;
- output declarations;
- source, dependency, and configuration hashes;
- author and creation metadata.

Composer should store source bundles as private Storage artifacts and store
resource metadata in its database. Small source files may be edited in Composer.
For substantial projects, **Open in Code** can create or connect a Code
repository, and **Open workspace** can provide an isolated development
environment. Composer remains authoritative for the immutable revision used by
a render.

### Procedural clip

The first integration unit should be a generated clip, not an inline callback
executed once per frame by Composer. A procedural clip references an immutable
procedure revision plus named asset bindings and parameters. Materialization
produces one or more durable artifacts that become ordinary Composer assets.

This keeps the timeline renderer deterministic and makes the result reusable by
other outputs. The clip can be trimmed, looped, transformed, layered, mixed, or
shared exactly like uploaded or AI-generated media.

Supported procedure targets should be:

- `clip`: produces a visual clip, optional alpha, and optional audio stems;
- `audio`: produces audio only;
- `still`: produces one or more images;
- `composition`: produces a fully muxed result for advanced cases that
  intentionally bypass later timeline composition.

`clip` should be the default. `composition` is an escape hatch and should be
clearly identified in the UI because timeline layers cannot be edited inside
its already-muxed result.

### Example composition binding

```json
{
  "asset": {
    "type": "procedural",
    "procedure_id": 42,
    "revision": 3,
    "target": "clip",
    "parameters": {
      "brand_name": "Horizon Estates",
      "accent": "#55CBB5"
    },
    "inputs": {
      "villa": {
        "ai": {
          "media_kind": "image",
          "prompt": "Modern Mediterranean villa at sunset, editorial luxury",
          "model": "provider:model",
          "aspect": "16:9",
          "cache_policy": "reuse"
        }
      },
      "logo": { "src": "storage:123" },
      "voiceover": {
        "ai": {
          "media_kind": "audio_tts",
          "prompt": "Space to live beautifully.",
          "voice": "brand-voice"
        }
      },
      "music": { "src": "mediastudio:391" }
    }
  },
  "start": 0,
  "length": 15
}
```

The stored render snapshot must replace mutable references with exact procedure
revisions, Storage IDs, content hashes, generation IDs, and runtime digests.

## AI and existing asset inputs

Procedures must receive assets, not provider credentials. Composer already
materializes AI clips through Media Studio and normalizes successful results to
`storage:N`. Procedural rendering should reuse that path.

An input binding may reference:

- `storage:N`;
- `mediastudio:N`, resolved and pinned before execution;
- a permitted HTTPS source, downloaded and hashed before execution;
- another composition asset by stable ID;
- an AI request using the existing `AIAsset` model;
- an output from another procedural clip in the frozen render graph;
- a project font, data file, JSON document, or subtitle file.

AI inputs may be images, videos, TTS, sound effects, music, or avatars. Existing
`source_image` and `source_images` semantics remain available for reference-to-
image and reference-to-video generation.

Composer must finish or resume AI materialization before scheduling procedure
execution. A retry reuses completed asset claims and existing provider job IDs;
it must not submit another billable request merely because procedure execution
failed.

Inputs are exposed under logical names such as `villa`, `logo`, and
`voiceover`. Procedure code must not depend on Storage IDs, expiring signed
URLs, or host paths. Composer validates project access, downloads the exact
bytes, records hashes and provenance, and stages read-only local files.

Direct provider or app calls from a procedure are out of scope for the initial
release. A future explicit capability model may permit narrow outbound calls,
but render reproducibility and cost authorization require those calls to be
declared and mediated rather than hidden inside arbitrary code.

## Language-neutral job contract

Every runtime implements the same filesystem and process contract.

```text
/composer/job.json          read-only job specification
/composer/source/           read-only immutable source revision
/composer/assets/           read-only staged inputs
/composer/fonts/            read-only staged fonts
/composer/output/           writable artifacts and result.json
/composer/tmp/              writable bounded scratch space
```

The runner sets only non-secret control variables:

```text
COMPOSER_JOB=/composer/job.json
COMPOSER_OUTPUT=/composer/output
COMPOSER_TMP=/composer/tmp
```

The procedure reads `job.json`; it never needs platform credentials.

### Job specification

```json
{
  "api_version": "composer/procedural/v1",
  "job_id": "render-918:clip-hero",
  "target": "clip",
  "entrypoint": "render.py",
  "timeline": {
    "composition_start": 0,
    "source_start": 0,
    "duration": 15,
    "fps": 30,
    "frame_start": 0,
    "frame_end": 450
  },
  "canvas": {
    "width": 1920,
    "height": 1080,
    "pixel_aspect": 1,
    "color_space": "srgb",
    "background": "#07131F"
  },
  "parameters": {
    "brand_name": "Horizon Estates",
    "accent": "#55CBB5"
  },
  "assets": {
    "villa": {
      "path": "/composer/assets/villa.jpg",
      "kind": "image",
      "mime_type": "image/jpeg",
      "sha256": "...",
      "width": 1920,
      "height": 1080,
      "storage_id": 845,
      "generation_id": 391
    },
    "voiceover": {
      "path": "/composer/assets/voiceover.mp3",
      "kind": "audio",
      "mime_type": "audio/mpeg",
      "sha256": "...",
      "duration": 14.8,
      "storage_id": 846
    }
  },
  "output": {
    "directory": "/composer/output",
    "preferred_container": "mp4",
    "video_codec": "h264",
    "audio_codec": "aac",
    "max_bytes": 1073741824
  },
  "preview": {
    "enabled": false,
    "scale": 1,
    "max_duration": 0
  }
}
```

The runner may add asset metadata obtained from `ffprobe`, but the source of
truth remains the frozen hash and local path.

### Result manifest

Successful execution writes `/composer/output/result.json`:

```json
{
  "api_version": "composer/procedural/v1",
  "artifacts": [
    {
      "name": "main",
      "kind": "video",
      "path": "main.mp4",
      "mime_type": "video/mp4",
      "role": "visual",
      "duration": 15
    },
    {
      "name": "music",
      "kind": "audio",
      "path": "music.wav",
      "mime_type": "audio/wav",
      "role": "audio_stem"
    }
  ],
  "metadata": {
    "title": "Horizon Estates advertisement"
  }
}
```

Paths must be relative, remain under the output directory, and match declared
file limits. Composer ignores undeclared files, probes every media artifact,
checks duration/dimensions/codecs, runs existing render QA, and uploads accepted
artifacts to Storage.

### Progress protocol

The process may emit newline-delimited JSON on stdout:

```json
{"type":"phase","name":"rendering"}
{"type":"progress","completed":180,"total":450,"message":"Rendering frames"}
{"type":"warning","code":"font_fallback","message":"Using bundled Inter"}
```

Non-JSON output is captured as a bounded log. Progress is advisory; exit status,
`result.json`, and artifact validation are authoritative. Composer maps progress
to its existing render events and card. Cancellation must terminate the entire
container process tree.

## Future isolated runtime profiles and language support

Composer must not contain a switch statement that knows how to install or run
every language. A runtime profile is a signed platform definition containing:

- stable profile ID and revision;
- OCI image pinned by digest;
- supported source extensions and entrypoint rules;
- literal build and run argument vectors;
- bundled tools and library catalog;
- accepted dependency/lock files;
- default and maximum CPU, memory, PIDs, scratch, output, and timeout;
- declared capabilities such as FFmpeg, Chromium, ImageMagick, or GPU;
- runner protocol version.

Commands are argument arrays, never interpolated shell strings. The generic
runner validates the profile and job, mounts the directories, and launches the
declared adapter.

### Initial profiles

The first production release should include:

| Profile | Entrypoints | Intended stack |
| --- | --- | --- |
| `python-3.13-media` | `.py` | Pillow, NumPy, CairoSVG, OpenCV, FFmpeg bindings |
| `bun-1-media` | `.js`, `.mjs`, `.ts`, `.tsx` | Canvas/SVG, Sharp, Web APIs, FFmpeg |
| `web-canvas-1` | HTML/JS/CSS bundle | Canvas, SVG, WebGL in pinned headless Chromium |
| `go-1.25-media` | Go module or single `.go` | compiled drawing, imaging, direct frame generation |

Python and Bun should ship first within that release. Web Canvas and Go may
follow behind the same contract without schema changes.

### Later profiles

The adapter model can add Rust, C/C++, Java, Kotlin/JVM, .NET, Ruby, Lua,
WASM/WASI, Blender Python, Processing, or GPU-focused images. Supporting a
language means publishing and testing a profile, not changing composition JSON.

Custom OCI profiles may eventually be allowed for administrators. They must use
an allowlisted registry, immutable digest, signature/provenance verification,
and the same runner contract and limits. User-supplied image tags are not
accepted.

### Dependencies

Dependency handling should have three levels:

1. **Bundled:** curated media libraries included in the pinned profile. This is
   the default and most reproducible path.
2. **Locked:** an allowlisted package manifest plus lockfile is built in a
   separate constrained build step. The resolved dependency layer is cached by
   hash and used offline during rendering.
3. **Custom profile:** an administrator-approved digest-pinned image.

Production execution never runs package installation and has no package-registry
network. Build failures are distinct from render failures. Native libraries and
licenses included in official profiles must be documented.

Host fonts such as `/System/Library/Fonts/SFNS.ttf` must not be referenced.
Procedures use fonts bundled by the runtime or named font assets staged by
Composer. Font bytes and hashes become part of the render snapshot.

## App responsibilities

### Composer

- Own procedures, immutable revisions, bindings, parameters, and render state.
- Freeze the full input snapshot.
- Validate asset access and procedure declarations.
- Coordinate AI materialization through Media Studio.
- Stage source and asset bundles for execution.
- Invoke configured local runtimes with cancellation and a bounded timeout.
- Validate results and persist artifacts through Storage.
- Expose previews, logs, provenance, costs, and history.
- Convert materialized procedural clips into normal timeline assets.

### Media Studio

- Generate requested AI image, video, TTS, sound-effect, music, and avatar
  inputs.
- Return durable Storage-backed results and generation metadata.
- Remain the authority for provider selection and provider-specific options.

### Storage

- Hold procedure source bundles, staged immutable input bundles when needed,
  dependency artifacts, and final outputs.
- Provide project-scoped reads and writes.
- Retain content hashes and metadata used for provenance.

### Compute (future isolation)

- Admit and schedule render jobs by resource class and priority.
- Add an artifact-aware `container` job kind rather than exposing arbitrary
  shell commands to Composer.
- Apply idempotency, concurrency, timeout, cancellation, host/pool placement,
  and usage accounting.
- Delegate execution to Containers locally or on a selected Instances host.

The required structured request should identify runtime profile, source bundle,
input bundle, resource limits, expected output contract, and owner reference.
It must not accept unvalidated host paths or a free-form privileged command.

### Containers (future isolation)

- Create the isolated workload from an approved digest-pinned image.
- Mount read-only input volumes and a bounded writable output volume.
- Enforce process, network, user, resource, and lifecycle constraints.
- Return bounded logs and an output archive to Compute.

### Workspaces and Code

- Code optionally owns the editable development repository.
- Workspaces provides an interactive place to build and debug a procedure.
- Publishing creates a new immutable Composer procedure revision.
- Neither app owns production render state or final media.

### Instances

- Supply remote CPU/GPU hosts through Compute and Containers.
- Remain invisible to procedure code.

### Functions

Functions is not part of the execution path. Its request/response serverless
model, supported runtimes, and warm-worker lifecycle are not appropriate for
long, artifact-producing media renders.

## Future isolated security model

The local MVP accepts trusted procedure code only. The following becomes the
required model if arbitrary or untrusted authors are supported later.

Every production render must have:

- a dedicated non-root user and no privilege escalation;
- read-only root filesystem and source/input mounts;
- a bounded writable scratch and output area;
- no host filesystem mounts or Docker socket;
- no platform token, provider secret, Storage credential, or signed URL;
- no network by default, including loopback access to platform services;
- dropped Linux capabilities;
- seccomp and AppArmor/Landlock controls where supported;
- CPU, memory, PID, file-size, inode, scratch, output, and wall-clock limits;
- a maximum source bundle size and bounded number/size of input assets;
- process-tree cancellation and cleanup;
- immutable runtime image digests;
- output path traversal and symlink rejection;
- media probing and declared-artifact validation before persistence;
- redacted, bounded logs;
- project ownership checks at every asset and result boundary.

Build jobs that need registry egress are separate from render jobs, use a narrow
allowlist, receive no project credentials, and produce a content-addressed
dependency artifact. Custom profiles require administrative policy.

## Reproducibility, caching, and provenance

The procedural materialization cache key should include:

- procedure revision and source bundle SHA-256;
- runtime profile revision and OCI digest;
- dependency artifact hash;
- normalized parameters;
- canvas, FPS, duration, frame range, and target;
- ordered logical input names and exact content hashes;
- AI generation IDs and immutable Storage IDs;
- staged font hashes;
- protocol version;
- preview/full-quality mode.

The key must not depend on signed URLs or temporary local paths. A cache hit
reuses the durable procedure artifact. An explicit new procedure revision,
changed parameter, changed input, new AI cache key, or runtime digest creates a
new result.

Each final artifact records:

- procedure/revision/source hash;
- runtime profile and image digest;
- dependency hash;
- parameter snapshot;
- input Storage IDs and content hashes;
- AI generation IDs, provider/model metadata, prompts, and recorded costs;
- Compute/Containers job IDs and measured usage;
- output probe data and content hash;
- Composer composition, output, attempt, and render IDs.

This metadata should be inspectable in Composer and available in a bounded API
response. Sensitive prompts or metadata follow existing project access rules
and are not embedded into public media unless explicitly requested.

## Render graph and timeline behavior

Composer should treat procedure execution as a materialization dependency in
the frozen render graph:

```text
AI requests / existing assets
             |
             v
    procedural clip jobs
             |
             v
   ordinary Composer assets
             |
             v
 native / browser / FFmpeg composition
             |
             v
       saved output artifact
```

Independent procedural clips may run in parallel subject to Compute capacity.
A procedure that consumes another procedure's output creates an explicit DAG
edge. Cycles are invalid. Composer should display generation, procedure build,
procedure render, final composition, QA, and persistence as distinct phases.

For excerpt rendering, Composer passes the requested source/frame window when a
procedure declares `supports_windowed_render: true`. Otherwise it materializes
the complete cached clip and applies the excerpt later. Windowed and complete
results use different cache keys.

The initial implementation should accept finished media artifacts. Raw frame
streaming between the procedure and Composer is deferred because it introduces
backpressure, partial-result, remote transport, and cancellation complexity.
Procedures may encode with the bundled FFmpeg or produce an image sequence that
the runtime adapter encodes before returning.

## API and schema additions

Suggested project-scoped resources:

- `procedures`: stable identity, name, description, latest revision, status;
- `procedure_revisions`: immutable manifest, source Storage reference, hashes,
  runtime profile, entrypoint, schemas, and creation metadata;
- `procedure_materializations`: cache key, frozen inputs, Compute job,
  status, logs, costs, result artifacts, and error category;
- `procedure_artifacts`: named durable outputs and probe/provenance metadata.

Suggested MCP tools:

| Tool | Purpose |
| --- | --- |
| `procedure_create` | Create a procedure and its first immutable revision |
| `procedure_revision_create` | Publish a new revision from source and manifest |
| `procedure_get` | Read metadata, schemas, revisions, and recent status |
| `procedure_list` | List project procedures and reusable templates |
| `procedure_validate` | Validate manifest, source shape, bindings, and profile |
| `procedure_preview` | Queue a bounded preview with explicit inputs |
| `procedure_run_status` | Read build/render progress and bounded logs |
| `procedure_run_cancel` | Cancel a preview or standalone materialization |

Normal composition creation/update should accept procedural asset bindings.
`composition_validate` should resolve the referenced revision and verify
required named inputs and parameter types without executing code or generating
AI assets. Existing composition render tools remain the normal way to render a
timeline containing procedural clips.

All mutating calls use expected revisions and caller idempotency keys consistent
with Composer v0.8 outputs. A project context cannot be overridden by an
argument.

## Procedure manifest

Each source revision includes a language-neutral manifest:

```yaml
schema: composer-procedure/v1
name: horizon-estates-ad
runtime: python-3.13-media
entrypoint: render.py
target: clip
supports_windowed_render: false
parameters:
  type: object
  properties:
    brand_name: { type: string, default: Horizon Estates }
    accent: { type: string, format: color, default: "#55CBB5" }
  additionalProperties: false
inputs:
  villa: { kind: image, required: true }
  logo: { kind: image, required: true }
  voiceover: { kind: audio, required: false }
outputs:
  main: { kind: video, role: visual, required: true }
  music: { kind: audio, role: audio_stem, required: false }
resources:
  class: render-medium
  timeout_seconds: 900
```

The manifest declares shape, not concrete project assets. A composition or
preview request supplies the bindings. JSON should be accepted as an equivalent
wire representation.

## Authoring and UI

Composer should add an **Advanced > Code** creation path without making code
prominent for ordinary users.

The authoring experience should include:

- procedure/template picker;
- runtime profile selector with bundled capabilities;
- source tree and code editor for small procedures;
- manifest editor with structured validation;
- named input panel with Upload, Storage, Media Studio, and Generate with AI;
- typed parameter controls derived from JSON Schema;
- low-resolution or short-range preview controls;
- progress, bounded logs, warnings, probe data, and output previews;
- **Publish revision**, **Duplicate**, **Open in Code**, and **Open workspace**;
- clear runtime/dependency/provenance display;
- explicit **Generate new AI asset** versus reuse behavior.

An agent should be able to create a procedure, bind assets, preview it, revise
source, and attach the resulting composition card without handling signed URLs
or invoking infrastructure tools directly.

Templates should start with practical examples:

- Pillow/NumPy branded advertisement;
- TypeScript Canvas kinetic typography;
- Web Canvas/SVG data visualization;
- Go image-sequence renderer;
- AI hero image plus animated text and TTS;
- reusable lower third, map route, waveform, and chart clips.

## Failure and retry semantics

Errors must identify their phase:

- `asset_generation_failed`;
- `asset_resolution_failed`;
- `procedure_build_failed`;
- `procedure_runtime_failed`;
- `procedure_timed_out`;
- `procedure_cancelled`;
- `result_manifest_invalid`;
- `artifact_validation_failed`;
- `artifact_persistence_failed`;
- `final_composition_failed`.

A retry reuses frozen successful AI assets, dependency artifacts, and valid
procedure artifacts. It only reruns the failed or invalid phase. An ambiguous AI
submission remains blocked under the current Composer rule; procedure failure
never silently causes another paid generation.

Composer retains bounded logs and the frozen job manifest. Retaining a failed
sandbox filesystem is an administrator-only diagnostic option with an expiry;
it must not be the default.

## Cost and resource accounting

Composer outputs should separate:

- AI generation cost by reusable asset claim;
- procedure build compute;
- procedure render compute;
- final timeline composition compute;
- remote/GPU surcharge when known;
- Storage bytes when available;
- unknown provider or infrastructure cost.

Estimates may report resource-class ranges and missing AI prices without
starting work. Measured CPU time, peak memory, GPU time, output bytes, and wall
time should flow from Compute/Containers into the render attempt.

## Compatibility

- Existing V1 and V2 documents and renderers are unchanged.
- Existing AI assets and `storage:N` / `mediastudio:N` references remain valid.
- A procedural clip becomes an ordinary resolved asset before final composition.
- Old Composer installs can retain the JSON but must report an unsupported asset
  type rather than silently omit it.
- Compute and Containers are not required for the trusted local MVP.
- Exported composition packages should include procedure manifest/revision
  references and optionally the private source bundle, subject to permissions.

## Delivery plan

### Phase 0: trusted local MVP (v0.8.0)

- Finalize `composer/procedural/v1`, manifest, progress, and result schemas.
- Build the local runner used by Composer and conformance tests.
- Add equivalent minimal Python and Bun fixtures that consume the same named AI
  image/audio inputs and produce equivalent contract-compliant artifacts.
- Establish media probe, cache-key, path-safety, log, and cancellation tests.
- Add procedure/revision/materialization tables and MCP/HTTP APIs.
- Materialize named existing and AI assets before the procedure runs.
- Convert successful results into ordinary cached Composer clips.

### Phase 1: authoring UI

- Add Advanced > Code, source/manifest editing, input binding, and parameters.
- Add bounded previews, logs, progress, revision publishing, and history.
- Add Open in Code / Open workspace handoffs.
- Ship curated Python and Bun templates, including the real-estate example.

### Phase 2: optional secure execution foundation

- Add structured artifact-aware container jobs to Compute.
- Integrate the Compute container executor with Containers.
- Publish signed, digest-pinned Python and Bun media profiles.
- Enforce offline execution, non-root mounts, resource limits, archive bounds,
  output validation, and cancellation.
- Return job usage, bounded logs, output archive, and terminal reason.

This phase replaces the local process adapter without changing procedure or
composition JSON.

### Phase 3: broader runtimes and rendering

- Add Web Canvas/Chromium and Go profiles.
- Add alpha-video and multiple audio-stem workflows.
- Add remote/GPU resource classes through Instances.
- Add frame-window and distributed segment rendering for conforming procedures.
- Add administrator-approved custom OCI profiles.
- Consider WASM/WASI for fast-start, highly restricted portable procedures.

## MVP acceptance criteria

The first releasable version is complete when:

- Python and Bun procedures use the same public contract.
- A procedure can consume staged uploaded, Storage, Media Studio, and newly
  generated AI image/video/audio inputs by logical name.
- The real-estate example renders without host font paths or platform secrets.
- A procedural clip can be reused in multiple outputs without rerendering.
- Identical frozen inputs hit the materialization cache.
- Changed code, runtime digest, parameter, font, or input hash invalidates it.
- AI assets are not regenerated after a code/build/render retry.
- Cancellation stops the process tree and leaves the render terminal.
- Project-crossing asset references are rejected before execution.
- Oversized outputs, symlink/path escapes, malformed manifests, and codec/
  duration mismatches are rejected.
- Logs are bounded and secrets are absent.
- Existing Composer V1/V2 tests and output behavior remain unchanged.
- Installation settings clearly label local execution as trusted and allow it
  to be disabled.

## Test strategy

- Contract tests run the same input/output fixtures against every profile.
- Golden-frame tests use perceptual comparisons rather than fragile byte-only
  comparisons where codecs differ.
- Unit tests cover schema validation, DAG construction, cache keys, project
  isolation, revisions, asset claims, and error classification.
- Integration tests use fake Media Studio jobs and local fixture assets; they do
  not incur paid generation.
- Local-MVP safety tests cover path traversal, symlinks, cancellation, malformed
  manifests, bounded logs, output limits, and the minimal child environment.
- Isolation-specific resource, network, and hostile-code tests are deferred to
  the optional Containers phase.
- Load tests render concurrent short 1080p jobs under configured capacity and
  prove that interactive work retains headroom.

## Recommended first implementation slice

Start with Python, Bun/TypeScript, and Go procedural **clips** that accept existing or already
materialized assets and produce one MP4 plus optional WAV stems. Include AI
bindings immediately, but reuse the exact v0.8 generation pipeline before the
process starts. Use installed/bundled dependencies only and trusted local
execution with explicit runtime paths.

That slice validates the important architecture without prematurely taking on
custom images, package downloads, GPU scheduling, raw frame streaming, or
distributed rendering. Every later language and executor remains an additive
runtime profile behind the same contract.
