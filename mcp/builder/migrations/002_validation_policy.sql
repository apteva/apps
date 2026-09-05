ALTER TABLE builder_goals
    ADD COLUMN validation_mode TEXT NOT NULL DEFAULT 'build_only'
    CHECK (validation_mode IN ('build_only','simulated','continuous'));

ALTER TABLE builder_goals
    ADD COLUMN validation_policy_json TEXT NOT NULL DEFAULT '{}';
