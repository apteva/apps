-- Provider-neutral mobile signing setup state.
--
-- Secret material is deliberately excluded. Private keys are delivered
-- directly to the selected build provider and never written to Deploy's DB.

CREATE TABLE mobile_signing_setups (
    id                          INTEGER PRIMARY KEY,
    deployment_id               INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    environment_id              INTEGER NOT NULL DEFAULT 0,
    platform                    TEXT    NOT NULL,
    provider                    TEXT    NOT NULL,
    provider_connection_id      INTEGER NOT NULL DEFAULT 0,
    bundle_id                   TEXT    NOT NULL,
    status                      TEXT    NOT NULL DEFAULT 'pending'
                                CHECK(status IN ('pending','provisioning','action_required','ready','failed')),
    app_store_app_id            TEXT    NOT NULL DEFAULT '',
    apple_bundle_resource_id    TEXT    NOT NULL DEFAULT '',
    apple_certificate_id        TEXT    NOT NULL DEFAULT '',
    apple_profile_id            TEXT    NOT NULL DEFAULT '',
    provider_secret_ref         TEXT    NOT NULL DEFAULT '',
    provider_config_json        TEXT    NOT NULL DEFAULT '{}',
    key_fingerprint             TEXT    NOT NULL DEFAULT '',
    last_error                  TEXT    NOT NULL DEFAULT '',
    created_at                  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at                  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    UNIQUE (deployment_id, environment_id, provider)
);

CREATE INDEX ix_mobile_signing_environment
    ON mobile_signing_setups(deployment_id, environment_id, platform);
