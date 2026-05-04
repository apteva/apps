-- torrent v0.1: local torrent registry + saved searches + indexers.
--
-- The bittorrent engine has its own session/state files under
-- working_dir/.engine/ — these tables don't try to mirror them
-- byte-for-byte. They store the metadata we want to query
-- efficiently (state, target folder, what storage rows the
-- completion-mover has produced) and the agent-facing config (saved
-- searches, indexer credentials).

CREATE TABLE IF NOT EXISTS torrents (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id              TEXT    NOT NULL,
    infohash                TEXT    NOT NULL,
    name                    TEXT    NOT NULL DEFAULT '',
    magnet                  TEXT    NOT NULL DEFAULT '',
    target_folder           TEXT    NOT NULL DEFAULT '',  -- storage path
    total_bytes             INTEGER NOT NULL DEFAULT 0,
    downloaded_bytes        INTEGER NOT NULL DEFAULT 0,
    state                   TEXT    NOT NULL DEFAULT 'downloading'
                            CHECK(state IN ('downloading','seeding','paused','completed','error','queued')),
    storage_file_ids_json   TEXT    NOT NULL DEFAULT '[]',
    last_error              TEXT    NOT NULL DEFAULT '',
    added_at                TEXT    NOT NULL,
    completed_at            TEXT,
    UNIQUE(project_id, infohash)
);

CREATE INDEX IF NOT EXISTS idx_torrents_scope_state ON torrents(project_id, state);

CREATE TABLE IF NOT EXISTS saved_searches (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id              TEXT    NOT NULL,
    name                    TEXT    NOT NULL DEFAULT '',
    query                   TEXT    NOT NULL,
    category                TEXT    NOT NULL DEFAULT '',
    min_seeders             INTEGER NOT NULL DEFAULT 1,
    max_size_bytes          INTEGER NOT NULL DEFAULT 0,    -- 0 = no cap
    exclude_terms           TEXT    NOT NULL DEFAULT '',   -- comma-joined
    auto_add_top_n          INTEGER NOT NULL DEFAULT 0,    -- 0 = notify only
    run_interval_minutes    INTEGER NOT NULL DEFAULT 60,
    last_run_at             TEXT,
    next_run_at             TEXT,
    created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_searches_due ON saved_searches(project_id, next_run_at);

CREATE TABLE IF NOT EXISTS indexers (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id              TEXT    NOT NULL,
    name                    TEXT    NOT NULL,
    kind                    TEXT    NOT NULL DEFAULT 'jackett'
                            CHECK(kind IN ('jackett','prowlarr','rss')),
    base_url                TEXT    NOT NULL,
    api_key_enc             TEXT    NOT NULL DEFAULT '',
    categories_json         TEXT    NOT NULL DEFAULT '[]',
    priority                INTEGER NOT NULL DEFAULT 0,
    enabled                 INTEGER NOT NULL DEFAULT 1,
    last_ok_at              TEXT,
    last_error              TEXT    NOT NULL DEFAULT '',
    created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, name)
);

CREATE INDEX IF NOT EXISTS idx_indexers_enabled ON indexers(project_id, enabled, priority);
