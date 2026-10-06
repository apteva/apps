# GraphQL v0.8.0

Adds domain-agnostic runtime scheduling, optional authorization-safe in-flight
read sharing, bulk resolver reads, bounded upstream snapshot contexts, and
runtime diagnostics in the Logs panel and exported dashboard widget.

- Per-API and per-operation admission limits, bounded queues/queue deadlines,
  waiter limits, cancellable execution, and the existing response/cost/row limits.
  Public execution respects release deadlines rather than a fixed 15-second cap.
- Opt-in query sharing keys canonical documents/coerced variables by immutable
  release, project/API/environment and full verified identity/authorization scope.
  Callers retain separate responses/logs; one cancellation leaves other waiters
  running. Completion removes every flight, including failed/partial executions.
- Extends the request-local loader: compatible Tables point reads become one
  indexed IN query; upstream backends can provide real key-based bulk reads.
- New generic `upstream` source with read/bulk/snapshot-open/snapshot-close tools,
  reusable per-source request snapshots, bounded expiry, and lifecycle cleanup.
- Tables adapter supports native snapshot batches and same-read projection
  metadata in GraphQL `extensions`. Request snapshots require a capable backend;
  Tables currently rejects that guarantee explicitly. There is no distributed
  consistency promise or application-specific freshness/fallback policy.
- Deploy UI exposes sharing, consistency and admission settings. Logs/widget add
  Queued/Shared views, filters and sorting, execution IDs, loader/batch statistics,
  queue/snapshot timings, per-field resolver completion timings and source metadata.
- Additive migration 008 preserves old request logs. SDK remains pinned to v0.95.0,
  the latest published tag on its main release line.

Validation: full Go suite including race checks, vet, source build with GOWORK=off,
real Tables v0.2.8 integration for bulk/snapshot reads, Bun telemetry tests, and
browser checks of controls, filters, details and the exported widget.

See RUNTIME.md for adapter contracts, limits, consistency boundaries and metrics.
This publishes source and marketplace metadata only. It does not deploy, upgrade
or restart production or any running installation.
