# Editorial

A standalone content calendar and content-operations app. Install it with **all optional integrations skipped** and immediately plan ideas, briefs, articles, videos, podcasts, newsletters, campaigns and refreshes. No CRM, Calendar, Jobs, Social, Campaigns, Storage, or external account is needed.

## Interface

The panel follows the same compact layout and shared components as CRM and Social. Calendar, Board, Content, Backlog and Settings views inherit the dashboard's Terminal or Clean theme in light and dark mode. Select a content item to edit its Brief, Details, Releases or History in a side inspector, with a responsive overlay on smaller screens and a prompt before discarding unsaved changes.

The monochrome app icon is embedded in the sidecar and served independently of its working directory.

## Planning

- Calendar (publication dates and channel releases, or editorial deadlines), board, table and unscheduled backlog.
- Search and filters for workflow, format, owner, approval, campaign and archive state.
- Content records with brief/draft, owner, reviewer, deadline, planned publication, approval, sources, attachment URLs, tags and custom fields.
- Configurable workflow statuses, formats and channel suggestions per project. The first status and format are defaults. Existing values cannot be removed while referenced by any item, including archived items.
- Multiple releases per content item, each with channel, planned/actual publication, status, URL, notes and results. A standalone release accepts manual result metrics.
- Archive/restore, atomic history snapshots and optimistic revisions. Editing approved title/body/format/sources/attachments/custom fields resets approval to pending, requiring a separate review save.

Owners and reviewers are free-text names, not assignments requiring another app. Attachment fields accept HTTP(S) links; file hosting remains external. Custom fields accept arbitrary JSON values through MCP/API; the panel offers named value rows. Approval is recorded workflow state, not a separate role-based permission boundary. Project access is controlled by Apteva.

## Optional Social and Campaigns integration

Both are `requires.integrations` entries with `required: false`; there are no `requires.apps` entries and no startup calls that install dependencies.

You can link an existing Social post or Campaigns campaign by ID, browse records after binding the corresponding app, and explicitly refresh its results. App-to-app reads use `CallAppResult` and forward the current project. Disconnecting an integration does not affect planning or erase previous results.

Editorial never invokes publishing, sending, scheduling, campaign creation, or configuration tools. In particular, Social's `post_create` publishes/schedules immediately and has no draft-only contract. Create and schedule delivery in the connected app, then link it here. Planning dates, approval and planning status are separate from the remote result snapshot. Changing one does not reschedule or approve the other app's record.

Social currently exposes only the latest 200 posts through `post_list`. A linked older/missing post produces a clear refresh error and preserves the prior snapshot. A direct per-post read API is needed for unbounded historical refresh. Campaigns uses `campaigns_get`, including live statistics. Refreshes are explicit; this version has no background polling, notifications or publishing automation.

## API / agents

All tools use the `editorial_` prefix. See `apteva.yaml` and the tool schemas in `main.go` for the full surface.

- `GET /items`: filters `q`, `status`, `format`, `owner`, `campaign`, `approval`, `archived` (`false`, `true`, `all`); `limit` (default 100, max 500), `offset`. Returns total, items and their releases. The panel loads all pages before filtering/calendar rendering.
- `POST /items`: title required; other editable fields optional.
- `GET /items/:id`: item, releases and latest 100 history snapshots, with `history_truncated`.
- `PATCH /items/:id`: `{revision, patch}`. Content edits can reset approval.
- `POST /releases`: `{item_id, channel, ...}`.
- `PATCH /releases/:id`: `{revision, patch}`.
- `POST /releases/:id/refresh`: read the saved link's results.
- `GET /settings`, `PATCH /settings`: settings with revision.
- `GET /integrations`: optional connection states. `?app=social|campaigns` browses existing records.

Dates accept `YYYY-MM-DD` or RFC3339 with timezone; the panel uses date pickers and displays calendar timestamps in the viewer's local timezone. You can also type a precise timestamp into the date field. Clear fields with empty strings/arrays/objects, not null. Item and release edits require the latest revision; stale writes return HTTP 409. Release `results` are snapshots or manually entered JSON, not normalized cross-platform metrics.

Project-scoped installs stay pinned to their project. Global HTTP calls use the gateway project header first, then the authenticated dashboard's `project_id` query. MCP tools require the SDK's current project and ignore argument attempts to override it. The sidecar belongs behind Apteva's authenticated gateway.

## Development

```sh
GOWORK=off GOTOOLCHAIN=local go test -race ./...
GOWORK=off GOTOOLCHAIN=local go build -o /tmp/apteva-editorial .
# Run from this directory so migrations and UI assets resolve.
APTEVA_BIND_HOST=127.0.0.1 APTEVA_APP_PORT=8098 APTEVA_PROJECT_ID=demo DB_PATH=/tmp/editorial-demo.db /tmp/apteva-editorial
# From apps/:
bun run scripts/build-panels.ts --app editorial
```

Go 1.25.1+, app-sdk v0.81.0 (latest tag by ancestry when implemented). The repository-wide workspace may request a different Go toolchain; `GOWORK=off` validates the standalone installation against its actual published dependencies.
