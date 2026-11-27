-- Scenario evidence, reproducibility and validation.
--
-- causal_basis grades what a relationship's sources actually support:
--   mechanism     — a source explains how the shock would pass (A halts
--                   supply of X to B)
--   asserted      — a source states the relation without explaining it
--   co_occurrence — the entities appear together; no relation is stated
--   unsupported   — the source does not support the relation
--   assumed       — an operator assumption (status assumed)
--   ''            — not graded yet
-- Scenarios propagate along mechanism, asserted and assumed links by
-- default; co_occurrence and ungraded links only when asked, and
-- unsupported links never.
ALTER TABLE intel_relationships ADD COLUMN causal_basis TEXT NOT NULL DEFAULT '';
ALTER TABLE intel_relationships ADD COLUMN causal_note TEXT NOT NULL DEFAULT '';
ALTER TABLE intel_relationships ADD COLUMN graded_by TEXT NOT NULL DEFAULT '';
ALTER TABLE intel_relationships ADD COLUMN graded_at TIMESTAMP;
UPDATE intel_relationships SET causal_basis = 'assumed', graded_by = 'status' WHERE status = 'assumed';

-- A scenario records its full input (spec), the knowledge cutoff it ran
-- with and a hash of the exact graph it used, so it can be replayed and
-- checked. report holds the latest assumption and sensitivity report.
ALTER TABLE intel_scenarios ADD COLUMN known_at TIMESTAMP;
ALTER TABLE intel_scenarios ADD COLUMN snapshot_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE intel_scenarios ADD COLUMN spec TEXT NOT NULL DEFAULT '{}';
ALTER TABLE intel_scenarios ADD COLUMN report TEXT;

-- Historical shock tests: past events replayed with only the links known
-- at the time, compared with later market moves.
CREATE TABLE IF NOT EXISTS intel_scenario_tests (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    params      TEXT    NOT NULL,
    report      TEXT    NOT NULL,
    created_at  TEXT    NOT NULL
);
