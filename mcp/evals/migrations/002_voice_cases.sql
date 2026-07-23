ALTER TABLE eval_cases ADD COLUMN mode TEXT NOT NULL DEFAULT 'text';
ALTER TABLE eval_cases ADD COLUMN voice_json TEXT;
ALTER TABLE eval_runs ADD COLUMN voice_call_json TEXT;
