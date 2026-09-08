-- Preserve billing inputs before external calls and pace recovery fairly.
ALTER TABLE saas_commerce_operations ADD COLUMN billing_request_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE saas_commerce_operations ADD COLUMN next_recovery_at TIMESTAMP;
CREATE INDEX ix_saas_commerce_recovery ON saas_commerce_operations(project_id, status, next_recovery_at, updated_at);

-- Old writes are scrubbed once, in bounded batches. New sanitized writes
-- explicitly set version 1. A privacy-policy edit invalidates its history.
ALTER TABLE saas_fulfillment_runs ADD COLUMN persistence_version INTEGER NOT NULL DEFAULT 0;
CREATE INDEX ix_saas_fulfillment_unscrubbed ON saas_fulfillment_runs(id) WHERE persistence_version < 1;
CREATE TRIGGER saas_fulfillment_privacy_changed
AFTER UPDATE OF persist_input, persist_output, sensitive_input_paths_json, sensitive_output_paths_json ON saas_plan_actions
WHEN OLD.persist_input <> NEW.persist_input OR OLD.persist_output <> NEW.persist_output
  OR OLD.sensitive_input_paths_json <> NEW.sensitive_input_paths_json
  OR OLD.sensitive_output_paths_json <> NEW.sensitive_output_paths_json
BEGIN
  UPDATE saas_fulfillment_runs SET persistence_version=0
  WHERE project_id=NEW.project_id AND plan_action_id=NEW.id;
END;
