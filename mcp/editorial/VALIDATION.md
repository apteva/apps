# Validation

Validated locally on 2026-09-14 against the published app-sdk v0.81.0 with no workspace overlay.

- `GOWORK=off GOTOOLCHAIN=local go test -race ./...` — passed.
- `GOWORK=off GOTOOLCHAIN=local go vet ./...` — passed.
- Standalone Go build and sidecar startup — passed; `/health` reports ready with an isolated SQLite database and no connected apps.
- Strict TypeScript checking of `ui/EditorialPanel.tsx` — passed.
- Panel bundle — built successfully; Editorial passes the shared React import-surface verification.
- Browser smoke check using the real standalone sidecar: create a content item, edit its brief/owner, select and save a publication date with the inline picker, create a standalone newsletter release on a different date, and verify both calendar entries and the board card.

Backend tests cover dependency-free installation and operation, HTTP and MCP project isolation, approval invalidation, clearing structured fields, stale writes, concurrent edits, validation, pagination, settings guards, archive/restore, and optional Social/Campaigns result refreshes with project propagation and preservation of missing results.

The repository-wide panel verifier exits nonzero for four pre-existing issues outside Editorial: development JSX-runtime imports in Instances, 3D Studio and SEO; and a missing default export in 3D Studio's preview module. These apps were not changed.

Delivery integrations were tested with SDK platform stubs. No real accounts were called, no content was published, and no production installation or deployment was performed. The release manifest and registry entry pin source and assets to editorial/v0.1.0.
