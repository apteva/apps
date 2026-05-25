CREATE TABLE IF NOT EXISTS computer_contexts (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  backend TEXT NOT NULL,
  provider_context_id TEXT NOT NULL DEFAULT '',
  persist_default INTEGER NOT NULL DEFAULT 1,
  auto_created INTEGER NOT NULL DEFAULT 0,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_used_at TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_computer_contexts_backend_name
  ON computer_contexts(backend, name);

CREATE UNIQUE INDEX IF NOT EXISTS idx_computer_contexts_backend_provider
  ON computer_contexts(backend, provider_context_id)
  WHERE provider_context_id != '';

