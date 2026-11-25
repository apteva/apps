CREATE INDEX IF NOT EXISTS idx_workspace_activity_project
  ON workspace_activity(project_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_activity_project_time
  ON workspace_activity(project_id, created_at);
