-- Analytics v0.7 — saved dashboards.
--
-- Dashboards are project-scoped collections of widget definitions. The
-- widget config stays JSON so the UI can add new visualization knobs
-- without a schema migration for every small option.

CREATE TABLE dashboards (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  description TEXT    NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE INDEX ix_dashboards_project ON dashboards(project_id, updated_at);

CREATE TABLE dashboard_widgets (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  dashboard_id INTEGER NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
  type         TEXT    NOT NULL,
  title        TEXT    NOT NULL,
  position     INTEGER NOT NULL DEFAULT 0,
  config_json  TEXT    NOT NULL DEFAULT '{}',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

CREATE INDEX ix_dashboard_widgets_dashboard ON dashboard_widgets(dashboard_id, position);
