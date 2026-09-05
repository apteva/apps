# API Gateway reliability work — 2026-09-05

Release **0.6.0**, branch `fix/api-v050-audit`, based on the exact release
`api/v0.5.0` (`04a090f3122b6b2916854a3432b93fbd1e3695f7`). Changes were prepared in an isolated worktree.
The user's existing `apps` checkout was not modified. The implementation audit did not change a production service, DNS record,
ingress route or installation. The release publishes the source tag and registry entry.

## Findings addressed

The numbers below correspond to the original 0.5.0 audit findings.

| Audit finding | Implemented change and relevant verification |
|---|---|
| 1. Project overrides | Authoritative request-scoped context; reject conflicting body scope. Audit reproduction, global REST creation, real SDK sidecars, and server proxy contract tests. |
| 2. Credential leakage | Strip gateway/internal credentials and hop headers; preserve only explicitly constructed outbound credentials; redact logged target URLs. Proxy/function audit tests and URL-redaction test. |
| 3. Invalid auth | Canonical typed policies at mutation, legacy data fails closed, nil-safe merging. Invalid/null auth audit tests. |
| 4. Shared cursor poisoning | Client replay cursor never seeds shared upstream sequence. Two-subscriber future-cursor regression. |
| 5. Stream authorization lifetime | Registry cancellation on key revoke, API/route mutation or deletion; token-expiry deadlines; registration serialized with mutation. Live revoke and expiry tests. |
| 6. Unbounded requests | Streaming uploads, explicit body and response bounds, auth/header/body/write deadlines, admission limits. Upload-progress, truncation and limit tests. |
| 7. Route precedence | Structural ordering, exact method/path precedence, validated patterns and integer bounds. Overlap, cache invalidation, concurrent mutation and zero-priority tests. |
| 8. Proxy HTTP behavior | Redirect preservation, sanitized transport headers, immediate SSE flushing, cancellation, failed-transfer abort, explicit upgrade rejection. Live SSE, redirect and truncated-wire-response tests. |
| 9. Cross-app dispatch | Bound callback, outbound credential, effective project, binding health check, manifest integration slot. Callback capture and two real SDK processes. |
| 10. Function truncation | Limit+1 reads with explicit overflow errors; no successful truncation. Both input/output regression tests. |
| 11. Function response semantics | Strict status/header validation, explicit envelope discriminator, Content-Type before status, multiple headers, preserve error/empty HTTP responses. Structured-response and upstream-status tests. |
| 12. JWT identity | Require a valid Auth user identity; no fabricated fallback subject. Malformed-success response regression. |
| 13. Lost invalidations | Disconnect on subscriber overflow, upstream loss or sequence reset; reconnect forces ready/reload. Three independent regressions and disconnect test. |
| 14. Hub lifecycle | Consistent membership lock order, atomic empty recheck, shared readiness/error state. Deterministic last-unsubscribe race and simultaneous-readiness tests. |
| 15. Hostname lifecycle | Disabled hosts cannot fall through; persist ownership/cleanup, recover after restart, retry failed cleanup, prevent cross-project hostname reuse. Disabled-host, failed cleanup and ownership tests. |
| 16. Logs/persistence | Batched bounded writer, retention, stable cursor, request IDs, error reporting, deletion transaction and late-log guard. Retention/deletion, cursor and redaction tests. |
| 17. Request/fan-out cost | Immutable compiled route snapshots, minimal match allocation, real circular replay buffer, shared bounded projections, pooled response buffers, reduced request-log transactions, management diagnostics kept off public lookup. Before/after measurements in PERFORMANCE.md. |
| 18. DNS | Longest managed-zone match, IPv4/IPv6 distinction, exact record ownership, preserve unowned records, surface conflicts. Managed-zone, AAAA and ownership tests. |
| 19. CORS consistency | Validate combined policy before commit, serialize remote sync, persist failures/retry, preserve advanced UI fields and explicit false. Combined-limit/retry tests and UI round-trip tests. |
| 20. Dashboard races | Abort/generation guards, clear old scope/rows/secrets, ownership checks, disable selection during mutations, load selected tab, retain failed forms and display errors. Async ordering/error tests and TypeScript/build checks. |

Smaller audit items also addressed: `coalesce_ms: 0`, normalized topic whitespace,
exact large numeric identifiers, root `/`, encoded path segments, base queries,
request-scoped REST creation, complete public URL context, bounded configuration,
atomic route insert/upsert, one canonical embedded manifest, and current SDK pin.

## Validation

- `GOWORK=off go test -race -cover ./...`: passes; final coverage and raw output
  are recorded in `validation-results/go-tests.txt`.
- All **23 original audit regression checks pass**. The DNS regression now
  supplies the actual managed zone, since hostname text cannot determine DNS
  delegation. The public-events regression checks both configuration rejection
  and fail-closed behavior for directly seeded legacy data.
- Real SDK integration builds and launches two separate sidecar processes with
  separate databases and tokens. A local callback adapter validates outbound
  credentials and project scope and replaces the target credential. It verifies
  bound HTTP dispatch, public credential stripping and cross-project rejection.
- Separately, the current local server passes
  `TestCallback_AppProxy*`, `TestGlobalAppProxy*`,
  `TestAppProxyRejectsConflicting*`, and `TestAppProxyDerivesEffective*`.
- `GOWORK=off go vet ./...` and a standalone `go build` pass. The executable was
  built outside the repository at `/private/tmp/apteva-api-0.6.0`.
- Four Bun UI behavior tests pass; React/TypeScript type checking passes.
- The panel was rebuilt from the edited source and verified against the host's
  React surface; the build script validated imports across 137 panels.
- `git diff --check` passes. No tests rely on live credentials or external DNS.
- Benchmarks use the same harness against the old release and candidate,
  `GOWORK=off`, `GOMAXPROCS=2`, on Go 1.26.6 / Darwin ARM64. Timing tests are
  opt-in; CI has no brittle latency threshold.

## Upgrade and verification boundaries

Read README.md for changed request limits, authentication/forwarding semantics,
route precedence, stream resynchronization, log retention and DNS ownership.
Migration 003 adds persistent reconciliation state and indexes, clears orphan
logs and normalizes root patterns. Existing invalid auth remains fail-closed
until corrected. Old DNS records are intentionally not retroactively claimed.

The manifest targets the current local Apteva 0.42.3 baseline. SDK v0.74.1 was
selected by tag ancestry, with standalone builds disabling the workspace overlay.
Compatibility with older server releases has not been established.

The integration evidence combines actual SDK processes with separately tested
server contracts. It is not a deployed server-plus-sidecars/browser test.
Production DNS/provider behavior, real-browser cross-origin behavior, and
production load/long-running soak tests remain deployment-level validation.

The existing Domains interface offers neither conditional create nor
compare-and-swap. Gateway cleanup checks the recorded record ID/value and refuses
known external conflicts, but cannot make a provider mutation atomic with a
concurrent external DNS writer. Diagnostic logs may be dropped under saturation;
they are not a durable security ledger. Per-route key scopes and short-lived
browser credentials remain product extensions, rather than silently changed
API-key semantics.

Release validation upgraded the SDK pin from v0.73.0 to v0.74.1, verified its
tag ancestry, and reran the standalone race suite and build checks. Original
performance measurements used v0.73.0; release measurements are recorded
separately to avoid mixing dependency versions.
