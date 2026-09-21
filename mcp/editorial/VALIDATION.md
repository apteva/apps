# Validation

## v0.4.3 native calendar widget

The existing `editorial-calendar` Home contribution now advertises an `apteva-native-surface/v1` renderer. Its native presentation is a project-scoped agenda, not a month grid, and binds the shared date, brand, release and horizon settings to a dedicated `GET /mobile/calendar-summary` source. The endpoint returns stable native row IDs, normalized timestamps, display-ready detail and brand text, and a non-nil `events` array. It derives project scope from the pinned app context or trusted gateway header and ignores caller-controlled `project_id` query parameters.

Regression coverage parses and validates the strict JSON surface, matches it to the manifest descriptor, checks every settings binding and the empty state, and exercises project/brand/date/release/horizon/result filters, cross-project isolation, empty arrays, timestamp normalization, truncation and invalid booleans. The app-sdk pin advances from v0.82.0 to v0.85.0, verified as the newest published tag by ancestry on 2026-09-21.

- `GOWORK=off GOTOOLCHAIN=local go test -race -count=1 ./...`, `go vet ./...`, and a standalone build passed.
- All 14 browser-widget tests and the host React import-surface verifier passed unchanged.

## v0.4.2 agent guidance

The live MCP schemas now distinguish project-configured content workflow statuses from fixed release statuses, publish the fixed approval and release-status enums, explain create defaults, and state that approved items require a reviewer. Validation errors include the project's allowed values and default, with a specific release-status hint when a release value such as `planned` is mistakenly supplied as an item status. The settings response and stored data are unchanged, and approval invalidation remains intact.

Focused regression tests cover a one-call approved item with a reviewer, rejection without a reviewer, customized first-status and first-format defaults, the actionable `planned` diagnostic, the default release status, schema descriptions, and both fixed enums.

Validated on 2026-09-20 with the standalone race suite, `go vet`, and a standalone binary build.

Validated locally on 2026-09-14 against the published app-sdk v0.81.0 with no workspace overlay.

- `GOWORK=off GOTOOLCHAIN=local go test -race ./...` — passed.
- `GOWORK=off GOTOOLCHAIN=local go vet ./...` — passed.
- Standalone Go build and sidecar startup — passed; `/health` reports ready with an isolated SQLite database and no connected apps.
- Strict TypeScript checking of `ui/EditorialPanel.tsx` — passed.
- Panel bundle — built successfully; Editorial passes the shared React import-surface verification.
- Browser smoke check using the real standalone sidecar: create a content item, edit its brief/owner, select and save a publication date with the inline picker, create a standalone newsletter release on a different date, and verify both calendar entries and the board card.

Backend tests cover dependency-free installation and operation, HTTP and MCP project isolation, approval invalidation, clearing structured fields, stale writes, concurrent edits, validation, pagination, settings guards, archive/restore, and optional Social/Campaigns result refreshes with project propagation and preservation of missing results.

The v0.1.1 panel build and host React import-surface verifier pass in the isolated release worktree. Browser checks use the actual dashboard stylesheet and shared UI kit in Terminal and Clean themes, in both light and dark modes. Verified the shared icon, calendar, board, item editor, inline date picker, standalone release creation, optional disconnected integrations, unsaved-change keep/discard flow, and a 390px mobile inspector. No browser page errors were reported.

The embedded icon route returns SVG successfully when the sidecar runs outside its source directory with no UI directory. A regression test covers that deployment condition.

Delivery integrations were tested with SDK platform stubs. No real accounts were called, no content was published, and no production installation or deployment was performed. The release manifest and registry entry pin source and assets to editorial/v0.2.0.


## v0.2.0 brands

- Go race tests, Go vet, standalone build, strict TypeScript and targeted panel build/host React verification passed.
- Regression tests cover legacy records without a brand field, brand creation and rename, stable assignment, stale settings revisions, archived-reference deletion guards, reassignment approval invalidation, invalid metadata, HTTP/MCP filtering and project isolation.
- Platform stubs verify brand-specific Social and Campaigns record browsing, cross-brand refresh rejection with previous snapshot preservation, mixed-account Social post exclusion and read-only calls with project propagation.
- Browser checks against an isolated sidecar and the real dashboard CSS verify creating and renaming brands in Settings, the deletion guard, inherited brand on new content, Calendar/Board/Content/Backlog filtering, Unassigned, release visibility, 390px layout and Terminal/Clean in both modes. No browser errors.
- No SQL schema migration is required. The release does not assign brands to existing user content. Optional mappings use existing project connections and are not permission boundaries.


## v0.3.0 home calendar widget

Validated on 2026-09-16 in an isolated worktree branched from `main` at editorial/v0.2.0.

- `GOWORK=off GOTOOLCHAIN=local go test -race ./...` — passed, including the v0.1.x and v0.2.0 suites unchanged.
- `GOWORK=off GOTOOLCHAIN=local go vet ./...` and `gofmt` — passed.
- `bun run test:editorial-ui` — 9 widget tests passed (view resolution by size, Monday-based month grid, window padding, local-timezone bucketing, query construction from settings, month render, error state, truncation notice).
- Strict TypeScript checking of `ui/EditorialCalendarWidget.tsx` — passed.
- `bun run scripts/build-panels.ts --app editorial` — both bundles built and passed the host React import-surface verification. The panel bundle rebuilt byte-identically, so this release does not perturb the v0.2.0 panel.

Backend tests cover the calendar window (items in and out of range, undated items, RFC3339 prefixes), releases whose parent item falls outside the window or has no date at all, deadline views excluding releases, `include_releases=false`, brand and `unassigned` filtering, archived items and archived releases, project isolation, default window and `date_field`, rejected windows (unparseable dates, reversed range, over 400 days, unknown `date_field`), limit truncation, and the HTTP route including a query-string boolean. A manifest test asserts every `refresh_topics` entry is a topic the manifest also publishes, so a widget cannot wait on an event that is never emitted.

No schema migration. No new permissions: the widget reads through the existing `db.write.app` sidecar. The optional Social and Campaigns integrations are untouched, and no publishing, scheduling or external call was added — `GET /calendar` is read-only and `release.refreshed` still fires only from the existing explicit refresh.

Browser render of the built `EditorialCalendarWidget.mjs` against stubbed data, in a standalone host supplying the dashboard's colour variables, at full and half width: verified the month grid, the stretched week grid, the compact dot grid, agenda grouping and ordering, the picked-day agenda under a compact cell, the today marker, and the colour separation between content and release entries. This caught two defects that the DOM tests could not see, both fixed here: anchors inheriting the browser's default link colour, and the week grid leaving the body empty below its single row.

Not yet done for this release: browser checks against the real dashboard stylesheet and shared UI kit in Terminal and Clean themes across light and dark modes, the 390px layout pass, and placing the widget on a live Home surface through the widget gallery at both sizes. The v0.2.0 panel checks still stand, since the panel and its bundle are unchanged — the panel bundle rebuilds byte-identically from this branch.


## v0.3.1 brands in the home widget

Validated on 2026-09-16 on a worktree branched from `main` at editorial/v0.3.0. UI only — no Go, schema or endpoint changes; `GET /calendar` already accepted `brand_id` including `unassigned`, and brands come from the existing `GET /settings`.

- `GOWORK=off GOTOOLCHAIN=local go test -race ./...` — passed unchanged.
- `bun run test:editorial-ui` — 14 widget tests passed, 5 of them new: brand lookups against unknown ids and colourless brands, the picker's option set and all-brands default, refetch scoped to a chosen brand including Unassigned and back to all, brand colour applied only where a brand supplies one, and a no-brands project getting no picker.
- Strict TypeScript and the panel build with host React import-surface verification — passed. The panel bundle again rebuilt byte-identically.
- Browser render against stubbed data at both widths: the picker appears in all three views, brand colours resolve exactly (Acme `#e0533f` → `rgb(224,83,63)`, Globex `#3f8ee0` → `rgb(63,142,224)`, Initech `#5ec27a` → `rgb(94,194,122)`), unbranded entries keep the accent, and three widget instances on one page held independent selections, confirming the per-instance storage key.

Brand filtering is applied by the sidecar, not the browser: the widget sends `brand_id` and the endpoint's own tests cover the scoping, including archived exclusion and project isolation. A viewer's selection is a view preference, not a permission boundary — brands organize content inside the existing project access boundary, as in v0.2.0.

Not yet done: browser checks against the real dashboard stylesheet in Terminal and Clean themes across light and dark modes, the 390px layout pass, and placing the widget on a live Home surface. Note that at half width the toolbar wraps to two lines once a brand picker is present.


## v0.3.2 search reach and strict query parameters

Validated on 2026-09-17 on a worktree branched from `main` at editorial/v0.3.1. Backend only; no UI, schema or migration changes.

Two defects were reported and both were confirmed in the shipped code before being fixed:

- `q` matched `$.title` and `$.body` only, so tags and custom fields — item data the panel shows and the API accepts — were invisible to search. It now also matches tag values and custom field values through `json_each`, which walks values and therefore never matches a custom field's key.
- Unknown query parameters were ignored in silence, so `GET /items?tags=x` returned every item: a deliberately narrow query answered with a full unfiltered page. They are now rejected with HTTP 400 naming the accepted parameters. `POST` and `PATCH` take arguments from the body, so any argument in their query is refused as well, and an `id` supplied by the path is refused in the query where it would have been ignored.

The accepted set is derived from the same `toolSpecs` the MCP tools publish, so the two front doors cannot drift: a test asserts every tool still sets `additionalProperties: false` and that `items_list` retains its `archived` enum after the refactor. **Superseded by v0.3.3:** this release enforced the rule in the HTTP handler only, on the mistaken assumption that MCP already rejected unknown arguments through `additionalProperties`. It does not — see below.

- `GOWORK=off GOTOOLCHAIN=local go test -race -count=1 ./...`, `go vet ./...`, `gofmt` — passed, including every v0.1.x–v0.3.1 suite unchanged.
- New tests cover tag and custom field matches, case-insensitivity, a field key deliberately not matching, rejection of unknown parameters on reads, writes and path-supplied ids, and that documented filters plus `project_id`/`install_id`/`api_key` still pass.
- A row inserted without `tags` or `fields` — the v0.1.x shape — stays searchable by title and body: `json_each` yields no rows for a missing path instead of failing the query. Covered by a regression test.
- End-to-end against a standalone sidecar on an isolated database: `q=case-study` now finds a tag-only match, `q=Research` finds a custom field value, `q=desk` correctly finds nothing, `?tags=` and `?fields=` return 400 with the accepted list, and `status`/`archived`/`limit` are unaffected.

Compatibility: every request the panel and the calendar widget make was audited against the new rule — `/items?archived&limit&offset`, `/items/:id`, `/releases/:id/refresh`, `/settings`, `/integrations?app&brand_id`, `/calendar?from&to&date_field&include_releases&brand_id` — and all use only schema or infrastructure parameters. The gateway forwards a caller's query untouched apart from setting `project_id`, so nothing else is injected in front of the sidecar. A third-party caller relying on a silently-ignored parameter will now get a 400; that is the point of the change, and it is a behaviour change worth noting for anyone scripting against the HTTP API.

Not done: a structured per-field filter (`fields.<key>=<value>`), which the report also asked about. Search now reaches custom field values, but narrowing by one named field needs a deliberate design for typing and indexing rather than a `LIKE` over the JSON blob.


## v0.3.3 the same rule on the MCP door

Validated on 2026-09-17 on a worktree branched from `main` at editorial/v0.3.2.

v0.3.2 rejected unknown arguments in the HTTP handler and claimed MCP already did the same through `additionalProperties: false`. That claim was wrong. The SDK publishes each tool's `inputSchema` in `tools/list` but `tools/call` reads `req.Params["arguments"]` and hands it to the handler verbatim — there is no schema validation anywhere in the sidecar or the SDK. `additionalProperties` tells a client what is accepted; it does not stop a call. Verified against a running sidecar before the fix: `editorial_items_list` with `{"tags":"case-study"}` returned every item, exactly the silent failure v0.3.2 set out to remove.

The check now lives in `dispatch`, the one place both doors pass through, so an unknown argument fails identically whether it arrives as an MCP argument or an HTTP query parameter. Transport parameters (`project_id`, `install_id`, `api_key`) are stripped before validation, because the panel and widget send them on every request and they are not arguments. The HTTP layer keeps its own check for the two cases `dispatch` cannot see: an argument in a `POST`/`PATCH` query, which is never read, and an `id` in the query when the path already supplied one.

- `GOWORK=off GOTOOLCHAIN=local go test -race -count=1 ./...`, `go vet ./...`, `gofmt` — passed, every earlier suite unchanged.
- A new test drives `dispatch` directly — the MCP path — for unknown arguments on reads and writes, for an operation that takes no arguments at all, and for transport parameters not being mistaken for arguments.
- End-to-end against a standalone sidecar over real JSON-RPC: `editorial_items_list` with `{"tags":...}` and `{"nonsense":...}` now fail with the accepted list, `{"q":"case-study"}` still returns the tag-only match, and `{"status":"idea"}` is unaffected. The HTTP door still returns 400 and 1 result for the same two cases.

Behaviour change: an MCP caller — including an agent — that passed an argument no tool declares now gets a tool error instead of a silently unfiltered result. That is the intent, and it is the same break v0.3.2 made for HTTP callers.


## v0.4.1 release routing

Validated on 2026-09-19. `release.due` now includes the parent content's `format`, so Processes can filter by channel, content type, brand and approval directly. Publishing remains in Processes. The SDK pin advances to v0.82.0, verified as a descendant of v0.81.0.

- The existing due-event regression test now verifies a custom `preview` format and approved parent alongside channel, brand and content identity.
- `GOWORK=off GOTOOLCHAIN=local go test -race -count=1 ./...` and `GOWORK=off GOTOOLCHAIN=local go vet ./...` passed.

## v0.4.0 due events

Validated on 2026-09-17 on a worktree branched from `main` at editorial/v0.3.3.

A content calendar whose dates did nothing was a drawing of a schedule rather than a schedule: every topic Editorial emitted was an echo of a write someone had just made, and `Workers()` returned nil. This release adds a `due_scanner` worker that emits `content.due`, `content.deadline` and `release.due` when a planning date arrives. Editorial still publishes nothing itself.

Every due payload carries `brand_id` and the human `brand` name, both empty strings for unassigned content so subscribers can route on brand without a settings lookup and without a varying shape.

- `GOWORK=off GOTOOLCHAIN=local go test -race -count=1 ./...`, `go vet ./...`, `gofmt` — passed, every earlier suite unchanged.
- The due tests drive an injected clock rather than sleeping, and cover: no replay of dates older than the first scan; exactly one event across repeated scans; a rescheduled date re-arming and firing again; brand, identity and `due_at` on item, deadline and release payloads; empty brand fields for unassigned content; `Australia/Sydney` at `08:00` firing at 21:00 UTC the previous day; an RFC3339 value ignoring both settings; archived items staying quiet; project isolation; the SDK's empty-project tick being a silent no-op; rejection of a bad timezone and three malformed due times; and the manifest declaring the worker and every topic it emits.
- Migration 002 applied to an existing v0.3.x database in a standalone sidecar — `applied migration file=002_due_notices.sql`, with `editorial_due_notices` and `editorial_due_state` created alongside the original tables and no change to existing rows.

Design notes. The fired marker lives in the app's own table, not on the item: writing it through the record would bump `revision` and 409 anyone with the panel open, append a history snapshot, and risk tripping the approval-reset rule. The due instant is part of the primary key, which is what makes rescheduling re-arm rather than needing an explicit reset. The first scan writes a watermark so installing into a project with a backlog does not stampede the bus, and a 48-hour catch-up floor means a sidecar that was down overnight still delivers recent events without replaying history. Unusable timezone or due-time settings fall back to UTC and 09:00 with a warning rather than wedging the worker for every other project.

Scheduling is `@every 5m` because the SDK's `parseSchedule` accepts only `@every <duration>`; a due time is therefore a frequent check that asks whether the instant has passed, not a cron expression. The platform's worker loop fans out per project — the pinned project for a project-scoped install, `ListProjects()` for a global one — so no fan-out code of our own was needed, and `ListProjects` requires no manifest permission.

Not done: `content.overdue`. "Overdue" means past deadline and still not done, but workflow statuses are project-configurable — `published` is only a default and a project may rename or remove it — so there is no reliable test for "done" without either hardcoding a status name or adding a terminal-status concept to settings. That is a design decision worth taking on its own. Also not done: per-user notifications, digest events, and any UI affordance for what is due today; the widget and panel are unchanged in this release.
