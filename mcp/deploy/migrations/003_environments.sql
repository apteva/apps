-- First-class deploy environments.
--
-- deployments stays the logical app/site row. Each environment owns
-- mutable runtime configuration, domain, and current release pointer.
-- Existing installs are backfilled into a production environment for
-- backwards compatibility.

CREATE TABLE deployment_environments (
    id                  INTEGER PRIMARY KEY,
    deployment_id       INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    name                TEXT    NOT NULL,
    description         TEXT    NOT NULL DEFAULT '',

    source_ref          TEXT    NOT NULL DEFAULT '',
    source_extra_json   TEXT    NOT NULL DEFAULT '{}',

    framework           TEXT    NOT NULL DEFAULT '',
    build_cmd           TEXT    NOT NULL DEFAULT '',
    start_cmd           TEXT    NOT NULL DEFAULT '',
    port_hint           INTEGER NOT NULL DEFAULT 0,
    env_json            TEXT    NOT NULL DEFAULT '{}',

    domain              TEXT    NOT NULL DEFAULT '',
    domain_record_id    TEXT    NOT NULL DEFAULT '',
    domain_attached_at  TIMESTAMP,

    current_release_id  INTEGER,

    archived_at         TIMESTAMP,
    created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    UNIQUE (deployment_id, name)
);
CREATE INDEX ix_deployment_environments_deployment ON deployment_environments(deployment_id, archived_at);

ALTER TABLE builds ADD COLUMN environment_id INTEGER REFERENCES deployment_environments(id);
ALTER TABLE releases ADD COLUMN environment_id INTEGER REFERENCES deployment_environments(id);
CREATE INDEX ix_builds_environment ON builds(environment_id, id DESC);
CREATE INDEX ix_releases_environment ON releases(environment_id, id DESC);

INSERT INTO deployment_environments (
    deployment_id, name, description,
    source_ref, source_extra_json,
    framework, build_cmd, start_cmd, port_hint, env_json,
    domain, domain_record_id, domain_attached_at,
    current_release_id, created_at, updated_at
)
SELECT
    id, 'production', description,
    source_ref, source_extra_json,
    framework, build_cmd, start_cmd, port_hint, env_json,
    domain, domain_record_id, domain_attached_at,
    current_release_id, created_at, updated_at
FROM deployments;

UPDATE builds
   SET environment_id = (
       SELECT e.id
         FROM deployment_environments e
        WHERE e.deployment_id = builds.deployment_id
          AND e.name = 'production'
   )
 WHERE environment_id IS NULL;

UPDATE releases
   SET environment_id = (
       SELECT e.id
         FROM deployment_environments e
        WHERE e.deployment_id = releases.deployment_id
          AND e.name = 'production'
   )
 WHERE environment_id IS NULL;
