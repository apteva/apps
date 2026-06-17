-- Draft generation rows let Media Studio store planned assets without
-- billing a provider until the user or agent explicitly generates them.

ALTER TABLE generations ADD COLUMN status TEXT NOT NULL DEFAULT 'ready';
ALTER TABLE generations ADD COLUMN request_json TEXT NOT NULL DEFAULT '{}';

CREATE INDEX idx_generations_project_status
  ON generations(project_id, status, id DESC);
