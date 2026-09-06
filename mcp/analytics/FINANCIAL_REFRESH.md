# Automatic financial reporting

Analytics 0.15.0 adds migration 015 without recreating objectives,
repaired Billing events, targets, FX history, or existing component snapshots.

## Runtime and durable state

The SDK dispatches `analytics-financial-refresh` every minute in the current
project. Each invocation has a 12-second budget, a 30-second fenced lease, up to
8 target evaluations (each with a 2-second read deadline), 16 component checks, and one optional provider request.
Definition/input changes invalidate all active project targets transactionally.
The worker also reconciles fixed saved periods every five minutes. It does not
advance existing monthly filters or invent next-month objectives.

Failures retain the last successful value and its original measurement time.
Retries use exponential backoff. A new input revision wakes failed targets.
An expired lease can be reclaimed after restart. Persisting a measurement checks
both the input revision and target definition under the write lock. Synchronous
MCP evaluations additionally reject results superseded since their read began.

The retention worker now prunes only the SDK-dispatched project.

## Optional Currencies dependency

Analytics declares `currencies` with `optional: true` and uses
`PlatformAPI().CallAppResult`. Local/EUR identity calculations need no provider.
Automatic ECB history requires the Currencies 0.3.0 or later:
`currencies_sync_now` accepts `provider=ecb-reference-rates`, base, quote, from,
and to and returns immutable observations. A missing/older Currencies install
surfaces a retryable provider error; existing usable manual rates still work.

The Currencies adapter requests at most 31 days from the official ECB EXR API.
Analytics requests a 14-day lookback, imports rates in the correct project, and
links each imported revision to immutable provider observation provenance, and
preserves the existing per-event decimal conversion and rounding policy. ECB rates
retain their approximately 16:00 Brussels publication timestamp; receipt and
accounting dates never see a future publication. TARGET closing days, weekends,
and DST are handled explicitly. Requests and provider provenance are durable;
unchanged imports are no-ops and importer-owned rates cannot overwrite manual
rates. Cross rates require both EUR observations from the same publication.

FX discovery has a 50,000-distinct-timestamp limit per target; exceeding it
surfaces an error requiring a narrower saved period. Large queues are processed
over multiple bounded ticks, with status retained between them.

The SDK's current CallAppResult has no context argument. Analytics bounds the
wait and permits only one outstanding provider call process-wide. A late response
cannot write Analytics state; the underlying SDK transport may take longer to
finish. No network fetch holds an Analytics transaction.

## Sharing and component adoption

Cross-project reads require two explicit project-scoped operator actions:

1. In the source project's Manage section, approve sharing one money target with
   the destination project. Supply a stable income stream key: revenue and its
   settlement payout must use the same key. Copy the returned sharing ID.
2. In the destination, accept that sharing ID for a destination target and an
   **existing component event ID**. This preserves the record's identity, source
   query, timestamp and month filters instead of creating a parallel set.

The gateway requires editor access for writes. Analytics rejects delegated agent
and sibling-app identities when granting consent. The destination worker reads
only the approved target's persisted measurement, checks consent on every use,
and never evaluates an arbitrary other-project event query. Source definitions
are bound to consent; edits require renewed sharing approval. Destination edits
also require re-acceptance. Revocation is immediate for subsequent use and
transactionally invalidates the destination cache. Re-acceptance preserves the
mapping ID. Ordinary Analytics HTTP and MCP reads remain project-scoped.

Periods, timezones and currency must match. Self-components, cycles, duplicate
source targets or income stream keys, a component adopted twice, filtered amount fields and rollup
components are rejected. Only explicitly tracked non-Billing component events
can be adopted; captured events and Billing receipts cannot be rewritten. Failed/stale sources preserve their previous component
with an explicit error. Measurement time and aggregation time are separate.
A mixture of revenue and realized net profit must be described as an operating
goal, not pure revenue.

## Freshness versus completeness

A current measurement only describes data present in Analytics. Best-effort bus
capture cannot establish source completeness. Empty money aggregates remain
unverified until an operator records a completed source reconciliation through a
specific timestamp. That attestation is scoped to the target/input revision,
records the actor and time, and expires on changes or after five minutes for an
open period. Closed reconciled periods can remain confirmed. Unavailable or
unverified zero sources cannot overwrite combined components.

The Objectives tab listens to `objective.progress.updated` and polls every 30
seconds while visible. Manual Refresh queues the same durable worker path. The
Manage section exposes enablement, FX needs, errors/retries, target freshness,
sharing, component adoption and explicit source reconciliation.

## Deployment procedure

After releasing and installing the tested Analytics and Currencies updates:

1. Back up Analytics' DB. Apply migration 015 through normal SDK migrations.
2. Inventory the existing objective/target/component IDs, periods, timezones,
   query filters, values and FX revisions. Do not recreate them.
3. Approve and accept each source mapping in its respective project. Keep
   independent business streams separate, exclude the destination itself, and
   preserve existing target amounts and metric meanings. Revenue and its
   settlement payout must use the same income stream key.
4. Enable optional Currencies binding and ECB import. Missing historical rates
   will be queued for the project containing the affected observations.
5. Run refreshes, resolve errors, and compare source measurements and destination
   components against the preserved records. Verify repeated runs keep IDs and
   totals stable, and test revocation before declaring rollout complete.

No production API key is stored in worker state or configuration. The release
contains no production-specific project IDs or deployed financial configuration.

## Validation

- Analytics: full Go suite with race detector (60.3% statement coverage), plus
  a final focused race run after the last authorization checks.
- Currencies: full Go suite with race detector, including historical CSV parsing,
  December 2025 backfill, immutable replay, failure bounds and cancellation.
- 28 UI unit tests (69 assertions), TypeScript check, and production panel builds.
- 9 Chrome browser scenarios, including an open Objectives tab receiving an
  actual worker notification and the manual action returning a queued response.
- 94,411 fuzz executions of event-dimension identity handling.
- Go vet and Linux amd64 builds for both apps.

The 16 new Analytics regression tests cover mutations, project isolation, failed
FX recovery, manual-rate preservation, immutable corrected quote provenance,
leases/restart recovery, transaction rollback, outdated results/definitions,
publication holidays and DST, source consent/revocation/renewal, component
adoption and cycles, income-stream deduplication, confirmed-zero attestation,
failed persistence notifications, intraday coverage and optional-provider
isolation. Tests use local fixtures; no live payments or production data were
modified.
