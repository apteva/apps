-- Preserve existing iOS and Android rows while adding a separate Mac platform.
CREATE TABLE mobile_version_allocations_next (
    id INTEGER PRIMARY KEY,
    deployment_id INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    environment_id INTEGER NOT NULL DEFAULT 0,
    build_id INTEGER NOT NULL UNIQUE REFERENCES builds(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK(platform IN ('ios','android','macos')),
    provider TEXT NOT NULL,
    app_key TEXT NOT NULL,
    version_name TEXT NOT NULL DEFAULT '',
    build_number TEXT NOT NULL DEFAULT '',
    version_code TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'reserved' CHECK(status IN ('reserved','built','failed')),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO mobile_version_allocations_next SELECT * FROM mobile_version_allocations;
DROP TABLE mobile_version_allocations;
ALTER TABLE mobile_version_allocations_next RENAME TO mobile_version_allocations;
CREATE UNIQUE INDEX ux_mobile_version_ios ON mobile_version_allocations(provider, app_key, version_name, build_number) WHERE platform = 'ios';
CREATE UNIQUE INDEX ux_mobile_version_macos ON mobile_version_allocations(provider, app_key, version_name, build_number) WHERE platform = 'macos';
CREATE UNIQUE INDEX ux_mobile_version_android ON mobile_version_allocations(provider, app_key, version_code) WHERE platform = 'android';
CREATE INDEX ix_mobile_version_build ON mobile_version_allocations(deployment_id, environment_id, build_id);

-- Dropping the old identity table cascades into the revision history; copy it
-- first and restore it after the new parent table takes the original name.
CREATE TABLE signing_identity_revisions_backup AS SELECT * FROM signing_identity_revisions;
CREATE TABLE mobile_signing_identities_next (
    id INTEGER PRIMARY KEY,
    project_id TEXT NOT NULL,
    platform TEXT NOT NULL CHECK(platform IN ('android','ios','macos')),
    authority_scope TEXT NOT NULL DEFAULT '',
    application_identifier TEXT NOT NULL,
    format TEXT NOT NULL,
    encrypted_payload BLOB NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1,
    source TEXT NOT NULL CHECK(source IN ('generated','imported')),
    key_alias TEXT NOT NULL DEFAULT '',
    certificate_pem TEXT NOT NULL DEFAULT '',
    certificate_sha1 TEXT NOT NULL DEFAULT '',
    certificate_sha256 TEXT NOT NULL DEFAULT '',
    expires_at TEXT NOT NULL DEFAULT '',
    external_state_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(project_id, platform, authority_scope, application_identifier)
);
INSERT INTO mobile_signing_identities_next SELECT * FROM mobile_signing_identities;
DROP TABLE mobile_signing_identities;
ALTER TABLE mobile_signing_identities_next RENAME TO mobile_signing_identities;
CREATE INDEX ix_mobile_signing_identity_lookup ON mobile_signing_identities(project_id, platform, application_identifier);
INSERT INTO signing_identity_revisions(identity_id, revision, identity_json, encrypted_payload, created_at)
    SELECT identity_id, revision, identity_json, encrypted_payload, created_at FROM signing_identity_revisions_backup;
DROP TABLE signing_identity_revisions_backup;
