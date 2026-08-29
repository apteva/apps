-- SaaS v0.10.0 — integration-backed fulfillment using app-owned credentials.

ALTER TABLE saas_plan_actions
  ADD COLUMN execution_kind TEXT NOT NULL DEFAULT 'app';
