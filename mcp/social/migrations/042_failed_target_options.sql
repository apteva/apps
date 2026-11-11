-- Corrections do not revise the parent publication or its successful siblings.
ALTER TABLE post_targets ADD COLUMN options_revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE post_targets ADD COLUMN retry_target_only INTEGER NOT NULL DEFAULT 0;
CREATE TABLE target_options_corrections (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 target_id INTEGER NOT NULL REFERENCES post_targets(id) ON DELETE CASCADE,
 post_revision INTEGER NOT NULL,
 options_revision INTEGER NOT NULL,
 source_attempt INTEGER NOT NULL,
 previous_options TEXT NOT NULL,
 corrected_options TEXT NOT NULL,
 previous_failure TEXT NOT NULL,
 actor TEXT NOT NULL,
 reason TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(target_id,options_revision)
);
