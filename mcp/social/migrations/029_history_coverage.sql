-- Per-pass listing evidence survives retries independently of imported content.
CREATE TABLE social_history_pass_posts (
 run_id TEXT NOT NULL REFERENCES social_history_runs(id) ON DELETE CASCADE,
 phase TEXT NOT NULL,
 native_post_id TEXT NOT NULL,
 PRIMARY KEY(run_id,phase,native_post_id)
);
CREATE TABLE social_history_pass_pages (
 run_id TEXT NOT NULL REFERENCES social_history_runs(id) ON DELETE CASCADE,
 phase TEXT NOT NULL,
 cursor_hash TEXT NOT NULL,
 listed_count INTEGER NOT NULL,
 PRIMARY KEY(run_id,phase,cursor_hash)
);
