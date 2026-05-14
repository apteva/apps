# Function examples (node)

Working node handler modules you can deploy as-is. Each file is a
complete handler — `export default async (event, context) => result`.

node is the only runtime in v1.0 (see the app README for why); python
and Go are planned follow-ons.

## Deploying one

`functions_create` takes the file's contents as `source`:

```json
{ "name": "hello", "runtime": "node", "source": "<contents of hello.mjs>" }
```

…or paste it into the Functions panel's **New function** dialog. A new
revision goes out with `functions_deploy` (same args, existing
function).

## Simple — return JSON

| File | What it shows |
|---|---|
| `hello.mjs` | the canonical echo handler |
| `sum.mjs` | reading a shaped event payload |
| `fetch-json.mjs` | the built-in global `fetch` (Node 18+) — no dependencies |
| `context-info.mjs` | what `context` exposes; the scrubbed `context.env` |

## Interacting with other apps — `context.call`

These call the **Tables** app to read and write a database. The
function never touches another app's DB directly — `context.call`
goes through the sidecar, which holds the platform token. They assume
a `leads` table:

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

| File | What it shows |
|---|---|
| `tables-insert.mjs` | write a row — `context.call("tables", "rows_insert", …)` |
| `tables-search.mjs` | read rows back — `rows_search` with a filter |
| `lead-capture.mjs` | a realistic webhook: dedupe (`rows_count`) + insert + receipt |

The target app (`tables` here) must be installed in the project for
`context.call` to reach it; an unreachable app surfaces as a thrown
error the handler can catch.
