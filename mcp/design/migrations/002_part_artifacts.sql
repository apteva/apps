DROP INDEX IF EXISTS idx_artifacts_revision_format_sha;

CREATE UNIQUE INDEX idx_artifacts_revision_format_sha_metadata
    ON artifacts(revision_id, format, sha256, metadata_json);
