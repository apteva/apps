# Function examples

Working handler modules you can deploy as-is — node (`.mjs`) and Go
(`.go.txt`). The Go examples use a `.go.txt` extension so they stay
out of the functions app's own `go build`; the content is real Go.

## Deploying one

`functions_create` takes the file's contents as `source`:

```json
{ "name": "hello", "runtime": "node", "source": "<contents of hello.mjs>" }
{ "name": "hello", "runtime": "go",   "source": "<contents of hello.go.txt>" }
```

…or paste it into the Functions panel's **New function** dialog and
pick the runtime. A new revision goes out with `functions_deploy`.

## node — `export default async (event, context) => result`

| File | What it shows |
|---|---|
| `hello.mjs` | the canonical echo handler |
| `sum.mjs` | reading a shaped event payload |
| `fetch-json.mjs` | the built-in global `fetch` (Node 18+) — no dependencies |
| `context-info.mjs` | what `context` exposes; the scrubbed `context.env` |
| `tables-insert.mjs` | write a row — `context.call("tables", "rows_insert", …)` |
| `tables-search.mjs` | read rows back — `rows_search` with a filter |
| `lead-capture.mjs` | a realistic webhook: dedupe + insert + receipt |
| `pushover-notify.mjs` | ping Pushover — `context.integration("pushover", …)` (v1.4.0) |
| `slack-post.mjs` | post to Slack — same shape, returns the message ts (v1.4.0) |

## go — `func Handle(event json.RawMessage, ctx *Context) (any, error)`

A Go function is `package main` with a `Handle` func and **no
`main()`** — the harness supplies `main()` and the `Context` type,
and `go build` compiles them together at deploy. stdlib only for now
(third-party Go modules are a planned follow-on).

| File | What it shows |
|---|---|
| `hello.go.txt` | the canonical echo handler |
| `sum.go.txt` | decoding a shaped event payload into a struct |
| `tables-insert.go.txt` | write a row via `ctx.Call`, decode the result |
| `pushover-notify.go.txt` | ping Pushover via `ctx.Integration("pushover", …)` (v1.4.0) |

## The Tables examples

The cross-app examples call the **Tables** app — the function never
touches another app's DB directly; `context.call` / `ctx.Call` goes
through the sidecar. They assume a `leads` table:

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

The target app must be installed in the project for the call to
reach it; an unreachable app surfaces as an error the handler can
catch.

## The integration examples (v1.4.0)

`context.integration(conn, tool, input)` reaches an integration
connection (Pushover, Slack, Resend, anything in the integrations
catalog) directly — same trust model as the cross-app bypass, no
operator pre-binding. `conn` is either:

- a **numeric connection id** (`31`) — what the Connections panel shows
- an **app slug** (`"pushover"`) — auto-resolved to the single matching
  connection in this project. The sidecar caches the lookup for 60s.

Multi-match (>1 connection of the same slug) errors with the list of
candidate ids; pass the id you want to disambiguate.

The handler receives the upstream tool's response data (the `data`
field of `{success, data, status}` — the success/status framing is
unwrapped automatically; non-success becomes a thrown error).

```js
// What pushover-notify.mjs is doing under the hood:
await context.integration("pushover", "pushover_send_notification", {
  message: "deploy finished",
  priority: 1,
});
```

Discover available tools per connection from the Connections panel
or via `GET /api/connections/<id>/tools` — every entry there is
callable by the name shown.
