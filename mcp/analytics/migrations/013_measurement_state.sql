CREATE TABLE objective_progress_new (
 target_id INTEGER PRIMARY KEY REFERENCES objective_targets(id) ON DELETE CASCADE,
 actual_value REAL, measured_at INTEGER,
 status TEXT NOT NULL CHECK(status IN ('ok','error','no_data')),
 error TEXT NOT NULL DEFAULT '', details_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(details_json)), updated_at INTEGER NOT NULL
);
INSERT INTO objective_progress_new SELECT * FROM objective_progress;
DROP TABLE objective_progress;
ALTER TABLE objective_progress_new RENAME TO objective_progress;
CREATE TABLE analytics_migration_issues (event_id INTEGER PRIMARY KEY, message TEXT NOT NULL, created_at INTEGER NOT NULL);
