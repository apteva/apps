# Apteva first-party apps

Monorepo for apps Apteva ships and maintains. Each subdirectory is a
standalone Apteva app with its own `apteva.yaml`, `go.mod` (when
applicable), and release line.

## Layout

```
apps/
├── mcp/        ← apps whose primary surface is MCP tools (sidecar services)
├── ui/        ← apps whose primary surface is a UI (kind: static, no sidecar)
├── channels/  ← apps that contribute Channel adapters (Slack, WhatsApp, …)
└── shared/    ← cross-cutting CI templates, lint configs, icon set
```

## Placement rule

> An app's bucket is decided by its **primary surface** — the *reason
> for installing it*. UI surfaces that exist only to display data live
> with their data app, not in `ui/`. `ui/` is reserved for apps where
> the UI is the entire product (kiosk views, white-label portals).

Examples:
- `mcp/crm` — primary value is the contacts data + tools the agent calls.
  The dashboard panel is a viewer, not the product.
- `mcp/tasks` — same: data + tools first, panel second.
- `ui/simple` — *(stays in its standalone repo for now;* see
  `github.com/apteva/simple`*)* — pure read-only kiosk, no data.
- `channels/slack` — adapter that bridges the agent ↔ Slack.

## Working on an app

```bash
cd mcp/crm
go build .
APTEVA_PROJECT_ID=test ./crm           # runs the sidecar locally
```

For dashboard panels (HTML + JS), the sidecar serves `ui/*` automatically
via the `app-sdk` framework — no separate build step unless the app uses a
bundler.

## Releasing

Per-app version tags in the `name/vX.Y.Z` form (e.g. `crm/v0.2.0`)
let each app cut its own release without coordinating with siblings.
The `apteva/app-registry` registry points at
`raw.githubusercontent.com/apteva/apps/main/<bucket>/<name>/apteva.yaml`,
so updates land for users on the next refresh.
