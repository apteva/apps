CREATE TABLE test_browser_leases (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 run_id INTEGER NOT NULL,
 check_id INTEGER NOT NULL,
 closed INTEGER NOT NULL DEFAULT 0,
 FOREIGN KEY(project_id,run_id) REFERENCES test_runs(project_id,id)
);
CREATE TABLE test_artifacts (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 run_id INTEGER NOT NULL,
 check_id INTEGER NOT NULL,
 step_id TEXT NOT NULL,
 mime TEXT NOT NULL,
 digest TEXT NOT NULL,
 bytes BLOB NOT NULL,
 created_at TEXT NOT NULL,
 FOREIGN KEY(project_id,run_id) REFERENCES test_runs(project_id,id)
);
CREATE INDEX test_artifacts_run ON test_artifacts(project_id,run_id,id);
CREATE INDEX test_artifacts_retention ON test_artifacts(created_at);
CREATE TABLE test_checks_browser (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 suite_id INTEGER NOT NULL,
 name TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('function','http','scenario','browser')),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 definition TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(project_id,suite_id) REFERENCES test_suites(project_id,id)
);
INSERT INTO test_checks_browser SELECT * FROM test_checks;
INSERT INTO sqlite_sequence(name,seq)
 SELECT 'test_checks_browser',seq FROM sqlite_sequence WHERE name='test_checks'
 AND NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='test_checks_browser');
UPDATE sqlite_sequence SET seq=max(seq,COALESCE((SELECT seq FROM sqlite_sequence WHERE name='test_checks'),0)) WHERE name='test_checks_browser';
DROP TABLE test_checks;
ALTER TABLE test_checks_browser RENAME TO test_checks;
CREATE INDEX test_checks_suite ON test_checks(project_id,suite_id,id);
