-- Append-only diagnostics, separate from the compact step handoff.
CREATE TABLE process_step_evidence (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES process_runs(id),
 step_id TEXT NOT NULL REFERENCES process_step_runs(id),
 evidence_key TEXT NOT NULL,
 artifact_id TEXT NOT NULL DEFAULT '',
 content TEXT NOT NULL,
 references_json TEXT NOT NULL,
 sha256 TEXT NOT NULL,
 author TEXT NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(step_id,evidence_key)
);
CREATE INDEX step_evidence_artifact ON process_step_evidence(run_id,step_id,artifact_id,id);
