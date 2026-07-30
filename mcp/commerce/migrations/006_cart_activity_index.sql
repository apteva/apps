-- Commerce v0.7.7 - efficient project-wide cart activity filtering.
CREATE INDEX IF NOT EXISTS ix_commerce_carts_activity
  ON commerce_carts(project_id, status, updated_at, id);
