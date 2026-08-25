CREATE TABLE IF NOT EXISTS currency_definitions (
    code          TEXT PRIMARY KEY,
    numeric_code  TEXT,
    name          TEXT NOT NULL,
    minor_units   INTEGER,
    kind          TEXT NOT NULL DEFAULT 'fiat',
    active        INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    data_version  TEXT NOT NULL,
    updated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (length(code) = 3),
    CHECK (minor_units IS NULL OR minor_units BETWEEN 0 AND 9),
    CHECK (kind IN ('fiat','fund','metal','special'))
);

CREATE TABLE IF NOT EXISTS tracked_pairs (
    project_id       TEXT NOT NULL,
    base             TEXT NOT NULL,
    quote            TEXT NOT NULL,
    enabled          INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    requested_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_refresh_at  TEXT,
    last_error       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (project_id, base, quote),
    CHECK (length(base) = 3 AND length(quote) = 3 AND base <> quote)
);

CREATE TABLE IF NOT EXISTS rate_observations (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id            TEXT NOT NULL,
    base                  TEXT NOT NULL,
    quote                 TEXT NOT NULL,
    rate_text             TEXT NOT NULL,
    rate_kind             TEXT NOT NULL,
    effective_at          TEXT NOT NULL,
    effective_date        TEXT NOT NULL,
    granularity           TEXT NOT NULL DEFAULT 'instant',
    observed_at           TEXT NOT NULL,
    ingested_at           TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    provider_slug         TEXT NOT NULL,
    connection_id         INTEGER,
    provider_ref          TEXT NOT NULL DEFAULT '',
    original_base         TEXT NOT NULL,
    original_quote        TEXT NOT NULL,
    payload_hash          TEXT NOT NULL DEFAULT '',
    adapter_version       TEXT NOT NULL DEFAULT 'v1',
    quality_flags_json    TEXT NOT NULL DEFAULT '[]',
    supersedes_rate_id    INTEGER REFERENCES rate_observations(id),
    CHECK (length(base) = 3 AND length(quote) = 3 AND base <> quote),
    CHECK (rate_kind IN ('spot','reference','open','high','low','close','manual')),
    CHECK (granularity IN ('instant','day')),
    UNIQUE (
      project_id, provider_slug, base, quote, rate_kind,
      effective_at, rate_text
    )
);

CREATE INDEX IF NOT EXISTS idx_rates_pair_time
    ON rate_observations(project_id, base, quote, effective_at DESC);
CREATE INDEX IF NOT EXISTS idx_rates_provider_time
    ON rate_observations(project_id, provider_slug, effective_at DESC);

CREATE TABLE IF NOT EXISTS provider_health (
    project_id       TEXT NOT NULL,
    connection_id    INTEGER NOT NULL,
    provider_slug    TEXT NOT NULL,
    priority         INTEGER NOT NULL DEFAULT 100,
    enabled          INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    last_attempt_at  TEXT,
    last_success_at  TEXT,
    last_error       TEXT NOT NULL DEFAULT '',
    failure_count    INTEGER NOT NULL DEFAULT 0,
    updated_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, connection_id)
);

CREATE TABLE IF NOT EXISTS sync_runs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       TEXT NOT NULL,
    provider_slug    TEXT NOT NULL,
    connection_id    INTEGER,
    base             TEXT NOT NULL,
    quote            TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('running','completed','failed')),
    observations     INTEGER NOT NULL DEFAULT 0,
    error            TEXT NOT NULL DEFAULT '',
    started_at       TEXT NOT NULL,
    completed_at     TEXT
);

CREATE TABLE IF NOT EXISTS manual_rate_audit (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       TEXT NOT NULL,
    rate_id          INTEGER NOT NULL REFERENCES rate_observations(id),
    reason           TEXT NOT NULL,
    source_ref       TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
