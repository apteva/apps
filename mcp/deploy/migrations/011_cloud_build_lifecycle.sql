-- Persist provider polling state so retries remain bounded and coordinated
-- across sidecar restarts and multiple worker processes.

ALTER TABLE builds ADD COLUMN external_submitted_at TEXT NOT NULL DEFAULT '';
ALTER TABLE builds ADD COLUMN external_verified_at TEXT NOT NULL DEFAULT '';
ALTER TABLE builds ADD COLUMN external_poll_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE builds ADD COLUMN external_next_poll_at TEXT NOT NULL DEFAULT '';
ALTER TABLE builds ADD COLUMN external_poll_lease_until TEXT NOT NULL DEFAULT '';
ALTER TABLE builds ADD COLUMN external_last_poll_error TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS ix_builds_external_pending;
CREATE INDEX ix_builds_external_pending
    ON builds(build_backend, status, external_next_poll_at, id)
    WHERE build_backend != 'local' AND status IN ('pending', 'running');
