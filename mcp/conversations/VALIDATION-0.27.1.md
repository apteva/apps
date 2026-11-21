# Conversations v0.27.1 validation — 2026-11-21

Deleting a conversation now stops its chat thread in the pass the delete
starts, even when threads retired by earlier releases are still queued. Each
kill pass handles 50 retired threads; in v0.27.0 it took them in no defined
order, so right after an upgrade the backlog could fill the pass and leave the
just-deleted conversation's thread running until a later five-minute worker
pass. This was seen live on local :5280 after the v0.27.0 upgrade (71 queued
rows; the new thread stopped one pass later). Passes now take the newest
retirements first (`ORDER BY kill_attempts, rowid DESC`). Go only: no
migration, tool, skill or frontend change.

## Checks

- New `TestDeleteStopsNewThreadBeforeRetiredBacklog` (60 queued retired rows,
  then a delete) failed on v0.27.0's ordering (first kill was the oldest queued
  row) and passes with the fix; the backlog still drains on later passes.
- `GOWORK=off go test -race -tags=integration ./...` passed (181.8s); `go vet`
  clean, including `-tags 'integration scenario'`. `GOWORK=off go mod tidy` left
  go.mod/go.sum unchanged.
- 142 Bun UI/client tests passed, with 2,044 assertions; package build and
  consumer TypeScript check passed. Frontend source is unchanged from v0.27.0
  (version literals only), so Playwright was not rerun.
- Tier 3 (real-model scenarios) not run: no Codex test credentials here.
- App SDK stays pinned at v0.115.2.
