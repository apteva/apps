-- Screenshots v0.1
--
-- One row per capture. storage_id is the foreign reference into the
-- bound storage app; we never store bytes here, only the registry +
-- metadata needed for listing and dedup.

CREATE TABLE screenshots (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    storage_id      INTEGER NOT NULL,
    url             TEXT    NOT NULL,                -- the URL the operator/agent asked for
    final_url       TEXT    NOT NULL,                -- where the browser ended up after redirects
    width           INTEGER NOT NULL,
    height          INTEGER NOT NULL,
    backend         TEXT    NOT NULL,                -- local|browserbase|steel
    label           TEXT,                            -- free-form tag for the gallery
    idempotency_key TEXT,                            -- present-only when the caller passed one
    project_id      TEXT,                            -- from CurrentProject(); empty for non-project installs
    captured_at     TIMESTAMP NOT NULL,
    deleted_at      TIMESTAMP                        -- soft-delete; rows stay so storage_id audit trail survives
);

-- Gallery list (most recent first) skips soft-deleted rows.
CREATE INDEX idx_screenshots_captured
    ON screenshots(captured_at DESC)
    WHERE deleted_at IS NULL;

-- Idempotency dedup lookup. Sparse — only rows where the caller
-- passed an idempotency_key.
CREATE INDEX idx_screenshots_idem
    ON screenshots(idempotency_key, captured_at)
    WHERE idempotency_key IS NOT NULL;

-- Project-scoped listing for global installs.
CREATE INDEX idx_screenshots_project
    ON screenshots(project_id, captured_at DESC)
    WHERE deleted_at IS NULL;
