# Actors

Actors v0.2 adds schema version 2 crawl actors. Computer owns browser JavaScript and browser challenges; the crawl engine owns the durable URL frontier and datasets.

Use Actors to save and repeatedly execute browser workflows. Use Web separately for one-off search, extraction, crawl, research and snapshots. Actors calls Computer directly; installing Web is unnecessary.

1. Create a definition with `actors_save`. Schema version 1 accepts either `steps` (the implicit `run` operation) or `operations` mapping names to `steps` and `output_schema`. Both share defaults, presets, browser options, allowed hosts and limits.
2. Use `actors_run` with `actor_id`, optional `revision`, `operation`, `preset` and `input`. It returns a queued run ID. Poll `actors_run_get`; tool submission success is not execution success.
3. Read committed rows with `actors_dataset_read(run_id, after?, limit?)`. Pass the returned `next_cursor` as `after`. Partial rows remain readable after failure. Successful runs also include private JSONL/CSV export references.
4. Save repeatable inputs with `actors_task_save`, which pins the actor revision. Run with `actors_task_run`. Editing the actor does not silently change a saved task.
5. Use `actors_schedule` for Jobs-owned schedules. Schedules pin a revision at creation and support operation, preset or preset-pool selection, input, and schedule overrides. `actors_schedules` lists them; `actors_unschedule` cancels them.

For multi-page crawls, use `actors_crawl_save` with `schema_version: 2`. Start from `examples/crawl-template.json` and replace the host, selectors, fields, routes and datasets. A crawl definition contains `seeds`, named `routes`, `datasets`, and optional `frontier` settings. Each route extracts records from CSS-selected nodes and may `follow` URL fields into another route. Crawls retry truncated rendered HTML with larger bounded responses (up to 1 MB) and reject pages that remain incomplete. Computer v0.7.92 or later supports this larger response limit. Use `actors_crawl_run`, `actors_crawl_status`, `actors_frontier_list`, and `actors_crawl_resume` to monitor or resume the durable queue. Materialized named datasets are read with `actors_dataset_query` and are keyed by each dataset's `key` field. The UFCStats file is an optional site-specific fixture used for tests and demonstrations.

Supported v1 actions: `goto`, `click`, `fill`/`set_text`, `upload_file`, `key`, `scroll`, `wait`, `wait_for`, `extract`, `paginate`, `assert_url`, `assert_element`, and `screenshot`. `fill` and `set_text` accept either a CSS selector or a semantic SOM locator (`text`, `role`, `exact`, `som_only`). `upload_file` uses the same locator and accepts exactly one of `source_url`, `base64`, or `file_path`, with optional `filename` and `mime_type`; its result is returned under `media`. Use semantic locators for upload controls and composers so the action is tied to the live page. Crawl routes use rendered HTML and CSS selectors, durable URL fan-out, and dataset transforms: `trim`, `lowercase`, `integer`, `number`, `date`, `duration_seconds`, `ratio_landed`, and `ratio_attempted`. Arbitrary JavaScript and unbounded control flow are not part of the actor definition; Computer executes browser JavaScript for pages that require it.

Omit browser.backend to respect Computer's configured default. Explicit supported backend names are local, browserbase, steel, browser-engine and service, subject to that backend's browser capabilities. For a saved login, use a Computer-owned `browser.context_id` from Computer's context tools; Actors forces persistence for that context. It does not create or log into accounts. Do not put passwords or cookies in definition defaults, presets, or input: definitions and inputs are retained in run history. Templates such as `{{context_id}}` can reference a non-secret context ID.

Use assertions to detect expired login or unexpected pages. A failed login assertion fails this release's run; reconnect through Computer and explicitly retry. Do not imply that automatic authentication pause/resume exists. Account locking covers Actors runs within this installation, not unrelated Computer clients.

Runs have page/item/time limits and bounded exports (32 MiB per run). One worker executes runs within a single app process. Interrupted running work is marked failed on restart rather than automatically replaying browser actions. Click, key and pagination steps are not automatically retried. Full-run retries explicitly repeat the saved workflow; inspect any external side effects first.

All site-specific behavior belongs in user definitions. Examples are optional and are never installed on mount. There are no built-in Patreon workflows.

For hosted video posts, enter `{{video_url}}` using semantic `fill` in the site's embed URL control. Use `wait_for` with `conditions: [{"type":"media_present"}]`, optional `match: "all"` and additional URL/text/selector/target conditions, and `timeout_ms` between 500 and 30000. Unmatched or timed-out waits fail the run before publishing. A successful media wait returns embed provider, iframe URL and thumbnail URL under `media`. See `examples/video-url-publisher.json`; video providers and site controls remain user inputs. For Bunny Stream, construct the player URL from `videoLibraryId` and `guid` returned by the Bunny integration: `https://iframe.mediadelivery.net/embed/{videoLibraryId}/{guid}`. No provider-specific defaults are installed.

Form operations support semantic `set_checked` (`checked` boolean), `select_option` (`value` or `values`), and `set_temporal` (`value` ISO date or displayed time). Extraction accepts `readability: false` to include navigation/dialogs and an optional field `pattern` (Go regexp; first capture becomes the extracted value before numeric conversion). A consequential `click` may include `once_key: "{{request_id}}"`. The key is reserved durably for that actor and operation before the attempt. Completed or uncertain attempts block later attempts using the same key, including explicit run retries; inspect the original run before intentionally issuing a different key. Keys do not guarantee the external site committed a click; use outcome waits.

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

Read-only operations may opt into `read_views.allow_partial:true` for a bounded page preview. Only a verified page/pagination bound can return a successful partial result; inaccessible views, identity mismatches, stalled pagination and truncation still fail. The output remains explicitly incomplete and cannot authorize a write or `select_record`. Pagination max_pages accepts a template, allowing caller-controlled page windows.
