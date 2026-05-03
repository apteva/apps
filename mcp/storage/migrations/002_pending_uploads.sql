-- Pending upload sessions for the direct presigned-upload protocol.
-- A row is created by POST /files/init when the install is on an S3-
-- compatible backend; consumed (deleted) by POST /files/{id}/finalize.
-- Stale rows older than expires_at are reaped on the next sweep —
-- the bucket-side object becomes a tombstone we can clean later.
--
-- We don't actually issue our own SQL writes from a client between
-- init and finalize, so there's no concurrency dance: a single row
-- exists per upload_id, finalize transactionally inserts into files
-- and deletes the pending row.

CREATE TABLE IF NOT EXISTS pending_uploads (
    upload_id      TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL,
    storage_key    TEXT NOT NULL,         -- becomes files.storage_key after finalize
    name           TEXT NOT NULL,
    folder         TEXT NOT NULL,
    content_type   TEXT,
    size_bytes     INTEGER NOT NULL,      -- declared at init; verified at finalize via Stat
    declared_sha256 TEXT NOT NULL,        -- client-supplied; we trust this (see proposal)
    visibility     TEXT,
    tags           TEXT,                  -- JSON array
    source         TEXT,
    requested_by   TEXT,
    created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at     INTEGER NOT NULL       -- unix seconds; cheaper to compare than TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_pending_uploads_expires_at
    ON pending_uploads(expires_at);
