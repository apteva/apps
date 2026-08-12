PRAGMA foreign_keys = ON;

CREATE TABLE designs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          TEXT NOT NULL,
    name                TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    kind                TEXT NOT NULL DEFAULT 'parametric'
                        CHECK (kind IN ('parametric', 'mesh', 'sketch2d')),
    status              TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'archived')),
    tags_json           TEXT NOT NULL DEFAULT '[]',
    current_revision_id INTEGER,
    created_at          TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_designs_project_updated
    ON designs(project_id, updated_at DESC);

CREATE TABLE design_revisions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    design_id           INTEGER NOT NULL REFERENCES designs(id) ON DELETE CASCADE,
    parent_revision_id  INTEGER REFERENCES design_revisions(id),
    revision_number     INTEGER NOT NULL,
    definition_json     TEXT NOT NULL,
    parameters_json     TEXT NOT NULL DEFAULT '{}',
    source_sha256       TEXT NOT NULL,
    note                TEXT NOT NULL DEFAULT '',
    author              TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(design_id, revision_number)
);

CREATE INDEX idx_design_revisions_design_created
    ON design_revisions(design_id, created_at DESC);

CREATE TABLE build_runs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    design_id           INTEGER NOT NULL REFERENCES designs(id) ON DELETE CASCADE,
    revision_id         INTEGER NOT NULL REFERENCES design_revisions(id) ON DELETE CASCADE,
    status              TEXT NOT NULL CHECK (status IN ('running', 'passed', 'warning', 'failed')),
    engine              TEXT NOT NULL DEFAULT 'replicad-opencascade',
    engine_version      TEXT NOT NULL,
    report_json         TEXT NOT NULL DEFAULT '{}',
    checks_json         TEXT NOT NULL DEFAULT '[]',
    error_text          TEXT NOT NULL DEFAULT '',
    duration_ms         INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at        TEXT
);

CREATE INDEX idx_build_runs_revision_created
    ON build_runs(revision_id, created_at DESC);

CREATE TABLE artifacts (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    design_id           INTEGER NOT NULL REFERENCES designs(id) ON DELETE CASCADE,
    revision_id         INTEGER NOT NULL REFERENCES design_revisions(id) ON DELETE CASCADE,
    build_run_id        INTEGER REFERENCES build_runs(id) ON DELETE SET NULL,
    kind                TEXT NOT NULL,
    format              TEXT NOT NULL,
    name                TEXT NOT NULL,
    content_type        TEXT NOT NULL DEFAULT 'application/octet-stream',
    sha256              TEXT NOT NULL,
    size_bytes          INTEGER NOT NULL,
    storage_file_id     INTEGER,
    local_path          TEXT NOT NULL DEFAULT '',
    metadata_json       TEXT NOT NULL DEFAULT '{}',
    created_at          TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_artifacts_revision_created
    ON artifacts(revision_id, created_at DESC);

CREATE UNIQUE INDEX idx_artifacts_revision_format_sha
    ON artifacts(revision_id, format, sha256);
