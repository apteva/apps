# Actors

Actors v0.1.0 is a standalone Apteva app for reusable browser workflows. It depends directly on **Computer**, **Storage** and **Jobs**. It does not call, import or require Web.

## Implemented

- Revisioned actor definitions with immutable revision history and per-run snapshots.
- Named operations sharing browser options, defaults, presets and limits; single-operation `steps` definitions also work.
- Computer browser sessions, saved-context selection, proxy/environment forwarding and saved-context exclusion between Actors runs.
- Navigate, click, fill, key, scroll, wait, extract, paginate, assert URL/element and screenshot steps.
- Queue, history, progress, cancellation, explicit snapshot retry and deadline-aware Computer calls.
- Saved tasks pinned to actor revision/operation/input.
- Page-by-page dataset persistence and cursor reads, including partial output after failure; private JSONL/CSV exports on success.
- Jobs schedules pinned to actor revisions, deterministic preset rotation and occurrence deduplication.
- Project-scoped MCP tools, HTTP routes and an Actors panel.

This is the working foundation for the platform described in `PLATFORM_PROPOSAL.md`, not completion of the entire roadmap. Durable distributed queues, checkpoint resume, full JSON Schema contracts, isolated code actors, public API publication, webhooks and a shared catalog remain future work.

## Create and run an actor

`examples/page-reader.json` is a complete `actors_save` input. It defines a `read` operation for example.com. Examples are files only; the app does not seed any definitions.

After saving it:

```json
{"actor_id": 1, "operation": "read", "input": {"url": "https://example.com"}}
```

Pass this to `actors_run`. Optional `idempotency_key` deduplicates retries with identical revision and inputs; reusing it for different work returns a conflict. Poll `actors_run_get` with `{"id": <run_id>}`. Read output with `actors_dataset_read` using `{"run_id": <run_id>, "limit": 50}` and pass `next_cursor` as `after` for subsequent pages. While a run is active, poll again at the last cursor for new committed rows.

For a saved login, add `browser.context_id` using a context created through Computer. Omit `backend` to defer to Computer; the saved context can determine its backend. The context remains owned by Computer, and Actors requests `persist: true`. Do not store credentials in inputs: input/definition snapshots are intentionally retained.

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
bun build ui/ActorsPanel.mjs --target browser --external react --external react-dom/client --outfile /tmp/ActorsPanel.mjs
```

The app-sdk pin was derived from local SDK HEAD and fetched tags: `950b91d` / `v0.82.0`. `GOWORK=off` verifies the app against its published dependency rather than the workspace overlay.

The release manifest pins source to `actors/v0.1.0`. The registry references the same immutable tag for the manifest and icon. Publishing a release makes Actors available for installation; it does not install the app into existing projects.

## Web compatibility

Web's search, extraction, crawl, map, research and snapshot tools remain independent. Its existing extractor endpoints and data have not been deleted or silently rerouted. New reusable workflows should use Actors. A later explicit migration can import definitions and replace old endpoints with compatibility forwarding; it must account for app-local IDs, saved Jobs targets and historical run ownership.
