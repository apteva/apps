CREATE TABLE validation_suites (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 portfolio_id INTEGER NOT NULL REFERENCES portfolios(id),
 source_run_id INTEGER NOT NULL REFERENCES backtest_runs(id),
 name TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'queued',
 spec_json TEXT NOT NULL,
 source_json TEXT NOT NULL,
 plan_json TEXT NOT NULL,
 plan_sha256 TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE validation_cases (
 suite_id INTEGER NOT NULL REFERENCES validation_suites(id) ON DELETE CASCADE,
 ordinal INTEGER NOT NULL,
 run_id INTEGER UNIQUE REFERENCES backtest_runs(id),
 selected_candidate INTEGER NOT NULL DEFAULT -1,
 PRIMARY KEY(suite_id, ordinal)
);
CREATE INDEX validation_suites_project ON validation_suites(project_id, id);
