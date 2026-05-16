# Workflow examples

Working workflow definitions you can deploy as-is. Each file is a
complete workflow — `workflows_create { source: "<file contents>" }`
or paste it into the dashboard's New-workflow dialog.

All three exercise the cross-app call path (`step.kind: app` →
`context.call("tables", ...)`), which is what workflows v0.2.0's
`requires.dynamic_app_calls` unlocks — the same gate that blocks
functions today.

## Deploying one

```json
// workflows_create
{
  "name": "tables-insert",
  "source": "<contents of tables-insert.yaml>"
}
```

…then trigger it:

```bash
curl -X POST 'https://<host>/api/apps/workflows/wf/tables-insert' \
  -H 'Content-Type: application/json' \
  -d '{"email":"marco@example.com"}'
```

## What each example shows

| File | What it shows |
|---|---|
| `tables-insert.yaml` | minimal `step.kind: app` — one row into a Tables table |
| `tables-search.yaml` | `rows_search` with a templated `where` filter, returns the rows |
| `lead-capture.yaml` | dedupe-then-insert: `rows_count` → `branch` → `rows_insert` (also uses `{{ now }}`) |
| `on-row-inserted.yaml` | **event-triggered** — fires on every `tables.row.inserted` event in the project; input carries the event payload |

They all assume a `leads` table; create it once with the Tables app:

```json
// tables_create
{
  "name": "leads",
  "columns": [
    { "name": "email", "type": "text" },
    { "name": "source", "type": "text", "nullable": true },
    { "name": "captured_at", "type": "text", "nullable": true }
  ]
}
```

## How event triggers work (v0.3.0+)

`kind: event` is wired up via an in-sidecar SSE-client manager
(`event_trigger.go`). At boot and after every workflow CRUD it
groups active event-triggered workflows by `(source_app, project)`
and opens one SSE connection per lane to `/api/app-events/<source>`
on the platform — authenticated with the sidecar's own
`APTEVA_APP_TOKEN`. Each incoming event is dispatched to every
matching workflow on the lane (exact topic match, or `prefix.*`).

Reconnect-with-`since` handles transient drops; the bus's 256-event
ring buffer covers brief outages. A sidecar restart re-subscribes
fresh; events that age out of the ring during a longer downtime are
lost.

`kind: schedule` is still a forward-compat stub — pair workflows
with the Jobs app's cron + an HTTP target for now.
