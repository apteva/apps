# Social 0.16.2 audit fixes

Base: `social/v0.16.2`, commit `ae7bb44436018dcaa3180cc8e07bbabf44603bde`.
Released as **Social 0.16.3**, with App SDK **v0.74.1**. Publication updates the source release and marketplace; running installations upgrade separately.

## Changes

| Audit items | Implemented behavior |
|---|---|
| 1–2: approval and concurrent transitions | Reject immediate publication with approval required; protect legacy editing of reviewed content; serialize post lifecycle operations and check revision, status, and approval in the SQL claim. Preserve delivery revisions and revision snapshots. |
| 3–4: provider reconciliation | Preserve local review/content ownership, reconcile individual targets, roll up whole-post status, distinguish publishing/failed/published, and resolve the correct account's public identity. |
| 5, 12: deletion | Cancel provider schedules and drafts, preserve records when remote cancellation/deletion fails, remember successful deletions for retries, reject deletion during publication, and detach inbox history transactionally. |
| 6, 8–10: scheduling | Carry project scope and schedule generation into Jobs, reject obsolete/early callbacks, recheck due time when claiming fallback work, repair delivery retries, and convert local schedule inputs to UTC instants with DST-gap validation. |
| 7, 15–16: inbox | Ignore stale responses, tie reply actions to loaded selection, retain outbound messages, recursively load descendants, page conversations by timestamp/id, paginate provider streams and nested replies, checkpoint after persistence, and expose partial failures. Bound a sync to 200 collection calls and back off after provider rate limiting. |
| 11: repeated editing | Apply new generic body edits consistently and retain explicit target input precedence. Create a revision for body and target-option edits. |
| 13–14: account connection | Refresh disconnected provider accounts on import and prevent native OAuth from reusing a provider backend connection. |
| 17–19: analytics correctness | Store known zero values, separate unavailable metrics, key cached results by exact dates/filters/dimensions/groups, isolate filtered series, and compare accounts only with a shared metric. Reject stale UI responses after query changes. |
| 20–21: media | Reject attachments a native strategy cannot represent, detect over-limit downloads without truncation, and bound HTTP operations. Preserve successful upload results and honor server chunk concurrency and cancellation. |
| 22: interrupted delivery | Save known operations before upload proceeds, reconcile Zernio/TikTok operations, keep unverified receipts pending, and mark abandoned untracked delivery as an unknown, non-retryable outcome. Bound worker concurrency and serialize delivery through a connection. |
| 23: TikTok settings | Retrieve current creator options, require valid explicit privacy, enforce creator interaction restrictions and known duration limits, and present the available settings in the composer. |
| 24: profiles | Reject self-reassignment, transact default changes, enforce one default per project, fix slug suffixes, and move metric profile attribution consistently with account reassignment. |
| 25: compose lifecycle | Revoke current previews, append each successful upload immediately, abort on unmount, interrupt retry delays, and cancel failed multipart sessions. |
| 26: measured performance | Extract delivery, analytics, inbox-pagination, external-content, and TikTok helpers; lazy-load charts; batch metric writes; add an index and grouped latest-snapshot query; expire rebuildable query-cache entries after 90 days. Preserve metric history. |
| 27: security/build | Require Go 1.26.6, scan source and produced binary, reject private avatar destinations and SVG serving, add restrictive response headers, protect OAuth callbacks with a one-time nonce and state validation, verify hosted completion, and label caller-supplied review actors as reported identities. Add scoped CI checks on PRs, main changes, and Social release tags. |

Type checking also found invalid UI-kit status variants in the post card and an ambiguous post-response type. Both are corrected.

Migration `020_audit_integrity.sql` adds the delivery/scheduling metadata, query cache, OAuth nonce, and indexes. Duplicate existing profile defaults are normalized before the unique index is created. No production database was migrated during this work.

## Validation

- Full Go suite: **196 tests passed**, including 21 original audit reproductions and 13 additional integrity regressions.
- Race detector: **passed** (`go test -race -count=1 -timeout 600s ./...`, 315.488 seconds).
- `go vet ./...` and `go build`: passed with `GOWORK=off` and Go 1.26.6.
- UI suite: **27 tests passed**, including reordered responses, upload cancellation, local UTC-offset conversion, and DST gaps.
- TypeScript: panel, charts, calendar card, and post card passed against the workspace UI-kit source contract.
- All three Social bundles rebuilt; the repository's React import verifier passed for **145 bundles** including shared chunks.
- Bun dependency audit: no vulnerabilities found.
- Go source scan: **0 affected vulnerabilities**; one Windows-only required-module advisory (`GO-2026-5024`, `golang.org/x/sys/windows`) has no imported-package or call path in this app.
- Built-binary scan: no vulnerabilities found.
- `git diff --check`: passed.

The panel entry is **143,010 bytes**, compared with **500,369 bytes** in the release. Charts are a separate **356,084-byte** lazy chunk; these figures describe entry loading, not a reduction of all downloadable assets. The complete Social bundle set is 520,341 bytes.

With 50,000 retained metric rows, the latest-breakdown query averaged about **0.38 seconds**, versus **1.25 seconds** for the indexed correlated version on this machine. The query plan uses the new covering index. These are local synthetic measurements under concurrent machine load, not production benchmarks.

## Operational limits

Tests use temporary databases and stub providers. No live post, message, account connection, deployment, or production data repair was performed. Actual provider permissions, end-to-end browser behavior, and production load still need deployment validation. Unknown native delivery outcomes intentionally require checking the remote service before creating a replacement.

The broader audit suggestions for production observability, webhook adoption, automatic historical downsampling, and a larger module/view decomposition remain future improvements. This patch preserves historical metrics and implements bounded polling, pagination, cache expiry, and the measured query/bundle changes above.

## Release validation

The 0.16.3 release candidate was rebased onto current apps/main and revalidated with the topology-latest SDK tag v0.74.1. The release changes the embedded and external manifest versions together and republishes the rebuilt frontend artifacts.

SDK v0.74.1 release rerun: all 196 Go tests passed (3.858 seconds), the full race suite passed (143.931 seconds), all 27 UI tests passed, and build/vet plus source/binary vulnerability scans passed. Both manifests declare 0.16.3.
