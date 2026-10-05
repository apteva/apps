-- Transport settlement is not evidence that the worker called step_claim.
ALTER TABLE process_step_runs ADD COLUMN claimed_at TEXT NOT NULL DEFAULT '';
ALTER TABLE process_step_runs ADD COLUMN claim_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE process_step_runs ADD COLUMN claim_next_at TEXT NOT NULL DEFAULT '';
ALTER TABLE process_step_runs ADD COLUMN claim_event_id TEXT NOT NULL DEFAULT '';
