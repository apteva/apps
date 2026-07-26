ALTER TABLE contact_activities ADD COLUMN idempotency_key TEXT;

CREATE UNIQUE INDEX ux_act_idempotency_key
  ON contact_activities(project_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;
