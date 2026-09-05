# Computer v0.7.87 audit fixes

Based on `computer/v0.7.87`, commit `20629a942080395d37f40aec7cc0badf7eb493d9`.
The initial audit was isolated on `fix/computer-0.7.87-audit`; the original dirty
workspace and installed app were not modified. This document records that audit.
The subsequent release candidate is documented in [v0.7.88](RELEASE_0.7.88.md).

| Audit | Implemented change |
|---|---|
| 1 — Proxy credentials | Answer only matching Proxy challenges, reject Server challenges, and bound the retry cache without blocking later legitimate requests. |
| 2 — Session concurrency | Serialize actions, tab operations, reads and teardown; revalidate registry membership after acquiring the lock; protect last-used timestamps; allow queued action callers to cancel. |
| 3 — Provider cleanup | Surface release errors, persist cleanup-pending leases, retry with backoff, reconcile interrupted sessions at startup, and retain original integration bindings. Import legacy active provider IDs for best-effort cleanup. |
| 4 — Upload targeting | Resolve an explicit input, associated label, or unambiguous nearby container. Remove document-wide/site-specific fallback; reject disabled controls and unsupported multiple uploads. |
| 5 — Download identity | Keep original provider filenames separate from sanitized names. Serialize ID reservations and reject ambiguous matching events/provider results instead of guessing by retrieval order. |
| 6 — Local persistence | Reject saved local profiles with persist=false before opening Chrome. Ephemeral sessions and persist=true saved profiles remain supported. |
| 7 — Backend policy | Validate the final resolved context backend against both the explicit request and the configured lock. |
| 8 — Deadlines | Bound CDP commands and DOM waits, propagate caller cancellation, include queue time in the action deadline, bound final observations and provider release, and standardize wait duration to milliseconds with a 30-second maximum. |
| 9 — Tabs | Reuse target contexts under a stable browser root and one stability tracker per target. Detach closed-tab download listeners, apply emulation on cloud tab switches, and invalidate semantic snapshots. |
| 10 — History queries | Use metadata-only queries for polling/replay. Fetch image BLOBs only for image retrieval. Add paginated history queries and opt-in retention that preserves active/pending sessions. |
| 11 — Observation cost | Use action-only execution across backends. Enumerate semantics without rendering a bitmap, and apply extraction format/size limits before transferring results from the browser. |
| 12 — Session selection | Preserve requested/new session IDs during loading and look up historical deep links independently of the first page. |
| 13 — UI ordering | Serialize/coalesce refreshes, abort obsolete requests, isolate project/session state, and discard stale settings responses. |
| 14 — Reproducible UI | Declare and lock hls.js, rebuild all six Computer UI bundles, add strict TypeScript checking and React behavior tests. Correct status-component props found by type checking. |
| 15 — Screenshot contract | Populate png_b64 from the actual binary envelope and convert JPEG data to real PNG. |
| 16 — Missing contexts | Reject unknown saved names and app IDs. Raw provider IDs use their explicit compatibility argument. |
| 17 — Dropdown correctness | Scope options to the associated popup, reject ambiguous labels, implement multiselect replacement, respect disabled controls, and verify selection after reconciliation. |
| 18 — Empty inputs | Distinguish omitted values from explicit empty strings, supporting field clearing and empty-value native options. |
| 19 — Settings | Serialize updates inside a transaction; disjoint patches survive concurrent updates and failed writes roll back completely. |
| 20 — Screenshot privacy | Use private/no-store for final screenshots. Replay resources also use private/no-store. |

Additional changes from the audit's design recommendations: common provider HTTP
redirect filtering; oversized replay responses fail instead of silently
truncating; replay retrieves credentials from the original saved integration
binding; backend API URLs come from operator configuration; Chrome sandbox
disabling is opt-in through `APTEVA_CHROME_NO_SANDBOX=1`.

## Validation

- Tier 1: complete unit suite with the Go race detector.
- Tier 2: complete local Chrome/sidecar fixture suite with the race detector,
  including new upload/dropdown, empty input, tab reuse/root-close download,
  timeout, cleanup, settings, context-policy and contract regressions.
- Tier 3: all 11 LLM cases passed with `gpt-5.6-terra`, using the current
  authenticated desktop Codex CLI. Coverage includes controlled dates,
  unavailable dropdowns, authenticated-download planning, outcome waits,
  semantic navigation/text, session arguments/lifetimes and guarded actions.
- UI: five React behavior tests, strict TypeScript check, six rebuilt bundles,
  and host React import verification (140 existing app panels checked).
- `go build ./...`, `go vet ./...`, and `git diff --check`.

Metadata benchmark, Apple M1 Pro, identical row containing a 2 MiB screenshot:

| Query | Time/read | Allocated/read |
|---|---:|---:|
| Previous image-bearing query | 748 µs | 4,197,521 bytes |
| Metadata-only query | 21.5 µs | 3,144 bytes |

This is a synthetic query benchmark, not a measurement of whole-app latency.
A regression test also verifies zero screenshot captures for a semantic-only
key action.

Live Browserbase/Steel/Browser Engine checks require provider credentials, which
were not configured in this checkout. Their unit/HTTP fixture tests ran. Real
Patreon account tests that edit or publish content were not enabled; equivalent
local publishing-shaped fixtures ran, including LLM guarded-action tests.
Legacy session rows cannot recover a credential binding that v0.7.87 never
stored: cleanup uses current operator configuration and stays pending when the
provider cannot be reached or authenticated. New sessions retain the binding.

## Re-running

From `mcp/computer`:

```sh
bash scripts/test-tiers.sh 1
bash scripts/test-tiers.sh 2
COMPUTER_LLM_CODEX_BIN=/path/to/current/codex bash scripts/test-tiers.sh 3
APTEVA_UI_KIT_DIR=/path/to/ui-kit APTEVA_DASHBOARD_DIR=/path/to/dashboard \
  bash scripts/test-tiers.sh ui
```

`GOWORK=off` preserves the release's pinned SDK rather than using the workspace
overlay. The runner defaults to the existing LLM test model; override with
`COMPUTER_LLM_TEST_MODEL`. No CLI installation or account configuration is changed.

History API: `GET /sessions?limit=100&offset=0` (maximum page size 500).
Set `APTEVA_COMPUTER_HISTORY_RETENTION_DAYS` to a positive day count to prune
terminal session history, screenshots and navigation. Retention is disabled by
default and never deletes active sessions or pending provider cleanup. Downloads
remain session-scoped and saved profiles retain their existing explicit deletion
controls.

Future design work, separate from these defect fixes: a comprehensive backend
capabilities API, streaming replay delivery, and production latency/cleanup
telemetry. No production load or cloud-provider parity claim is made here.

## Subsequent live validation

Browserbase and the saved Patreon test context were subsequently configured for
the tier 3 expansion. See [Patreon tier 3 results](PATREON_TIER3_RESULTS.md) for
new observations, fixes, reruns, and current limitations. The cloud-coverage
limitations above describe the initial audit run.
