-- Native observations are separate from managed delivery state and intent.
CREATE TABLE social_native_posts (
 project_id TEXT NOT NULL,
 social_account_id INTEGER NOT NULL REFERENCES social_accounts(id) ON DELETE CASCADE,
 native_post_id TEXT NOT NULL,
 post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
 target_id INTEGER NOT NULL REFERENCES post_targets(id) ON DELETE CASCADE,
 metadata TEXT NOT NULL,
 last_known_metadata TEXT NOT NULL,
 content_hash TEXT NOT NULL,
 observed_at TEXT NOT NULL,
 PRIMARY KEY(project_id,social_account_id,native_post_id)
);
CREATE INDEX idx_native_post_target ON social_native_posts(target_id);
CREATE TABLE social_history_runs (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 social_account_id INTEGER NOT NULL REFERENCES social_accounts(id) ON DELETE CASCADE,
 binding TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_history_account ON social_history_runs(project_id,social_account_id,updated_at);
CREATE TABLE social_history_seen (
 run_id TEXT NOT NULL REFERENCES social_history_runs(id) ON DELETE CASCADE,
 native_post_id TEXT NOT NULL,
 imported INTEGER NOT NULL DEFAULT 0,
 updated INTEGER NOT NULL DEFAULT 0,
 unchanged INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(run_id,native_post_id)
);
-- A connection can be reauthorized, but a Social account must not silently
-- become a different native owner (including legacy rows without an owner ID).
CREATE TABLE social_history_owners (
 project_id TEXT NOT NULL,
 social_account_id INTEGER NOT NULL REFERENCES social_accounts(id) ON DELETE CASCADE,
 platform TEXT NOT NULL,
 native_owner_id TEXT NOT NULL,
 observed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(project_id,social_account_id)
);
