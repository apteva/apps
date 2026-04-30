-- social v0.1 — accounts + posts + per-platform target rows.
--
-- Design notes:
--   - social_accounts is the app's own concept; one row per "thing the
--     user can post AS" (a Twitter handle, a Facebook Page, an IG
--     business account, a YouTube channel). Multiple rows can share a
--     connection_id when one OAuth grant covers multiple destinations
--     (the FB token that grants access to 5 Pages produces 5 rows).
--   - pending_accounts holds OAuth-in-flight rows. The platform's
--     OAuth callback writes conn_id back here; the panel/agent calls
--     account_finalize to graduate one to social_accounts.
--   - posts is the user's intent ("publish this body to N accounts").
--   - post_targets is the per-account result row, one per fanout. Jobs
--     drive these to terminal state independently so a TikTok failure
--     doesn't block X.

CREATE TABLE IF NOT EXISTS pending_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    integration_slug TEXT NOT NULL,           -- "twitter-api", "facebook-graph", ...
    connection_id INTEGER NOT NULL DEFAULT 0, -- set when OAuth completes
    status TEXT NOT NULL,                     -- pending_oauth | ready | finalized | expired
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_pending_project ON pending_accounts(project_id, status);
CREATE INDEX IF NOT EXISTS idx_pending_conn ON pending_accounts(connection_id);

CREATE TABLE IF NOT EXISTS social_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    platform TEXT NOT NULL,                  -- twitter | facebook | instagram | linkedin | tiktok | youtube | reddit | pinterest | threads
    connection_id INTEGER NOT NULL,
    external_account_id TEXT,                -- FB page_id, IG ig_user_id, YT channel_id; NULL for personal Twitter/LinkedIn
    display_name TEXT NOT NULL,
    avatar_url TEXT,
    status TEXT NOT NULL DEFAULT 'active',   -- active | needs_reauth | disconnected
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_accounts_project ON social_accounts(project_id, status);
CREATE INDEX IF NOT EXISTS idx_accounts_conn ON social_accounts(connection_id);

CREATE TABLE IF NOT EXISTS posts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    body TEXT NOT NULL,
    media_storage_ids TEXT,                  -- JSON array of storage file ids
    schedule_at TIMESTAMP,                   -- NULL = publish now
    status TEXT NOT NULL DEFAULT 'draft',    -- draft | scheduled | publishing | partial | published | failed
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_posts_project ON posts(project_id, status);
CREATE INDEX IF NOT EXISTS idx_posts_schedule ON posts(schedule_at) WHERE status = 'scheduled';

CREATE TABLE IF NOT EXISTS post_targets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id INTEGER NOT NULL,
    social_account_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',  -- pending | publishing | published | failed
    platform_post_id TEXT,
    platform_url TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    last_attempt_at TIMESTAMP,
    published_at TIMESTAMP,
    FOREIGN KEY(post_id) REFERENCES posts(id),
    FOREIGN KEY(social_account_id) REFERENCES social_accounts(id)
);

CREATE INDEX IF NOT EXISTS idx_targets_post ON post_targets(post_id);
CREATE INDEX IF NOT EXISTS idx_targets_status ON post_targets(status);
