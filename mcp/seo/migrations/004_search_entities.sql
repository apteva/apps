-- seo v0.4 -- search-engine-aware entities and SERP snapshots.
--
-- The original v0.3 tables remain the Google/domain compatibility layer.
-- These tables add the generic model used by Google and YouTube first:
-- search_engine + entity + keyword/location + ranked result snapshots.

CREATE TABLE IF NOT EXISTS search_entities (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          TEXT    NOT NULL,
    search_engine       TEXT    NOT NULL DEFAULT 'google', -- google | youtube
    entity_type         TEXT    NOT NULL,                  -- domain | page | channel | video
    identifier          TEXT    NOT NULL,                  -- domain, URL, channel id/handle, video id
    label               TEXT    NOT NULL DEFAULT '',
    url                 TEXT    NOT NULL DEFAULT '',
    default_location_id INTEGER REFERENCES seo_locations(id),
    raw_json            TEXT    NOT NULL DEFAULT '{}',
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, search_engine, entity_type, identifier)
);

CREATE INDEX IF NOT EXISTS idx_search_entities_scope
    ON search_entities(project_id, search_engine, entity_type);

CREATE TABLE IF NOT EXISTS search_serp_snapshots (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    search_engine TEXT    NOT NULL DEFAULT 'google',
    keyword_id    INTEGER REFERENCES keywords(id) ON DELETE SET NULL,
    keyword_text  TEXT    NOT NULL,
    location_id   INTEGER REFERENCES seo_locations(id),
    provider      TEXT    NOT NULL,
    ts            INTEGER NOT NULL,
    raw_json      TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_search_serp_snapshots_lookup
    ON search_serp_snapshots(project_id, search_engine, keyword_text, location_id, ts DESC);

CREATE TABLE IF NOT EXISTS search_serp_results (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id          INTEGER NOT NULL REFERENCES search_serp_snapshots(id) ON DELETE CASCADE,
    entity_id            INTEGER REFERENCES search_entities(id) ON DELETE SET NULL,
    rank                 INTEGER,
    result_type          TEXT    NOT NULL DEFAULT '',
    title                TEXT    NOT NULL DEFAULT '',
    url                  TEXT    NOT NULL DEFAULT '',
    identifier           TEXT    NOT NULL DEFAULT '',
    channel_identifier   TEXT    NOT NULL DEFAULT '',
    channel_title        TEXT    NOT NULL DEFAULT '',
    snippet              TEXT    NOT NULL DEFAULT '',
    published_at         TEXT    NOT NULL DEFAULT '',
    raw_json             TEXT    NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_search_serp_results_entity
    ON search_serp_results(entity_id, rank);

CREATE INDEX IF NOT EXISTS idx_search_serp_results_snapshot
    ON search_serp_results(snapshot_id, rank);

CREATE INDEX IF NOT EXISTS idx_search_serp_results_channel
    ON search_serp_results(channel_identifier, rank);
