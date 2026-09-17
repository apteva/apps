# Validation

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

The accepted set is derived from the same `toolSpecs` the MCP tools publish, so the two front doors cannot drift: a test asserts every tool still sets `additionalProperties: false` and that `items_list` retains its `archived` enum after the refactor.

- `GOWORK=off GOTOOLCHAIN=local go test -race -count=1 ./...`, `go vet ./...`, `gofmt` — passed, including every v0.1.x–v0.3.1 suite unchanged.
- New tests cover tag and custom field matches, case-insensitivity, a field key deliberately not matching, rejection of unknown parameters on reads, writes and path-supplied ids, and that documented filters plus `project_id`/`install_id`/`api_key` still pass.
- A row inserted without `tags` or `fields` — the v0.1.x shape — stays searchable by title and body: `json_each` yields no rows for a missing path instead of failing the query. Covered by a regression test.
- End-to-end against a standalone sidecar on an isolated database: `q=case-study` now finds a tag-only match, `q=Research` finds a custom field value, `q=desk` correctly finds nothing, `?tags=` and `?fields=` return 400 with the accepted list, and `status`/`archived`/`limit` are unaffected.

Compatibility: every request the panel and the calendar widget make was audited against the new rule — `/items?archived&limit&offset`, `/items/:id`, `/releases/:id/refresh`, `/settings`, `/integrations?app&brand_id`, `/calendar?from&to&date_field&include_releases&brand_id` — and all use only schema or infrastructure parameters. The gateway forwards a caller's query untouched apart from setting `project_id`, so nothing else is injected in front of the sidecar. A third-party caller relying on a silently-ignored parameter will now get a 400; that is the point of the change, and it is a behaviour change worth noting for anyone scripting against the HTTP API.

Not done: a structured per-field filter (`fields.<key>=<value>`), which the report also asked about. Search now reaches custom field values, but narrowing by one named field needs a deliberate design for typing and indexing rather than a `LIKE` over the JSON blob.
