-- Resumable bulk keyword metric refreshes. A job is scoped to one Google
-- locale because DataForSEO requires one location/language per task.

CREATE TABLE IF NOT EXISTS keyword_metric_jobs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          TEXT    NOT NULL,
    provider            TEXT    NOT NULL,
    search_engine       TEXT    NOT NULL DEFAULT 'google',
    location_id         INTEGER NOT NULL REFERENCES seo_locations(id),
    status              TEXT    NOT NULL DEFAULT 'pending',
    phase               TEXT    NOT NULL DEFAULT 'queued',
    total_keywords      INTEGER NOT NULL DEFAULT 0,
    completed_keywords  INTEGER NOT NULL DEFAULT 0,
    incomplete_keywords INTEGER NOT NULL DEFAULT 0,
    last_error          TEXT    NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    started_at          INTEGER,
    completed_at        INTEGER,
    updated_at          INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_keyword_metric_jobs_scope
    ON keyword_metric_jobs(project_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS keyword_metric_job_items (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id              INTEGER NOT NULL REFERENCES keyword_metric_jobs(id) ON DELETE CASCADE,
    keyword_id          INTEGER NOT NULL REFERENCES keywords(id) ON DELETE CASCADE,
    metric_snapshot_id  INTEGER REFERENCES keyword_metrics(id) ON DELETE SET NULL,
    volume_done         INTEGER NOT NULL DEFAULT 0,
    difficulty_done     INTEGER NOT NULL DEFAULT 0,
    attempts            INTEGER NOT NULL DEFAULT 0,
    last_error          TEXT    NOT NULL DEFAULT '',
    updated_at          INTEGER NOT NULL,
    UNIQUE(job_id, keyword_id)
);

CREATE INDEX IF NOT EXISTS idx_keyword_metric_job_items_pending
    ON keyword_metric_job_items(job_id, volume_done, difficulty_done, keyword_id);
