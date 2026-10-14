-- Provider delivery copies never replace post media or Catalog originals.
CREATE TABLE image_delivery_cache (
 cache_key TEXT NOT NULL,
 media_project_id TEXT NOT NULL,
 source_sha256 TEXT NOT NULL,
 policy TEXT NOT NULL,
 delivery_file_id INTEGER NOT NULL,
 diagnostics TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(cache_key,media_project_id)
);
ALTER TABLE post_targets ADD COLUMN media_delivery_diagnostics TEXT NOT NULL DEFAULT '[]';
CREATE TABLE target_delivery_attempts (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 target_id INTEGER NOT NULL REFERENCES post_targets(id) ON DELETE CASCADE,
 operation_id TEXT NOT NULL,
 status TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(target_id,operation_id)
);
