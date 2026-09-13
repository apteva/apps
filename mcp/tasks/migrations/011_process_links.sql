CREATE TABLE process_task_links (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id),
 install_id INTEGER NOT NULL,
 project_id TEXT NOT NULL,
 process_id TEXT NOT NULL,
 version INTEGER NOT NULL,
 run_key TEXT NOT NULL,
 UNIQUE(install_id, project_id, run_key)
);
CREATE INDEX process_task_lookup ON process_task_links(install_id, project_id, process_id);
