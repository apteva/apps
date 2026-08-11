-- Deploy-owned, provider-neutral mobile signing identities.
-- Secret payloads are encrypted by a key stored in Deploy's persistent DataDir.

CREATE TABLE mobile_signing_identities (
    id                          INTEGER PRIMARY KEY,
    project_id                  TEXT    NOT NULL,
    platform                    TEXT    NOT NULL CHECK(platform IN ('android','ios')),
    authority_scope             TEXT    NOT NULL DEFAULT '',
    application_identifier      TEXT    NOT NULL,
    format                      TEXT    NOT NULL,
    encrypted_payload           BLOB    NOT NULL,
    revision                    INTEGER NOT NULL DEFAULT 1,
    source                      TEXT    NOT NULL CHECK(source IN ('generated','imported')),
    key_alias                   TEXT    NOT NULL DEFAULT '',
    certificate_pem             TEXT    NOT NULL DEFAULT '',
    certificate_sha1            TEXT    NOT NULL DEFAULT '',
    certificate_sha256          TEXT    NOT NULL DEFAULT '',
    expires_at                  TEXT    NOT NULL DEFAULT '',
    external_state_json         TEXT    NOT NULL DEFAULT '{}',
    created_at                  TEXT    NOT NULL,
    updated_at                  TEXT    NOT NULL,

    UNIQUE(project_id, platform, authority_scope, application_identifier)
);

CREATE INDEX ix_mobile_signing_identity_lookup
    ON mobile_signing_identities(project_id, platform, application_identifier);

ALTER TABLE mobile_signing_setups ADD COLUMN identity_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mobile_signing_setups ADD COLUMN prepared_revision INTEGER NOT NULL DEFAULT 0;
