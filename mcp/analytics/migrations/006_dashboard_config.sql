-- Analytics v0.8.3 — dashboard-level config.
--
-- Stores filters and future dashboard-scoped settings as JSON so widgets
-- can reference shared controls without each control becoming a widget.

ALTER TABLE dashboards
ADD COLUMN config_json TEXT NOT NULL DEFAULT '{}';
