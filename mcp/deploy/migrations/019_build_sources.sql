-- Source provenance per build (v0.33.0). Written before build commands run;
-- cannot change after the build's artifact attestation exists.
CREATE TABLE build_sources (
 build_id INTEGER PRIMARY KEY REFERENCES builds(id) ON DELETE CASCADE,
 source_digest TEXT NOT NULL,
 digest_method TEXT NOT NULL,
 file_count INTEGER NOT NULL DEFAULT 0,
 lockfiles_json TEXT NOT NULL DEFAULT '{}',
 source_kind TEXT NOT NULL DEFAULT '',
 snapshot_id TEXT NOT NULL DEFAULT '',
 source_revision TEXT NOT NULL DEFAULT '',
 repository TEXT NOT NULL DEFAULT '',
 subdir TEXT NOT NULL DEFAULT '',
 archive_sha256 TEXT NOT NULL DEFAULT '',
 archive_size INTEGER NOT NULL DEFAULT 0,
 archive_note TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL
);
CREATE INDEX build_sources_digest ON build_sources(source_digest);
