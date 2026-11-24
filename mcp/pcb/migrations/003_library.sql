CREATE TABLE IF NOT EXISTS pcb_library_parts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  part_id TEXT NOT NULL,
  name TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT '',
  search_text TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL,
  source_sha256 TEXT NOT NULL,
  part_json TEXT NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_pcb_library_parts_part ON pcb_library_parts(project_id, part_id);
CREATE INDEX IF NOT EXISTS idx_pcb_library_parts_category ON pcb_library_parts(project_id, category, name);
