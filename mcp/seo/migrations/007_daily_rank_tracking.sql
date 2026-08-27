-- seo v0.6 -- automated, budgeted SERP rank tracking.
--
-- Full SERP snapshots remain bounded by the existing retention policy. These
-- tables keep a compact observation for every tracked target and day forever,
-- including an explicit row when the target was not found in the checked
-- depth. DataForSEO scheduled checks use its cheaper Standard Queue.

CREATE TABLE IF NOT EXISTS serp_tracking_settings (
    project_id          TEXT PRIMARY KEY,
    enabled             INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    monthly_budget_usd  REAL    NOT NULL DEFAULT 5.0 CHECK (monthly_budget_usd >= 0),
    daily_depth         INTEGER NOT NULL DEFAULT 20 CHECK (daily_depth BETWEEN 10 AND 100),
    weekly_depth        INTEGER NOT NULL DEFAULT 100 CHECK (weekly_depth BETWEEN 10 AND 100),
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS serp_trackers (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          TEXT    NOT NULL,
    keyword_id          INTEGER NOT NULL REFERENCES keywords(id) ON DELETE CASCADE,
    entity_id           INTEGER NOT NULL REFERENCES search_entities(id) ON DELETE CASCADE,
    provider            TEXT    NOT NULL DEFAULT 'dataforseo',
    device              TEXT    NOT NULL DEFAULT 'desktop' CHECK (device IN ('desktop', 'mobile')),
    enabled             INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    daily_depth         INTEGER NOT NULL DEFAULT 20 CHECK (daily_depth BETWEEN 10 AND 100),
    weekly_depth        INTEGER NOT NULL DEFAULT 100 CHECK (weekly_depth BETWEEN 10 AND 100),
    next_run_at         INTEGER NOT NULL DEFAULT 0,
    last_attempt_at     INTEGER,
    last_success_at     INTEGER,
    last_error          TEXT    NOT NULL DEFAULT '',
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, keyword_id, entity_id, provider, device)
);

CREATE INDEX IF NOT EXISTS idx_serp_trackers_due
    ON serp_trackers(project_id, enabled, next_run_at);

CREATE TABLE IF NOT EXISTS serp_refresh_jobs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          TEXT    NOT NULL,
    keyword_id          INTEGER NOT NULL REFERENCES keywords(id) ON DELETE CASCADE,
    location_id         INTEGER NOT NULL REFERENCES seo_locations(id),
    search_engine       TEXT    NOT NULL DEFAULT 'google',
    provider            TEXT    NOT NULL,
    device              TEXT    NOT NULL DEFAULT 'desktop',
    depth               INTEGER NOT NULL,
    observed_date       TEXT    NOT NULL,
    status              TEXT    NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending', 'submitted', 'complete', 'failed', 'budget_blocked')),
    provider_task_id    TEXT    NOT NULL DEFAULT '',
    estimated_cost_usd  REAL    NOT NULL DEFAULT 0,
    actual_cost_usd     REAL    NOT NULL DEFAULT 0,
    attempts            INTEGER NOT NULL DEFAULT 0,
    next_attempt_at     INTEGER NOT NULL DEFAULT 0,
    submitted_at        INTEGER,
    completed_at        INTEGER,
    snapshot_id         INTEGER REFERENCES search_serp_snapshots(id) ON DELETE SET NULL,
    last_error          TEXT    NOT NULL DEFAULT '',
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, keyword_id, location_id, provider, device, observed_date)
);

CREATE INDEX IF NOT EXISTS idx_serp_refresh_jobs_work
    ON serp_refresh_jobs(project_id, status, next_attempt_at);

CREATE TABLE IF NOT EXISTS serp_rank_observations (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          TEXT    NOT NULL,
    tracker_id          INTEGER NOT NULL REFERENCES serp_trackers(id) ON DELETE CASCADE,
    job_id              INTEGER REFERENCES serp_refresh_jobs(id) ON DELETE SET NULL,
    snapshot_id         INTEGER REFERENCES search_serp_snapshots(id) ON DELETE SET NULL,
    observed_date       TEXT    NOT NULL,
    ts                  INTEGER NOT NULL,
    found               INTEGER NOT NULL CHECK (found IN (0, 1)),
    rank                INTEGER,
    rank_url            TEXT    NOT NULL DEFAULT '',
    checked_depth       INTEGER NOT NULL,
    provider            TEXT    NOT NULL,
    UNIQUE(tracker_id, observed_date)
);

CREATE INDEX IF NOT EXISTS idx_serp_rank_observations_history
    ON serp_rank_observations(project_id, tracker_id, observed_date DESC);
