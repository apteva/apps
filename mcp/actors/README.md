# Actors

Actors v0.2.0 adds durable schema version 2 crawls with URL fan-out, route matching, bounded retries, resumable frontier state, keyed materialized datasets, and declarative normalization transforms. Computer remains the browser and JavaScript runtime.

Actors is a standalone Apteva app for reusable browser workflows. It depends directly on **Computer**, **Storage** and **Jobs**. It does not call, import or require Web.

## Implemented

- Revisioned actor definitions with immutable revision history and per-run snapshots.
- Named operations sharing browser options, defaults, presets and limits; single-operation `steps` definitions also work.
- Computer browser sessions, saved-context selection, proxy/environment forwarding and saved-context exclusion between Actors runs.
- Navigate, click, fill, key, scroll, wait, extract, paginate, assert URL/element and screenshot steps.
- Queue, history, progress, cancellation, explicit snapshot retry and deadline-aware Computer calls.
- Saved tasks pinned to actor revision/operation/input.
- Page-by-page dataset persistence and cursor reads, including partial output after failure; private JSONL/CSV exports on success.
- Jobs schedules pinned to actor revisions, deterministic preset rotation and occurrence deduplication.
- Project-scoped MCP tools, HTTP routes and a self-contained Actors panel mounted by the dashboard, with project and installation IDs on every Actors request.
- Data explorer: choose a run and named dataset, browse typed rows with cursor pagination, search loaded rows, inspect full records, and export shown rows as JSON or CSV. Crawl reads use immutable run results, including partial results, rather than the latest materialized dataset.

This is the working foundation for the platform described in `PLATFORM_PROPOSAL.md`, not completion of the entire roadmap. Durable distributed queues, checkpoint resume, full JSON Schema contracts, isolated code actors, public API publication, webhooks and a shared catalog remain future work.

## Create and run an actor

`examples/page-reader.json` is a complete `actors_save` input. It defines a `read` operation for example.com. `examples/crawl-template.json` is a generic schema version 2 crawl starting point; replace its host, selectors, fields, routes and datasets for the site you own or are authorized to collect. Examples are files only; the app does not seed or install any definitions.

After saving it:

```json
{"actor_id": 1, "operation": "read", "input": {"url": "https://example.com"}}
```

Pass this to `actors_run`. Optional `idempotency_key` deduplicates retries with identical revision and inputs; reusing it for different work returns a conflict. Poll `actors_run_get` with `{"id": <run_id>}`. Read output with `actors_dataset_read` using `{"run_id": <run_id>, "limit": 50}` and pass `next_cursor` as `after` for subsequent pages. While a run is active, poll again at the last cursor for new committed rows.

For a saved login, add `browser.context_id` using a context created through Computer. Omit `backend` to defer to Computer; the saved context can determine its backend. The context remains owned by Computer, and Actors requests `persist: true`. Do not store credentials in inputs: input/definition snapshots are intentionally retained.

## Publish media

`upload_file` attaches an image or file using a semantic locator and exactly one of `source_url`, `base64`, or `file_path`. See `examples/media-publisher.json`. For a hosted video, use ordinary `fill`/`set_text` to enter `{{video_url}}` in the site's video URL field; no upload or provider-specific code is needed. `examples/video-url-publisher.json` is a generic starting point. Replace the host and controls for your site. The saved Patreon actor can accept a Bunny Stream player URL such as `https://iframe.mediadelivery.net/embed/{videoLibraryId}/{guid}`, constructed from the integration's video metadata.

Use `wait_for` with Computer's declarative `conditions`, `match: any|all`, and a bounded `timeout_ms` (500–30000). A timeout or unmatched result fails the actor before subsequent actions. `media_present` verifies a rendered audio/video player; combine it with a saved-draft text condition when needed. Successful embeds return provider, iframe and thumbnail URLs under run output `media`. This confirms the rendered embed, not continuous video playback. The same action supports URL, text, selector and semantic-target conditions for other sites.

## Verify saved copy

Text extraction normally normalizes whitespace. For exact editor copy, a text field may set `all: true` to collect every matching paragraph in document order and `preserve_line_breaks: true` to retain paragraph boundaries, blank paragraphs and inline text spacing. Use an explicit `separator` when the editor represents paragraph boundaries differently from inline line breaks (for example, `"\n\n"` for ProseMirror paragraphs). Select content paragraphs to exclude editor controls. Combine this with a required anchored pattern before a consequential action. Missing paragraphs or mismatched copy fail the run. Existing definitions keep their previous extraction behavior.

For saved plain-text messages whose renderer adds indentation around line breaks,
`trim_line_edges: true` may be combined with `preserve_line_breaks: true` on a
text field. It removes spaces, tabs, carriage returns and non-breaking spaces
only at line edges, plus outer empty lines. Internal spacing, case, punctuation,
paragraph boundaries and blank paragraphs remain intact. Exact-copy assertions
remain exact; this setting is opt-in and is invalid on attributes or non-text
fields. Retain an unnormalized companion field when raw rendering evidence is
useful. Do not use it to bypass identity, newer-record or duplicate-send guards.

`examples/patreon-gallery-post.json` is an optional two-original gallery example. It is never seeded into an install. Contexts, sources, copy and access settings are runtime inputs.

## Crawl a site

For a concrete test fixture, `examples/ufcstats.json` shows how a site-specific definition can model events, fights and fighters. It is optional example data only. Save a definition's `definition` with `actors_crawl_save`, then pass the returned actor ID to `actors_crawl_run`. The run creates a durable URL frontier, follows configured URL fields into their routes, upserts keyed records into named datasets, and can be inspected with `actors_crawl_status`, `actors_frontier_list`, and `actors_dataset_query`. `actors_crawl_resume` continues pending work after a budget limit or restart.

`actors_task_save` accepts `name`, `actor_id`, `operation`, `input`, optional `preset` and optional `revision`. It pins the current revision when omitted. Later actor edits do not change that task. Use `actors_task_list`, `actors_task_run` and `actors_task_delete` to manage it.

## HTTP routes

Routes are relative to the server's authenticated app mount, normally `/api/apps/actors`. Global installs use the gateway-authorized `project_id` query context. The app relies on the Apteva gateway for caller authentication; it is not a public unauthenticated service.

| Method | Path | Behavior |
|---|---|---|
| GET / POST | `/actors` | List / save definitions |
| GET | `/actors/{id}?revision=N` | Retrieve an exact immutable definition and operation names |
| DELETE | `/actors/{id}` | Delete an actor after removing tasks and schedules |
| POST | `/actors/run` | Queue a run; same input as `actors_run` |
| GET | `/runs`, `/runs/{id}` | History and run detail |
| POST | `/runs/{id}/cancel`, `/runs/{id}/retry` | Cancel / explicitly repeat the snapshot |
| POST | `/runs/{id}/recover` | Recover with a reviewed revision and fresh duplicate inspection |
| GET | `/runs/{id}/dataset?after=0&limit=50` | Committed items |
| GET / POST | `/tasks` | List / save tasks |
| POST | `/tasks/{id}/run` | Execute a pinned task |
| DELETE | `/tasks/{id}` | Delete a task |
| GET / POST | `/schedules` | List / create Jobs schedules |
| POST | `/schedules/{id}/run`, `/schedules/{id}/cancel` | Run now / cancel an Actors-owned schedule |

HTTP submissions return a JSON queued-run response; they do not synchronously execute the browser workflow. These are authenticated app routes, not generated public endpoints. Use API publication only after implementing and verifying the proposal's publication contract.

## Operational bounds

The initial worker claims one queued run per tick. Operate one app process per database; multiple control-plane replicas and distributed runners are not yet supported. On startup, interrupted running work becomes failed. Queued runs survive restart. Explicit retry creates a new run from the old snapshot; there is no automatic replay after a crash.

Datasets are persisted after each validated page; export generation still retains a bounded in-memory dataset (maximum 32 MiB, 100,000 records, 500 pages and one hour per run). Dataset reads are bounded by count and response bytes. No automatic retention is enabled; manage database and Storage capacity accordingly. Deleting a definition preserves versions and historical runs. Actor IDs are never reused.

Context locks cover Actors runs in the same installation and project. They expire after the run deadline plus a cleanup interval; direct Computer clients remain responsible for avoiding concurrent use of the same context. Browser close is attempted on completion/failure/cancellation.

Login checks can be represented by `assert_url` / `assert_element`. Failure requires reconnection through Computer and an explicit run retry; interactive pause/resume is planned. Site redesigns require editing the actor definition. No site-specific behavior is compiled into the engine.

Click, key and pagination steps are not automatically retried because an uncertain action may already have changed the site. A full-run retry can still repeat side effects. This release provides no exactly-once guarantee for external writes.

Scroll steps can forward an observed Computer scroll region using `target_id`,
with optional `expected_name` and `expected_role` identity guards. For example,
`{"action":"scroll","direction":"down","amount":450,"target_id":"scroll_document","expected_name":"Document","expected_role":"document"}`
targets the observed document even when a rich-text editor is independently
scrollable. Before a targeted scroll, Actor refreshes the observation, verifies
the selected region and its identity guards, and forwards the new semantic
revision. Nested panes need their own current observed region ID. Actor does
not infer a region when the fields are omitted, and Computer still rejects
ambiguous, stale, or mismatched targets.

## Development

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go build -o /tmp/apteva-actors .
cd ../..
bun run scripts/build-panels.ts --app actors
bun test mcp/actors/ui/ActorsPanel.test.ts
```

The app-sdk pin is `v0.100.0`, derived from fetched tag ancestry (`v0.99.0` is its ancestor). `GOWORK=off` verifies the app against its published dependency rather than the workspace overlay.

The release manifest pins source to the matching immutable Actors tag. The registry references the same tag for the manifest and icon. Publishing a release makes Actors available for installation; it does not install the app into existing projects.

## Web compatibility

Web's search, extraction, crawl, map, research and snapshot tools remain independent. Its existing extractor endpoints and data have not been deleted or silently rerouted. New reusable workflows should use Actors. A later explicit migration can import definitions and replace old endpoints with compatibility forwarding; it must account for app-local IDs, saved Jobs targets and historical run ownership.

## Form and consequential actions

Semantic `set_checked`, `select_option`, and `set_temporal` support access toggles, dropdowns and schedule fields. Use `readability: false` on extract/assert steps when controls live outside the primary content. An extracted field may supply `pattern` to capture a labelled value before type conversion.

For sends, payments or deletions, add `once_key: "{{request_id}}"` to the acknowledged consequential click. Reservations persist across run retries, restarts and actor revisions. The same actor operation cannot attempt that key again, even if its prior outcome is uncertain. Inspect the referenced original run before deliberately issuing a new key. This guards duplicate attempts; it does not provide exactly-once execution on an external site.

## Exact selections from runtime lists

`set_checked` accepts `labels: "{{tier_labels}}"` with an exact `som_only` locator and optional role. The input must be a unique array of single-line names, at most 50 entries. Each target is resolved using a fresh SOM observation; missing or ambiguous targets stop the operation. An empty list performs no selections. Clear existing choices in an earlier step when replacing a selection.

`assert_values` supports `equals_set: "{{tier_labels}}"` to compare the exact set of selected labels, ignoring order but rejecting duplicates, omissions and extra entries. An aggregated text field should use `all: true, separator: "\n"`; absent optional fields represent an empty set. No creator names, prices or tier policy live in the engine.

An exact SOM locator may declare `text_suffix_pattern`, for example `(?:\s+[0-9]+\s+members?)?`, to allow a known metadata suffix while retaining the exact literal name. The regex is anchored after the escaped name. It cannot silently match another name that starts with the same word. Semantic dispatch retains the fresh target ID, SOM revision and role checks. Ordinary locators also pass the observed name; metadata-suffix locators omit that extra name check because native accessible names can concatenate whitespace differently. Authors must verify the saved values before committing.

### Scroll and paginated views (0.2.18)

Scroll traversal can also follow an optional exact `som_only` Next locator at
the bottom. Only a fresh `navigation_only` control can be clicked, and its
record signature must advance. Disabled or missing controls do not override
the required collected-count check.

### Raw extraction dependency (0.2.17)

Computer 0.7.94 or later is required so `readability:false` preserves navigation,
headers and sidebars used for creator verification and observed view links.

### Read-only view coverage (0.2.16)

Named operations can set `read_only:true` and optional `limits` to override their
own page/item/duration budgets. Read-only operations reject form changes,
uploads, generic clicks and consequential actions at validation and execution.
`observe_page` takes a raw DOM extraction plus a fresh semantic observation and
returns `current_url`, `page_title`, configured fields, `visible_navigation`
(with observed URLs) and `visible_controls` (labels/roles). Raw extractions
retry a larger HTML budget and fail explicitly if still truncated at 5 MB.

`inspect_views` is a generic read-only collection traversal. Its `read_views`
configuration declares an entry URL, required creator/account identity link,
expected identity URL, and named views. Each view declares `covers`, an observed
navigation anchor selector, verified URL pattern, ready/empty/loading/error
selectors, item fields, required deduplication key, and bounded pagination.
Sites, accounts, labels and statuses belong entirely to user definitions.

Pagination supports fresh semantic Next controls verified by Computer as
`navigation_only`, with an explicit end selector, or a named scroll region.
Scroll traversal records every overlapping batch and requires repeated unchanged
DOM keys and scroll geometry at the verified end, with no loading indicators.
It never treats a missing control, silent empty list, inaccessible view,
truncated HTML, item/page/time budget, changed identity or conflicting record as
exhaustive. It retains partial dataset rows and returns a failed run with
`coverage.inspection_complete:false` and `more_results_remaining:true` when
results may remain. A completed run has per-view URL, checked/completion flags,
page count, unique count and end evidence. A configured required numeric `total` field additionally requires the deduplicated count to match the UI total before completion; missing totals and mismatches fail closed. Coverage is a point-in-time UI scan;
workers must also reconcile previously recorded exact post references and
repeat the check if another publisher can change the library.

`field_defaults` supply missing values and `field_rewrites` derive fields with a
regular expression (`from`, `pattern`, `replacement`, optional `when_field` and
`equals`). Rewrites read the original extracted/defaulted values, independently
of map order. Conflicting copies of an ID fail closed. Large datasets must be
read through `actors_dataset_read`; the run preview is deliberately bounded.
The optional Patreon inspection example documents combined published/scheduled
Posts and separate Drafts navigation; it is not engine behavior or an account
preset.

## Verified record and media recovery (0.2.19)

Definitions support up to 500 named operations within the existing 1 MiB definition limit. This does not raise the 100-step limit for an individual operation or its runtime/dataset limits. Use `actors_get` with an exact revision and `operation` to retrieve only the operation needed for execution.

`select_option` accepts exactly one of `value`, `values`, `text` or `texts`, so browser dropdowns can be selected using either internal values or observed display labels. Optional `mode` is forwarded to Computer (`replace`, `add`, `remove` or `toggle`); it defaults to Computer's normal replace behavior.
A write operation may declare an `inspect_views` step with `read_only:true`;
scoped read-only steps reject writes at validation and execution.
`select_record` requires complete `inspect_views` coverage and selects exactly
one collected record by `verified_field` and `value`, including earlier pages.
Its fields feed subsequent `assert_values` checks. Missing/ambiguous/incomplete
records fail. `observe_page` returns fresh Computer media source, player/error
and draft-save observations. `assert_values.matches` accepts a declared regex
for exact identity checks with known optional URL query parameters.

The optional `examples/patreon-existing-video-draft.json` separates read-only
inspection, attachment and publication of the same existing draft. It preserves
copy, requires exact saved media/access checks and never creates a replacement.

## Verify embedded URL identity

`assert_values` supports `equals_url` with `url`, optional `allow_extra_query`
keys and explicit `wrappers` (`url` and source `parameters`). It checks the exact
source origin/path and all expected query values. A wrapper must match its
configured origin/path, and every present source parameter must match the
approved source. Repeated/empty/conflicting source parameters fail. Provider
hosts are definition policy, never built into the engine. See the optional
`examples/patreon-existing-video-draft.json` recovery operations (Actors 0.2.21+).

Read collection pagination supports `advance_timeout_ms` (500–60000; default 10000) to wait for a verified AJAX response without another click. `reveal_from:"start"|"end"` chooses the document boundary used to reveal controls through verified scroll regions and overlapping viewports. `on_stall:"partial"` requires `allow_partial:true` and a read-only operation; only an unchanged, identity-verified collection after the wait may return partial success. It retains incomplete coverage and a stall reason; unsafe targets, inaccessible views, changed records and truncation remain errors. Page/item bounds preserve the verified dataset prefix.

Fields may set `own_text:true` to extract only a node’s direct text, excluding nested badges or controls. It also works with `all` and `many`; attributes and `preserve_line_breaks` cannot be combined with it. The generic `type` action sends 1–256 bytes as literal keyboard input when a control requires native typing events. Prefer `set_text` for full fields, verify focus and field contents, and keep literal typing out of read-only operations.

## Historical reads and repaired recovery

`actors_get` accepts `revision` with either `id` or `name`. It returns the immutable definition at that exact revision, `retrieved_revision`, the current `latest_revision`, and sorted operation names. Unknown revisions fail instead of substituting the latest definition. HTTP: `GET /actors/{id}?revision=N`.

Ordinary `actors_run_retry` retains the original definition and inputs. `actors_run_recover` (HTTP `POST /runs/{id}/recover`) accepts `revision`, `reviewed:true`, and `duplicate_check_run_id` to use an explicitly reviewed repair. A publishing operation opts in with generic `recovery: {inspection_operation, required_views, match_fields}`; match_fields maps dataset fields to original input variable names. The engine contains no provider-specific post, audience or duplicate rules.

Recovery accepts only a failed run with durable no-commit execution evidence, or a complete legacy read-only trace prefix ending in a pre-dispatch locator failure. Successful, cancelled, active, uncertain and incomplete outcomes fail closed. The original operation, inputs, preset, context and consequential once keys are retained; a new linked run records the original execution key. Reserved, uncertain or completed consequence guards are never cleared. The supplied read must use the reviewed immutable revision, match the original creator/context, finish within the previous 15 minutes and prove complete coverage of every required view. Read all dataset records to check configured exact duplicate fields. The worker repeats the inspection as a separate immutable child run before the repaired attempt, blocking if a duplicate appeared or coverage became unavailable.

The editable [Patreon publishing example](examples/patreon-publishing.md) includes guarded native composer navigation and format-specific preflights that reload and verify drafts before Publish. Preflights autosave drafts; use your test account for content checks.

## Compact immutable operation retrieval

Use `actors_get` with `id` (or `name`), `revision` and `operation` to inspect only
one operation, for example `{"id":1,"revision":55,"operation":"gallery_post"}`.
The response includes actor ID/name, actual retrieved/latest revisions, the
complete stored operation JSON, shared execution settings, effective limits and
input requirements. Shared defaults and presets are retained because execution
renders that configuration too. `required_inputs` describes missing bindings
without a preset; `required_inputs_by_preset` describes each configured preset.
Constraints remain in the original `assert_inputs` steps, identified by zero-based
`assertion_steps`. Precedence is defaults, preset, schedule overrides, then input.

Operation limits replace the actor limits as a whole, as they do during execution.
Unresolved templated limits report their expression and engine bounds, rather
than pretending to be a fixed budget. `step_count`, `operation_sha256` (SHA-256 of
the compact stored operation JSON) and `complete:true` support completeness checks.
Discovery and complete operation MCP text responses are capped at 32 KiB.
An oversized operation returns `complete:false`, no executable fragment, and an
exact `actors_definition_export` reference. Missing operations/revisions fail;
even a latest-revision request reads its immutable version row.

In v0.3.0, omitting `operation` returns metadata and operation names only, with
`definition_included:false`. Follow `next_operation_after` as `operation_after`
while `operations_complete:false`. `actors_list` also omits definitions and
pages within the response budget; pass `next_cursor` as `after` while `has_more`.

Full definitions require explicit `actors_definition_export(id, revision)` for
editing/debugging. It returns bounded base64 chunks, byte offsets, total bytes,
a SHA-256 digest and explicit completeness metadata. Assemble all chunks for
that exact revision and verify the total/digest before treating the JSON as
complete. Add `operation` to export an oversized complete operation contract,
including all execution settings. A chunk alone is never executable.

The editor lists metadata and explicitly downloads
`GET /actors/{id}/definition/file?revision=N` when Edit is selected. The download
returns the exact immutable JSON with revision/digest headers. Other editor
clients must migrate from implicit `actor.definition` discovery to this export;
they must never populate a starter definition when an existing download fails.
Retrieval does not execute steps, reserve publish keys or alter actor definitions.

Exact-label comparisons can opt into `canonical_map` with `equals` or
`equals_field`. Both operands must be strings present in the explicit map;
unknown labels fail closed. `assertion_evidence` retains original and canonical
values. This does not infer relationships between providers.

A guarded opener click may declare
`effect_phase: "prepare"`. It sets `preparation_attempted`, while a guarded
consequential `effect_phase: "commit"` sets `commit_attempted` before dispatch.
Unannotated steps retain conservative legacy classification. Preparation guards
still block automatic recovery/replay. A financial modal opener retains its
`financial_action` effect and identical acknowledgement; a normal
`open_configuration` opener omits irreversible acknowledgement.
`click_verified` cannot declare preparation.

Read-only single-view `inspect_views` operations can opt into
`continuation_enabled:true`, `continuation_ref:"{{continuation_ref}}"` and
`continuation_ttl_seconds:600`. They must be entirely read-only, use one
entry-page next view and allow partial results. A bounded read returns
`output.continuation.ref` and `expires_at`. Pass the ref in the next run's
input at the same actor, immutable revision, operation and context. The live
verified browser resumes at its existing page and emits new records; it does
not reopen or scan the inbox again. References are single-use, scope-bound and
expire; unavailable/changed sessions require an explicit fresh read. Read pages
can change between batches. Close unused references with `actors_read_close`.
A worker closes expired retained browsers. Provider expiry can happen earlier.

Resumed coverage has `read_scope:incremental` and `resumed_from`; its dataset
is a delta, not a new full-inbox snapshot. Keep earlier datasets when combining
batches. Such results are rejected as recovery duplicate evidence.

`coverage.views[].pagination` records exact dispatched targets, before/after
IDs, new IDs, loading observations, errors and stopping reasons. A native
`click_mode:selector` pagination option verifies a unique DOM element and its
exact label, then Computer atomically checks the native target and navigation
effect. SOM remains the default. No coordinates or scripted click bypasses are
used. An unchanged page with a present control remains incomplete.

Each read view may declare `verification:{fields,assertions}` using the same
field extraction and `assert_values` rules as ordinary steps. Verification runs
against untruncated, complete rendered HTML with readability disabled before
records are emitted, and after each page advance. Scalar fields must resolve to
at most one element; duplicate selected options fail explicitly. Computer
0.7.96+ materializes live form values and selections in this snapshot, including
controls outside the viewport. This allows site definitions to verify actual
country, location and radius state without scrolling every control into view.
`coverage.views[].verification` returns `verified`, `applied_values`,
`result_url`, `source`, `requires_visible_controls:false` and `checked_at`.
Configure `inactive_fields:["radius", "location"]` to move those observed
values into `inactive_values`; inactive fields cannot assert applied state.
A mismatch leaves coverage incomplete and returns an error; requested input or
URL parameters alone never count as proof of applied filters. If the site uses
inactive controls, the site definition must distinguish their reported values
from filters actually applied to the search.

`read_views.use_current_page:true` inspects a verified widget in an already open
browser, preserving the native query/composer state instead of navigating again.
It requires a single entry-page view, no entry query and no continuation. Its
normal creator, URL, untruncated DOM and filter checks still run before records.
`pagination.mode:"snapshot"` returns a bounded, explicitly non-exhaustive read
without scrolling or clicking a pagination control. It requires `allow_partial`;
a preparation/search operation must also mark that inspection step
`read_only:true` and may not place any write/input action after partial
inspection. Snapshot coverage always remains incomplete and cannot be used
as complete duplicate-inspection evidence. A rejected durable once guard reports
no new commit attempt, while unknown dispatch failures retain conservative
commit-attempt metadata. Restart recovery releases terminal context locks without
replaying version-1 writes.

## Saved structured media and same-resource recovery (0.2.37)

`observe_json` reads exactly one raw rendered DOM node containing a complete JSON
value. `json_document: {selector, fields}` uses RFC 6901 pointers. A field has
`path`, `required`, optional `select` (ordered array projection), `unique`
(identity cardinality), or `is_null`. `resolve: {pool, keys, through?, drop_null?}`
joins typed references from the same document; duplicate/missing references fail.
`through` follows at most five additional typed references, and `drop_null` skips
only an explicit null reference. The engine preserves order, gathers partial
field evidence, and rejects truncated/invalid/ambiguous documents. Array
`equals_exact` and `equals_field` comparisons preserve identity and order.

A read-only `goto` may use `url_from_field` from prior verified extraction. An
optional `url_match_pattern` must match the entire extracted URL before replacing
it with the declared `url` using Go regexp capture expansion. The resulting URL
still passes normal host and browser-target checks. This permits reading a saved
resource through the authenticated browser without exposing cookies or arbitrary
JavaScript. `document_evidence` records the source page, document URL, observed
fields and array counts; failures include `failed_condition` and commit metadata.

`inspect_control` can open an exact semantic menu/dialog button only when a fresh
SOM observation verifies `open_configuration`, an enabled button role and no
danger/loading flag. It rejects selection controls and unknown/write effects.
`dismiss_inspection` sends only fixed Escape. These read-only actions permit exact
selected-label inspection without changing selection or inferring legacy prices.

A creation operation can declare `recovery.existing` with `resume_operation`,
`inspection_operation`, `url_pattern` (one resource-ID capture), exact
`preparation_locators` for reviewed historical missing annotations, and exact
`inspection_locators` allowed in the resume path. Explicit `bindings` map resource
ID/URL inputs, duplicate ID/URL/status fields, ordered upload evidence, readiness
and media-type values, expected-media input names and literal uncommitted-state
assertions. All original variables must match the inspection. These field names
and status values are user configuration, not provider rules in the engine. `actors_run_recover` then
requires a fresh `resource_check_run_id` as well as complete duplicate evidence.
The draft inspection must use the reviewed immutable definition, original
context/request/copy/access inputs and saved resource. Ordered saved media IDs,
filenames, sizes and readiness must match the original completed upload evidence.
The only permitted duplicate is that exact original unscheduled draft; all other
matches block. Recovery supplies protected resource inputs itself, retains the
original operation/input/effect scope, and repeats full duplicate coverage.

The resume definition has no uploads, text/access edits, new-composer navigation
or unapproved controls. `recovery_only:true` blocks ordinary queue/retry/schedule
entry, and the guarded commit must remain bound to the saved resource and ordered
media. Old immutable snapshots and failed runs are untouched; deployment never
replays them. See `examples/patreon-native-image-drafts.md` and its editable
example for the site-specific contract. Preserve upstream approved-file lineage
and actual-live predecessor checks.
# Revealing semantic controls

`reveal_control` scrolls a named region and observes a fresh SOM until one exact enabled control is visible. It never clicks or uses coordinates. Set `locator: {text: "Send!", exact: true, som_only: true}` and `reveal: {scroll_target_name: "Document", direction: "down", max_scrolls: 40}`. Scrolls overlap the viewport and verify the region ID/revision; missing, loading, ambiguous or changed-page results fail explicitly. After revealing, re-extract and assert the intended content/recipient, then use the existing guarded semantic click. A click resolution failure before dispatch records `commit_attempted:false` and effect state `not_dispatched`; its once reservation remains in place. Transport failures after dispatch remain uncertain.

Named operations may declare `browser` to fully replace the actor browser configuration (no field inheritance). Use a distinct runtime variable such as `{{onboarding_context_id}}` for onboarding. `required_explicit_inputs: [onboarding_context_id]` rejects missing, null or blank values before browser allocation; defaults, presets and schedule overrides cannot satisfy it. The caller creates an isolated saved Computer context and supplies its non-secret ID. `actors_get(..., operation=...)` returns the effective browser and explicit input requirements. Existing operations continue using shared browser settings. This does not provide credential storage or automatic email verification: keep passwords and verification tokens out of actor input and snapshots.

`fill_secret` resolves an expiring reference from encrypted install settings (`browser_secrets`, per project), requiring an exact saved context and HTTPS host. Only the reference is stored in actor definitions and inputs. Backend fill diagnostics are suppressed and Actors screenshots disabled after secret input. Provision these settings through the authenticated operator API; keep plaintext credentials and verification codes out of ordinary inputs, extraction selectors and logs. Computer/provider telemetry has its own retention policy.

For an emailed verification link, `goto_secret` uses the same expiring secret reference after opening the bound saved context. It permits only HTTPS on the current bound host, suppresses URL-bearing errors and in-flight progress, and retains only the resulting origin (no path, query or fragment). Secret input and navigation steps are not automatically retried.

## Larger extraction budget

Rendered HTML reads grow from 200,000 through 400,000, 800,000, 1,000,000 and 2,000,000 up to a 5,000,000 response budget, stopping as soon as Computer returns a complete result. The bound applies to raw view inspection and crawl HTML. Actors v0.2.47 requires Computer v0.7.97 or newer for the same larger ceiling. Older Computer builds can clamp the response at 1 MB, which Actors treats as incomplete. Per-record, dataset and exported-file bounds are unchanged.

## Bounded read-only navigation retry

A reviewed `goto` step can opt into `read_only: true, read_retries: 1` (maximum2). This overrides the legacy operation-wide retry limit only for that navigation. Transport timeouts may reopen the same saved browser context and repeat that read, within the original run deadline. It never replays earlier fills/uploads or subsequent clicks, and non-transient errors still stop. The field is rejected on writes, secret operations and consequential/effect-bearing steps. Existing definitions and global retry defaults are unchanged.

The optional `examples/flights/` definitions and transaction fixtures document local checkout preparation and synthetic payment guards. They do not enable live ticket purchasing or install any site-specific actor automatically.
## Native navigation and Clips4Sale example

Read views may use `navigation` with one to five exact SOM-only native tab/button
locators, mutually exclusive with `link_selector` and `use_entry_page`. Each hop
requires a fresh control whose effect is `navigation_only`. `read_navigation`
provides the same check for non-optional read-only steps.

`assert_no_record` rejects an exact field/value duplicate only after complete
collection coverage with no remaining results. `assert_inputs` checks execution
variables before writes while preserving the previous extraction evidence.
For searchable or controlled dropdowns, use the observed native control and
an exact SOM option, then verify the saved label. `select_option` uses internal
`value`/`values`.

The [Clips4Sale Creator example](examples/clips4sale-control.md) covers account,
current-month statistics, catalog coverage, draft preparation, read-only preflight
and guarded submission for review. It is caller-configured and currently gates
publication to explicitly declared no-real-person footage.

A `navigation_only` opener can reserve a `once_key` without
`confirm_consequence`. Navigation is not an irreversible acknowledgement class;
consequential clicks continue to require a matching acknowledgement.

`goto_record` follows an absolute HTTP URL selected from a complete verified
collection. It requires a unique collected record, read-only non-optional
navigation and the usual host checks. This supports reopening a persisted draft
whose ID was assigned by the site during upload.

Record selection and duplicate assertions use only the latest verified
collection. Prior composer extractions and older catalog snapshots remain in
the audit dataset but cannot satisfy those checks.
