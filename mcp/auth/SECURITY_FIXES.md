# Auth v0.11.0 — implementation and validation

Prepared 5 September 2026 from `auth/v0.10.0` (`dcfbd462`) on branch
`fix/auth-v0.10.0-audit`, in the isolated `apps-auth-audit-fixes` checkout.
Publishing this source release does not upgrade installed deployments. Review the compatibility changes below before upgrading.

## Results

| Check | Result |
|---|---|
| Go unit/regression suite | PASS — 97 top-level tests |
| `go test -race -tags integration -coverprofile=… ./...` | PASS — 105 top-level tests including real sidecars; 113.143 s |
| Repeated concurrency suite under `-race` | PASS — four tests repeated ten times; 23.052 s |
| UI tests with Bun/React/Happy DOM | PASS — seven tests, 23 assertions |
| Strict TypeScript check | PASS |
| `go vet ./...` | PASS |
| Source `govulncheck` | Zero reachable or imported-package vulnerabilities |
| Compiled-binary `govulncheck` | Zero reachable or imported-package vulnerabilities |
| Compiled toolchain / SDK | Go 1.25.13 / app-sdk v0.74.1 |
| Generated panel | Rebuilt; embedded source matches TSX; host import check passes |
| `git diff --check` | PASS |

Statement coverage is **55.9%** for the instrumented Go package, compared with
51.8% in the baseline audit. Spawned sidecar coverage is not merged into that
figure. Passing checks do not imply every possible execution path is covered.
The source scanner also reports 22 advisories in required modules whose
vulnerable packages/symbols are not called by this app; the compiled-binary
scan reports 21 such module advisories. Those are distinct from the five
previously reachable standard-library advisories, which are resolved by the
patched toolchain.

On an Apple M1 Pro, a 100-user page from a 10,000-user database measured
**0.480 ms/op**, approximately 119 KB/op. The same new cursor query before adding
its supporting index measured 18.5 ms/op. Password hashing measured about
104 ms/op and 64 MiB/op. These are local microbenchmarks, not production
throughput or latency guarantees. Search returns one SQL query per page,
including MFA filtering.

## Audit findings addressed

| # | Finding | Implemented behavior |
|---|---|---|
| 1 | MFA requirements ignored | One eligibility gate rejects MFA-required/enrolled authentication across issuance paths; discovery and UI disclose unavailable challenge support. |
| 2 | Invalid organization widened scope | Strict absent-vs-invalid selectors, numeric bounds, conflicting-selector rejection and explicit project/install agreement. |
| 3 | Concurrent refresh replay | Serialized transaction consumes a credential and creates its successor; replay revokes the logical family. Tested through independent WAL database pools. |
| 4 | Disabled users redeem recovery | Disable invalidates recovery credentials; confirmation checks current active user/org/client and does not mint a session. |
| 5 | Recovery secrets in audit | Hash-only token storage, credential-free audit outcomes, fragment links, and migration invalidation/scrubbing. |
| 6 | Wrong-user dashboard actions | User/project/org keyed boundaries, cancellation and stale-response rejection; role selections reset and cannot save while loading. |
| 7 | Abuse/resource limits absent | Bounded JSON/password sizes and Argon2 concurrency, persistent quotas, atomic lockout, dummy hashing for unknown users and queued public recovery delivery. |
| 8 | Inconsistent revocation | JWT session IDs, online family/epoch/client/org validation, atomic force logout and credential replacement; separate delegated minting disabled. |
| 9 | Organization policies ignored | Typed validated effective policy used for signup, provisioning, password replacement, upgrade, authentication, refresh and admin policy display. |
| 10 | Recovery partially committed / old links survive | Transactional consume/password/verification/revocation; password changes invalidate outstanding recovery credentials. Fault tests prove rollback. |
| 11 | Password whitespace changes | Exact bytes preserved; Unicode-aware minimum length; validation precedes token consumption. |
| 12 | Broken recovery landing / false delivery success | Working GET forms, restrictive CSP, no-referrer, fragment removal; durable non-secret delivery queue and explicit failed-delivery status. |
| 13 | False OAuth/magic-link contract | Advertise proprietary implemented endpoints only; confidential-client secrets enforced; M2M user authentication rejected. |
| 14 | Orphan identities / quota overshoot | Identity, guest, quota reservation and initial session share one transaction; duplicate concurrent login resolves one identity. |
| 15 | Nonrotating refresh fan-out | Stable credential and logical family; absolute expiry never extended by refresh. |
| 16 | Malformed RBAC clears roles | Whole-array validation rejects null, strings, fractions, overflow and nonpositive IDs; explicit empty arrays remain valid. |
| 17 | Disabled client restores CORS | Reject disabled-client edits, serialize mutations, retry reconciliation hourly/on mount; UI supports origin editing and retry. |
| 18 | Search filtering / N+1 / stale results | SQL-side MFA EXISTS, indexed descending-ID pagination, one query per user page, batched role permissions, debounced/cancelled UI searches. |
| 19 | Unbounded history / expired sessions shown active | Absolute family lifetimes, retention worker, live replay-evidence preservation, capped/prioritized session list and expiry-aware UI. |
| 20 | Vulnerable Go baseline | Go 1.25.13, latest SDK tag by checked commit topology, source/binary vulnerability scans. |

Additional implemented changes include strict JWT expiry/issued-at checks,
canonical issuer routing without request-Host fallback, private-key retirement,
graceful/emergency signing-key rotation, atomic organization/key creation,
active-org key repair on mount, coherent RBAC snapshots and token-size limits,
audit-write diagnostics, statistics query error propagation, guest-upgrade
revocation, masked password fields, effective policy hints, dialog focus
handling and CI for the Go and UI checks.

## Important compatibility and operational limits

Read [README.md](README.md) before rollout. The migration forces re-login and
invalidates previously issued recovery links. Email verification/reset require
a normal login afterward. Password replacement always revokes sessions,
including when an older caller requests `revoke_sessions: false`. Guest upgrade
removes linked identities to prevent an old device entering the new account.

MFA challenge implementation, OAuth/OIDC and magic-link login remain unsupported;
this release fails closed and removes false capability claims. Independent
Apteva delegated tokens are no longer minted. Previously issued platform tokens
require platform-side expiry/revocation; this Auth migration cannot invalidate
them. Offline JWT validators likewise cannot observe session revocation without
consulting current Auth state.

Active signing keys still reside in SQLite. External key encryption/HSM support
and per-bound-caller provider/client allowlists are separate platform/integration
work. Authorized sibling apps remain trusted identity providers; actual device
or provider possession proof is their responsibility.

Session history is capped at 200 rows per user, with active credentials first;
it is not an unlimited forensic browser. The default request quota uses the
actual network peer, so a reverse proxy can share that quota among many users.
Validate canonical public URLs, proxy limits, Messaging configuration and
previously issued delegated credentials when deploying. This task tested local
sidecars, not a production deployment or real outbound email delivery.

## Regression evidence

- `audit_regression_test.go`: fixes the independently reproduced v0.10.0 failures.
- `security_extended_test.go`: malformed selectors/arrays, secret enforcement,
  MFA gates, rollback, credential epochs, absolute refresh expiry, concurrency,
  query counts, key rotation, strict JWTs, resource caps, migration, mail/queue
  behavior, cleanup, policy inheritance and two independent WAL pools.
- `security_integration_test.go`: unauthenticated public routes, admin isolation,
  real user JWTs, logout revocation, recovery landing, truthful discovery and
  rejection of an unbound identity-tool caller.
- `ui/AuthPanel.test.tsx`: user and organization switching, delayed responses,
  panel project/install isolation, pending-role protection and actual role-save
  target/body validation.

Validation logs and a locally compiled binary are saved in
`/Users/marcoschwartz/Documents/code/auth-v0.11.0-validation-20260905`.
CI is defined in `.github/workflows/auth.yml`; hosted results are recorded in GitHub Actions for each release commit.
