ALTER TABLE deliveries ADD COLUMN project_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_deliveries_project_created
  ON deliveries(project_id, created_at DESC);
