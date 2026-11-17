ALTER TABLE test_suites ADD COLUMN suite_config TEXT NOT NULL DEFAULT '{}';
ALTER TABLE test_runs ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0;
CREATE TABLE test_results_blocked (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 run_id INTEGER NOT NULL,
 check_id INTEGER NOT NULL,
 name TEXT NOT NULL,
 kind TEXT NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('passed','failed','error','blocked')),
 duration_ms INTEGER NOT NULL,
 output TEXT NOT NULL,
 assertions TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(project_id, run_id) REFERENCES test_runs(project_id, id),
 UNIQUE(run_id, check_id)
);
INSERT INTO test_results_blocked SELECT * FROM test_results;
INSERT INTO sqlite_sequence(name,seq)
 SELECT 'test_results_blocked',seq FROM sqlite_sequence WHERE name='test_results'
 AND NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='test_results_blocked');
DROP TABLE test_results;
ALTER TABLE test_results_blocked RENAME TO test_results;
