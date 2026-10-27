-- Application-owned receipts: no upload URL, token, or caller-supplied age.
CREATE TABLE delivery_upload_operations (
 project_id TEXT NOT NULL,
 target_id INTEGER NOT NULL REFERENCES post_targets(id) ON DELETE CASCADE,
 operation_id TEXT NOT NULL,
 correlation_id TEXT NOT NULL,
 source_attempt INTEGER NOT NULL,
 initialized_at TEXT NOT NULL,
 uploader_state TEXT NOT NULL,
 uploader_stopped_at TEXT,
 provenance TEXT NOT NULL,
 abandoned_at TEXT,
 recovery_id TEXT,
 stop_evidence TEXT NOT NULL DEFAULT '{}',
 evidence TEXT NOT NULL DEFAULT '{}',
 PRIMARY KEY(project_id,target_id,operation_id)
);
