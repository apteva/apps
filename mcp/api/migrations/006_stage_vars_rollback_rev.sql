-- Stage variables, rollback-to-previous and an optimistic concurrency guard.
-- vars_json: JSON object of string values substituted into route targets of
--   stage-served requests ({{stage.vars.<key>}}).
-- previous_configuration_id: configuration the stage pointed at before its
--   last promote/rollback (0 = none).
-- rev: bumped on every stage change; callers may pass expected_rev.
ALTER TABLE api_stages ADD COLUMN vars_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE api_stages ADD COLUMN previous_configuration_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE api_stages ADD COLUMN rev INTEGER NOT NULL DEFAULT 1;
