-- Due notices are the worker's own bookkeeping, deliberately kept off the item
-- and release records: writing a marker into their JSON would bump revision and
-- 409 anyone with the panel open, append a history snapshot, and risk tripping
-- the approval-reset rule.
CREATE TABLE editorial_due_notices (
 project_id TEXT NOT NULL,
 topic TEXT NOT NULL,
 ref_id INTEGER NOT NULL,
 -- due_at is part of the key so rescheduling re-arms the notice: a release moved
 -- to a new date is a new row, and moving it back finds the old one already there.
 due_at TEXT NOT NULL,
 fired_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY (project_id, topic, ref_id, due_at)
);
CREATE INDEX editorial_due_notices_project ON editorial_due_notices(project_id, fired_at);

-- One row per project, written on the first scan, so installing the app into a
-- project with a year of back-dated content does not stampede the app bus.
CREATE TABLE editorial_due_state (
 project_id TEXT PRIMARY KEY,
 since TEXT NOT NULL
);
