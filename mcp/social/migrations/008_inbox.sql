-- Inbox: comments, DMs, mentions, and reviews pulled from each
-- connected social account. v1 is polling-only — a background worker
-- pages platform APIs on a cadence and upserts here. Webhooks land
-- in a later iteration; the unique constraint below makes both paths
-- idempotent.
--
-- Design notes:
--   - One table, kind-discriminated (comment | dm | mention | review).
--     Replying inserts another row whose parent_external_id walks back
--     to the original; the parent row flips to status='replied'.
--   - project_id is denormalised from social_accounts for the same
--     reason it lives on posts / pending_accounts — list filters by
--     project routinely and a per-account JOIN every read is wasted
--     work.
--   - external_id is the platform-side id (FB comment_id, IG message
--     id, tweet conversation reply id, …). The (account, kind, ext_id)
--     UNIQUE constraint dedups re-polls and protects against double
--     delivery once webhooks land.
--   - post_id is set when the inbox item is a reaction to one of OUR
--     posts (a comment on a post_targets row we own). external_post_id
--     is set when the platform-side post isn't ours (mentions, replies
--     to other users' posts).

CREATE TABLE IF NOT EXISTS inbox_items (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          TEXT    NOT NULL,
    social_account_id   INTEGER NOT NULL,
    platform            TEXT    NOT NULL,        -- twitter | facebook | instagram | ...
    kind                TEXT    NOT NULL,        -- comment | dm | mention | review
    external_id         TEXT    NOT NULL,
    parent_external_id  TEXT,                    -- thread/parent comment id
    post_id             INTEGER,                 -- our post, when this is a reaction to one
    external_post_id    TEXT,                    -- foreign post id (mentions on posts we don't own)
    author_external_id  TEXT,
    author_name         TEXT,
    author_handle       TEXT,
    author_avatar_url   TEXT,
    body                TEXT,
    media_json          TEXT,                    -- JSON: array of {type, url, mime?}
    permalink           TEXT,
    rating              INTEGER,                 -- reviews only (1-5); NULL otherwise
    occurred_at         TIMESTAMP NOT NULL,      -- platform-reported event time
    fetched_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status              TEXT    NOT NULL DEFAULT 'unread', -- unread | read | replied | hidden | archived
    raw_json            TEXT,                    -- raw upstream payload, for debug
    FOREIGN KEY(social_account_id) REFERENCES social_accounts(id),
    FOREIGN KEY(post_id) REFERENCES posts(id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_inbox_dedupe
  ON inbox_items(social_account_id, kind, external_id);
CREATE INDEX IF NOT EXISTS idx_inbox_project_status
  ON inbox_items(project_id, status, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_inbox_account_status
  ON inbox_items(social_account_id, status, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_inbox_parent
  ON inbox_items(parent_external_id);
CREATE INDEX IF NOT EXISTS idx_inbox_post
  ON inbox_items(post_id) WHERE post_id IS NOT NULL;

-- Per-account, per-kind sync cursor. Stored separately from
-- social_accounts so adding new kinds doesn't require an ALTER on the
-- main account row. Value semantics are platform-specific: an "after"
-- token, a last-seen timestamp, etc — the worker is responsible for
-- interpreting its own value.
CREATE TABLE IF NOT EXISTS inbox_cursors (
    social_account_id   INTEGER NOT NULL,
    kind                TEXT    NOT NULL,
    cursor              TEXT    NOT NULL DEFAULT '',
    last_sync_at        TIMESTAMP,
    last_error          TEXT,
    PRIMARY KEY(social_account_id, kind),
    FOREIGN KEY(social_account_id) REFERENCES social_accounts(id)
);
