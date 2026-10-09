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

`examples/patreon-gallery-post.json` is an optional two-original gallery example. It is never seeded into an install. Contexts, sources, copy and access settings are runtime inputs.

## Crawl a site

For a concrete test fixture, `examples/ufcstats.json` shows how a site-specific definition can model events, fights and fighters. It is optional example data only. Save a definition's `definition` with `actors_crawl_save`, then pass the returned actor ID to `actors_crawl_run`. The run creates a durable URL frontier, follows configured URL fields into their routes, upserts keyed records into named datasets, and can be inspected with `actors_crawl_status`, `actors_frontier_list`, and `actors_dataset_query`. `actors_crawl_resume` continues pending work after a budget limit or restart.

`actors_task_save` accepts `name`, `actor_id`, `operation`, `input`, optional `preset` and optional `revision`. It pins the current revision when omitted. Later actor edits do not change that task. Use `actors_task_list`, `actors_task_run` and `actors_task_delete` to manage it.

## HTTP routes

Routes are relative to the server's authenticated app mount, normally `/api/apps/actors`. Global installs use the gateway-authorized `project_id` query context. The app relies on the Apteva gateway for caller authentication; it is not a public unauthenticated service.

| Method | Path | Behavior |
|---|---|---|
| GET / POST | `/actors` | List / save definitions |
| DELETE | `/actors/{id}` | Delete an actor after removing tasks and schedules |
| POST | `/actors/run` | Queue a run; same input as `actors_run` |
| GET | `/runs`, `/runs/{id}` | History and run detail |
| POST | `/runs/{id}/cancel`, `/runs/{id}/retry` | Cancel / explicitly repeat the snapshot |
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

## Development

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go build -o /tmp/apteva-actors .
cd ../..
bun run scripts/build-panels.ts --app actors
bun test mcp/actors/ui/ActorsPanel.test.ts
```

The app-sdk pin is `v0.97.0`, derived from fetched tag ancestry (including v0.96.0 and v0.82.0). `GOWORK=off` verifies the app against its published dependency rather than the workspace overlay.

The release manifest pins source to the matching immutable Actors tag. The registry references the same tag for the manifest and icon. Publishing a release makes Actors available for installation; it does not install the app into existing projects.

## Web compatibility

Web's search, extraction, crawl, map, research and snapshot tools remain independent. Its existing extractor endpoints and data have not been deleted or silently rerouted. New reusable workflows should use Actors. A later explicit migration can import definitions and replace old endpoints with compatibility forwarding; it must account for app-local IDs, saved Jobs targets and historical run ownership.

## Form and consequential actions

Semantic `set_checked`, `select_option`, and `set_temporal` support access toggles, dropdowns and schedule fields. Use `readability: false` on extract/assert steps when controls live outside the primary content. An extracted field may supply `pattern` to capture a labelled value before type conversion.

For sends, payments or deletions, add `once_key: "{{request_id}}"` to the acknowledged consequential click. Reservations persist across run retries, restarts and actor revisions. The same actor operation cannot attempt that key again, even if its prior outcome is uncertain. Inspect the referenced original run before deliberately issuing a new key. This guards duplicate attempts; it does not provide exactly-once execution on an external site.

## Exact selections from runtime lists

`set_checked` accepts `labels: "{{tier_labels}}"` with an exact `som_only` locator and optional role. The input must be a unique array of single-line names, at most 50 entries. Each target is resolved using a fresh SOM observation; missing or ambiguous targets stop the operation. An empty list performs no selections. Clear existing choices in an earlier step when replacing a selection.

`assert_values` supports `equals_set: "{{tier_labels}}"` to compare the exact set of selected labels, ignoring order but rejecting duplicates, omissions and extra entries. An aggregated text field should use `all: true, separator: "\n"`; absent optional fields represent an empty set. No creator names, prices or tier policy live in the engine.
