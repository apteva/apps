# Processes 0.16.1

Fixes excessive CPU and SQLite write activity in global installations and makes
agent delivery retries durable and safe.

- Every worker respects the SDK-dispatched project instead of rescanning all
  projects for each callback. Reconciliation selects work that needs attention
  rather than loading terminal run history.
- Workflow steps initialize once. Unchanged run, schedule, and synchronization
  state no longer generates repeated writes.
- Complete delivery envelopes and worker provisioning requests are persisted
  before sending. Lost acknowledgements replay identical content and reuse the
  same worker rather than creating another session.
- Permanent HTTP 409 conflicts suspend automatic delivery and remain visible as
  repair required. Migration 013 also suspends known existing content conflicts
  without discarding their diagnostics or assigning replacement event IDs.
- Delivery warnings persist throughout backoff. Temporarily unavailable agents
  recover through due retries; valid acknowledgements must match the executor
  and thread before resolving an ambiguous delivery.
- The app pins the latest published SDK by commit topology, v0.90.0. No SDK or
  Conversations source changes are included.

Existing conflicts still require an operator to compare the original platform
event and establish what executed before repairing the record. The release does
not blindly redeliver old conflicting steps.

## Validation

- Go race suite and regression coverage for ten-project worker dispatch,
  unchanged blocked workflows, lost acknowledgements, permanent conflicts,
  unavailable agents, migration, and lifecycle target validation.
- A file-backed SQLite test performs 1,000 reconciliations and 1,000 worker ticks
  with zero changed rows and zero WAL bytes.
- 53 UI/verifier tests, 19 Playwright tests, TypeScript checks, and bundled panel
  import verification.
- Live gpt-6-sol sequential and browser-continuity scenarios: one persistent
  worker; one browser session opened, reused across three steps, and closed once.
