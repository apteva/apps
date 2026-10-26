# Actors

Actors v0.2 adds schema version 2 crawl actors. Computer owns browser JavaScript and browser challenges; the crawl engine owns the durable URL frontier and datasets.

Use Actors to save and repeatedly execute browser workflows. Use Web separately for one-off search, extraction, crawl, research and snapshots. Actors calls Computer directly; installing Web is unnecessary.

1. Create a definition with `actors_save`. Schema version 1 accepts either `steps` (the implicit `run` operation) or `operations` mapping names to `steps` and `output_schema`. Both share defaults, presets, browser options, allowed hosts and limits.
2. Use `actors_run` with `actor_id`, optional `revision`, `operation`, `preset` and `input`. It returns a queued run ID. Poll `actors_run_get`; tool submission success is not execution success.
3. Read committed rows with `actors_dataset_read(run_id, after?, limit?)`. Pass the returned `next_cursor` as `after`. Partial rows remain readable after failure. Successful runs also include private JSONL/CSV export references.
4. Save repeatable inputs with `actors_task_save`, which pins the actor revision. Run with `actors_task_run`. Editing the actor does not silently change a saved task.
5. Use `actors_schedule` for Jobs-owned schedules. Schedules pin a revision at creation and support operation, preset or preset-pool selection, input, and schedule overrides. `actors_schedules` lists them; `actors_unschedule` cancels them.

For multi-page crawls, use `actors_crawl_save` with `schema_version: 2`. Start from `examples/crawl-template.json` and replace the host, selectors, fields, routes and datasets. A crawl definition contains `seeds`, named `routes`, `datasets`, and optional `frontier` settings. Each route extracts records from CSS-selected nodes and may `follow` URL fields into another route. Crawls retry truncated rendered HTML with larger bounded responses (up to 5 MB) and reject pages that remain incomplete. Computer must support the matching 5 MB extraction ceiling; older builds still cap at 1 MB and fail incomplete. Use `actors_crawl_run`, `actors_crawl_status`, `actors_frontier_list`, and `actors_crawl_resume` to monitor or resume the durable queue. Materialized named datasets are read with `actors_dataset_query` and are keyed by each dataset's `key` field. The UFCStats file is an optional site-specific fixture used for tests and demonstrations.

Supported v1 actions: `goto`, `click`, `fill`/`set_text`, `upload_file`, `key`, `scroll`, `wait`, `wait_for`, `extract`, `paginate`, `assert_url`, `assert_element`, and `screenshot`. `fill` and `set_text` accept either a CSS selector or a semantic SOM locator (`text`, `role`, `exact`, `som_only`). `upload_file` uses the same locator and accepts exactly one of `source_url`, `base64`, or `file_path`, with optional `filename` and `mime_type`; its result is returned under `media`. Use semantic locators for upload controls and composers so the action is tied to the live page. Crawl routes use rendered HTML and CSS selectors, durable URL fan-out, and dataset transforms: `trim`, `lowercase`, `integer`, `number`, `date`, `duration_seconds`, `ratio_landed`, and `ratio_attempted`. Arbitrary JavaScript and unbounded control flow are not part of the actor definition; Computer executes browser JavaScript for pages that require it.

Omit browser.backend to respect Computer's configured default. Explicit supported backend names are local, browserbase, steel, browser-engine and service, subject to that backend's browser capabilities. For a saved login, use a Computer-owned `browser.context_id` from Computer's context tools; Actors forces persistence for that context. Account signup and login are user-defined workflows. Do not put passwords or cookies in definition defaults, presets, or input: definitions and inputs are retained in run history. Templates such as `{{context_id}}` can reference a non-secret context ID.

Use assertions to detect expired login or unexpected pages. A failed login assertion fails this release's run; reconnect through Computer and explicitly retry. Do not imply that automatic authentication pause/resume exists. Account locking covers Actors runs within this installation, not unrelated Computer clients.

Runs have page/item/time limits and bounded exports (32 MiB per run). One worker executes runs within a single app process. Interrupted running work is marked failed on restart rather than automatically replaying browser actions. Click, key and pagination steps are not automatically retried. Full-run retries explicitly repeat the saved workflow; inspect any external side effects first.

All site-specific behavior belongs in user definitions. Examples are optional and are never installed on mount. There are no built-in Patreon workflows.

For hosted video posts, enter `{{video_url}}` using semantic `fill` in the site's embed URL control. Use `wait_for` with `conditions: [{"type":"media_present"}]`, optional `match: "all"` and additional URL/text/selector/target conditions, and `timeout_ms` between 500 and 30000. Unmatched or timed-out waits fail the run before publishing. A successful media wait returns embed provider, iframe URL and thumbnail URL under `media`. See `examples/video-url-publisher.json`; video providers and site controls remain user inputs. For Bunny Stream, construct the player URL from `videoLibraryId` and `guid` returned by the Bunny integration: `https://iframe.mediadelivery.net/embed/{videoLibraryId}/{guid}`. No provider-specific defaults are installed.

Form operations support semantic `set_checked` (`checked` boolean), `select_option` (exactly one of `value`, `values`, `text`, or `texts`, with optional `mode`), and `set_temporal` (`value` ISO date or displayed time). A `scroll` step accepts an observed scroll-region `target_id` and optional `expected_name`/`expected_role` guards. For the observed document region, use `target_id: "scroll_document"`, `expected_name: "Document"`, and `expected_role: "document"`; nested regions require their own current observed IDs. Omitted targets retain Computer's ambiguity checks. Extraction accepts `readability: false` to include navigation/dialogs and an optional field `pattern` (Go regexp; first capture becomes the extracted value before numeric conversion). A consequential `click` may include `once_key: "{{request_id}}"`. The key is reserved durably for that actor and operation before the attempt. Completed or uncertain attempts block later attempts using the same key, including explicit run retries; inspect the original run before intentionally issuing a different key. Keys do not guarantee the external site committed a click; use outcome waits.

Targeted scrolls refresh their semantic observation automatically, require the selected region to remain present with the declared identity, and forward the current semantic revision to Computer. They never substitute a different region when the selected one disappears or changes.

For library/account duplicate inspection, prefer named `read_only:true`
operations using `observe_page` and `inspect_views` (Actors 0.2.16+). The engine
rejects write actions in read-only operations. Sites and account/tier rules
remain in definitions and caller input. `observe_page` returns current URL,
configured account fields, visible navigation with observed URLs and semantic
control labels. Raw extraction retries a larger HTML budget and refuses
remaining truncation.

An `inspect_views` run must be completed AND return
`coverage.inspection_complete=true`, `more_results_remaining=false`, all
required checked views, matching context/account and complete per-view end
(and configured total-count) evidence before claiming absence. Read the full
dataset with cursors; the run preview is bounded. A failed, inaccessible,
truncated, stalled or budget-limited scan preserves partial rows and incomplete
coverage. An empty partial list never authorizes a new write. Reconcile any
existing exact record URLs and prior accepted write runs independently. Never
switch contexts or use a generic run to bypass a failed inspection.

Collection reads can use `read_views.entry_query` for safely encoded URL parameters and `views[].use_entry_page:true` for a URL that is already the desired collection. `empty_text_pattern` verifies an empty-state message. Next-control pagination can use `end_when_next_absent:true` with an exact SOM name and DOM `next.selector`; absence must remain stable after the verified view is ready. Read pagination reveals controls through verified document scroll targets and revalidates `navigation_only` in Computer before clicking.

Fields with `many:true` and a selector return ordered arrays, including attributes and resolved URLs. `assert_values.equals_exact` preserves case and whitespace; `greater_than` and `greater_than_field` can establish that a saved server record is newer than a pre-action baseline. See `examples/adultfolio-control.json` for an editable messaging workflow with identity, recipient, idempotency and saved-message checks.

`greater_than_or_equal` and `less_than_or_equal` enforce inclusive numeric bounds against literal or runtime values; missing, nonnumeric and nonfinite values fail. A consequential click may set `once_scope` to share its durable `once_key` namespace with another operation in the same actor. Without it the namespace remains the operation name. Keep both the scope and logical request key stable across revisions and retries. A failed/uncertain send must be reconciled with a read-only saved-record verifier; an unsuccessful Computer result is never proof that no action occurred.

Read-only operations may opt into `read_views.allow_partial:true` for a bounded page preview. Only a verified page/pagination bound can return a successful partial result; inaccessible views, identity mismatches, stalled pagination and truncation still fail. The output remains explicitly incomplete and cannot authorize a write or `select_record`. Pagination max_pages accepts a template, allowing caller-controlled page windows.

Inspect a pinned actor with `actors_get(id, revision)`; use its returned `retrieved_revision` and operation names, not the latest actor description, when assessing support. A missing revision is a failure.

A repair uses `actors_run_recover`, not a changed request/idempotency key. First inspect the failed run and explicitly review the replacement revision. Obtain a fresh complete read-only duplicate check using the replacement's configured recovery inspection operation in the original context, reading every dataset cursor. Pass `id`, `revision`, `reviewed:true` and `duplicate_check_run_id`. Unknown or successful original outcomes, consequence reservations, wrong contexts, incomplete/stale coverage or matching records block recovery; never bypass these checks. Recovery retains logical inputs and once keys and performs another fresh inspection as a linked child run before continuing.

For a pinned named operation, call `actors_get(id=..., revision=..., operation=...)`.
Read the complete compact operation and its shared execution settings; verify
`retrieved_revision`, `complete` and `step_count`. Do not infer that an operation
is absent from a large full-definition preview. A missing operation/revision or
incomplete response must stop execution; never substitute the latest pin.
Unqualified `actors_get` and `actors_list` are bounded metadata discovery only.
Follow their operation/list cursors when present. Full JSON editing/debugging
requires `actors_definition_export` with an explicit revision. Export chunks
must be fully assembled and hash/byte-count verified. An oversized exact
operation returns `complete:false` and its export reference, never partial
steps; ordinary workers should obtain only the named operation they need.

For approved label aliases, `assert_values` accepts an exact `canonical_map`
with `equals`/`equals_field`; preserve raw extracted labels, and inspect
`assertion_evidence`. Unknown labels are errors. Never infer provider equivalence.
Use `effect_phase:"prepare"` only on a guarded opener click that opens a
reversible dialog. Preserve Computer's effect classification: a financial
opener still requires financial acknowledgement; an open_configuration click
omits irreversible acknowledgement. `click_verified` cannot be preparation. Use `commit` on the
guarded final consequential click. Inspect both progress flags and effects; a
preparation guard is still not authorization for automatic retry/recovery.

For incremental inbox reads, use the operation's opt-in continuation support.
Pass `output.continuation.ref` back as `input.continuation_ref` at the same actor,
revision, operation and context before expiry. This continues the live read;
keep prior datasets because resumed rows are a delta. An expired, consumed,
changed or unavailable cursor is an error; do not silently start again. Close
unused refs with `actors_read_close`. Inspect `coverage.views[].pagination`
for target identity, before/after IDs, loading, errors and stop_reason.
`no_change_control_present` means incomplete history, never end-of-history.

For native-image verification (0.2.37+), use `observe_json` to read saved typed
attachment references and ordered IDs in the authenticated browser. Do not infer
attachment count/order from generated classes, thumbnails or CDN paths. Verify
exact count, distinct IDs, filenames/readiness and unchanged IDs after reload.
`document_evidence` and `failed_condition` retain useful native-media diagnostics.

Existing-draft recovery requires the creation operation's `recovery.existing`
contract, a fresh complete `duplicate_check_run_id`, and a fresh read-only
`resource_check_run_id`. `actors_run_recover` binds the same draft to the failed
run's original uploads, copy/access inputs and durable effect scope. Use named
`inspect_image_draft`/`inspect_gallery_draft` and the corresponding saved-draft
preflight. Dedicated `publish_*_draft` operations are `recovery_only`; never call
ordinary retry/new creation to recover media. Keep Catalog originals and process
actual-live predecessor requirements. See `examples/patreon-native-image-drafts.md`.


Reviewed same-draft copy amendments (0.2.39): `actors_run_recover` may receive
`reviewed_copy` only for explicitly approved text fields declared by the selected
revision's `recovery.existing.reviewed_copy_fields` (input to observed-field map).
First use a separately reviewed draft-only preparation operation, then run the
canonical read-only resource inspection with the amended text and original other
inputs. Its exact observed-value assertion and fresh completed run are required.
The recovery records original/amended text in provenance; duplicate identity,
creator, access, ordered media and original publish-once keys remain fixed.
No copy amendment is allowed in ordinary recovery or in the commit-only resume
operation. This capability does not itself authorize editing or publishing.

Named operations may declare `browser` to fully replace the actor browser configuration (no field inheritance). Use a distinct runtime variable such as `{{onboarding_context_id}}` for onboarding. `required_explicit_inputs: [onboarding_context_id]` rejects missing, null or blank values before browser allocation; defaults, presets and schedule overrides cannot satisfy it. The caller creates an isolated saved Computer context and supplies its non-secret ID. `actors_get(..., operation=...)` returns the effective browser and explicit input requirements. Existing operations continue using shared browser settings. This does not provide credential storage or automatic email verification: keep passwords and verification tokens out of actor input and snapshots.

For credential fields, use `fill_secret` with a normal locator and a non-secret `secret_ref`. The reference resolves only at dispatch from encrypted install `browser_secrets`, indexed first by project ID, then reference. Each entry requires `value`, exact `context_id`, exact HTTPS `host`, and RFC3339 `expires_at`; missing/expired/cross-project/cross-context/cross-host references fail before input. Literal `text`/`value` and optional secret fills are rejected. Raw values are not added to definition/input snapshots; fill errors suppress text-bearing backend diagnostics, and Actors screenshots are disabled after secret input. Computer/provider recording and telemetry have their own retention rules. Keep credential selectors out of extraction fields. Email retrieval, secret provisioning and signup-state reconciliation belong to the caller/process, not to site-specific engine code.

For an emailed verification link, `goto_secret` uses the same expiring secret reference after opening the bound saved context. It permits only HTTPS on the current bound host, suppresses URL-bearing errors and in-flight progress, and retains only the resulting origin (no path, query or fragment). Secret input and navigation steps are not automatically retried.
