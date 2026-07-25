ALTER TABLE calls ADD COLUMN forwarded_from TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN ingress_path TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_calls_ingress_path
    ON calls(project_id, ingress_path, placed_at DESC);
