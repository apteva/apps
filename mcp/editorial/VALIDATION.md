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

Delivery integrations were tested with SDK platform stubs. No real accounts were called, no content was published, and no production installation or deployment was performed. The release manifest and registry entry pin source and assets to editorial/v0.1.1.
