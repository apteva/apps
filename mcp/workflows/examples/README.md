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

## Not yet shippable: event triggers

The trigger schema accepts `kind: event` and `kind: schedule` for
forward-compat, but workflows v0.2.0 only **dispatches** `http` and
`manual` triggers. An event-triggered workflow ("on `row.inserted` in
tables, run me with the row as input") parses and saves, but the
subscription machinery that would deliver events to it isn't built
yet — that's a v0.3.0 follow-on requiring matching sidecar-auth on
the platform's SSE endpoint. See definition.go line 31 for the
forward-compat comment.
