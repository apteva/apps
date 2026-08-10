-- SaaS v0.7.1 - generic sensitive fulfillment persistence controls.

ALTER TABLE saas_plan_actions
  ADD COLUMN persist_input TEXT NOT NULL DEFAULT 'redacted';
ALTER TABLE saas_plan_actions
  ADD COLUMN persist_output TEXT NOT NULL DEFAULT 'redacted';
ALTER TABLE saas_plan_actions
  ADD COLUMN sensitive_input_paths_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE saas_plan_actions
  ADD COLUMN sensitive_output_paths_json TEXT NOT NULL DEFAULT '[]';
