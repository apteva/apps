# Deploy 0.24.8 corrective work

This change addresses the 26 findings in the 5 September 2026 review of
`deploy/v0.24.8` (`fed83949765dc544964fa5d14b8154e1798234cc`). The corrective release is **Deploy 0.25.0**, pinned to
`deploy/v0.25.0`. It was prepared and validated in
`codex/deploy-review-feedback` before integration with the current main branch. The app-sdk dependency is pinned to v0.75.0, selected from
the fetched tags by commit ancestry.

## Changes and regression coverage

| Audit | Change | Evidence |
|---|---|---|
| 1 — project boundaries | Shared scoped record lookups precede HTTP/MCP reads and mutations, including logs, cancellation and mobile actions. | Cross-project route/tool rejection tests and original log disclosure regression. |
| 2 — ambient credentials | Builds, mobile commands and services use a toolchain environment allowlist. Platform variables are excluded; runtime `PORT` is allocator-controlled. | Synthetic platform/host-secret canaries and reserved-port test. |
| 3 — unsafe termination | Save process birth identity, validate process group ownership, and refuse changed/unknown owners. Never select a termination target from its TCP port alone. | Real isolated child-group TERM test, wrong-identity refusal, legacy launch-time checks; Linux and macOS ownership tests. |
| 4 — unintended crash promotion | Restart the intended release's exact build and saved effective configuration. A committed release's crash does not revert to its predecessor. | Original newer-unreleased-build regression, boot recovery fixtures and healthy-release crash regression. |
| 5 — stop/recovery intent | Persist running/stopped/archived intent per environment. Stop clears queued automatic releases and stops active candidates. Archive is visible before cleanup. Restart budgets reset only after a healthy dwell. | Stop/queued-restart and initial-starting-release regressions; lifecycle integration tests. |
| 6 — reordered callbacks | Serialize environment operations, persist release metadata before activating runtime callbacks, condition promotion on current intent, and explicitly retire superseded candidates. Old callbacks cannot remove a replacement route/lease. | Forced early-readiness regression, superseded-candidate test and service integration flows. |
| 7 — failed replacement downtime | Keep the previous process until candidate readiness and ingress succeed. Fixed ports use controlled downtime with restoration of previous intent on failure. | Failed-start preservation/rollback-intent regression; actual build/release/stop integration. |
| 8 — cloud poll/cancel races | Poll only submitted jobs with IDs; condition terminal/metadata updates on active status. Persist cancellation before remote calls and retain late-returned job IDs for retry. | Original premature-poll and cancelled-finalization regressions; cloud lifecycle suite. |
| 9 — lost automatic release | Create the release reference, configuration and intent transactionally. Retry successful builds with unfinished release work. Mobile preparation resumes its existing pending record after restart. | Atomic/idempotent creation and pending-mobile restart tests. |
| 10 — wrong GitHub run | Dispatch a unique `apteva_deploy_run_id`, match the workflow's exact run name, expire discovery, and update both durable/in-memory IDs before artifact retrieval. | Overlapping/unrelated-run rejection plus correlated first-poll completion/artifact test. |
| 11 — destructive key rotation | Persist an encrypted pending revision before provisioning, reuse it on retry, activate only after successful provider setup, and retain the prior encrypted revision. | Failed-provider rotation regression and archived-key decryption after a simulated restart. |
| 12 — non-resumable Apple review | Track review submission separately from build attachment and resume unfinished preparation/submission idempotently. | Transient-failure retry regression and existing Apple submission/replacement fixtures. |
| 13 — modern Apple states | Prefer `appVersionState`, retain legacy aliases, recognize modern live/terminal states and retain unknown external states visibly. | Modern-state regression and mobile provider fixtures. |
| 14 — mobile availability | Observe actual TestFlight availability, expose invalid/failed processing, bound processing independently of upload ID spelling and reconcile nonterminal Google Play operations too. | Processing failure and TestFlight fixture tests; provider sync suite. |
| 15 — macOS false readiness | Verify a live PID and listener ownership using `lsof` and the process tree; unavailable ownership evidence fails closed. | Original nonexistent-PID regression and real macOS service integration tests. |
| 16 — abrupt/unbounded shutdown | Send TERM first to the verified group, wait a bounded grace period, escalate to KILL, and bound final waiting. Build cancellation uses the same group cleanup. | Real TERM-hook test, cancelled build test and integration stop checks. |
| 17 — artifact pruning races | Hold read locks during release start/download, exclude those operations from pruning, pin release/recovery intent and give new orphan directories a grace period. | Audit release-intent retention regression, retention/rollback and symlink-download tests. |
| 18 — uncancellable builds | Return pending local builds promptly, enforce a deadline, expose cancellation, report interrupted work on startup and cancel active builds on shutdown. Signing-lock waits honor cancellation. | Hung-build cancellation, signing-lock cancellation and asynchronous build integration fixtures. |
| 19 — incomplete blank artifacts | Preserve generated output and dependencies when staging custom/blank builds. | Original generated-output regression. |
| 20 — ignored Go build override | Execute the supplied Go build command and require its executable artifact. | Original custom-Go-command regression. |
| 21 — unverified binary identity | Read IPA Info.plist/AAB protobuf manifest identity and version, reject ambiguity/mismatch, and populate metadata from the binary. Retain independent Android managed-signature checks. | Wrong identifier/version fixtures for both formats; Android signed/unsigned artifact suite. See signing limitation below. |
| 22 — shared iOS signing state | Hold a cross-process, cancellable resource lock through cleanup; preserve existing profiles and remove only the temporary keychain from the current search list. | Lock cancellation regression; signing setup/cleanup source review. Actual concurrent Xcode builds were not run. |
| 23 — unbounded ZIP expansion | Share an extractor with runtime byte/entry limits, duplicate/type/path checks, staged cleanup and confined relative symlinks. | Expansion, duplicate, escaping-link and runtime-symlink round-trip regressions; capsule tests. |
| 24 — stale panel responses | Abort and generation-check detail plus secondary requests against project/install/environment/selection. Guard action targets and expose an environment selector. | Two real Chromium tests resolve old detail/signing requests after a new selection, then verify the build action's deployment/environment; two RequestGate tests. |
| 25 — polling cost | Seek a bounded log tail, rotate logs, poll sequentially only for active visible records, cache/coalesce retention work, skip excluded source trees and stream hashes. | Large log tail/rotation and excluded-directory regressions; browser tests and typecheck. Large-fixture microbenchmarks also recorded below; no production throughput claim. |
| 26 — ingress retry loss | Persist ingress desired/applied/error state, reconcile failures after restart, preserve the old service until routing succeeds, and expose routing degradation separately. | Failed unexpose/retry across App instances, lifecycle ownership checks and panel typecheck. |

## Validation

The original 16 audit regression tests are retained in `audit_regression_test.go`.
Additional regression tests are in `operation_safety_test.go` and the UI tests.
Existing provider/build fixtures now contain real mobile metadata and observe
the asynchronous local-build contract.

Run from `mcp/deploy`:

```sh
GOWORK=off go test ./... -count=1
GOWORK=off go test -race -tags=integration ./... -count=1 -timeout=300s
GOWORK=off go vet ./...
bun test ui/requestGate.test.ts
(cd ui/tests && bun install && bun run typecheck && bunx playwright install chromium && bun test)
```

The panel's `.mjs` and source map are regenerated with the repository's production
Bun settings. The browser harness can use an existing Chromium via
`PLAYWRIGHT_CHROMIUM_EXECUTABLE`. Linux tests run in an isolated arm64 Docker
container; they do not signal host services. Final captured results are stored
with the external audit artifacts.

## Recorded results

| Check | Result |
|---|---|
| Final macOS unit sweep | PASS, 12.861 s |
| macOS full race + integration suite | PASS, 115.462 s |
| Linux arm64 full race + integration suite | PASS, 96.654 s; disposable container with Go, Bun, Node and npm |
| Final smoke-build credential regression | PASS under the race detector; repeated on Linux |
| Strict TypeScript check against React 19 types | PASS |
| Browser/RequestGate tests | 4 PASS, including 2 real Chromium scenarios |
| Production panel bundle and source map | Regenerated successfully |
| `go vet` and `git diff --check` | PASS |

The final suite contains 250 Go test functions on macOS, including the six real
sidecar integration flows. Platform-specific tests differ on Linux. The last
smoke-build credential compatibility change was checked with focused race tests
on both platforms and the final full unit sweep.

On this Apple M1 Pro, a sparse one-GiB log tail benchmark measured **1.385 ms/op**
and **6.29 MB allocated/op** (bounded independently of total file size). Copying
a source tree with **10,000 excluded dependency files** measured **0.200 ms/op**
and **37.9 KB allocated/op**. These are local microbenchmarks, not production
load tests; `performance_test.go` reproduces them.

## Compatibility and practical limits

- **GitHub workflow change required:** declare `apteva_deploy_run_id` and set
  `run-name: ${{ inputs.apteva_deploy_run_id }}`. The old timestamp heuristic is
  intentionally removed. See README for the workflow fragment.
- **Asynchronous local builds:** HTTP/MCP callers must wait for build completion
  and the matching release. A successful submission response is not build success.
- **Migration and backups:** migration 013 is required. Preserve both the app
  database and signing vault key for encrypted revision recovery. No deployed
  database was migrated during this work.
- **Process upgrade:** legacy process adoption requires a matching recorded
  launch time and listener ownership. A mismatch is refused rather than assumed
  safe. Linux amd64/arm64 and macOS are the tested ownership implementations.
- **Trusted workloads:** local processes still share the host user's filesystem
  and privileges. Container/user isolation and hard resource quotas remain a
  separate runtime feature; filtering environment variables is not a sandbox.
- **iOS signing:** IPA identifier/version validation is implemented. Independent
  validation of Apple's full code-signature/trust chain is not added; Xcode and
  App Store Connect remain the signing validators. Android cryptographic bundle
  verification is covered by local fixtures.
- **External services:** tests use synthetic credentials, provider fixtures and
  local HTTP servers. No real Apple/Google/GitHub/Codemagic submission, live key
  rotation, provider fault injection or production ingress change was performed.
- **Logging:** periodic runtime copy/truncate rotation preserves child process
  survival across sidecar restarts; it may lose bytes during rotation and does
  not enforce a hard disk quota between watchdog ticks.

These repairs and tests address the concrete audit failures. They do not prove
that the application or its external providers have no remaining defects.
