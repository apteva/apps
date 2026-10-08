-- Per-file recovery is independent of provider-wide rate-limit cooldowns.
CREATE TABLE description_recovery (
 project_id TEXT NOT NULL,
 file_id TEXT NOT NULL,
 source_sha256 TEXT NOT NULL,
 prose_revision INTEGER NOT NULL,
 audience_revision INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'pending',
 next_attempt_at TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '{}',
 PRIMARY KEY(project_id,file_id)
);

CREATE TRIGGER description_recovery_cleanup AFTER DELETE ON media BEGIN
 DELETE FROM description_recovery WHERE project_id=OLD.project_id AND file_id=OLD.file_id;
END;
