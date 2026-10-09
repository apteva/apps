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

## Ads mobile conversions

The Ads app extends the existing campaign, creative, delivery, and performance
APIs with project-scoped `mobile_app`, `measurement_source`, and conversion
resources. Website campaigns keep their existing calls and behavior.

Use `conversion_capabilities_get` for the selected account, then
`mobile_app_bind`, `measurement_source_bind`, and `conversion_event_list`.
Pass `mobile_app_resource_id`, `measurement_source_resource_id`, `app_goal`,
and an optional `conversion_event_resource_id` to `campaign_create`. Post-install
and value goals require an eligible event. Create campaigns, groups, and ads
paused; `delivery_activate` refreshes readiness before activation. Unknown
measurement requires explicit acknowledgement; ineligible events cannot be
acknowledged away. Source binding records configuration and does not install
SDKs or configure a measurement partner.

Google uses App campaigns and App ads. Meta uses application promoted objects.
X supports install and re-engagement campaigns with app cards and catalog-based
OS/location targeting. Reddit retains its required conversion pixel and checks
recent app-event receipt. Capabilities expose the differences between providers.

`conversion_performance_get` reports each event/source/attribution separately,
including CPI, available value and ROAS. Costs come from the delivery cache;
event rows cannot multiply spend. First opens stay distinct from installs.
Background collection updates mobile event caches, and failed refreshes preserve
previous data with separate sync diagnostics. Optional creation
`idempotency_key` values survive restarts and prevent duplicate dispatch after
an ambiguous provider response.

Deploy integrations v0.47.1 or newer with this app: Meta advertiser-app discovery, Reddit app discovery/last-event timestamps,
and X line-item app identifiers are required. The new Conversions panel uses the
same MCP APIs. Provider payload, cache, ownership, retry, and UI tests are local
contracts; validate real account permissions and SDK/MMP event receipt before
activating delivery. Google mobile conversion import follows the
[provider's conversion action contract](https://developers.google.com/google-ads/api/docs/conversions/categories).

## Releasing

Per-app version tags in the `name/vX.Y.Z` form (e.g. `crm/v0.2.0`)
let each app cut its own release without coordinating with siblings.
The `apteva/app-registry` registry points at
`raw.githubusercontent.com/apteva/apps/main/<bucket>/<name>/apteva.yaml`,
so updates land for users on the next refresh.
