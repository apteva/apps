-- Fast newest-first history scans when media_history is not kind-filtered.
CREATE INDEX IF NOT EXISTS idx_generations_project_id
  ON generations(project_id, id DESC);
