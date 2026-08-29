ALTER TABLE instances ADD COLUMN lifecycle_stage TEXT NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN primary_error TEXT NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN cleanup_error TEXT NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN retain_on_failure INTEGER NOT NULL DEFAULT 1;
ALTER TABLE instances ADD COLUMN provider_inventory_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE instances ADD COLUMN provider_checked_at DATETIME;

CREATE TABLE instance_storage_benchmarks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  instance_id INTEGER NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  target_path TEXT NOT NULL,
  result_json TEXT NOT NULL DEFAULT '{}',
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_instance_storage_benchmarks_instance ON instance_storage_benchmarks(instance_id, id DESC);
