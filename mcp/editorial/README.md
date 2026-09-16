# Editorial

A standalone content calendar and content-operations app. Install it with **all optional integrations skipped** and immediately plan ideas, briefs, articles, videos, podcasts, newsletters, campaigns and refreshes. No CRM, Calendar, Jobs, Social, Campaigns, Storage, or external account is needed.

## Interface

The panel follows the same compact layout and shared components as CRM and Social. Calendar, Board, Content, Backlog and Settings views inherit the dashboard's Terminal or Clean theme in light and dark mode. Select a content item to edit its Brief, Details, Releases or History in a side inspector, with a responsive overlay on smaller screens and a prompt before discarding unsaved changes.

The monochrome app icon is embedded in the sidecar and served independently of its working directory.

## Home widget

**Editorial calendar** is a `dashboard.home` widget with Month, Week and List views. Operators add it from the Home widget gallery; `suggested` only ranks it there and never places it on its own. Half width defaults to the agenda and renders the grid as day numbers with per-entry dots, because one column of the dashboard's two-column layout cannot hold seven readable columns; full width defaults to the month grid. Clicking a compact day, or the "+n more" chip on a full cell, opens that day's entries underneath.

Per-instance settings cover default view, `planned_at` or `deadline` dates, one brand or all, whether channel releases appear, and the list horizon. The widget reads `GET /calendar` only; it never writes, and every entry links back into the panel. Releases with a saved URL link to that record instead.

The widget refreshes on the app bus topics under `publishes`, so a content or release write anywhere — panel, HTTP or MCP — updates an open dashboard without a reload.

## Brands

Create optional brands in **Settings → Brands**, with a name, color and optional HTTP(S) logo URL. Use the **All brands** selector to view one brand or **Unassigned** across Calendar, Board, Content and Backlog. New content inherits the selected brand; an item's brand is editable in Details. Campaign / initiative remains a separate field, and its filter choices follow the current brand.

Brands have stable project-scoped IDs: renaming a brand preserves assignments. Remove a brand only after reassigning every referenced item, including archived content. Changing the brand of approved content resets approval to pending. Brands organize content within the existing project access boundary; they do not create separate permissions.

Optional per-brand mappings accept Social account IDs and Campaigns record IDs from the project's existing app connections. These filter the release browser and guard result refreshes. An empty mapping leaves that app's records unrestricted. Social posts must have all targets in the mapped account list; posts spanning other accounts are omitted. The Social browser still uses the latest 200 posts. Manual links can be saved for planning while disconnected; refreshing a link outside the saved brand mapping fails without erasing previous results. Mappings do not install apps, connect accounts, schedule or publish anything.

Existing v0.1.x records need no data migration: they appear as Unassigned. Zero brands and zero connected apps remain valid defaults.

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

- `GET /items`: filters `q`, `brand_id` (omit for all brands; `unassigned` for records without a brand), `status`, `format`, `owner`, `campaign`, `approval`, `archived` (`false`, `true`, `all`); `limit` (default 100, max 500), `offset`. Returns total, items and their releases. The panel loads all pages before filtering/calendar rendering.
- `POST /items`: title required; other editable fields optional.
- `GET /items/:id`: item, releases and latest 100 history snapshots, with `history_truncated`.
- `PATCH /items/:id`: `{revision, patch}`. Content edits can reset approval.
- `POST /releases`: `{item_id, channel, ...}`.
- `PATCH /releases/:id`: `{revision, patch}`.
- `POST /releases/:id/refresh`: read the saved link's results.
- `GET /calendar`: the flat, pre-merged planning stream a calendar surface needs. Filters `from`, `to` (`YYYY-MM-DD` or RFC3339, defaulting to today and 30 days out, 400 days maximum), `date_field` (`planned_at` or `deadline`), `brand_id`, `include_releases`, `limit` (default 500, max 2000). Returns `events` sorted by date with `truncated`. Each event carries `kind` (`item` or `release`), `date` (the server-side bucket), `at` (the value as stored), the item's identity and, for releases, `release_id`, `channel` and `url`. Releases are matched on their own planned date and joined back to their parent, so a release inside the window appears even when its item's date sits outside it — which is why this is not a filter on `/items`. Archived content and archived releases are always excluded, and releases appear on `planned_at` views only, as in the panel.
- `GET /settings`, `PATCH /settings`: settings with revision, including optional `brands: [{id, name, color, logo_url, social_account_ids, campaign_ids}]`. IDs are stable strings; mappings are arrays of positive integers.
- `GET /integrations`: optional connection states. `?app=social|campaigns` browses existing records. Add `brand_id` to apply the saved brand mappings.

Dates accept `YYYY-MM-DD` or RFC3339 with timezone; the panel uses date pickers and displays calendar timestamps in the viewer's local timezone. You can also type a precise timestamp into the date field. Clear fields with empty strings/arrays/objects, not null. Item and release edits require the latest revision; stale writes return HTTP 409. Release `results` are snapshots or manually entered JSON, not normalized cross-platform metrics.

Project-scoped installs stay pinned to their project. Global HTTP calls use the gateway project header first, then the authenticated dashboard's `project_id` query. MCP tools require the SDK's current project and ignore argument attempts to override it. The sidecar belongs behind Apteva's authenticated gateway.

## Events

Writes emit on the project's app bus: `content.created`, `content.updated`, `content.archived`, `content.restored`, `release.created`, `release.updated`, `release.refreshed` and `settings.updated`. Emission is best-effort and never fails a committed write; a dropped event leaves a widget stale until its next render. Archiving gets its own topic because it is what removes an item from every calendar.

## Development

```sh
GOWORK=off GOTOOLCHAIN=local go test -race ./...
GOWORK=off GOTOOLCHAIN=local go build -o /tmp/apteva-editorial .
# Run from this directory so migrations and UI assets resolve.
APTEVA_BIND_HOST=127.0.0.1 APTEVA_APP_PORT=8098 APTEVA_PROJECT_ID=demo DB_PATH=/tmp/editorial-demo.db /tmp/apteva-editorial
# From apps/:
bun run scripts/build-panels.ts --app editorial
bun run test:editorial-ui
```

Go 1.25.1+, app-sdk v0.81.0 (latest tag by ancestry when implemented). The repository-wide workspace may request a different Go toolchain; `GOWORK=off` validates the standalone installation against its actual published dependencies.
