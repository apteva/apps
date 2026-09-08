CREATE TABLE test_suites (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 name TEXT NOT NULL,
 description TEXT NOT NULL DEFAULT '',
 environment TEXT NOT NULL DEFAULT 'test',
 archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0,1)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(project_id, id)
);
CREATE INDEX test_suites_project ON test_suites(project_id, archived, id);
CREATE TABLE test_checks (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 suite_id INTEGER NOT NULL,
 name TEXT NOT NULL,
 kind TEXT NOT NULL CHECK (kind IN ('function','http')),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
 definition TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(project_id, suite_id) REFERENCES test_suites(project_id, id)
);
CREATE INDEX test_checks_suite ON test_checks(project_id, suite_id, id);
CREATE TABLE test_runs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 suite_id INTEGER NOT NULL,
 suite_name TEXT NOT NULL,
 environment TEXT NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('queued','running','passed','failed')),
 trigger TEXT NOT NULL,
 request_key TEXT,
 snapshot TEXT NOT NULL,
 passed INTEGER NOT NULL DEFAULT 0,
 failed INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 started_at TEXT NOT NULL DEFAULT '',
 finished_at TEXT NOT NULL DEFAULT '',
 UNIQUE(project_id, request_key),
 UNIQUE(project_id, id),
 FOREIGN KEY(project_id, suite_id) REFERENCES test_suites(project_id, id)
);
CREATE INDEX test_runs_queue ON test_runs(project_id, status, id);
CREATE INDEX test_runs_suite ON test_runs(project_id, suite_id, id);
CREATE TABLE test_results (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 run_id INTEGER NOT NULL,
 check_id INTEGER NOT NULL,
 name TEXT NOT NULL,
 kind TEXT NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('passed','failed','error')),
 duration_ms INTEGER NOT NULL,
 output TEXT NOT NULL,
 assertions TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(project_id, run_id) REFERENCES test_runs(project_id, id),
 UNIQUE(run_id, check_id)
);
CREATE TABLE test_outbox (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 topic TEXT NOT NULL,
 payload TEXT NOT NULL
);
