-- dlna v0.1: configuration + a rolling client-access log.
--
-- The bulk of "what's in the library" is computed live from the
-- storage app on every Browse SOAP call — no caching here. The two
-- tables below are:
--
--   published_folders  — allowlist of storage paths exposed to LAN
--                        clients. A folder not in this table (and
--                        publish_root_by_default=false) is invisible.
--   client_log         — per-client browsing history, kept ~24h, for
--                        the panel's "recent clients" list and for
--                        ops debugging ("did the kid's PS5 hit
--                        Movies/ at 9pm again").
--
-- Everything is project-scoped to match the storage / tapo / todo
-- apps' convention.

CREATE TABLE IF NOT EXISTS published_folders (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    folder          TEXT    NOT NULL,             -- storage path, e.g. /movies/kids
    label           TEXT    NOT NULL DEFAULT '',  -- override surfaced to clients
    sort_order      INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, folder)
);

CREATE INDEX IF NOT EXISTS idx_published_scope ON published_folders(project_id);

CREATE TABLE IF NOT EXISTS client_log (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    ip              TEXT    NOT NULL,
    user_agent      TEXT    NOT NULL DEFAULT '',
    last_object_id  TEXT    NOT NULL DEFAULT '',
    last_action_at  TEXT    NOT NULL,             -- RFC3339 UTC
    browse_count    INTEGER NOT NULL DEFAULT 1,
    UNIQUE(project_id, ip, user_agent)
);

CREATE INDEX IF NOT EXISTS idx_client_recent ON client_log(project_id, last_action_at DESC);
