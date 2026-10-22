# Processes 0.16.25

Completed, failed and cancelled workflow runs now queue durable worker shutdown
in the same transaction as their terminal state. Independent cleanup stops the
recorded workers through the platform thread API, with retry backoff and restart
recovery. Migration 020 also recovers workers left behind by older terminal runs.
Run receipts, ownership and worker history remain available; main stays alive.
External operations already accepted still require reconciliation before retrying.

A separate durable terminal event notifies the coordinator even if cleanup beats
native `done`. Blocked/waiting worker guidance uses event-only parking, and stale
deliveries and lifecycle callbacks cannot revive or mutate terminal runs.

Validation: three real GPT-6.1 Sol Tier 3 scenarios passed: completed/failed/cancelled
worker cleanup (including an armed wake timer), sequential execution, and continuity
through validation and real HTTP human approval. The full Go race suite, 139 Bun
verifier tests and scenario TypeScript checks passed. Changes are confined to
Processes; Core and Server sources are unchanged.
