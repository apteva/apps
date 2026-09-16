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
