PRAGMA foreign_keys = ON;

-- The judge model is part of the sealed benchmark definition and is
-- snapshotted onto each run. Existing packs/runs remain deterministic-only.
ALTER TABLE bench_packs ADD COLUMN judge_model TEXT NOT NULL DEFAULT '';
ALTER TABLE bench_runs ADD COLUMN judge_model TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_bench_runs_judge ON bench_runs(pack_digest, judge_model, created_at);

-- Keep the complete Evals assertion/judge evidence as one additive JSON
-- object so future verdict fields do not require destructive migrations.
ALTER TABLE bench_results ADD COLUMN evaluation_json TEXT NOT NULL DEFAULT '{}';
