# Actors

Use Actors to save and repeatedly execute browser workflows. Use Web separately for one-off search, extraction, crawl, research and snapshots. Actors calls Computer directly; installing Web is unnecessary.

1. Create a definition with `actors_save`. Schema version 1 accepts either `steps` (the implicit `run` operation) or `operations` mapping names to `steps` and `output_schema`. Both share defaults, presets, browser options, allowed hosts and limits.
2. Use `actors_run` with `actor_id`, optional `revision`, `operation`, `preset` and `input`. It returns a queued run ID. Poll `actors_run_get`; tool submission success is not execution success.
3. Read committed rows with `actors_dataset_read(run_id, after?, limit?)`. Pass the returned `next_cursor` as `after`. Partial rows remain readable after failure. Successful runs also include private JSONL/CSV export references.
4. Save repeatable inputs with `actors_task_save`, which pins the actor revision. Run with `actors_task_run`. Editing the actor does not silently change a saved task.
5. Use `actors_schedule` for Jobs-owned schedules. Schedules pin a revision at creation and support operation, preset or preset-pool selection, input, and schedule overrides. `actors_schedules` lists them; `actors_unschedule` cancels them.

Supported actions: `goto`, `click`, `fill` (selector plus text), `key`, `scroll`, `wait`, `extract`, `paginate`, `assert_url`, `assert_element`, `screenshot`. Extraction uses rendered HTML and CSS selectors. The output schema is currently a flat field-to-type mapping: string, number, integer, boolean or url. Arbitrary JavaScript and nested workflow control flow are not supported yet.

Omit browser.backend to respect Computer's configured default. Explicit supported backend names are local, browserbase, steel, browser-engine and service, subject to that backend's browser capabilities. For a saved login, use a Computer-owned `browser.context_id` from Computer's context tools; Actors forces persistence for that context. It does not create or log into accounts. Do not put passwords or cookies in definition defaults, presets, or input: definitions and inputs are retained in run history. Templates such as `{{context_id}}` can reference a non-secret context ID.

Use assertions to detect expired login or unexpected pages. A failed login assertion fails this release's run; reconnect through Computer and explicitly retry. Do not imply that automatic authentication pause/resume exists. Account locking covers Actors runs within this installation, not unrelated Computer clients.

Runs have page/item/time limits and bounded exports (32 MiB per run). One worker executes runs within a single app process. Interrupted running work is marked failed on restart rather than automatically replaying browser actions. Click, key and pagination steps are not automatically retried. Full-run retries explicitly repeat the saved workflow; inspect any external side effects first.

All site-specific behavior belongs in user definitions. Examples are optional and are never installed on mount. There are no built-in Patreon workflows.
