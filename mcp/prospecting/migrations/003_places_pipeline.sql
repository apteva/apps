ALTER TABLE candidates ADD COLUMN edit_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE candidates ADD COLUMN operator_fields_json TEXT NOT NULL DEFAULT '[]';
CREATE TABLE prospecting_settings (
    project_id TEXT PRIMARY KEY,
    places_connection_id INTEGER NOT NULL DEFAULT 0,
    daily_places_request_limit INTEGER NOT NULL DEFAULT 100
);
CREATE TABLE places_usage (
    project_id TEXT NOT NULL,
    day TEXT NOT NULL,
    requests INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(project_id, day)
);
CREATE TABLE candidate_places (
    project_id TEXT NOT NULL,
    profile_id INTEGER NOT NULL,
    place_id TEXT NOT NULL,
    candidate_id INTEGER NOT NULL REFERENCES candidates(id) ON DELETE CASCADE,
    details_json TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    PRIMARY KEY(project_id, profile_id, place_id),
    UNIQUE(project_id, candidate_id)
);
CREATE TABLE prospecting_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    profile_id INTEGER NOT NULL REFERENCES target_profiles(id),
    idempotency_key TEXT NOT NULL,
    options_json TEXT NOT NULL,
    state_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'queued',
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(project_id, idempotency_key)
);
CREATE TABLE prospecting_job_items (
    run_id INTEGER NOT NULL REFERENCES prospecting_jobs(id) ON DELETE CASCADE,
    source_key TEXT NOT NULL,
    candidate_id INTEGER REFERENCES candidates(id) ON DELETE SET NULL,
    was_created INTEGER NOT NULL DEFAULT 0,
    stage TEXT NOT NULL DEFAULT 'qualify',
    status TEXT NOT NULL DEFAULT 'pending',
    reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(run_id, source_key)
);
CREATE INDEX idx_prospecting_jobs_pending ON prospecting_jobs(project_id,status,lease_until,id);
