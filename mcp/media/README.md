# Media

Media owns its catalog, transcripts, processing requests, API, UI, provider
connections and durable render outcomes. Local processing remains the default.

## Render queue and audio extents (v0.15.8)

Renders keep their original ID while waiting for weighted host capacity. Supply
`overall_timeout_seconds` to bound queue, cache-lock, processing and validation
time from submission (default 86400 seconds; range 30–604800). This saved deadline
survives restart. `timeout_seconds` separately limits execution after admission.
The former 600-second admission timeout is now a diagnostic threshold.

Inspect `media_get_render.metrics` for `overall_deadline_at`, `queue_age_ms`,
`execution_started`, `execution_started_at`, capacity, queue position, work units
and blocking leases. A claimed row can remain `running` at stage `admission`
without having started execution. Capacity expiry returns
`render_capacity_deadline_exceeded`; execution and overall processing expiry use
`render_execution_timeout` and `render_overall_deadline_exceeded`. Poll the same
render ID while queued; do not submit duplicate jobs. An expired request needs a
new explicit request and deadline.

Audio redaction preserves the original audio presentation extent with explicit
sample bounds, independently of video length. Timeline preparation applies no
gain changes. Diagnostics expose source/output sample counts and expected versus
observed start, duration, endpoint and drift, including on validation failure.
Existing validation tolerances and full audio/video checks remain enforced.
See [release details](RELEASE-v0.15.8.md).

## Per-request transcription options (v0.15.7)

Choose a transcription model/language directly on the request:

```json
{"file_id":"27706","model":"nova-2","language":"en","require_word_timings":true,"force":true}
```

Media saves the effective model, requested language and diarization default with
that queued attempt. Omitting model/language uses the operator's defaults when a
new job is queued. Automatic retries and ordinary retries without overrides keep
the saved settings; `force=true` starts a new attempt using the supplied options
and current defaults. A replacement source starts with current defaults.
Pending/running attempts cannot be reconfigured; a conflicting request fails
clearly (REST HTTP 409). This allows different files to use different models
without changing global installation settings or restarting Media.

Read `options` in the queue response and transcript, or `last_attempt.options`
when an upgrade failed while retaining an earlier completed transcript.
`options.language` is the requested language; transcript `language` is the result
language. Pre-upgrade pending jobs freeze their defaults once when claimed.
REST accepts the same options as query parameters on POST `/media/:id/transcribe`.

Transcription scripts should check that `media_transcribe` advertises `model`
and `language`, then pass them per request. They must not change and restore
`transcribe_model` or `transcribe_language` through the installation config API.

## Transcript timing (v0.15.6)

Refresh the Deepgram integration catalog before installing this release. Media
keeps its existing `listen` binding; that tool now preserves provider words and
audio/request identity. Large provider payloads stay inside the transcription
pipeline; callers can retrieve only the data they need:

```json
{"file_id":"27706","view":"summary"}
{"file_id":"27706","view":"words","start_ms":450000,"end_ms":482580,"limit":100}
{"file_id":"27706","view":"words","query":"mind control","occurrence":2}
```

`full` remains the default; `text`, `segments` and `words` are optional views.
Word/segment/match pages default to 200 rows and allow at most 1000. Repeat the
same filters with `cursor=next_cursor` while `has_more=true`. A changed revision
invalidates its cursors. `word_count` always describes the complete transcript;
`matched_count` and `returned_count` describe the filtered page. Phrase queries
return `matches` containing exact word indexes, intervals and occurrence numbers;
channel streams are never mixed into an artificial phrase. Windows use overlap
semantics. Segment views keep sentence text/bounds; speaker filters select
sentences containing that speaker. Nested words are included only when
`include_words=true`. Words support `channel` and `speaker` filters directly.

Timing stays in the stored timebase (Deepgram: `transcript_audio`). Request
`timebase="source"` to apply the recorded, source-matched mapping exactly once.
Summaries include the mapping, word timing status, source match and redaction
readiness. These fields do not establish complete recognition of all speech.
Text redaction rejects missing timings and ambiguous multichannel selection.

Request `media_transcribe({file_id,require_word_timings:true})` to safely upgrade
an existing transcript. Read `last_attempt` to track a candidate while the
previous completed transcript stays available. Failed attempts preserve that
version. No migration automatically retranscribes completed files. Verified saved
provider responses can use `media_set_transcript` with `deepgram_response`,
`source_sha256` and `transcription_audio_file_id`; a matching provider audio hash
and Media's validated audio evidence are required. Unverifiable responses must be
regenerated through Media. REST `/media/:id/transcript` exposes the same views
and filters, and POST `/media/:id/transcribe?require_word_timings=true` queues an
upgrade. Original media files remain unchanged.

## Executor component (v0.15.0)

Offloaded processing uses the private `executor` component and
`apteva.media-executor/v1`. Media uses App SDK v0.115.1; the pinned worker uses
v0.109.0. A Server supporting owned components and managed runtime backends is required; older
strict manifest parsers cannot install this release.

The worker runs the existing trusted Media processing plans: FFmpeg rendering,
indexing/derivations, analysis, Smart Crop sampling/pose runtimes and transcript
audio preparation. Media retains planning, source authorization, database writes,
provider calls and completion notifications. Processing scripts are generated by
the owning Media release; the worker must never be exposed as a general agent
shell tool. Server's private component routing enforces ownership and scope.

There is no infrastructure-app dependency, provider binding, host inventory
selector or direct infrastructure command callback in Media. Server selects and
manages the worker's execution backend. The worker has its own configuration,
token, data directory and deployment history. It receives no parent app token,
provider credentials or implicit app bindings.

### Operator setup

1. Install Media on a supporting Server.
2. Inspect `GET /api/apps/installs/:parent/components`.
3. Deploy the declared executor using administrator authentication and an
   `Idempotency-Key`:

   ```http
   POST /api/apps/installs/:parent/components/executor
   Content-Type: application/json
   Idempotency-Key: media-executor-setup-1

   {
     "placement": {"type":"local"},
     "config": {"work_capacity":"2"},
     "approved_permissions": ["net.egress"]
   }
   ```

   Select any compatible registered backend placement for remote execution.
   The parent release pins the worker's source to an exact commit. Declaring a
   component does not deploy it or grant permission to create infrastructure.
4. Wait for the deployment operation to complete, then set Media's
   `execution_mode` configuration to `component` through the normal app settings.

An unavailable or incompatible worker fails explicitly. It never causes a
silent local processing fallback. `execution_mode=local` retains the existing
local processing and optional Cloudinary behavior without deploying a worker.

For an older installation with nonzero `render_host_id`, first deploy the
component, remove the obsolete `render_host_id` configuration, and select
`execution_mode=component`. A retained legacy offloading setting selects the
component rather than silently using local CPU; its old host identifier is never
used to select infrastructure. Update unused old bindings through normal settings
if desired. Upgrading Media does not migrate live infrastructure automatically.

### Execution and transfers

The asynchronous protocol exposes `capabilities`, `submit`, `get`, `cancel` and
`acknowledge`. Jobs have bounded deadlines, weighted FIFO admission and a separate
control lane. Parent render attempts retain their exact submitted plan and job ID
in SQLite. A lost submit response is reconciled by inspecting that ID. Worker
receipts survive restarts; interrupted work is not automatically replayed. The
parent acknowledges only after its outcome is durably saved, allowing the worker
to compact output while keeping an idempotency tombstone.

The worker starts passively and accepts work only after Server activation.
Drain stops admission and reports success only once processing has stopped.
Cancellation kills and verifies the processing session, including descendants.
Uncertain termination remains an explicit failure. A crash during an unrecorded
process start blocks recovery until the owning backend/operator confirms cleanup;
the worker never assumes an unknown process stopped.

Source bytes use signed Storage URLs and the worker's verified source cache.
Outputs use expiring, project/destination/size-bounded upload capabilities issued
by Media. The relay permits only new upload initialization and that grant's own
upload sessions. It cannot read, delete or modify existing source files. It uses
Media's Storage credential internally and revokes grants after terminal receipts.
S3 output bytes use presigned PUTs; disk-backed Storage uses streamed resumable
uploads through the relay. The relay enforces private output visibility. Existing
size/checksum/destination validation remains in the parent before completion.
Resumable component chunks use base64 on the public hop to avoid binary-body
rejection by gateways; the relay streams original bytes to Storage. Encoded bytes
still count toward the job's transfer budget. Direct S3 uploads are unchanged.

Worker targets require POSIX Bash, curl, ps, awk, base64 and standard file tools. Python 3
is required for guarded audio processing and Smart Crop pose runtimes. System
FFmpeg/FFprobe may be selected with worker configuration. On supported Linux
amd64/arm64 targets lacking them, the worker installs a fixed checksum-verified
FFmpeg release in its own data directory. Smart Crop's existing versioned Python
setup and model checks remain in effect. Dependency setup may require network
access on first use. Scratch/cache paths are scoped to the worker installation.

## Validation

Run tests with the published SDK rather than a workspace overlay:

```sh
GOWORK=off go test -race -short ./...
GOWORK=off go test -race -run 'Test(Component|ExecutorTransfer)' .
cd executor
GOWORK=off go test -race ./...
```

The component integration tests start the real SDK worker process and execute
FFmpeg rendering, thumbnail derivation and guarded audio against disposable
Storage fixtures. Server's `TestAppComponent*` suite separately covers backend
placement, ownership, permission approval, protocol validation and rollback.
Real provider/backend deployment remains an operator-specific integration check.

## Audio-only changes to MOV/MP4 video

Guarded normalization verifies copied compressed packets before restoring the
original video presentation tables. This retains hidden HEVC dependencies and
source timestamps while keeping fast-start layout and encoded audio. Full
picture, timeline, audio and upload validation still runs before completion.
Unsupported restoration layouts fail explicitly; source files are never modified.
