-- Immutable mobile version contract attached to each build.

ALTER TABLE builds ADD COLUMN target_config_json TEXT NOT NULL DEFAULT '{}';

CREATE TABLE mobile_version_allocations (
    id              INTEGER PRIMARY KEY,
    deployment_id   INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    environment_id  INTEGER NOT NULL DEFAULT 0,
    build_id         INTEGER NOT NULL UNIQUE REFERENCES builds(id) ON DELETE CASCADE,
    platform         TEXT    NOT NULL CHECK(platform IN ('ios','android')),
    provider         TEXT    NOT NULL,
    app_key          TEXT    NOT NULL,
    version_name     TEXT    NOT NULL DEFAULT '',
    build_number     TEXT    NOT NULL DEFAULT '',
    version_code     TEXT    NOT NULL DEFAULT '',
    status           TEXT    NOT NULL DEFAULT 'reserved'
                             CHECK(status IN ('reserved','built','failed')),
    created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_mobile_version_ios
    ON mobile_version_allocations(provider, app_key, version_name, build_number)
    WHERE platform = 'ios';

CREATE UNIQUE INDEX ux_mobile_version_android
    ON mobile_version_allocations(provider, app_key, version_code)
    WHERE platform = 'android';

CREATE INDEX ix_mobile_version_build
    ON mobile_version_allocations(deployment_id, environment_id, build_id);
