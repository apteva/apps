# Analytics v0.14.0 — audit fixes

Implementation and validation, 5 September 2026.

Base: release commit `9cc593fe367412eccf8d42a54c5f035893e3f3a0`. Working branch: `fix/analytics-v013-audit`. Release: `analytics/v0.14.0`. The release publishes the tested changes and versioned marketplace manifest. Updating an installed production instance is separate.

This document records the fixes to the original v0.13.0 audit, subsequent validation and production compiler inspection.

## Results

The original failing probes now pass. The change set fixes typed filtering, accumulator arithmetic, safe spec patches, objective identities, money validation, replay/idempotency, empty states, live refresh, chart gaps, tracking sessions and network fallback. It also adds guided management controls, resource limits, opt-in retention, diagnostics and a patched build toolchain.

Validation on the final Go source:

- **103 Go test functions pass**, including the race detector. The separate browser fixture test is intentionally skipped during ordinary Go runs.
- **59.2% statement coverage**, up from 49.4% in the release audit. Coverage remains incomplete; this is not proof that every possible bug has been eliminated.
- **28 Bun tests pass**, with 69 assertions; strict TypeScript checking passes.
- **8 real Chrome browser scenarios pass**, including rendered components and actual local HTTP handlers. The widget-edit scenario also checks invalid numeric filters and typed lists.
- The identity fuzz target completed **71,972 executions** over approximately 16 seconds without a failure.
- `go vet`, native macOS build, Linux amd64 build and production UI builds pass.
- The repository panel verifier confirms compatibility with the actual sibling dashboard's React export surface. The two Analytics bundles are rebuilt and included.
- `govulncheck` reports **zero reachable vulnerabilities**, zero affected imported packages, and one advisory in a required module whose affected code is not called.

The new browser runner initially exposed a development-JSX/production-React mismatch in its own bundle command. It now uses Bun's production mode, and all scenarios pass. The final suite also fixed an existing time-of-day-dependent money test, a current-millisecond query boundary, stale objective-cache writes after target edits, and an unbucketed legacy-key migration case.

## Finding-by-finding disposition

| Audit | Changes |
|---|---|
| F01 | SQL bindings preserve numbers, booleans, strings and explicit JSON null. Missing properties remain distinct from null. Discovered options retain types; the UI encodes filter selections without string coercion. Scalar lists work end to end. Invalid paths/objects are rejected. |
| F02 | Aggregation reads the previous accumulator before merging incoming properties. Tests cover sum/min/max, initialization, negative values, concurrent updates and retries. |
| F03 | Missing explicit spec IDs fail. HTTP and MCP patches preserve omitted fields; updates use transaction/version checks. Duplicate creates and templates cannot replace existing specs. |
| F04 | All-time windows keep their meaning. Dimension sentinels are encoded separately, allowing literal values such as `all`. |
| F05 | Capture writes a durable local inbox before processing, persists its cursor after successful or intentional outcomes, and retries pending writes. Delivery receipts deduplicate replay. Producer timestamps are retained. Detected gaps/resets and connection failures are visible. **The upstream firehose remains best effort; see limitations below.** |
| F06 | Money aggregation validates the full candidate population. Missing amount, currency or an explicitly configured accounting date fails visibly instead of silently reducing the total. |
| F07 | Target IDs survive edits and reordering. Removed targets are retired, preventing ID reuse. Relational dashboard links supplement the JSON API. Query/period changes invalidate caches, and late measurements cannot restore an old cache revision. |
| F08 | Empty collections serialize as arrays and components handle empty projects. The Home empty-state browser test passes. |
| F09 | Stable delivery IDs distinguish retry from replacement. Corrected raw upserts rebuild affected old/new rollup groups, including min/max and dimension changes. |
| F10 | Aggregate identity uses a versioned hash of a canonical typed tuple, sorted dimensions, timezone and UTC bucket start. Tests cover delimiter collisions and repeated DST hours. Reconstructable legacy keys migrate; ambiguous history is flagged instead of guessed. |
| F11 | Nested outputs and dimensions use full paths and nested JSON setters. |
| F12 | Dry runs and writes share preparation, timestamp/source defaults and policy validation. Preview errors are returned. |
| F13 | Rejected attempts persist bounded, sampled diagnostics without event IDs. Diagnostic storage failures propagate and roll back ingestion. Dry runs do not write diagnostics. |
| F14 | Objectives accept the actual `track`, `auto`, `bus`, `web` and `rollup` source values across Go, MCP and UI. |
| F15 | Observational empty sets return null/no-data, not successful zero. No-data objectives are not achieved. Count and sum retain explicit zero semantics. |
| F16 | Save-time validation rejects unsupported policies, malformed paths, missing required policies and nonfinite operands. Serialization failures propagate. The transformed event is validated in the write transaction. |
| F17 | Examples decode according to their declared types, including numeric/boolean enums. Validation responses mark whether the generated example actually validates and expose errors/violations when prerequisites are missing. |
| F18 | Refresh queues have a maximum wait, one request in flight and a trailing refresh. Capture emits bounded project invalidations. Visible panels also refresh periodically and clear timers on unmount. |
| F19 | Each refresh computes fresh bounds; dashboard batches share a clock and read snapshot. Obsolete responses are ignored, project switches reset drafts/state, and initial preferences no longer override manual dashboard selection. Definitions refresh periodically. |
| F20 | Series use timestamps, explicit gaps and adaptive resolution capped at 1,000 points. Additive gaps are zero; missing observations are null. The overview daily series is also bounded and gap-filled. |
| F21 | Visitor and session identities are separate. Sessions rotate after 30 minutes of inactivity and share active state across tabs. Legacy visitor identity is retained without rewriting historical session counts. |
| F22 | Rejected fetch promises trigger the image fallback with the same event ID. The collector deduplicates both stored events and write-key counters. |
| F23 | Read queries use the SDK read pool with cancellation/deadlines. Dashboard evaluation shares identical widget results and FX indexes within one snapshot. Home evaluates only requested visible widgets. Ingestion uses lean spec reads. See measured performance and remaining optimizations below. |
| F24 | Limits cover request bodies, query strings, event properties, identifiers, capture inbox/messages, widget counts, chart points and limiter entries. Catalog/reference values are paginated. Unknown generic events no longer create a diagnostic row each. Opt-in raw retention uses recoverable archives; diagnostics are sampled/pruned. |
| F25 | Reconnect waiters end with either request or app cancellation. Backoff is cancelable and resets after healthy connections. SSE messages and connection/header setup are bounded. An 80-reconnect test checks goroutine growth. |
| F26 | Reference patches preserve omitted status/metadata; explicit reactivation remains possible. Metadata must be an object. Discovery includes truncation indicators, search and pagination. |
| F27 | The app requires Go 1.26.6 and pins app-sdk v0.75.0, selected by tag topology. Native and Linux builds verify that compiler/pin. A scoped CI workflow enforces builds, race tests, browser tests and vulnerability scanning. **Existing production binaries still require deployment of a new build.** |

## UI and governance changes

- Dashboard create/rename/delete, guided widget query editing, typed property filters and widget deletion.
- Existing-objective editing with preserved target IDs, explicit retirement, required target values, direction, currency and accounting periods.
- Reference-set/value management, inactive-status preservation, search and pagination; FX-rate management with immutable revision IDs.
- Retention settings, capture/database/query diagnostics and restoration by archived event ID.
- Per-project capture policy, origin-allowlist controls and clear best-effort delivery wording.
- Goal directions/errors, Home visibility settings, safe currency formatting and explicit fraction-versus-percentage-point display conventions.
- Default URL query/fragment scrubbing in both the tag and collector. Public keys remain write-only project credentials; allowlists and rate limits are abuse controls, not proof of event provenance.

Money conversion uses exact decimal arithmetic for input/quote conversion, per-event half-away-from-zero minor-unit rounding, overflow checks, direct/inverse precedence and four-decimal CLF/UYW support. Reports identify the immutable FX revisions used. Editing a quote still changes subsequent current reports; no historical-revision selector has been added.

## Performance

Synthetic fixture: 100,000 events in SQLite, one connection, on this Mac. The release audit measured one money widget at 208 ms, six identical widgets at 1.226 s, and latest-per-minute series at 206 ms.

The final isolated run measured **197 ms for one money widget and 197 ms for six identical widgets**, with latest series at 222 ms. An earlier implementation run measured 246 ms for the money scans. Repeated-widget evaluation is roughly five to six times faster in this fixture; individual scans remain linear. This is not a production latency guarantee; compiler versions and run conditions differ from the baseline.

The main contention fix is moving reads away from the single writer and sharing repeated work. A concurrent WAL test verifies that the read snapshot does not block a writer. Ordered latest/change queries still scan their matching population; this work does not claim constant-time analytics. Promoted JSON columns, workload-specific indexes and persistent summaries require real workload measurements before adoption.

## Migrations and remaining boundaries

Migrations 001–010 are unchanged. New migrations 011–014 add capture state/inbox, delivery receipts, target retirement/links, retention/archive tables, FX revisions, no-data measurement state and widget/resource guards. Tests upgrade a populated release-schema database and check foreign-key integrity.

Before a release, use a database backup and rehearse against a copy of the actual install. Startup reconstructs legacy aggregate keys only where identity is recoverable. Ambiguous rows are recorded in `analytics_migration_issues` and further writes to the affected aggregate are blocked. A conflicting legacy-key merge aborts migration rather than combining historical totals speculatively. This may require explicit data repair before an affected install can resume.

Historical overwritten aggregate values and events dropped before this version cannot be recovered from the analytics tables alone. Trustworthy raw records or upstream replay are required. No production data repair was attempted.

The platform firehose is a finite in-memory ring without a reliable producer epoch/durable replay contract. The local inbox protects events after receipt and exposes detected discontinuities, but cannot guarantee recovery of events that never reached Analytics. A complete durable-delivery guarantee requires a platform change outside this app. Capture is labeled best effort rather than claiming otherwise.

Raw-event retention is disabled by default. When enabled, expiry archives eligible raw rows before deleting them; rollup correction inputs and snapshots are retained. Archives expire after their configured period. Restored old events remain subject to that retention policy; adjust the policy before restoring data that must remain live. Delivery receipts and immutable FX history are retained to preserve deduplication and traceability, so total storage is not a fixed-size cache.

PII classifications remain annotations rather than automatic business-field redaction. URL scrubbing is enforced, but arbitrary event properties are not silently rewritten. Public collection cannot establish authenticated provenance from browser-supplied data.

The browser suite uses actual components, React and local app routes with synthetic authorized project fixtures and representative CSS. It does not validate every host dashboard layout, production dataset, browser engine or third-party integration. CI is added but has not run on the remote GitHub service in this task.

## Release dependency check

The SDK advanced after the initial audit validation. The release pins v0.75.0 (a descendant of v0.73.0), including atomic migration execution. Tests and builds were rerun against this pin with `GOWORK=off`.

## Production build verification

Read-only inspection of the deployed Analytics binary found Analytics v0.13.0 built from the audited commit with **Go 1.26.3 and app-sdk v0.67.0**. The newly built local Linux artifact uses **Go 1.26.6 and app-sdk v0.75.0**. The release publishes source, a tag and registry metadata; no production restart or database write is part of publication.

## Reproduce validation

Run from this app directory, with Bun dependencies installed using the checked-in lockfile:

```sh
bun install --frozen-lockfile
GOWORK=off go vet ./...
GOWORK=off go test -race -coverprofile=/tmp/analytics-coverage.out ./...
GOWORK=off go test -run '^$' -fuzz '^FuzzDimensionIdentity$' -fuzztime=15s
bun test ui
bun run typecheck
bun run scripts/build-ui.ts
bun run test:browser
GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@latest ./...
GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/analytics-linux-amd64 .
```

The browser script creates an isolated fixture directory/server, runs Playwright and shuts the server down. On macOS it uses installed Chrome; override with `CHROME_PATH` if needed. On Linux install Playwright Chromium first with `bunx --no-install playwright install --with-deps chromium`.

For host import compatibility, run `scripts/build-panels.ts --app analytics` from the repository root with `APTEVA_DASHBOARD_DIR` pointing to the actual dashboard checkout.

The Analytics validation workflow uploads browser results and Go coverage as a CI artifact. Local validation logs are retained with the audit evidence.
