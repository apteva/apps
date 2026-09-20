-- Processes 0.15: retain the source-event id for the current step delivery.
-- A step may first wake the agent's main thread and then be reassigned to an
-- already-created worker. Each target needs its own tracked event id so the
-- platform's idempotency ledger does not suppress the worker wake.
ALTER TABLE process_step_runs ADD COLUMN delivery_event_id TEXT NOT NULL DEFAULT '';
