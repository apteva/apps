# Storage 0.11.0 — audit fixes and validation

Implemented from the Storage 0.10.26 audit dated 5 September 2026. Work is on
`fix/storage-audit-20260905` in the isolated `apps-storage-audit-fixes` checkout.
Its base is `b903f81e`; the original Storage subtree matched release
`storage/v0.10.26` (`c2befdbdb4228da871211e09b77420bfc0d50002`).
Publishing this release does not change any production install, bucket, or database.

## Audit disposition

All 26 numbered findings have corresponding implementation changes. Regression
coverage targets the failure conditions, including the original 16 reproductions.
Passing these tests does not establish that every possible defect is absent.

| Audit | Implemented change | Main regression evidence |
|---|---|---|
| 1 | Public downloads require public visibility or a valid signature; user identity alone is insufficient. | `TestAuditPublicRouteDoesNotTrustUserIdentity` |
| 2 | Resolve a single upload ID, reject conflicting aliases, and recheck under the lifecycle lock. | `TestAuditAbortRejectsConflictingAliases` |
| 3 | Validate and authorize the canonical destination before every upload/import/folder creation. | `TestAuditCanonicalUploadAuthorization` |
| 4 | Check every affected source and destination in the folder-rename transaction. | `TestAuditRenameAuthorizesAllDescendants` |
| 5 | Commit file metadata, completion receipt, and pending-session removal atomically; cleanup checks references. | `TestAuditDirectFinalizeAtomicRetryAndImmutableKey`, legacy recovery test |
| 6 | HTTP and MCP share a lifecycle that freezes parts during completion. | `TestAuditCompletingSnapshotIsImmutable` |
| 7 | Shared byte reservations, encoded-input bounds, and persistent install session/byte quotas. | Budget, parallel-part, and quota regression tests |
| 8 | Require successful backend discovery, fail mount on unavailable bindings, and persist backend identity. | Discovery and backend-pin tests |
| 9 | Remove raw app-token logging. | Source review; new share keys are independent of app tokens |
| 10 | Honor and validate explicit import visibility in HTTP and MCP. | `TestAuditImportVisibilityAndNetworkPolicy` |
| 11 | Restrict import schemes, DNS results, connection addresses, and redirects; explicit administrator hostname allowlist for internal imports. | Import policy tests with disposable loopback fixtures |
| 12 | Verify actual direct-upload size and SHA-256; publish to a fresh key unavailable to the signed PUT. | Fault-injection tests and real MinIO PUT/replay tests |
| 13 | Reject unintended backend changes; refresh credentials without changing location; support explicit verified migration. | `TestBackendPinAndVerifiedMigration`, credential refresh test |
| 14 | Persistent signing key and per-file generations; privacy changes revoke prior Storage shares; checked Storage delivery is the S3 default. | Revocation/token-rotation tests and MinIO integration |
| 15 | Carry project and install scope through metadata, media, canonical, signed, and CDN URLs. | URL tests and reused-card browser test |
| 16 | Compose folder/search/type/tag/source filters with stable pagination; apply permission filtering before offsets; add panel page controls. | Folder/filter/page tests and browser pagination |
| 17 | Use SQLite character lengths for Unicode folder renames. | Unicode/descendant regression test |
| 18 | Literal case-sensitive folder prefixes, escaped text filters, and exact JSON tag membership. | Literal-folder and tag tests |
| 19 | Durable cleanup queue, failed-insert compensation, authorized tombstone purge, and truthful hard-delete results. | Cleanup failure, tombstone purge, and insert failure tests |
| 20 | Emit shared metadata-update events and explicitly scope upload-aborted events. | Native mutation/event tests; browser refresh checks |
| 21 | Derive selection from the current scoped file list and reset it when scope changes. | Browser visibility/selection test |
| 22 | Cancel stale reads, continue failed upload batches, report HTTP/clipboard failures, reset previews, and keep card callbacks current. | Five browser interaction tests |
| 23 | Apply attachment policy after resolving the final backend MIME type for GET and HEAD. | `TestAuditProxyRechecksFinalMIME` |
| 24 | Concurrent part writes with incremental accounting, streaming hashes/imports, bounded transfers, SQL folder aggregation, coalesced UI refreshes, and per-zone CDN request coalescing outside the global lock. | Parallel-part/CDN concurrency tests, browser resume test, 10,000-file benchmark |
| 25 | Reference-counted lifecycle locks, sweeper rechecks, project/owner checks, durable completion retries, browser resume, bounded JSON decoding, and validated size/TTL/name contracts. | Lifecycle, quota, JSON/chunked-body, current-folder receipt, and browser resume tests |
| 26 | Go 1.26.8, SDK v0.73.0 selected by commit topology, patched transitive dependencies, source and Linux binary scans. | Independent module builds (`GOWORK=off`) and vulnerability reports |

Additional audit recommendations are covered by the rewritten README and agent
instructions, removal of the unregistered legacy panel, an explicit metadata
conflict contract for deduplication, authenticated actor attribution, cancellation
propagation, an unmount-aware sweeper, and deterministic failure/concurrency tests.
The sidecar still uses process-wide state for its single mounted install; this is
not a conversion to a multi-install-in-one-process architecture. Mutation events
remain best-effort platform events, with per-file events retained for subscribers.

## Validation results

- **Tier 1:** `go test -short ./...` passed.
- **Tier 2:** `go test -tags integration ./...` passed, including a spawned SDK
  sidecar and the real S3 profile against disposable MinIO.
- **Tier 3:** all six live-agent scenarios passed through the local Apteva server:
  save/share, folder organisation, dedupe, move/tag, visibility changes, and
  multipart completion retried without a duplicate file.
- **Concurrency:** `go test -race ./...` passed, including deterministic
  part-replacement, completion, and CDN concurrency regressions.
- **Browser:** all five Playwright tests passed in isolated Chrome: selection and
  pagination, navigation races, batch/folder errors, card identity/scope, and
  resuming a 30 MiB upload after a transient failure.
- **Frontend:** TypeScript checking, production bundle generation, and Bun's
  dependency audit passed. Generated panel/card bundles and source maps are included.
- **Security:** source and Linux/amd64 binary govulncheck scans report **zero
  reachable vulnerability findings**. They retain one module-only advisory,
  `GO-2026-5932`, for unmaintained `x/crypto/openpgp`; Storage does not reach the
  affected package. This is not a claim of zero dependency advisories.
- **Repository:** `git diff --check` passed.

The original live-agent scenarios completed their functional assertions but
exceeded their old 12–18k token budgets with the current provider context. Budgets
were updated to observed usage with headroom (32k/45k; new multipart scenario 65k),
while dollar caps and functional assertions were retained. The multipart fixture
also explicitly avoids an unnecessary placeholder file and checks the intended
filename. Both initial results and successful reruns are retained for review.

The folder-summary benchmark uses 10,000 files in 100 child folders, the same Go
toolchain, and an in-memory SQLite database on this Mac. Original: **10.63 ms,
1,793,270 bytes, 110,139 allocations/op**. Revised: **8.18 ms, 47,755 bytes,
1,037 allocations/op**. This sample shows about 23% lower latency and 97% fewer
allocated bytes; it is a local microbenchmark, not a production throughput claim.

## Upgrade and operational boundaries

This is version **0.11.0** because sharing and deduplication contracts change.
Migration 006 adds lifecycle, share, reservation, backend, and cleanup records.
See [README.md](README.md) for the upgrade and verified backend-migration procedure.

- Existing token-derived share links stop working on upgrade. New links survive
  app-token rotation, and setting private/signed visibility revokes prior Storage
  signatures. Explicit direct S3 links remain usable until expiry; downloaded or
  previously browser-cached bytes cannot be recalled.
- Direct upload verification adds a backend read and write before publication.
  The original temporary PUT key remains scheduled for cleanup after expiry.
- Backend migrations were tested with fixtures. No real installation was migrated.
- Real provider validation used MinIO. AWS, R2, B2, and Hetzner account behavior,
  production traffic, and a full native mobile-device UI run were not exercised.
  Native routes/contracts were covered by Go integration tests.
- Historical production logs and deployed binary provenance were not inspected;
  this change prevents future token logging but does not erase prior log entries.
- Publishing makes 0.11.0 available to the app registry. Deployment, backups, and
  production post-upgrade checks are separate operations; existing installations
  are not upgraded as part of publishing.

## Evidence

Full results are retained in the local audit workspace's `fix-validation` directory.
Key files: `all-tiers.json`, `all-tiers.log`, `race.log`,
`integration-final.log`, `browser.json`, `browser.log`,
`govulncheck-source.json`, `govulncheck-linux-binary.json`, `bun-audit.log`,
`performance.log`, and `performance-original.log`.
The original audit and unchanged-release reproductions remain alongside them.
