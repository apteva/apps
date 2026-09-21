PRAGMA foreign_keys = ON;

-- Rating behavior is snapshotted onto each case so historical suites keep the
-- exact legacy pass/fail contract. New cases may opt into agentic-quality-v2.
ALTER TABLE eval_cases ADD COLUMN rating_profile TEXT NOT NULL DEFAULT '';

-- Outcome is deliberately separate from execution status. A partial or
-- disqualified agent result is still a successfully executed evaluation run.
ALTER TABLE eval_runs ADD COLUMN outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE eval_runs ADD COLUMN quality_score REAL;
