-- Prospecting v0.2: deterministic site qualification and enrichment.

ALTER TABLE candidates ADD COLUMN location TEXT NOT NULL DEFAULT '';
ALTER TABLE candidates ADD COLUMN employee_estimate INTEGER;
ALTER TABLE candidates ADD COLUMN location_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE candidates ADD COLUMN eligibility TEXT NOT NULL DEFAULT 'review'
    CHECK(eligibility IN ('eligible','review','ineligible'));
ALTER TABLE candidates ADD COLUMN eligibility_reasons_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE candidates ADD COLUMN automation_signals_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE candidates ADD COLUMN enriched_at TEXT;

CREATE INDEX IF NOT EXISTS idx_candidates_project_eligibility
    ON candidates(project_id, eligibility, fit_score DESC, confidence_score DESC);
