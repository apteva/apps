ALTER TABLE test_suites ADD COLUMN release_config TEXT NOT NULL DEFAULT 'null';
ALTER TABLE test_runs ADD COLUMN release_record TEXT NOT NULL DEFAULT 'null';
ALTER TABLE test_runs ADD COLUMN fixture_spec TEXT NOT NULL DEFAULT 'null';
ALTER TABLE test_runs ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0,1));
ALTER TABLE test_runs ADD COLUMN cleanup_pending INTEGER NOT NULL DEFAULT 0;

CREATE INDEX test_runs_candidate ON test_runs (
 project_id, suite_id, json_extract(release_record, '$.candidate_digest'), id DESC
) WHERE release_record != 'null';

CREATE TABLE test_checks_new (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 suite_id INTEGER NOT NULL,
 name TEXT NOT NULL,
 kind TEXT NOT NULL CHECK (kind IN ('function','http','scenario')),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
 definition TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(project_id, suite_id) REFERENCES test_suites(project_id, id)
);
INSERT INTO test_checks_new SELECT * FROM test_checks;
-- Retain the high-water mark even when the largest check IDs were deleted.
INSERT INTO sqlite_sequence(name, seq)
 SELECT 'test_checks_new', seq FROM sqlite_sequence WHERE name='test_checks'
 AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='test_checks_new');
UPDATE sqlite_sequence SET seq=max(seq, COALESCE(
 (SELECT seq FROM sqlite_sequence WHERE name='test_checks'), 0
)) WHERE name='test_checks_new';
DROP TABLE test_checks;
ALTER TABLE test_checks_new RENAME TO test_checks;
CREATE INDEX test_checks_suite ON test_checks(project_id, suite_id, id);
