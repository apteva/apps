-- Native post workflow and provider/retry metadata.
--
-- posts.status remains the compatibility lifecycle projection consumed by
-- existing panels and agents. approval_status and revision make review
-- independent from delivery while requested_mode records explicit intent.

ALTER TABLE posts ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE posts ADD COLUMN approval_status TEXT NOT NULL DEFAULT 'not_requested';
ALTER TABLE posts ADD COLUMN approved_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE posts ADD COLUMN approval_required INTEGER NOT NULL DEFAULT 0;
ALTER TABLE posts ADD COLUMN rejection_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE posts ADD COLUMN requested_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE posts ADD COLUMN provider_sync_mode TEXT NOT NULL DEFAULT 'local';
ALTER TABLE posts ADD COLUMN source TEXT NOT NULL DEFAULT 'local';
ALTER TABLE posts ADD COLUMN updated_at TIMESTAMP;

ALTER TABLE post_targets ADD COLUMN failure_code TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN retryable INTEGER NOT NULL DEFAULT 1;
ALTER TABLE post_targets ADD COLUMN upstream_status INTEGER NOT NULL DEFAULT 0;
ALTER TABLE post_targets ADD COLUMN existing_post_id TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN provider_sync_status TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN provider_updated_at TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS post_revisions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    actor TEXT NOT NULL DEFAULT 'system',
    snapshot TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(post_id) REFERENCES posts(id) ON DELETE CASCADE,
    UNIQUE(post_id, revision)
);

CREATE INDEX IF NOT EXISTS idx_post_revisions_post
    ON post_revisions(post_id, revision DESC);

CREATE TABLE IF NOT EXISTS post_reviews (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    action TEXT NOT NULL,
    actor TEXT NOT NULL DEFAULT 'system',
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(post_id) REFERENCES posts(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_post_reviews_post
    ON post_reviews(post_id, id DESC);

UPDATE posts
   SET requested_mode = CASE
       WHEN status='draft' THEN 'draft'
       WHEN status='scheduled' THEN 'schedule'
       ELSE 'publish'
   END,
       updated_at = COALESCE(updated_at, created_at);
