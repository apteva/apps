CREATE TABLE test_results_code (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 run_id INTEGER NOT NULL,
 check_id INTEGER NOT NULL,
 name TEXT NOT NULL,
 kind TEXT NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('passed','failed','error','blocked','flaky')),
 duration_ms INTEGER NOT NULL,
 output TEXT NOT NULL,
 assertions TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(project_id, run_id) REFERENCES test_runs(project_id, id),
 UNIQUE(run_id, check_id)
);
INSERT INTO test_results_code SELECT * FROM test_results;
INSERT INTO sqlite_sequence(name,seq)
 SELECT 'test_results_code',seq FROM sqlite_sequence WHERE name='test_results'
 AND NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='test_results_code');
DROP TABLE test_results;
ALTER TABLE test_results_code RENAME TO test_results;

CREATE TABLE test_checks_code (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 suite_id INTEGER NOT NULL,
 name TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('function','http','scenario','browser','code')),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 definition TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(project_id,suite_id) REFERENCES test_suites(project_id,id)
);
INSERT INTO test_checks_code SELECT * FROM test_checks;
INSERT INTO sqlite_sequence(name,seq)
 SELECT 'test_checks_code',seq FROM sqlite_sequence WHERE name='test_checks'
 AND NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='test_checks_code');
UPDATE sqlite_sequence SET seq=max(seq,COALESCE((SELECT seq FROM sqlite_sequence WHERE name='test_checks'),0)) WHERE name='test_checks_code';
DROP TABLE test_checks;
ALTER TABLE test_checks_code RENAME TO test_checks;
CREATE INDEX test_checks_suite ON test_checks(project_id,suite_id,id);

-- One row per test case a code check reported.
CREATE TABLE test_cases (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 suite_id INTEGER NOT NULL,
 run_id INTEGER NOT NULL,
 check_id INTEGER NOT NULL,
 case_key TEXT NOT NULL,
 file TEXT NOT NULL DEFAULT '',
 suite TEXT NOT NULL DEFAULT '',
 name TEXT NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('passed','failed','error','skipped','flaky')),
 quarantined INTEGER NOT NULL DEFAULT 0,
 duration_ms INTEGER NOT NULL DEFAULT 0,
 message TEXT NOT NULL DEFAULT '',
 stack TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 FOREIGN KEY(project_id,run_id) REFERENCES test_runs(project_id,id)
);
CREATE INDEX test_cases_run ON test_cases(project_id,run_id,check_id,status,id);
CREATE INDEX test_cases_key ON test_cases(project_id,suite_id,case_key,id DESC);
CREATE INDEX test_cases_created ON test_cases(created_at);

-- Rolling per-case history across runs of a suite.
CREATE TABLE test_case_history (
 project_id TEXT NOT NULL,
 suite_id INTEGER NOT NULL,
 case_key TEXT NOT NULL,
 file TEXT NOT NULL DEFAULT '',
 name TEXT NOT NULL,
 runs INTEGER NOT NULL DEFAULT 0,
 failures INTEGER NOT NULL DEFAULT 0,
 flaky INTEGER NOT NULL DEFAULT 0,
 recent TEXT NOT NULL DEFAULT '',
 last_status TEXT NOT NULL DEFAULT '',
 last_run_id INTEGER NOT NULL DEFAULT 0,
 last_failure_at TEXT NOT NULL DEFAULT '',
 last_failure_message TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,suite_id,case_key)
);

CREATE TABLE test_quarantine (
 project_id TEXT NOT NULL,
 suite_id INTEGER NOT NULL,
 case_key TEXT NOT NULL,
 reason TEXT NOT NULL,
 added_by TEXT NOT NULL DEFAULT '',
 expires_at TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,suite_id,case_key)
);
