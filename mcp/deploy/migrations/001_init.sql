-- Apteva Deploy v0.1 — local build + runtime supervision.
--
-- Five tables:
--   deployments    — one per (project, name); pluggable source spec
--   builds         — every build attempt; artifacts on disk
--   releases       — a build promoted to runnable; supervised process
--   release_events — audit log of state transitions per release
--   port_leases    — claims on local ports (avoids races in the supervisor)
--
-- Source + framework are stored as text so future kinds (git, zip,
-- node, python, …) plug in without a schema change.

CREATE TABLE deployments (
    id                  INTEGER PRIMARY KEY,
    project_id          TEXT    NOT NULL,
    name                TEXT    NOT NULL,                  -- url-safe; unique within project
    description         TEXT    NOT NULL DEFAULT '',

    -- Source spec — pluggable.
    source_kind         TEXT    NOT NULL,                  -- 'code' | 'local' | 'git' | 'zip'
    source_ref          TEXT    NOT NULL DEFAULT '',       -- code: <slug>; local: <path>; git: <url>; zip: <upload_id>
    source_extra_json   TEXT    NOT NULL DEFAULT '{}',     -- e.g. {"git_ref": "main"}

    -- Build + runtime hints (overrides for framework defaults).
    framework           TEXT    NOT NULL DEFAULT '',       -- '' = auto-detect from source
    build_cmd           TEXT    NOT NULL DEFAULT '',
    start_cmd           TEXT    NOT NULL DEFAULT '',
    port_hint           INTEGER NOT NULL DEFAULT 0,        -- 0 = auto-allocate
    env_json            TEXT    NOT NULL DEFAULT '{}',

    domain              TEXT    NOT NULL DEFAULT '',       -- '' = use Apteva's /_deploy/<name>/

    current_release_id  INTEGER,                           -- FK -> releases.id (nullable; no live release yet)

    archived_at         TIMESTAMP,
    created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    UNIQUE (project_id, name)
);
CREATE INDEX ix_deployments_project ON deployments(project_id, archived_at);

CREATE TABLE builds (
    id              INTEGER PRIMARY KEY,
    deployment_id   INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    source_sha      TEXT    NOT NULL DEFAULT '',           -- hex digest of unpacked source tree
    framework       TEXT    NOT NULL,
    build_cmd       TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'pending'
                    CHECK(status IN ('pending','running','succeeded','failed','cancelled')),
    started_at      TIMESTAMP,
    finished_at     TIMESTAMP,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    exit_code       INTEGER NOT NULL DEFAULT 0,
    artifact_path   TEXT    NOT NULL DEFAULT '',           -- /data/builds/<id>/dist/
    artifact_size   INTEGER NOT NULL DEFAULT 0,
    log_path        TEXT    NOT NULL DEFAULT '',           -- /data/builds/<id>/build.log
    error           TEXT    NOT NULL DEFAULT '',
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_builds_deployment ON builds(deployment_id, id DESC);

CREATE TABLE releases (
    id              INTEGER PRIMARY KEY,
    deployment_id   INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    build_id        INTEGER NOT NULL REFERENCES builds(id),
    status          TEXT    NOT NULL DEFAULT 'starting'
                    CHECK(status IN ('starting','live','stopped','crashed','failed')),
    port            INTEGER NOT NULL DEFAULT 0,
    pid             INTEGER NOT NULL DEFAULT 0,
    started_at      TIMESTAMP,
    stopped_at      TIMESTAMP,
    restart_count   INTEGER NOT NULL DEFAULT 0,
    last_health_at  TIMESTAMP,
    log_path        TEXT    NOT NULL DEFAULT '',           -- /data/releases/<id>/runtime.log
    error           TEXT    NOT NULL DEFAULT '',
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_releases_deployment ON releases(deployment_id, id DESC);
CREATE INDEX ix_releases_status     ON releases(status);

CREATE TABLE release_events (
    id              INTEGER PRIMARY KEY,
    release_id      INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    kind            TEXT    NOT NULL,                      -- 'start' | 'exit' | 'restart' | 'crash' | 'stop' | 'health_ok' | 'health_fail'
    payload_json    TEXT    NOT NULL DEFAULT '{}',
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_release_events ON release_events(release_id, id DESC);

CREATE TABLE port_leases (
    port            INTEGER PRIMARY KEY,
    release_id      INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    acquired_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
