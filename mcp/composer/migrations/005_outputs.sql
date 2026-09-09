-- Additive migration: original edits, render ids and artifacts remain intact.
CREATE TABLE composition_outputs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  composition_id INTEGER NOT NULL REFERENCES compositions(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK(kind IN ('song','image_video','full_clip')),
  revision INTEGER NOT NULL DEFAULT 1,
  settings_json TEXT NOT NULL,
  plan_json TEXT NOT NULL DEFAULT '{}',
  UNIQUE(composition_id, kind)
);
CREATE TABLE composition_shared_inputs (
  composition_id INTEGER PRIMARY KEY REFERENCES compositions(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL DEFAULT 1,
  master_json TEXT NOT NULL DEFAULT 'null'
);
ALTER TABLE renders ADD COLUMN output_id INTEGER REFERENCES composition_outputs(id);
ALTER TABLE renders ADD COLUMN output_revision INTEGER;
ALTER TABLE renders ADD COLUMN input_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE renders ADD COLUMN idempotency_key TEXT;
ALTER TABLE renders ADD COLUMN pending_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE renders ADD COLUMN generation_cost_usd REAL;
CREATE UNIQUE INDEX idx_output_idempotency ON renders(output_id, idempotency_key) WHERE output_id IS NOT NULL;
CREATE INDEX idx_output_history ON renders(output_id, id DESC);
CREATE TABLE output_asset_jobs (
  project_id TEXT NOT NULL,
  composition_id INTEGER NOT NULL REFERENCES compositions(id) ON DELETE CASCADE,
  cache_key TEXT NOT NULL,
  state TEXT NOT NULL,
  media_kind TEXT NOT NULL DEFAULT '',
  first_render_id INTEGER,
  asset_json TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  cost_usd REAL,
  PRIMARY KEY(project_id, composition_id, cache_key)
);
