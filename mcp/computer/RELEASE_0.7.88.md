# Computer v0.7.88

Reliability and efficiency fixes for shared browser control, with expanded
model-driven Patreon regression coverage.

- Stable identity for text, date/time, selection, and upload targets when pages
  move or replace controls.
- Native checkbox/radio activation that updates framework state and remains
  idempotent; explicit checked/mixed/current-value observations.
- Occlusion-aware controls and scroll panels, plus bounded offscreen control
  hints to reduce navigation loops.
- Real tab activation, bounded browser operations, request cancellation,
  session serialization, and durable provider cleanup retries.
- Metadata-only history reads, fewer unnecessary screenshots, and coordinated
  UI refreshes. No additional user workflow steps.
- Pin app-sdk v0.74.1, the latest tag by commit topology, as required by the
  workspace release policy.
- Correct upload/dropdown handling, download identity, proxy credential scoping,
  context policy, settings updates, and reproducible UI dependencies.

## Validation

The preceding audit and live-browser runs passed all 13 top-level LLM cases
across full runs and focused reruns, along with unit/race, local browser,
UI, build, and static checks. See AUDIT_FIXES.md and PATREON_TIER3_RESULTS.md
for observations and the initial failures that led to the fixes.

This release expands TestLLMPatreonSchedulingLive with a scheduled_publication
phase: the model commits Schedule; independent assertions require the unique
post ID, exact title field value, and scheduled date/time, then reload and
verify them again. URL slugs may change without changing post identity.
The provider session must also survive beyond five minutes and close cleanly.
Release validation passed on SDK v0.74.1:

- Complete unit/race and tier 2 browser suites; build and static analysis.
- Five UI tests, strict type checking, rebuilt panels, frozen lockfile, and
  shared frontend dependency tests.
- All 13 top-level LLM cases across the full run and focused reruns.
- Final scheduling: nine preparation actions, one Schedule click, dismissal
  of the success dialog, and reopening the scheduled post. The immutable post
  ID, exact title, and September 19, 2026 at 7:00 PM schedule all matched after
  reload. The separate lifetime subtest passed at 377 seconds; sessions closed.

The test harness was corrected to read textarea values, observe offscreen
fields without changing them, allow cosmetic URL slugs, and retain bounded
field observations in model history. Failed discovery attempts remain in the
local evidence alongside successful reruns.

Primary logs: release-sdk-tier1.log, release-sdk-tier2.log,
release-sdk-tier3.log, release-final-schedule.log, release-history-fixture.log,
release-final-unit.log, and release-ui.log in the Computer audit evidence folder.

The scheduling assertion verifies that Patreon accepted and persisted the
future publication schedule.
