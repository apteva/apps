-- Scoped reconciliation can apply independent provider areas while others
-- remain blocked. Rebuild the SQLite table to admit that durable state.

CREATE TABLE mobile_store_configs_next (
    id                  INTEGER PRIMARY KEY,
    deployment_id       INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    environment_id      INTEGER NOT NULL DEFAULT 0,
    platform            TEXT    NOT NULL,
    provider            TEXT    NOT NULL,
    desired_json        TEXT    NOT NULL DEFAULT '{}',
    observed_json       TEXT    NOT NULL DEFAULT '{}',
    validation_json     TEXT    NOT NULL DEFAULT '{}',
    desired_hash        TEXT    NOT NULL DEFAULT '',
    applied_hash        TEXT    NOT NULL DEFAULT '',
    status              TEXT    NOT NULL DEFAULT 'draft'
                                CHECK(status IN ('draft','blocked','ready','applying','partial','applied','failed')),
    last_error          TEXT    NOT NULL DEFAULT '',
    created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    UNIQUE (deployment_id, environment_id, platform, provider)
);

INSERT INTO mobile_store_configs_next (
    id, deployment_id, environment_id, platform, provider, desired_json,
    observed_json, validation_json, desired_hash, applied_hash, status,
    last_error, created_at, updated_at
)
SELECT
    id, deployment_id, environment_id, platform, provider, desired_json,
    observed_json, validation_json, desired_hash, applied_hash, status,
    last_error, created_at, updated_at
FROM mobile_store_configs;

DROP TABLE mobile_store_configs;
ALTER TABLE mobile_store_configs_next RENAME TO mobile_store_configs;

CREATE INDEX ix_mobile_store_configs_environment
    ON mobile_store_configs(deployment_id, environment_id, platform);
