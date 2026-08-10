-- Provider-neutral mobile store listing state.
--
-- Existing builds remain binary artifacts and releases remain publication
-- attempts. This table stores the desired listing/compliance document plus
-- the last observed provider state for one deployment environment.

CREATE TABLE mobile_store_configs (
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
                                CHECK(status IN ('draft','blocked','ready','applying','applied','failed')),
    last_error          TEXT    NOT NULL DEFAULT '',
    created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    UNIQUE (deployment_id, environment_id, platform, provider)
);

CREATE INDEX ix_mobile_store_configs_environment
    ON mobile_store_configs(deployment_id, environment_id, platform);
