-- Prospecting v0.1: Web-first candidate discovery and CRM handoff.
--
-- The database intentionally stops at the CRM boundary. Accepted contacts are
-- referenced by crm_contact_id; their canonical fields and relationship
-- history remain owned by CRM.

CREATE TABLE IF NOT EXISTS target_profiles (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id         TEXT    NOT NULL,
    name               TEXT    NOT NULL,
    description        TEXT    NOT NULL DEFAULT '',
    industries_json    TEXT    NOT NULL DEFAULT '[]',
    locations_json     TEXT    NOT NULL DEFAULT '[]',
    employee_min       INTEGER,
    employee_max       INTEGER,
    target_titles_json TEXT    NOT NULL DEFAULT '[]',
    keywords_json      TEXT    NOT NULL DEFAULT '[]',
    status             TEXT    NOT NULL DEFAULT 'active'
                               CHECK(status IN ('active','archived')),
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL,
    archived_at        TEXT,
    UNIQUE(project_id, name)
);

CREATE TABLE IF NOT EXISTS search_runs (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id     TEXT    NOT NULL,
    profile_id     INTEGER NOT NULL REFERENCES target_profiles(id),
    query          TEXT    NOT NULL,
    source         TEXT    NOT NULL DEFAULT 'web',
    status         TEXT    NOT NULL DEFAULT 'running'
                           CHECK(status IN ('running','completed','failed','cancelled')),
    requested_limit INTEGER NOT NULL DEFAULT 20,
    result_count   INTEGER NOT NULL DEFAULT 0,
    error          TEXT    NOT NULL DEFAULT '',
    started_at     TEXT    NOT NULL,
    completed_at   TEXT,
    created_at     TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS candidates (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id         TEXT    NOT NULL,
    profile_id         INTEGER NOT NULL REFERENCES target_profiles(id),
    run_id             INTEGER REFERENCES search_runs(id),
    canonical_key      TEXT    NOT NULL,
    company_name       TEXT    NOT NULL,
    company_domain     TEXT    NOT NULL DEFAULT '',
    website            TEXT    NOT NULL DEFAULT '',
    person_first_name  TEXT    NOT NULL DEFAULT '',
    person_last_name   TEXT    NOT NULL DEFAULT '',
    person_display_name TEXT   NOT NULL DEFAULT '',
    job_title          TEXT    NOT NULL DEFAULT '',
    email              TEXT    NOT NULL DEFAULT '',
    phone              TEXT    NOT NULL DEFAULT '',
    summary            TEXT    NOT NULL DEFAULT '',
    fit_score          INTEGER NOT NULL DEFAULT 0 CHECK(fit_score BETWEEN 0 AND 100),
    confidence_score   INTEGER NOT NULL DEFAULT 0 CHECK(confidence_score BETWEEN 0 AND 100),
    score_reasons_json TEXT    NOT NULL DEFAULT '[]',
    status             TEXT    NOT NULL DEFAULT 'ready'
                               CHECK(status IN ('discovered','researching','ready','deferred','accepted','rejected')),
    source             TEXT    NOT NULL DEFAULT 'manual',
    source_url         TEXT    NOT NULL DEFAULT '',
    decision_reason    TEXT    NOT NULL DEFAULT '',
    crm_contact_id     INTEGER,
    researched_at      TEXT,
    accepted_at        TEXT,
    rejected_at        TEXT,
    deferred_at        TEXT,
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL,
    UNIQUE(project_id, profile_id, canonical_key)
);

CREATE TABLE IF NOT EXISTS candidate_evidence (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    candidate_id INTEGER NOT NULL REFERENCES candidates(id) ON DELETE CASCADE,
    source_kind TEXT    NOT NULL DEFAULT 'web',
    title       TEXT    NOT NULL DEFAULT '',
    url         TEXT    NOT NULL,
    excerpt     TEXT    NOT NULL DEFAULT '',
    artifact_id INTEGER,
    retrieved_at TEXT   NOT NULL,
    UNIQUE(project_id, candidate_id, url)
);

CREATE TABLE IF NOT EXISTS exclusions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK(kind IN ('domain','company','email','phone')),
    value       TEXT    NOT NULL,
    reason      TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL,
    UNIQUE(project_id, kind, value)
);

CREATE TABLE IF NOT EXISTS crm_handoffs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    candidate_id    INTEGER NOT NULL REFERENCES candidates(id),
    crm_contact_id  INTEGER NOT NULL,
    channel_kind    TEXT    NOT NULL,
    channel_value   TEXT    NOT NULL,
    was_created     INTEGER NOT NULL DEFAULT 0,
    activity_warning TEXT   NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL,
    UNIQUE(project_id, candidate_id)
);

CREATE INDEX IF NOT EXISTS idx_profiles_project_status
    ON target_profiles(project_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_project_profile
    ON search_runs(project_id, profile_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_candidates_project_status
    ON candidates(project_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_candidates_project_profile
    ON candidates(project_id, profile_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_evidence_candidate
    ON candidate_evidence(project_id, candidate_id, retrieved_at DESC);
CREATE INDEX IF NOT EXISTS idx_exclusions_project_kind
    ON exclusions(project_id, kind, value);
